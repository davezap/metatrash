package service

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var testGitHubKeyOnce sync.Once
var testGitHubKey *rsa.PrivateKey

func githubTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	testGitHubKeyOnce.Do(func() { testGitHubKey, _ = rsa.GenerateKey(rand.Reader, 2048) })
	if testGitHubKey == nil {
		t.Fatal("cannot generate key")
	}
	return testGitHubKey
}

// githubTestConfig writes secret files and returns an enabled configuration.
func githubTestConfig(t *testing.T, keyPEM string) githubConfig {
	t.Helper()
	dir := t.TempDir()
	write := func(name, value string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return githubConfig{Enabled: true, AppID: 4242, AppSlug: "metatrash-test", ClientID: "Iv23liTestClient",
		ClientSecretFile: write("client-secret", "client-secret-value\n"), WebhookSecretFile: write("webhook-secret", "webhook-secret-0123456789\n"),
		PrivateKeyFile: write("key.pem", keyPEM)}
}

func pkcs1PEM(key *rsa.PrivateKey) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func TestGitHubConfig(t *testing.T) {
	key := githubTestKey(t)
	cfg := githubTestConfig(t, pkcs1PEM(key))
	settings, err := cfg.settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.clientSecret != "client-secret-value" || string(settings.webhookSecret) != "webhook-secret-0123456789" || settings.webBase != "https://github.com" || settings.apiBase != "https://api.github.com" {
		t.Fatalf("settings %+v", settings)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := githubTestConfig(t, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	if _, err := pkcs8.settings(); err != nil {
		t.Fatalf("PKCS#8 key: %v", err)
	}
	bad := []func(*githubConfig){
		func(c *githubConfig) { c.AppID = 0 },
		func(c *githubConfig) { c.AppSlug = "Metatrash App" },
		func(c *githubConfig) { c.AppSlug = "" },
		func(c *githubConfig) { c.ClientID = "x" },
		func(c *githubConfig) { c.ClientSecretFile = "" },
		func(c *githubConfig) { c.ClientSecretFile = "/nonexistent/secret" },
		func(c *githubConfig) { c.WebhookSecretFile = cfg.ClientSecretFile + ".missing" },
		func(c *githubConfig) { c.PrivateKeyFile = c.ClientSecretFile },
		func(c *githubConfig) { c.PrivateKeyFile = "" },
	}
	for i, change := range bad {
		c := cfg
		change(&c)
		if _, err := c.settings(); err == nil {
			t.Fatalf("bad config %d accepted", i)
		}
	}
	short := githubTestConfig(t, pkcs1PEM(key))
	_ = os.WriteFile(short.WebhookSecretFile, []byte("short"), 0600)
	if _, err := short.settings(); err == nil {
		t.Fatal("short webhook secret accepted")
	}
	// The "github" key is part of the strict account configuration.
	var account accountConfig
	if err := strictJSON([]byte(`{"origin":"https://metatrash.com","github":{"enabled":false}}`), &account); err != nil || account.GitHub == nil || account.GitHub.Enabled {
		t.Fatalf("disabled github section: %v", err)
	}
	if err := strictJSON([]byte(`{"github":{"enabled":false,"appName":"x"}}`), &account); err == nil {
		t.Fatal("unknown github key accepted")
	}
}

func TestGitHubAppJWT(t *testing.T) {
	key := githubTestKey(t)
	cfg := githubTestConfig(t, pkcs1PEM(key))
	settings, err := cfg.settings()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	token, err := settings.appJWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatal("bad signature")
	}
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	if string(header) != `{"alg":"RS256","typ":"JWT"}` || json.Unmarshal(claimsJSON, &claims) != nil || claims.Iss != "4242" || claims.Iat != now.Unix()-60 || claims.Exp != now.Unix()+540 {
		t.Fatalf("header %s claims %s", header, claimsJSON)
	}
}

func githubSign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubWebhookSignature(t *testing.T) {
	s := &githubSettings{webhookSecret: []byte("webhook-secret-0123456789")}
	body := []byte(`{"zen":"ok"}`)
	good := githubSign("webhook-secret-0123456789", string(body))
	if !s.validSignature(body, good) || !s.validSignature(body, "sha256="+strings.ToUpper(good[7:])) {
		t.Fatal("good signature refused")
	}
	for _, header := range []string{"", good[7:], "sha1=" + good[7:], good[:len(good)-1], githubSign("other-secret-value-xx", string(body))} {
		if s.validSignature(body, header) {
			t.Fatalf("bad signature accepted: %q", header)
		}
	}
	if s.validSignature([]byte(`{"zen":"changed"}`), good) {
		t.Fatal("changed body accepted")
	}
}

func TestGitHubRoutesDisabled(t *testing.T) {
	h, _ := testOAuthHandler(t)
	for _, target := range []string{"/github/callback?code=x&installation_id=1&state=s", "/github/webhook"} {
		if w := oauthCall(h, "GET", target, nil); w.Code != 404 {
			t.Fatalf("%s with GitHub off: %d", target, w.Code)
		}
	}
}

// fakeGitHub answers the token exchange, /user and /user/installations.
type fakeGitHub struct {
	server        *httptest.Server
	installations []map[string]any
	exchanges     int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/web/login/oauth/access_token":
			f.exchanges++
			_ = r.ParseForm()
			if r.Method != "POST" || r.PostForm.Get("client_id") != "Iv23liTestClient" || r.PostForm.Get("client_secret") != "client-secret-value" {
				w.WriteHeader(401)
				return
			}
			if code := r.PostForm.Get("code"); strings.HasPrefix(code, "good-") {
				fmt.Fprintf(w, `{"access_token":"ghu_%s","token_type":"bearer","scope":""}`, strings.TrimPrefix(code, "good-"))
				return
			}
			fmt.Fprint(w, `{"error":"bad_verification_code"}`)
		case "/api/user", "/api/user/installations":
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !strings.HasPrefix(token, "ghu_") || r.Header.Get("X-GitHub-Api-Version") == "" {
				w.WriteHeader(401)
				return
			}
			who := strings.TrimPrefix(token, "ghu_")
			if r.URL.Path == "/api/user" {
				id := 42
				if who == "other" {
					id = 43
				}
				fmt.Fprintf(w, `{"id":%d,"login":"gh-%s"}`, id, who)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(f.installations), "installations": f.installations})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func randomInstallationID(t *testing.T) int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<40))
	if err != nil {
		t.Fatal(err)
	}
	return n.Int64() + 1
}

func TestGitHubConnectAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	fake := newFakeGitHub(t)
	cfg := githubTestConfig(t, pkcs1PEM(githubTestKey(t)))
	settings, err := cfg.settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.webBase, settings.apiBase = fake.server.URL+"/web", fake.server.URL+"/api"
	w.s.accounts.github = settings
	w.h.github = newGitHubServer(settings)
	mine, otherApp, orgID := randomInstallationID(t), randomInstallationID(t), randomInstallationID(t)
	fake.installations = []map[string]any{
		{"id": mine, "app_id": 4242, "account": map[string]any{"login": "dave-zap", "type": "User"}, "suspended_at": nil},
		{"id": otherApp, "app_id": 7, "account": map[string]any{"login": "dave-zap", "type": "User"}},
		{"id": orgID, "app_id": 4242, "account": map[string]any{"login": "zaptronics", "type": "Organization"}, "suspended_at": "2026-10-01T00:00:00Z"},
	}
	owner := w.session(w.owner)
	csrf := func(cookie *http.Cookie, action string) string {
		return w.s.accounts.mac("github-" + action + ":" + cookie.Value)
	}

	page := oauthCall(w.h, "GET", "/account", nil, owner)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "GitHub is not connected.") || !strings.Contains(page.Header().Get("Content-Security-Policy"), "form-action 'self' "+settings.webBase) {
		t.Fatalf("account page: %d %s", page.Code, page.Header().Get("Content-Security-Policy"))
	}

	// connect starts an attempt and sends the browser to GitHub with a state.
	connect := func(cookie *http.Cookie) (*http.Cookie, string) {
		t.Helper()
		resp := oauthCall(w.h, "POST", "/account/github/connect", url.Values{"csrf": {csrf(cookie, "connect")}}, cookie)
		if resp.Code != 303 {
			t.Fatalf("connect: %d %s", resp.Code, resp.Body.String())
		}
		location, _ := url.Parse(resp.Header().Get("Location"))
		if location.Path != "/web/apps/metatrash-test/installations/new" || len(location.Query().Get("state")) != 64 {
			t.Fatalf("install URL %s", location)
		}
		browser := responseCookie(resp, w.h.githubCookieName())
		if browser == nil || browser.SameSite != http.SameSiteLaxMode || !browser.HttpOnly || !browser.Secure {
			t.Fatalf("github cookie %+v", browser)
		}
		return &http.Cookie{Name: browser.Name, Value: browser.Value}, location.Query().Get("state")
	}
	callback := func(browser *http.Cookie, query url.Values) *httptest.ResponseRecorder {
		return oauthCall(w.h, "GET", "/github/callback?"+query.Encode(), nil, browser)
	}
	stored := func() []githubInstallation {
		list, err := w.db.githubInstallations(ctx, w.owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}

	// Wrong CSRF, missing session.
	if resp := oauthCall(w.h, "POST", "/account/github/connect", url.Values{"csrf": {"0"}}, owner); resp.Code != 403 {
		t.Fatalf("bad csrf: %d", resp.Code)
	}
	// No attempt in this browser, then a wrong state (which also spends the attempt).
	if resp := callback(nil, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(mine)}, "setup_action": {"install"}, "state": {strings.Repeat("a", 64)}}); resp.Code != 400 || !strings.Contains(resp.Body.String(), "Connect GitHub from Your account") {
		t.Fatalf("no attempt: %d", resp.Code)
	}
	browser, state := connect(owner)
	if resp := callback(browser, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(mine)}, "state": {strings.Repeat("b", 64)}}); resp.Code != 400 {
		t.Fatalf("wrong state: %d", resp.Code)
	}
	if resp := callback(browser, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(mine)}, "state": {state}}); resp.Code != 400 {
		t.Fatalf("spent attempt reused: %d", resp.Code)
	}
	// Unexpected parameters are refused.
	browser, state = connect(owner)
	if resp := callback(browser, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(mine)}, "state": {state}, "extra": {"1"}}); resp.Code != 400 {
		t.Fatalf("extra parameter: %d", resp.Code)
	}
	// A code GitHub rejects.
	browser, state = connect(owner)
	if resp := callback(browser, url.Values{"code": {"bad"}, "installation_id": {fmt.Sprint(mine)}, "setup_action": {"install"}, "state": {state}}); resp.Code != 400 || !strings.Contains(resp.Body.String(), "GitHub did not confirm") {
		t.Fatalf("bad code: %d %s", resp.Code, resp.Body.String())
	}
	// Installations the user cannot access, or of another app, are refused.
	for _, id := range []int64{randomInstallationID(t), otherApp} {
		browser, state = connect(owner)
		if resp := callback(browser, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(id)}, "setup_action": {"install"}, "state": {state}}); resp.Code != 403 {
			t.Fatalf("installation %d: %d", id, resp.Code)
		}
	}
	// An organization install awaiting approval.
	browser, state = connect(owner)
	if resp := callback(browser, url.Values{"setup_action": {"request"}, "state": {state}}); resp.Code != 200 || !strings.Contains(resp.Body.String(), "Installation requested") {
		t.Fatalf("request: %d", resp.Code)
	}
	if len(stored()) != 0 {
		t.Fatal("refused attempts stored a link")
	}

	// The real thing.
	browser, state = connect(owner)
	resp := callback(browser, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(mine)}, "setup_action": {"install"}, "state": {state}})
	if resp.Code != 200 || !strings.Contains(resp.Body.String(), "GitHub connected") || !strings.Contains(resp.Body.String(), `http-equiv="refresh" content="2;url=/account#github"`) {
		t.Fatalf("callback: %d %s", resp.Code, resp.Body.String())
	}
	if cleared := responseCookie(resp, w.h.githubCookieName()); cleared == nil || cleared.MaxAge >= 0 {
		t.Fatal("attempt cookie not cleared")
	}
	list := stored()
	if len(list) != 1 || list[0].InstallationID != mine || list[0].GitHubUserID != 42 || list[0].GitHubLogin != "gh-me" || list[0].AccountLogin != "dave-zap" || list[0].AccountType != "User" || list[0].Status != "active" {
		t.Fatalf("stored %+v", list)
	}
	// A suspended organization installation is linked with its status.
	browser, state = connect(owner)
	if resp := callback(browser, url.Values{"code": {"good-me"}, "installation_id": {fmt.Sprint(orgID)}, "setup_action": {"update"}, "state": {state}}); resp.Code != 200 {
		t.Fatalf("org: %d", resp.Code)
	}
	page = oauthCall(w.h, "GET", "/account", nil, owner)
	body := page.Body.String()
	if !strings.Contains(body, "<strong>dave-zap</strong>") || !strings.Contains(body, "suspended on GitHub") || !strings.Contains(body, "Connect another GitHub account") ||
		!strings.Contains(body, settings.webBase+"/settings/installations/"+fmt.Sprint(mine)) || !strings.Contains(body, settings.webBase+"/organizations/zaptronics/settings/installations/"+fmt.Sprint(orgID)) {
		t.Fatalf("account page after connect: %s", body)
	}

	// Another Metatrash account cannot take over the installation.
	member := w.session(w.member)
	browser, state = connect(member)
	if resp := callback(browser, url.Values{"code": {"good-other"}, "installation_id": {fmt.Sprint(mine)}, "setup_action": {"install"}, "state": {state}}); resp.Code != 409 {
		t.Fatalf("takeover: %d", resp.Code)
	}

	// Webhooks: signature required; suspend, unsuspend, delete.
	hook := func(event, body, signature string) int {
		req := httptest.NewRequest("POST", "/github/webhook", strings.NewReader(body))
		req.Host = "metatrash.com"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", event)
		if signature != "" {
			req.Header.Set("X-Hub-Signature-256", signature)
		}
		rec := httptest.NewRecorder()
		w.h.ServeHTTP(rec, req)
		return rec.Code
	}
	secret := "webhook-secret-0123456789"
	installationEvent := func(action string, id, app int64) string {
		return fmt.Sprintf(`{"action":%q,"installation":{"id":%d,"app_id":%d,"account":{"login":"dave-zap"}}}`, action, id, app)
	}
	statusOf := func(id int64) string {
		for _, inst := range stored() {
			if inst.InstallationID == id {
				return inst.Status
			}
		}
		return "gone"
	}
	if code := hook("installation", installationEvent("deleted", mine, 4242), ""); code != 401 {
		t.Fatalf("unsigned webhook: %d", code)
	}
	if code := hook("installation", installationEvent("deleted", mine, 4242), githubSign("wrong-secret-0123456789", installationEvent("deleted", mine, 4242))); code != 401 || statusOf(mine) != "active" {
		t.Fatalf("badly signed webhook: %d", code)
	}
	if code := hook("ping", `{"zen":"hi"}`, githubSign(secret, `{"zen":"hi"}`)); code != 204 {
		t.Fatalf("ping: %d", code)
	}
	if code := hook("push", `{"ref":"refs/heads/main"}`, githubSign(secret, `{"ref":"refs/heads/main"}`)); code != 204 {
		t.Fatalf("push: %d", code)
	}
	for _, step := range []struct {
		action string
		app    int64
		want   string
	}{{"suspend", 4242, "suspended"}, {"unsuspend", 4242, "active"}, {"deleted", 7, "active"}, {"created", 4242, "active"}, {"deleted", 4242, "gone"}} {
		event := installationEvent(step.action, mine, step.app)
		if code := hook("installation", event, githubSign(secret, event)); code != 204 || statusOf(mine) != step.want {
			t.Fatalf("webhook %s (app %d): %d, status %s", step.action, step.app, code, statusOf(mine))
		}
	}
	if code := hook("installation", `not json`, githubSign(secret, `not json`)); code != 400 {
		t.Fatalf("bad payload: %d", code)
	}
	if code := hook("installation", installationEvent("deleted", mine, 4242), githubSign(secret, installationEvent("deleted", mine, 4242))); code != 204 {
		t.Fatalf("repeat delete: %d", code)
	}

	// Disconnect from Your account; only the account's own connections.
	if resp := oauthCall(w.h, "POST", "/account/github/disconnect", url.Values{"csrf": {csrf(member, "disconnect")}, "installation": {fmt.Sprint(orgID)}}, member); resp.Code != 404 || statusOf(orgID) != "suspended" {
		t.Fatalf("other account's disconnect: %d", resp.Code)
	}
	if resp := oauthCall(w.h, "POST", "/account/github/disconnect", url.Values{"csrf": {csrf(owner, "connect")}, "installation": {fmt.Sprint(orgID)}}, owner); resp.Code != 403 {
		t.Fatalf("disconnect with connect csrf: %d", resp.Code)
	}
	if resp := oauthCall(w.h, "POST", "/account/github/disconnect", url.Values{"csrf": {csrf(owner, "disconnect")}, "installation": {"x"}}, owner); resp.Code != 400 {
		t.Fatalf("bad installation field: %d", resp.Code)
	}
	if resp := oauthCall(w.h, "POST", "/account/github/disconnect", url.Values{"csrf": {csrf(owner, "disconnect")}, "installation": {fmt.Sprint(orgID)}}, owner); resp.Code != 303 || resp.Header().Get("Location") != "/account#github" || statusOf(orgID) != "gone" {
		t.Fatalf("disconnect: %d", resp.Code)
	}
	if resp := oauthCall(w.h, "POST", "/account/github/disconnect", url.Values{"csrf": {csrf(owner, "disconnect")}, "installation": {fmt.Sprint(orgID)}}, owner); resp.Code != 404 {
		t.Fatalf("disconnect again: %d", resp.Code)
	}
	// Once the owner is gone, the other account may link the installation.
	browser, state = connect(member)
	if resp := callback(browser, url.Values{"code": {"good-other"}, "installation_id": {fmt.Sprint(mine)}, "setup_action": {"install"}, "state": {state}}); resp.Code != 200 {
		t.Fatalf("member link after removal: %d", resp.Code)
	}
	if list, _ := w.db.githubInstallations(ctx, w.member.ID); len(list) != 1 || list[0].GitHubUserID != 43 {
		t.Fatalf("member links %+v", list)
	}
}
