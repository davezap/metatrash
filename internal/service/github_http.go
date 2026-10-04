package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/github.html
var githubHTML string
var githubTemplate = template.Must(template.New("github").Parse(githubHTML))

const githubPendingLifetime = 10 * time.Minute
const maxGitHubPending = 1024
const maxGitHubWebhookBytes = 8 << 20

// githubServer holds the GitHub App routes' state. Connection attempts are
// ephemeral: each is bound to the browser that started it (a Lax cookie, so
// it survives the return from github.com) and to a random state value.
type githubServer struct {
	settings *githubSettings
	client   *githubClient
	mu       sync.Mutex
	pending  map[string]githubPending // by digest of the browser cookie
	now      func() time.Time
}

type githubPending struct {
	UserID  string
	State   string
	Expires time.Time
}

func newGitHubServer(settings *githubSettings) *githubServer {
	return &githubServer{settings: settings, client: newGitHubClient(settings), pending: map[string]githubPending{}, now: time.Now}
}

// take removes and returns the browser's pending attempt (one use only).
func (g *githubServer) take(browser string) (githubPending, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := secretDigest(browser)
	p, ok := g.pending[key]
	delete(g.pending, key)
	if !ok || !g.now().Before(p.Expires) {
		return githubPending{}, false
	}
	return p, true
}

func (h *httpAdapter) githubCookieName() string {
	name := "__Host-metatrash-github"
	if h.basePath != "" {
		name += "-" + secretDigest(h.basePath)[:16]
	}
	return name
}

// githubPage is the page shown on return from GitHub.
type githubPage struct {
	BasePath, Title, Message, ContinueURL string
	OK                                    bool
}

