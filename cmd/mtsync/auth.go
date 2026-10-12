package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// credentials are the tokens for one server, kept in the user's config
// directory (never in the synced folder).
type credentials struct {
	Access  string    `json:"accessToken"`
	Refresh string    `json:"refreshToken"`
	Expires time.Time `json:"expires"`
	Scope   string    `json:"scope"`
}

func credentialsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "metatrash", "sync-credentials.json"), nil
}

func loadAllCredentials() (map[string]credentials, error) {
	all := map[string]credentials{}
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return all, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("%s is damaged; delete it and run mtsync login", path)
	}
	return all, nil
}

func saveCredentials(server string, c *credentials) error {
	all, err := loadAllCredentials()
	if err != nil {
		return err
	}
	if c == nil {
		delete(all, server)
	} else {
		all[server] = *c
	}
	path, _ := credentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	return writeFileAtomic(path, b, 0600)
}

func clientID(server string) string { return server + "/oauth/clients/sync" }
func resource(server string) string { return server + "/api/v1/account" }

func randomURLString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// login runs the authorization code flow with PKCE, receiving the code on a
// loopback port (RFC 8252). The user picks spaces and access in the browser.
func login(ctx context.Context, server string, out io.Writer) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)
	verifier, state := randomURLString(32), randomURLString(16)
	sum := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID(server)},
		"redirect_uri":          {redirect},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {resource(server)},
		"scope":                 {"spaces:read spaces:write"},
	}
	authorize := server + "/oauth/authorize?" + query.Encode()
	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	var once sync.Once
	finish := func(r result) { once.Do(func() { done <- r }) }
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		page := func(message string) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<!doctype html><title>Metatrash Sync</title><p>%s</p>", html.EscapeString(message))
		}
		switch {
		case q.Get("state") != state:
			page("This sign-in response does not match. Run mtsync login again.")
			finish(result{err: fmt.Errorf("sign-in response did not match the request")})
		case q.Get("error") != "":
			page("Sign-in was not completed: " + q.Get("error_description"))
			finish(result{err: fmt.Errorf("sign-in failed: %s %s", q.Get("error"), q.Get("error_description"))})
		case q.Get("iss") != "" && q.Get("iss") != server:
			page("This sign-in came from another server. Run mtsync login again.")
			finish(result{err: fmt.Errorf("sign-in came from %s, not %s", q.Get("iss"), server)})
		default:
			page("Metatrash Sync is connected. You can close this tab.")
			finish(result{code: q.Get("code")})
		}
	})}
	go func() { _ = srv.Serve(listener) }()
	defer srv.Close()
	fmt.Fprintf(out, "Opening your browser to connect Metatrash Sync. If it does not open, visit:\n\n  %s\n\n", authorize)
	openBrowser(authorize)
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(10 * time.Minute):
		return fmt.Errorf("timed out waiting for the browser sign-in")
	}
	if r.err != nil {
		return r.err
	}
	creds, err := tokenRequest(ctx, server, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID(server)},
		"code_verifier": {verifier},
		"resource":      {resource(server)},
	})
	if err != nil {
		return err
	}
	return saveCredentials(server, creds)
}

func tokenRequest(ctx context.Context, server string, form url.Values) (*credentials, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", server+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Access      string `json:"access_token"`
		Refresh     string `json:"refresh_token"`
		ExpiresIn   int    `json:"expires_in"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("token endpoint: %s", resp.Status)
	}
	if resp.StatusCode != 200 || body.Access == "" {
		if body.Error == "invalid_grant" {
			return nil, errSignedOut
		}
		return nil, fmt.Errorf("token endpoint: %s %s", body.Error, body.Description)
	}
	return &credentials{Access: body.Access, Refresh: body.Refresh, Scope: body.Scope, Expires: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)}, nil
}

var errSignedOut = errors.New("this computer is signed out of metatrash (the connection expired or was removed); run mtsync login")

// tokenSource returns access tokens for server, refreshing them before they
// expire or when the server refuses one. Refresh tokens rotate on every use,
// so each new pair is saved at once, under a lock file that keeps two mtsync
// processes from spending the same refresh token.
func tokenSource(server string) func(ctx context.Context, refresh bool) (string, error) {
	var mu sync.Mutex
	last := ""
	return func(ctx context.Context, refresh bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		unlock, err := lockCredentials(ctx)
		if err != nil {
			return "", err
		}
		defer unlock()
		all, err := loadAllCredentials()
		if err != nil {
			return "", err
		}
		creds, ok := all[server]
		if !ok {
			return "", errSignedOut
		}
		// Another process may already have refreshed since our token was refused.
		fresh := time.Until(creds.Expires) > time.Minute
		if fresh && (!refresh || creds.Access != last) {
			last = creds.Access
			return creds.Access, nil
		}
		next, err := tokenRequest(ctx, server, url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {creds.Refresh},
			"client_id":     {clientID(server)},
			"resource":      {resource(server)},
		})
		if err != nil {
			return "", err
		}
		if err := saveCredentials(server, next); err != nil {
			return "", err
		}
		last = next.Access
		return next.Access, nil
	}
}

// lockCredentials takes the credentials lock file, breaking one left behind
// by a process that stopped while holding it.
func lockCredentials(ctx context.Context) (func(), error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock := path + ".lock"
	for {
		f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			f.Close()
			return func() { os.Remove(lock) }, nil
		}
		if info, statErr := os.Stat(lock); statErr == nil && time.Since(info.ModTime()) > time.Minute {
			os.Remove(lock)
			continue
		}
		if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
			return nil, err
		}
	}
}

// openBrowser opens target in the user's browser; tests replace it.
var openBrowser = func(target string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	_ = cmd.Start()
}
