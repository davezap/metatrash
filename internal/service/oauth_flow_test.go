package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash"
)

var integrationDataOnce sync.Once
var integrationDataDir string

// oauthWorld is a full service with a real account database (integration
// test). It runs only when METATRASH_TEST_DB_CONFIG is set.
type oauthWorld struct {
	t       *testing.T
	s       *Service
	h       *httpAdapter
	db      *accountDatabase
	owner   userAccount
	member  userAccount
	ownedID string
	joinID  string
	// ownedName and joinName are the owner/slug names agents use.
	ownedName string
	joinName  string
	joinSlug  string
}

func randomName(t *testing.T, prefix string) string {
	t.Helper()
	value, err := randomHex(5)
	if err != nil {
		t.Fatal(err)
	}
	return prefix + value
}

func newOAuthWorld(t *testing.T) *oauthWorld {
	t.Helper()
	db := testDatabase(t)
	ctx := context.Background()
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Spaces["private"] = SpaceConfig{Visibility: "private", Overrides: json.RawMessage(`{}`)}
	b, _ = json.Marshal(cfg)
	configPath, keyPath := filepath.Join(dir, "spaces.json"), filepath.Join(dir, "keys.json")
	_ = os.WriteFile(configPath, b, 0600)
	sum := sha256.Sum256([]byte("writer"))
	b, _ = json.Marshal(map[string]Keys{"private": {WriteHashes: []string{hex.EncodeToString(sum[:])}}})
	_ = os.WriteFile(keyPath, b, 0600)
	// Owned repositories must outlive each test's temporary directory, because
	// every world loads all ready spaces from the shared database.
	integrationDataOnce.Do(func() { integrationDataDir, _ = os.MkdirTemp("", "metatrash-oauth-test-") })
	s, err := Open(ctx, configPath, keyPath, integrationDataDir, metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	settings, err := (&oauthConfig{Enabled: true}).settings()
	if err != nil {
		t.Fatal(err)
	}
	s.accounts = &accounts{config: accountConfig{Origin: "https://metatrash.com"}, store: db, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{},
		secret: []byte("test-only-secret"), mailSlots: make(chan struct{}, 2), oauth: &settings}
	if err := s.loadOwnedSpaces(ctx, db); err != nil {
		t.Fatal(err)
	}
	handler, err := s.Handler(metatrash.ToolSchema, nil, "https://metatrash.com")
	if err != nil {
		t.Fatal(err)
	}
	w := &oauthWorld{t: t, s: s, h: handler.(*httpAdapter), db: db}
	w.h.oauth.clients, _ = testOAuthClients(map[string]string{testClientID: testClientDocument()})
	w.owner = w.user("owner")
	w.member = w.user("member")
	space, err := s.createOwnedSpace(ctx, w.owner.ID, "Owner notes", randomName(t, "notes-"))
	if err != nil {
		t.Fatal(err)
	}
	w.ownedID = space.ID
	w.ownedName = w.owner.Username + "/" + space.Slug
	shared, err := s.createOwnedSpace(ctx, w.member.ID, "Shared plans", randomName(t, "plans-"))
	if err != nil {
		t.Fatal(err)
	}
	w.joinID = shared.ID
	w.joinName = w.member.Username + "/" + shared.Slug
	w.joinSlug = shared.Slug
	invitation, err := db.inviteHuman(ctx, w.member.ID, shared.ID, w.owner.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.acceptHumanInvitation(ctx, w.owner.ID, shared.ID, invitation); err != nil {
		t.Fatal(err)
	}
	return w
}

// user creates an account with a username; owner and member each own one space.
func (w *oauthWorld) user(prefix string) userAccount {
	w.t.Helper()
	ctx := context.Background()
	user, err := w.db.FindOrCreate(ctx, randomName(w.t, prefix)+"@example.com")
	if err != nil {
		w.t.Fatal(err)
	}
	if err := w.db.ChooseUsername(ctx, user.ID, randomName(w.t, prefix)); err != nil {
		w.t.Fatal(err)
	}
	user, _, err = w.db.ByID(ctx, user.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	return user
}

func (w *oauthWorld) session(user userAccount) *http.Cookie {
	token, _ := randomHex(32)
	w.s.accounts.mu.Lock()
	w.s.accounts.sessions[secretDigest(token)] = accountSession{UserID: user.ID, Expires: time.Now().Add(time.Hour)}
	w.s.accounts.mu.Unlock()
	return &http.Cookie{Name: sessionCookie, Value: token}
}

// csrfPattern finds the consent form's token (the page can also hold the
// first-space setup form and the sign-out form).
var csrfPattern = regexp.MustCompile(`action="/oauth/consent">\s*<input type="hidden" name="csrf" value="([0-9a-f]{64})"`)

func pkcePair(t *testing.T) (string, string) {
	verifier, err := newOAuthToken("")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorize runs authorize, consent and approval and returns the code.
func (w *oauthWorld) authorize(user userAccount, challenge string, choices map[string]string, resource string) (string, *httptest.ResponseRecorder) {
	w.t.Helper()
	res := oauthCall(w.h, "GET", authorizeURL(map[string]string{"code_challenge": challenge, "resource": resource}), nil)
	browser := responseCookie(res, oauthCookie)
	if res.Code != 200 || browser == nil {
		w.t.Fatalf("authorize: %d %s", res.Code, res.Body)
	}
	session := w.session(user)
	page := oauthCall(w.h, "GET", "/oauth/consent", nil, browser, session)
	match := csrfPattern.FindStringSubmatch(page.Body.String())
	if page.Code != 200 || match == nil {
		w.t.Fatalf("consent page: %d %s", page.Code, page.Body)
	}
	form := url.Values{"csrf": {match[1]}, "decision": {"approve"}}
	for space, choice := range choices {
		form.Set("s-"+space, choice)
	}
	res = oauthCall(w.h, "POST", "/oauth/consent", form, browser, session)
	if res.Code != 303 {
		return "", res
	}
	loc, _ := url.Parse(res.Header().Get("Location"))
	if loc.Query().Get("state") != "st" || loc.Query().Get("iss") != "https://metatrash.com" || !strings.HasPrefix(loc.String(), testRedirect+"?") {
		w.t.Fatalf("approval redirect %s", loc)
	}
	return loc.Query().Get("code"), res
}

func (w *oauthWorld) token(form url.Values) (int, map[string]any) {
	w.t.Helper()
	req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
	req.Host = "metatrash.com"
	req.RemoteAddr = "192.0.2.1:1000"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res := httptest.NewRecorder()
	w.h.ServeHTTP(res, req)
	var body map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	return res.Code, body
}

func (w *oauthWorld) exchange(code, verifier string) (int, map[string]any) {
	return w.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}, "code": {code}, "redirect_uri": {testRedirect}, "code_verifier": {verifier}, "resource": {"https://metatrash.com/mcp/account"}})
}

func (w *oauthWorld) valid(access string) bool {
	_, err := w.db.validateAccessToken(context.Background(), access, w.h.oauth.mcpResource, time.Now(), w.h.oauth.settings)
	return err == nil
}

func TestOAuthFlowAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()

	// Consent lists the owned space and the joined space.
	verifier, challenge := pkcePair(t)
	code, res := w.authorize(w.owner, challenge, map[string]string{w.ownedID: "read_write", w.joinID: "read_only"}, "")
	if code == "" {
		t.Fatalf("approval failed: %d %s", res.Code, res.Body)
	}
	if responseCookie(res, oauthCookie) == nil || responseCookie(res, oauthCookie).MaxAge >= 0 {
		t.Fatal("pending cookie not cleared after approval")
	}
	consent, err := w.db.grantConsent(ctx, w.owner.ID, testClientID, w.h.oauth.mcpResource)
	if err != nil || consent[w.ownedID] != "read_write" || consent[w.joinID] != "read_only" || len(consent) != 2 {
		t.Fatalf("stored consent %v %v", consent, err)
	}

	// Wrong redirect URI, client or verifier fail; the code is single use.
	if status, body := w.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}, "code": {code}, "redirect_uri": {"http://localhost/callback"}, "code_verifier": {verifier}}); status != 400 || body["error"] != "invalid_grant" {
		t.Fatalf("wrong redirect accepted: %v", body)
	}
	// That failed attempt spent the code; a later correct exchange is refused.
	if status, body := w.exchange(code, verifier); status != 400 || body["error"] != "invalid_grant" {
		t.Fatalf("spent code accepted: %v", body)
	}

	verifier, challenge = pkcePair(t)
	code, _ = w.authorize(w.owner, challenge, map[string]string{w.ownedID: "read_write", w.joinID: "read_only"}, "https://metatrash.com/mcp/account")
	wrongVerifier, _ := pkcePair(t)
	if status, body := w.exchange(code, wrongVerifier); status != 400 || body["error"] != "invalid_grant" {
		t.Fatalf("wrong PKCE verifier accepted: %v", body)
	}

	verifier, challenge = pkcePair(t)
	code, _ = w.authorize(w.owner, challenge, map[string]string{w.ownedID: "read_write", w.joinID: "read_only"}, "")
	status, tokens := w.exchange(code, verifier)
	if status != 200 || tokens["token_type"] != "Bearer" || tokens["scope"] != scopeReadWrite || tokens["expires_in"] != float64(3600) {
		t.Fatalf("code exchange: %d %v", status, tokens)
	}
	access, refresh := tokens["access_token"].(string), tokens["refresh_token"].(string)
	if !w.valid(access) {
		t.Fatal("issued access token invalid")
	}
	if _, err := w.db.validateAccessToken(ctx, access, w.h.oauth.restResource, time.Now(), w.h.oauth.settings); err == nil {
		t.Fatal("token accepted for another resource")
	}
	if _, err := w.db.validateAccessToken(ctx, refresh, w.h.oauth.mcpResource, time.Now(), w.h.oauth.settings); err == nil {
		t.Fatal("refresh token accepted as access token")
	}
	// Reusing the code revokes the tokens issued with it.
	if status, body := w.exchange(code, verifier); status != 400 || body["error"] != "invalid_grant" || w.valid(access) {
		t.Fatalf("code reuse: %v", body)
	}

	// Fresh tokens, then refresh rotation and replay detection.
	verifier, challenge = pkcePair(t)
	code, _ = w.authorize(w.owner, challenge, map[string]string{w.ownedID: "read_write"}, "")
	_, tokens = w.exchange(code, verifier)
	refresh = tokens["refresh_token"].(string)
	status, rotated := w.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {testClientID}, "refresh_token": {refresh}})
	if status != 200 || rotated["refresh_token"] == refresh || !w.valid(rotated["access_token"].(string)) {
		t.Fatalf("refresh: %d %v", status, rotated)
	}
	if status, body := w.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {"https://claude.ai/other"}, "refresh_token": {rotated["refresh_token"].(string)}}); status != 400 || body["error"] != "invalid_grant" {
		t.Fatal("refresh by another client accepted")
	}
	if status, body := w.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {testClientID}, "refresh_token": {rotated["refresh_token"].(string)}, "scope": {"spaces:read"}}); status != 200 || body["scope"] != scopeRead {
		t.Fatalf("narrowed refresh: %v", body)
	} else {
		rotated = body
	}
	if status, body := w.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {testClientID}, "refresh_token": {rotated["refresh_token"].(string)}, "scope": {"spaces:read spaces:write"}}); status != 400 || body["error"] != "invalid_scope" {
		t.Fatalf("widened refresh: %v", body)
	}
	// Replaying the first (spent) refresh token revokes the whole connection.
	if status, body := w.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {testClientID}, "refresh_token": {refresh}}); status != 400 || body["error"] != "invalid_grant" {
		t.Fatalf("replay accepted: %v", body)
	}
	if w.valid(rotated["access_token"].(string)) {
		t.Fatal("tokens survived replay revocation")
	}
	if consent, _ := w.db.grantConsent(ctx, w.owner.ID, testClientID, w.h.oauth.mcpResource); len(consent) != 0 {
		t.Fatal("connection survived replay revocation")
	}

	// RFC 7009 revocation by the client.
	verifier, challenge = pkcePair(t)
	code, _ = w.authorize(w.owner, challenge, map[string]string{w.joinID: "read_only"}, "")
	_, tokens = w.exchange(code, verifier)
	revoke := func(token, client string) int {
		req := httptest.NewRequest("POST", "/oauth/revoke", strings.NewReader(url.Values{"token": {token}, "client_id": {client}}.Encode()))
		req.Host = "metatrash.com"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, req)
		return res.Code
	}
	if revoke(tokens["refresh_token"].(string), "https://claude.ai/other") != 200 || !w.valid(tokens["access_token"].(string)) {
		t.Fatal("another client revoked a token")
	}
	if revoke(tokens["refresh_token"].(string), testClientID) != 200 || w.valid(tokens["access_token"].(string)) {
		t.Fatal("refresh revocation did not end the connection's tokens")
	}

	// Removing the member deletes the space from the member's grants.
	if consent, _ := w.db.grantConsent(ctx, w.owner.ID, testClientID, w.h.oauth.mcpResource); consent[w.joinID] != "read_only" {
		t.Fatal("joined space consent missing")
	}
	if err := w.db.manageHumanMember(ctx, w.member.ID, w.joinID, w.owner.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	if consent, _ := w.db.grantConsent(ctx, w.owner.ID, testClientID, w.h.oauth.mcpResource); len(consent) != 0 {
		t.Fatalf("consent survived membership removal: %v", consent)
	}
	// A space the account cannot access cannot be consented to.
	verifier, challenge = pkcePair(t)
	if code, res := w.authorize(w.owner, challenge, map[string]string{w.joinID: "read_only"}, ""); code != "" || res.Code != 409 {
		t.Fatalf("consent to a removed membership: %d", res.Code)
	}
	_ = verifier

	// Deny returns access_denied.
	res = oauthCall(w.h, "GET", authorizeURL(nil), nil)
	browser := responseCookie(res, oauthCookie)
	session := w.session(w.owner)
	page := oauthCall(w.h, "GET", "/oauth/consent", nil, browser, session)
	match := csrfPattern.FindStringSubmatch(page.Body.String())
	res = oauthCall(w.h, "POST", "/oauth/consent", url.Values{"csrf": {match[1]}, "decision": {"deny"}}, browser, session)
	if loc, _ := url.Parse(res.Header().Get("Location")); res.Code != 303 || loc.Query().Get("error") != "access_denied" {
		t.Fatal("deny")
	}
	if res := oauthCall(w.h, "POST", "/oauth/consent", url.Values{"csrf": {match[1]}, "decision": {"approve"}}, browser, session); res.Code != 400 {
		t.Fatal("consumed request approved")
	}
	// Login continuation: an unauthenticated browser signs in and returns to consent.
	res = oauthCall(w.h, "GET", authorizeURL(nil), nil)
	browser = responseCookie(res, oauthCookie)
	if page := oauthCall(w.h, "GET", "/oauth/consent", nil, browser); !strings.Contains(html.UnescapeString(page.Body.String()), "Sign in to connect Claude") {
		t.Fatal("sign-in prompt")
	}
	login := responseCookie(oauthCall(w.h, "GET", "/login", nil), loginCookie)
	sent := ""
	w.s.accounts.send = func(_ context.Context, _, code string) error { sent = code; return nil }
	oauthCall(w.h, "POST", "/login/send", url.Values{"email": {w.owner.Email}, "csrf": {login.Value}}, login)
	res = oauthCall(w.h, "POST", "/login/verify", url.Values{"code": {sent}, "csrf": {login.Value}}, login, browser)
	if res.Code != 303 || res.Header().Get("Location") != "/oauth/consent" {
		t.Fatalf("login did not continue to consent: %d %s", res.Code, res.Header().Get("Location"))
	}
}
