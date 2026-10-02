package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testOAuthClients(docs map[string]string) (*oauthClients, *int) {
	fetches := new(int)
	c := newOAuthClients(map[string]bool{"claude.ai": true, "chatgpt.com": true})
	c.fetch = func(_ context.Context, clientID string) ([]byte, time.Duration, error) {
		*fetches++
		doc, ok := docs[clientID]
		if !ok {
			return nil, 0, fmt.Errorf("not found")
		}
		return []byte(doc), time.Hour, nil
	}
	return c, fetches
}

const testClientID = "https://claude.ai/oauth/test-client-metadata"
const testRedirect = "https://claude.ai/api/mcp/auth_callback"

func testClientDocument() string {
	return `{"client_id":"` + testClientID + `","client_name":"Claude","redirect_uris":["` + testRedirect + `","http://localhost/callback","http://127.0.0.1/callback"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"application_type":"native"}`
}

func TestOAuthClientIDShape(t *testing.T) {
	c, _ := testOAuthClients(nil)
	for _, bad := range []string{
		"http://claude.ai/oauth/x", "https://evil.example/oauth/x", "https://claude.ai", "https://claude.ai/",
		"https://claude.ai:8443/x", "https://user@claude.ai/x", "https://claude.ai/x?y=1", "https://claude.ai/x#f",
		"https://CLAUDE.AI/x", "https://claude.ai/a/../b", "https://claude.ai//x", "https://claude.ai/%41", "https://sub.claude.ai/x",
		"https://claude.ai/x y",
	} {
		if _, err := c.clientIDHost(bad); err == nil {
			t.Errorf("accepted client_id %q", bad)
		}
	}
	if host, err := c.clientIDHost(testClientID); err != nil || host != "claude.ai" {
		t.Fatal("rejected valid client_id")
	}
}

func TestOAuthClientDocument(t *testing.T) {
	c, fetches := testOAuthClients(map[string]string{testClientID: testClientDocument()})
	client, err := c.lookup(context.Background(), testClientID)
	if err != nil || client.Name != "Claude" || client.Host != "claude.ai" {
		t.Fatalf("lookup failed: %v", err)
	}
	if _, err := c.lookup(context.Background(), testClientID); err != nil || *fetches != 1 {
		t.Fatal("cache not used")
	}
	base := map[string]any{"client_id": testClientID, "redirect_uris": []string{testRedirect}}
	for name, change := range map[string]func(map[string]any){
		"wrong id":         func(d map[string]any) { d["client_id"] = "https://claude.ai/other" },
		"secret":           func(d map[string]any) { d["client_secret"] = "x" },
		"confidential":     func(d map[string]any) { d["token_endpoint_auth_method"] = "client_secret_basic" },
		"no code grant":    func(d map[string]any) { d["grant_types"] = []string{"client_credentials"} },
		"no code response": func(d map[string]any) { d["response_types"] = []string{"token"} },
		"no redirects":     func(d map[string]any) { d["redirect_uris"] = []string{} },
		"bad redirect":     func(d map[string]any) { d["redirect_uris"] = []string{"javascript:alert(1)"} },
		"remote http":      func(d map[string]any) { d["redirect_uris"] = []string{"http://claude.ai/cb"} },
		"fragment":         func(d map[string]any) { d["redirect_uris"] = []string{"https://claude.ai/cb#x"} },
	} {
		doc := map[string]any{}
		for k, v := range base {
			doc[k] = v
		}
		change(doc)
		b, _ := json.Marshal(doc)
		if _, err := parseClientDocument(b, testClientID, "claude.ai"); err == nil {
			t.Errorf("accepted document: %s", name)
		}
	}
	b, _ := json.Marshal(map[string]any{"client_id": testClientID, "redirect_uris": []string{testRedirect}, "client_name": "‮evil"})
	if client, err := parseClientDocument(b, testClientID, "claude.ai"); err != nil || client.Name != "claude.ai" {
		t.Fatal("control-character name not replaced by host")
	}
	if _, err := c.lookup(context.Background(), "https://claude.ai/missing"); err == nil {
		t.Fatal("missing document accepted")
	}
}

