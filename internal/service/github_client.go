package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// githubClient talks to GitHub for the Metatrash GitHub App. Responses are
// size-limited and GitHub's error text is never shown or logged verbatim.
type githubClient struct {
	settings *githubSettings
	http     *http.Client
}

func newGitHubClient(settings *githubSettings) *githubClient {
	return &githubClient{settings: settings, http: &http.Client{
		Timeout: 15 * time.Second,
		// Never follow redirects with credentials attached.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var errGitHubUnavailable = problem(502, "github_unavailable", "GitHub could not be reached or refused the request. Please try again.")

func (c *githubClient) do(req *http.Request, limit int64, out any) (int, error) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "metatrash")
	if strings.HasPrefix(req.URL.String(), c.settings.apiBase) {
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, errGitHubUnavailable
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(b)) > limit {
		return resp.StatusCode, errGitHubUnavailable
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, errGitHubUnavailable
	}
	if out != nil && json.Unmarshal(b, out) != nil {
		return resp.StatusCode, errGitHubUnavailable
	}
	return resp.StatusCode, nil
}

// exchangeCode turns the callback code into a user access token. The token is
// used only to verify the installation and is never stored.
func (c *githubClient) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{"client_id": {c.settings.clientID}, "client_secret": {c.settings.clientSecret}, "code": {code}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.settings.webBase+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Error       string `json:"error"`
	}
	if _, err := c.do(req, 16*1024, &out); err != nil {
		return "", err
	}
	if out.Error != "" || out.AccessToken == "" || !strings.EqualFold(out.TokenType, "bearer") || len(out.AccessToken) > 1024 {
		return "", problem(400, "github_denied", "GitHub did not confirm the connection. The link may have expired; start again from Your account.")
	}
	return out.AccessToken, nil
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (c *githubClient) userRequest(ctx context.Context, token, path string, limit int64, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.settings.apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	_, err = c.do(req, limit, out)
	return err
}

// userInstallations returns the GitHub user behind token and every
// installation of this app that the user can access (up to 500).
func (c *githubClient) userInstallations(ctx context.Context, token string) (githubUser, []githubInstallation, error) {
	var user githubUser
	if err := c.userRequest(ctx, token, "/user", 64*1024, &user); err != nil {
		return user, nil, err
	}
	if user.ID <= 0 || !githubLoginPattern.MatchString(user.Login) {
		return user, nil, errGitHubUnavailable
	}
	list := []githubInstallation{}
	for page := 1; page <= 5; page++ {
		var out struct {
			Installations []struct {
				ID      int64 `json:"id"`
				AppID   int64 `json:"app_id"`
				Account struct {
					Login string `json:"login"`
					Type  string `json:"type"`
				} `json:"account"`
				SuspendedAt *string `json:"suspended_at"`
			} `json:"installations"`
		}
		if err := c.userRequest(ctx, token, "/user/installations?per_page=100&page="+strconv.Itoa(page), 2*1024*1024, &out); err != nil {
			return user, nil, err
		}
		for _, item := range out.Installations {
			if item.AppID != c.settings.appID {
				continue
			}
			status := "active"
			if item.SuspendedAt != nil && *item.SuspendedAt != "" {
				status = "suspended"
			}
			inst := githubInstallation{InstallationID: item.ID, GitHubUserID: user.ID, GitHubLogin: user.Login,
				AccountLogin: item.Account.Login, AccountType: item.Account.Type, Status: status}
			if !validGitHubInstallation(inst) {
				return user, nil, errGitHubUnavailable
			}
			list = append(list, inst)
		}
		if len(out.Installations) < 100 {
			break
		}
	}
	return user, list, nil
}

// verifyInstallation checks with the user's token that the user can access
// installationID of this app, and returns the link to store. The callback's
// installation_id is attacker-controlled until this check passes.
func (c *githubClient) verifyInstallation(ctx context.Context, token string, installationID int64) (githubInstallation, error) {
	_, list, err := c.userInstallations(ctx, token)
	if err != nil {
		return githubInstallation{}, err
	}
	for _, inst := range list {
		if inst.InstallationID == installationID {
			return inst, nil
		}
	}
	return githubInstallation{}, problem(403, "forbidden", "Your GitHub account cannot access that installation of the Metatrash app.")
}

// appJWT signs a short-lived JWT for calls made as the app itself (RS256).
// Issued 60 seconds in the past and valid for 9 minutes, per GitHub's limits.
func (s *githubSettings) appJWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(s.appID, 10)})
	if err != nil {
		return "", err
	}
	signing := header + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", errors.New("cannot sign GitHub app token")
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// installURL is where a user installs (or reconfigures) the app.
func (s *githubSettings) installURL(state string) string {
	return fmt.Sprintf("%s/apps/%s/installations/new?state=%s", s.webBase, s.appSlug, url.QueryEscape(state))
}

// authorizeURL asks the user to authorize the app without installing it, for
// linking installations that already exist. redirectURI must be the app's
// Callback URL.
func (s *githubSettings) authorizeURL(state, redirectURI string) string {
	return s.webBase + "/login/oauth/authorize?" + url.Values{"client_id": {s.clientID}, "state": {state}, "redirect_uri": {redirectURI}}.Encode()
}

// installationSettingsURL is the installation's page on GitHub, where it can
// be configured or uninstalled.
func (s *githubSettings) installationSettingsURL(inst githubInstallation) string {
	if inst.AccountType == "Organization" {
		return fmt.Sprintf("%s/organizations/%s/settings/installations/%d", s.webBase, inst.AccountLogin, inst.InstallationID)
	}
	return fmt.Sprintf("%s/settings/installations/%d", s.webBase, inst.InstallationID)
}