func (h *httpAdapter) renderGitHub(w http.ResponseWriter, status int, page githubPage) {
	page.BasePath = h.basePath
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	var b bytes.Buffer
	if err := githubTemplate.Execute(&b, page); err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

// serveGitHub handles /github/callback and /github/webhook. It returns false
// for other paths or when the GitHub App is not configured.
func (h *httpAdapter) serveGitHub(w http.ResponseWriter, r *http.Request, client string) bool {
	if h.github == nil || (r.URL.Path != "/github/callback" && r.URL.Path != "/github/webhook") {
		return false
	}
	if r.URL.Path == "/github/webhook" {
		h.githubWebhook(w, r)
		return true
	}
	h.githubCallback(w, r, client)
	return true
}

// githubCallback is GitHub's return after installing or authorizing the app
// (the app's Callback URL, with "Request user authorization during
// installation"). The query is code, installation_id, setup_action and the
// state we sent. Account routes reject queries, hence this separate route.
func (h *httpAdapter) githubCallback(w http.ResponseWriter, r *http.Request, client string) {
	g := h.github
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return
	}
	back := h.basePath + "/account#github"
	fail := func(status int, title, message string) {
		h.renderGitHub(w, status, githubPage{Title: title, Message: message, ContinueURL: back})
	}
	if err := h.service.rates.take(allowance{"github:callback:" + client, 30, 600}); err != nil {
		fail(429, "Too many attempts", "Please wait a few minutes, then connect GitHub again from Your account.")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		fail(400, "GitHub sent an unexpected reply", "Start again from Your account.")
		return
	}
	for key, values := range query {
		if len(values) != 1 || !(key == "code" || key == "installation_id" || key == "setup_action" || key == "state") {
			fail(400, "GitHub sent an unexpected reply", "Start again from Your account.")
			return
		}
	}
	// The attempt is single-use whatever happens next.
	browser := cookieToken(r, h.githubCookieName())
	http.SetCookie(w, &http.Cookie{Name: h.githubCookieName(), Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	pending, ok := g.take(browser)
	state := query.Get("state")
	if !ok || state == "" || subtle.ConstantTimeCompare([]byte(pending.State), []byte(state)) != 1 {
		fail(400, "Connect GitHub from Your account", "This GitHub reply did not match a connection started in this browser in the last ten minutes. If you installed the Metatrash app on GitHub directly, finish by choosing Connect GitHub on Your account.")
		return
	}
	if query.Get("setup_action") == "request" {
		fail(200, "Installation requested", "GitHub sent your request to install the Metatrash app to the organization's owners. Once they approve it, choose Connect GitHub on Your account again.")
		return
	}
	code := query.Get("code")
	installationID, err := strconv.ParseInt(query.Get("installation_id"), 10, 64)
	if code == "" || len(code) > 256 || err != nil || installationID <= 0 {
		fail(400, "GitHub did not finish the connection", "GitHub returned without an installation. Start again from Your account and make sure you install the app.")
		return
	}
	if err := h.service.rates.take(allowance{"github:link:user:" + pending.UserID, 20, 600}); err != nil {
		fail(429, "Too many attempts", "Please wait a few minutes, then connect GitHub again from Your account.")
		return
	}
	db := h.service.ownedDB
	if db == nil {
		fail(503, "Accounts are temporarily unavailable", "Please try again shortly.")
		return
	}
	token, err := g.client.exchangeCode(r.Context(), code)
	var inst githubInstallation
	if err == nil {
		inst, err = g.client.verifyInstallation(r.Context(), token, installationID)
	}
	if err == nil {
		inst.UserID = pending.UserID
		err = db.linkGitHubInstallation(r.Context(), inst, g.now())
	}
	if err != nil {
		status, message := 503, "GitHub could not be connected. Please try again from Your account."
		var p *Error
		if errors.As(err, &p) {
			status, message = p.Status, p.Message
		}
		fail(status, "GitHub was not connected", message)
		return
	}
	h.renderGitHub(w, 200, githubPage{OK: true, Title: "GitHub connected", Message: "Connected the Metatrash app on " + inst.AccountLogin + " (signed in to GitHub as " + inst.GitHubLogin + ").", ContinueURL: back})
}

// startGitHubConnect handles the Connect GitHub form (POST, after the account
// form checks): record the attempt and send the browser to GitHub.
func (h *httpAdapter) startGitHubConnect(w http.ResponseWriter, r *http.Request, user userAccount) {
	g := h.github
	if err := h.service.rates.take(allowance{"github:connect:user:" + user.ID, 20, 600}); err != nil {
		sendError(w, err)
		return
	}
	browser, err := randomHex(32)
	if err != nil {
		sendError(w, err)
		return
	}
	state, err := randomHex(32)
	if err != nil {
		sendError(w, err)
		return
	}
	now := g.now()
	g.mu.Lock()
	for key, p := range g.pending {
		if !now.Before(p.Expires) {
			delete(g.pending, key)
		}
	}
	if len(g.pending) >= maxGitHubPending {
		g.mu.Unlock()
		sendError(w, problem(503, "busy", "Metatrash is busy. Please try again in a few minutes."))
		return
	}
	g.pending[secretDigest(browser)] = githubPending{UserID: user.ID, State: state, Expires: now.Add(githubPendingLifetime)}
	g.mu.Unlock()
	// Lax: the cookie must come back on the top-level return from github.com.
	http.SetCookie(w, &http.Cookie{Name: h.githubCookieName(), Value: browser, Path: "/", MaxAge: int(githubPendingLifetime / time.Second), Expires: now.Add(githubPendingLifetime), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, g.settings.installURL(state), http.StatusSeeOther)
}

// githubInstallationView is one connection on Your account.
type githubInstallationView struct {
	ID                                     string
	AccountLogin, AccountType, GitHubLogin string
	Status, Connected, SettingsURL         string
}

func (h *httpAdapter) githubConnections(r *http.Request, user userAccount) ([]githubInstallationView, error) {
	if h.service.ownedDB == nil {
		return nil, errors.New("accounts unavailable")
	}
	list, err := h.service.ownedDB.githubInstallations(r.Context(), user.ID)
	if err != nil {
		return nil, err
	}
	views := make([]githubInstallationView, 0, len(list))
	for _, inst := range list {
		views = append(views, githubInstallationView{ID: strconv.FormatInt(inst.InstallationID, 10), AccountLogin: inst.AccountLogin,
			AccountType: strings.ToLower(inst.AccountType), GitHubLogin: inst.GitHubLogin, Status: inst.Status,
			Connected: formatUnix(inst.CreatedAt), SettingsURL: h.github.settings.installationSettingsURL(inst)})
	}
	return views, nil
}

// githubWebhook receives the app's webhook deliveries. Only signed deliveries
// are accepted. Installation removal and suspension update the stored links;
// other events (push and the rest) are acknowledged and ignored for now.
func (h *httpAdapter) githubWebhook(w http.ResponseWriter, r *http.Request) {
	g := h.github
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGitHubWebhookBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sendError(w, problem(413, "payload_too_large", "Delivery too large."))
		return
	}
	if !g.settings.validSignature(body, r.Header.Get("X-Hub-Signature-256")) {
		sendError(w, problem(401, "unauthorized", "Invalid signature."))
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event != "installation" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID    int64 `json:"id"`
			AppID int64 `json:"app_id"`
		} `json:"installation"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Installation.ID <= 0 {
		sendError(w, invalid("Unexpected installation payload."))
		return
	}
	if payload.Installation.AppID != g.settings.appID {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	db := h.service.ownedDB
	if db == nil {
		sendError(w, problem(503, "unavailable", "Accounts are temporarily unavailable."))
		return
	}
	switch payload.Action {
	case "deleted":
		err = db.forgetGitHubInstallation(r.Context(), payload.Installation.ID)
	case "suspend":
		err = db.setGitHubInstallationStatus(r.Context(), payload.Installation.ID, "suspended", g.now())
	case "unsuspend":
		err = db.setGitHubInstallationStatus(r.Context(), payload.Installation.ID, "active", g.now())
	}
	if err != nil {
		// GitHub shows failed deliveries and they can be redelivered from there.
		log.Printf("github webhook: installation %s not recorded", payload.Action)
		sendError(w, problem(503, "unavailable", "Not recorded; redeliver later."))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validSignature checks X-Hub-Signature-256 (HMAC-SHA256 of the raw body with
// the webhook secret) in constant time.
func (s *githubSettings) validSignature(body []byte, header string) bool {
	given, ok := strings.CutPrefix(header, "sha256=")
	if !ok || len(given) != 64 {
		return false
	}
	want := hmac.New(sha256.New, s.webhookSecret)
	_, _ = want.Write(body)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(given)), []byte(hex.EncodeToString(want.Sum(nil)))) == 1
}

// submitGitHub handles the Connect GitHub and Disconnect forms after the
// account form, Origin and CSRF checks.
func (h *httpAdapter) submitGitHub(w http.ResponseWriter, r *http.Request, session, path string) {
	a := h.service.accounts
	user, signedIn, err := a.currentUser(r.Context(), session)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return
	}
	if !signedIn {
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return
	}
	if h.github == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	if path == "/account/github/connect" {
		h.startGitHubConnect(w, r, user)
		return
	}
	installationID, parseErr := strconv.ParseInt(r.PostForm.Get("installation"), 10, 64)
	err = h.service.rates.take(allowance{"github:disconnect:user:" + user.ID, 20, 600})
	if err == nil && (parseErr != nil || installationID <= 0) {
		err = invalid("Invalid GitHub connection.")
	}
	if err == nil {
		err = h.service.ownedDB.unlinkGitHubInstallation(r.Context(), user.ID, installationID)
	}
	if err != nil {
		page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "The GitHub connection could not be removed. Reload before retrying."}
		status := http.StatusServiceUnavailable
		var p *Error
		if errors.As(err, &p) {
			status, page.Message = p.Status, p.Message
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
		}
		h.renderAccount(w, r, status, page)
		return
	}
	http.Redirect(w, r, h.basePath+"/account#github", http.StatusSeeOther)
}