func TestOAuthRedirectMatching(t *testing.T) {
	client := oauthClient{RedirectURIs: []string{testRedirect, "http://localhost/callback", "http://127.0.0.1/cb?x=1", "cursor://anysphere/cb"}}
	for _, ok := range []string{testRedirect, "http://localhost:3118/callback", "http://localhost/callback", "http://127.0.0.1:5000/cb?x=1", "cursor://anysphere/cb"} {
		if !client.matchRedirectURI(ok) {
			t.Errorf("rejected %q", ok)
		}
	}
	for _, bad := range []string{testRedirect + "/", "https://claude.ai/api/mcp/auth_callback?x=1", "http://localhost:3118/callback2", "http://127.0.0.1:3118/callback", "http://localhost:99999/callback", "https://localhost/callback", "http://127.0.0.1:5000/cb", "cursor://anysphere/cb2", "HTTPS://claude.ai/api/mcp/auth_callback"} {
		if client.matchRedirectURI(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
	if redirectSource(testRedirect) != "https://claude.ai" || redirectSource("http://localhost:3118/cb") != "http://localhost:3118" || redirectSource("cursor://anysphere/cb") != "cursor:" {
		t.Fatal("redirect CSP source")
	}
}

func TestOAuthPublicAddress(t *testing.T) {
	for _, bad := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "224.0.0.1", "64:ff9b::a00:1"} {
		if publicAddress(netip.MustParseAddr(bad)) {
			t.Errorf("allowed %s", bad)
		}
	}
	for _, ok := range []string{"160.79.104.10", "2607:6bc0::10"} {
		if !publicAddress(netip.MustParseAddr(ok)) {
			t.Errorf("refused %s", ok)
		}
	}
	dial := clientMetadataHTTPClient().Transport.(*http.Transport).DialContext
	for _, address := range []string{"127.0.0.1:443", "169.254.169.254:443", "10.0.0.1:443", "160.79.104.10:80"} {
		if _, err := dial(context.Background(), "tcp", address); err == nil || !strings.Contains(err.Error(), "client metadata address refused") {
			t.Errorf("dial %s not refused by address check: %v", address, err)
		}
	}
}

func TestOAuthCacheLifetime(t *testing.T) {
	cases := map[string]time.Duration{"": time.Hour, "max-age=60": time.Minute, "public, max-age=999999": 999999 * time.Second, "no-store": 0, "no-cache": 0, "max-age=x": 0}
	for value, want := range cases {
		h := http.Header{}
		if value != "" {
			h.Set("Cache-Control", value)
		}
		if got := cacheLifetime(h); got != want {
			t.Errorf("%q: got %v want %v", value, got, want)
		}
	}
}

func TestOAuthScopeAndResource(t *testing.T) {
	o := newOAuthServer(oauthSettings{clientHosts: map[string]bool{"claude.ai": true}}, "https://metatrash.com")
	for raw, want := range map[string]string{"": o.mcpResource, "https://metatrash.com/mcp/account": o.mcpResource, "https://metatrash.com/mcp/account/": o.mcpResource, "HTTPS://Metatrash.com/mcp/account": o.mcpResource, "https://metatrash.com/api/v1/account": o.restResource} {
		if got, ok := o.canonicalResource(raw); !ok || got != want {
			t.Errorf("resource %q", raw)
		}
	}
	for _, bad := range []string{"https://metatrash.com/mcp", "https://evil.example/mcp/account", "https://metatrash.com/mcp/account?x=1", "https://metatrash.com/mcp/account#x"} {
		if _, ok := o.canonicalResource(bad); ok {
			t.Errorf("accepted resource %q", bad)
		}
	}
	for raw, want := range map[string]string{"": scopeReadWrite, "spaces:read": scopeRead, "spaces:write": scopeReadWrite, "openid spaces:read": scopeRead, "openid": scopeReadWrite} {
		if got, _ := parseScope(raw, true); got != want {
			t.Errorf("scope %q got %q", raw, got)
		}
	}
	if _, ok := parseScope("openid", false); ok {
		t.Fatal("strict scope accepted unknown")
	}
}

// testOAuthHandler builds a handler with OAuth enabled and no database; enough
// for discovery and request validation.
func testOAuthHandler(t *testing.T) (*httpAdapter, *Service) {
	t.Helper()
	s, _, _ := testAccounts(t)
	settings, err := (&oauthConfig{Enabled: true}).settings()
	if err != nil {
		t.Fatal(err)
	}
	s.accounts.oauth = &settings
	handler, err := s.Handler([]byte(`{"tools":[]}`), nil, "https://metatrash.com")
	if err != nil {
		t.Fatal(err)
	}
	h := handler.(*httpAdapter)
	h.oauth.clients, _ = testOAuthClients(map[string]string{testClientID: testClientDocument()})
	return h, s
}

func oauthCall(h http.Handler, method, target string, body url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://metatrash.com")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Host = "metatrash.com"
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func responseCookie(w *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	return nil
}

func authorizeURL(changes map[string]string) string {
	q := url.Values{"response_type": {"code"}, "client_id": {testClientID}, "redirect_uri": {testRedirect}, "code_challenge": {strings.Repeat("A", 43)}, "code_challenge_method": {"S256"}, "state": {"st"}, "resource": {"https://metatrash.com/mcp/account"}, "scope": {"spaces:read spaces:write"}}
	for k, v := range changes {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}
	return "/oauth/authorize?" + q.Encode()
}

func TestOAuthDiscovery(t *testing.T) {
	h, _ := testOAuthHandler(t)
	w := oauthCall(h, "GET", "/.well-known/oauth-authorization-server", nil)
	var meta map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &meta) != nil || meta["issuer"] != "https://metatrash.com" || meta["client_id_metadata_document_supported"] != true || meta["token_endpoint"] != "https://metatrash.com/oauth/token" || fmt.Sprint(meta["code_challenge_methods_supported"]) != "[S256]" || fmt.Sprint(meta["token_endpoint_auth_methods_supported"]) != "[none]" || meta["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("authorization server metadata: %d %s", w.Code, w.Body)
	}
	w = oauthCall(h, "GET", "/.well-known/oauth-protected-resource/mcp/account", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"resource":"https://metatrash.com/mcp/account"`) || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("protected resource metadata: %s", w.Body)
	}
	// Disabled OAuth exposes none of these routes.
	s, _, _ := testAccounts(t)
	plain, _ := s.Handler([]byte(`{"tools":[]}`), nil, "https://metatrash.com")
	if w := oauthCall(plain, "GET", "/.well-known/oauth-authorization-server", nil); w.Code != 404 {
		t.Fatal("metadata served while OAuth disabled")
	}
	// Folder hosting: issuer and endpoints include the prefix.
	o := newOAuthServer(oauthSettings{}, "https://example.com/metatrash")
	if o.issuer != "https://example.com/metatrash" || o.mcpResource != "https://example.com/metatrash/mcp/account" {
		t.Fatal("prefixed issuer")
	}
}

func TestOAuthAuthorizeValidation(t *testing.T) {
	h, _ := testOAuthHandler(t)
	// Unknown client or unregistered redirect: an error page, never a redirect.
	for _, changes := range []map[string]string{
		{"client_id": "https://evil.example/client"},
		{"client_id": "https://claude.ai/unknown"},
		{"redirect_uri": "https://evil.example/cb"},
		{"redirect_uri": ""},
	} {
		w := oauthCall(h, "GET", authorizeURL(changes), nil)
		if w.Code != 400 || w.Header().Get("Location") != "" {
			t.Errorf("%v: status %d location %q", changes, w.Code, w.Header().Get("Location"))
		}
	}
	if w := oauthCall(h, "GET", authorizeURL(nil)+"&state=dup", nil); w.Code != 400 || w.Header().Get("Location") != "" {
		t.Fatal("duplicate parameter accepted")
	}
	// Other errors go back to the client with state and iss.
	for changes, code := range map[string]string{
		`{"response_type":"token"}`:               "unsupported_response_type",
		`{"code_challenge":""}`:                   "invalid_request",
		`{"code_challenge_method":"plain"}`:       "invalid_request",
		`{"code_challenge":"short"}`:              "invalid_request",
		`{"resource":"https://evil.example/mcp"}`: "invalid_target",
		`{"prompt":"none"}`:                       "consent_required",
	} {
		var m map[string]string
		_ = json.Unmarshal([]byte(changes), &m)
		w := oauthCall(h, "GET", authorizeURL(m), nil)
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != 303 || loc == nil || !strings.HasPrefix(loc.String(), testRedirect+"?") || loc.Query().Get("error") != code || loc.Query().Get("state") != "st" || loc.Query().Get("iss") != "https://metatrash.com" {
			t.Errorf("%s: %d %s", changes, w.Code, w.Header().Get("Location"))
		}
	}
	// A valid request stores a pending authorization bound to a Lax cookie and
	// continues on a same-site page (no consent shown on the cross-site hop).
	w := oauthCall(h, "GET", authorizeURL(nil), nil)
	cookie := responseCookie(w, oauthCookie)
	if w.Code != 200 || cookie == nil || cookie.SameSite != http.SameSiteLaxMode || !cookie.Secure || !cookie.HttpOnly || !strings.Contains(w.Body.String(), `http-equiv="refresh"`) || !strings.Contains(w.Body.String(), "/oauth/consent") {
		t.Fatalf("authorize continuation: %d %s", w.Code, w.Body)
	}
	// Not signed in: sign-in prompt naming the client.
	w = oauthCall(h, "GET", "/oauth/consent", nil, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Sign in to connect Claude") {
		t.Fatalf("sign-in page: %d %s", w.Code, w.Body)
	}
	// Without the browser binding cookie the request is unknown.
	if w := oauthCall(h, "GET", "/oauth/consent", nil); w.Code != 400 {
		t.Fatal("consent without browser binding")
	}
	// Consent POST requires the session-bound CSRF value.
	if w := oauthCall(h, "POST", "/oauth/consent", url.Values{"csrf": {"x"}, "decision": {"approve"}}, cookie); w.Code != 403 {
		t.Fatalf("consent POST without session: %d", w.Code)
	}
}

func TestOAuthTokenRequestValidation(t *testing.T) {
	h, _ := testOAuthHandler(t)
	post := func(form url.Values, header map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/oauth/token", strings.NewReader(form.Encode()))
		req.Host = "metatrash.com"
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	errorCode := func(w *httptest.ResponseRecorder) string {
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return body["error"]
	}
	if w := post(url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}}, map[string]string{"Authorization": "Basic eDp5"}); w.Code != 401 || errorCode(w) != "invalid_client" {
		t.Fatal("client authentication accepted")
	}
	if w := post(url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}, "client_secret": {"x"}}, nil); errorCode(w) != "invalid_client" {
		t.Fatal("client secret accepted")
	}
	if w := post(url.Values{"grant_type": {"password"}, "client_id": {testClientID}}, nil); w.Code != 400 || errorCode(w) != "unsupported_grant_type" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("password grant")
	}
	if w := post(url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}, "code": {"mt_ac_nope"}, "redirect_uri": {testRedirect}, "code_verifier": {strings.Repeat("a", 43)}}, nil); errorCode(w) != "invalid_grant" {
		t.Fatalf("unknown code: %s", w.Body)
	}
	if w := post(url.Values{"grant_type": {"refresh_token"}, "client_id": {testClientID}, "refresh_token": {"nope"}}, nil); errorCode(w) != "invalid_grant" {
		t.Fatal("malformed refresh token")
	}
	if w := post(url.Values{"grant_type": {"refresh_token"}, "client_id": {testClientID}, "resource": {"https://evil.example/"}}, nil); errorCode(w) != "invalid_target" {
		t.Fatal("foreign resource")
	}
	req := httptest.NewRequest("POST", "/oauth/token?code=x", strings.NewReader(""))
	req.Host = "metatrash.com"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("query parameters accepted on token endpoint")
	}
}
