package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const testMicrosoftClient = "11111111-2222-3333-4444-555555555555"
const testTenant = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

var testRSAOnce sync.Once
var testRSAKey, testOtherRSAKey *rsa.PrivateKey

func testRSA(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	testRSAOnce.Do(func() {
		testRSAKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		testOtherRSAKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	})
	return testRSAKey, testOtherRSAKey
}

// fakeMicrosoft is a Microsoft identity platform that signs its own tokens.
type fakeMicrosoft struct {
	t      *testing.T
	server *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	// claims is the next ID token's claims; challenge the PKCE challenge the
	// authorize request sent, checked against the token request's verifier.
	claims     map[string]any
	challenge  string
	keyFetches int
}

func newFakeMicrosoft(t *testing.T) *fakeMicrosoft {
	key, _ := testRSA(t)
	f := &fakeMicrosoft{t: t, key: key}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/common/discovery/v2.0/keys":
			f.mu.Lock()
			f.keyFetches++
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "kid": "k1",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			}}})
		case "/common/oauth2/v2.0/token":
			r.ParseForm()
			f.mu.Lock()
			defer f.mu.Unlock()
			if r.PostForm.Get("client_secret") != "test-secret" || r.PostForm.Get("code") != "good-code" || pkceChallenge(r.PostForm.Get("code_verifier")) != f.challenge || r.PostForm.Get("redirect_uri") != "https://metatrash.com/login/microsoft/callback" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"id_token": f.sign(f.claims, "k1", key)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMicrosoft) provider() *oidcProvider {
	p := newOIDCProvider(testMicrosoftClient, "test-secret", f.server.URL)
	p.http = f.server.Client()
	return p
}

func (f *fakeMicrosoft) sign(claims map[string]any, kid string, key *rsa.PrivateKey) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeMicrosoft) idClaims(oid, email, nonce string) map[string]any {
	now := time.Now().Unix()
	return map[string]any{"iss": f.server.URL + "/" + testTenant + "/v2.0", "aud": testMicrosoftClient, "exp": now + 3600, "iat": now, "nbf": now,
		"nonce": nonce, "tid": testTenant, "oid": oid, "email": email, "name": "Test Person"}
}

func TestMicrosoftIDTokenChecks(t *testing.T) {
	f := newFakeMicrosoft(t)
	p := f.provider()
	ctx := context.Background()
	_, other := testRSA(t)
	oid := "01234567-89ab-cdef-0123-456789abcdef"
	good := f.idClaims(oid, "Person@Example.com", "n1")
	id, err := p.verifyIDToken(ctx, f.sign(good, "k1", f.key), "n1")
	if err != nil || id.Tenant != testTenant || id.Object != oid || id.Email != "person@example.com" || id.Name != "Test Person" {
		t.Fatalf("good token: %+v %v", id, err)
	}
	with := func(key string, value any) map[string]any {
		c := map[string]any{}
		for k, v := range good {
			c[k] = v
		}
		if value == nil {
			delete(c, key)
		} else {
			c[key] = value
		}
		return c
	}
	cases := map[string]string{
		"other tenant's issuer": f.sign(with("iss", f.server.URL+"/ffffffff-bbbb-cccc-dddd-eeeeeeeeeeee/v2.0"), "k1", f.key),
		"common issuer":         f.sign(with("iss", f.server.URL+"/common/v2.0"), "k1", f.key),
		"other audience":        f.sign(with("aud", "99999999-2222-3333-4444-555555555555"), "k1", f.key),
		"expired":               f.sign(with("exp", time.Now().Add(-time.Hour).Unix()), "k1", f.key),
		"no expiry":             f.sign(with("exp", nil), "k1", f.key),
		"not yet valid":         f.sign(with("nbf", time.Now().Add(time.Hour).Unix()), "k1", f.key),
		"wrong nonce":           f.sign(with("nonce", "n2"), "k1", f.key),
		"no nonce":              f.sign(with("nonce", nil), "k1", f.key),
		"no object id":          f.sign(with("oid", nil), "k1", f.key),
		"bad tenant":            f.sign(with("tid", "common"), "k1", f.key),
		"other signer":          f.sign(good, "k1", other),
		"unknown key":           f.sign(good, "k2", f.key),
		"two audiences":         f.sign(with("aud", []string{testMicrosoftClient, "x"}), "k1", f.key),
	}
	parts := strings.Split(f.sign(good, "k1", f.key), ".")
	none, _ := json.Marshal(map[string]string{"alg": "none", "kid": "k1"})
	cases["alg none"] = base64.RawURLEncoding.EncodeToString(none) + "." + parts[1] + "."
	hs, _ := json.Marshal(map[string]string{"alg": "HS256", "kid": "k1"})
	cases["alg HS256"] = base64.RawURLEncoding.EncodeToString(hs) + "." + parts[1] + "." + parts[2]
	cases["garbage"] = "abc"
	for name, token := range cases {
		if _, err := p.verifyIDToken(ctx, token, "n1"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// An unknown key ID does not fetch the keys again within five minutes of
	// the last fetch, so bad tokens cannot make us hammer Microsoft.
	f.mu.Lock()
	fetches := f.keyFetches
	f.mu.Unlock()
	if fetches != 1 {
		t.Fatalf("key fetches %d, want 1", fetches)
	}
	// The email claim falls back to preferred_username, and a non-address
	// is no label at all.
	id, err = p.verifyIDToken(ctx, f.sign(with("email", nil), "k1", f.key), "n1")
	if err != nil || id.Email != "" {
		t.Fatalf("no email: %+v %v", id, err)
	}
	c := with("email", nil)
	c["preferred_username"] = "upn@work.example"
	if id, err = p.verifyIDToken(ctx, f.sign(c, "k1", f.key), "n1"); err != nil || id.Email != "upn@work.example" {
		t.Fatalf("preferred_username: %+v %v", id, err)
	}
}

// microsoftStart presses Sign in with Microsoft (or Link a Microsoft
// account, with a session) and returns the cookie and the authorize URL.
func microsoftStart(t *testing.T, w *oauthWorld, f *fakeMicrosoft, session *http.Cookie) (*http.Cookie, url.Values) {
	t.Helper()
	var res *httptest.ResponseRecorder
	if session == nil {
		login := oauthCall(w.h, "GET", "/login", nil)
		browser := responseCookie(login, loginCookie)
		if !strings.Contains(login.Body.String(), "Sign in with Microsoft") {
			t.Fatal("no Microsoft button on the sign-in page")
		}
		res = oauthCall(w.h, "POST", "/login/microsoft", url.Values{"csrf": {browser.Value}}, browser)
	} else {
		res = oauthCall(w.h, "POST", "/account/microsoft/link", url.Values{"csrf": {w.s.accounts.signInCSRF("/account/microsoft/link", session.Value)}}, session)
	}
	if res.Code != 303 {
		t.Fatalf("start: %d %s", res.Code, res.Body)
	}
	target, err := url.Parse(res.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(target.String(), f.server.URL+"/common/oauth2/v2.0/authorize?") {
		t.Fatalf("redirect %q", res.Header().Get("Location"))
	}
	q := target.Query()
	if q.Get("client_id") != testMicrosoftClient || q.Get("redirect_uri") != "https://metatrash.com/login/microsoft/callback" || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("authorize query %v", q)
	}
	if csp := res.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' "+f.server.URL) {
		t.Fatalf("form-action does not allow Microsoft: %q", csp)
	}
	cookie := responseCookie(res, "__Host-metatrash-microsoft")
	if cookie == nil || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("attempt cookie %+v", cookie)
	}
	f.mu.Lock()
	f.challenge = q.Get("code_challenge")
	f.mu.Unlock()
	return cookie, q
}

func microsoftReturn(w *oauthWorld, f *fakeMicrosoft, cookie *http.Cookie, q url.Values, oid, email string) *httptest.ResponseRecorder {
	f.mu.Lock()
	f.claims = f.idClaims(oid, email, q.Get("nonce"))
	f.mu.Unlock()
	return oauthCall(w.h, "GET", "/login/microsoft/callback?"+url.Values{"code": {"good-code"}, "state": {q.Get("state")}, "session_state": {"x"}}.Encode(), nil, cookie)
}

func TestMicrosoftSignIn(t *testing.T) {
	w := newOAuthWorld(t)
	f := newFakeMicrosoft(t)
	a := w.s.accounts
	a.microsoft = f.provider()
	notices := make(chan string, 8)
	a.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
		return nil
	}
	expect := func(subject, text string) {
		t.Helper()
		select {
		case n := <-notices:
			if !strings.Contains(n, "|"+subject+"|") || !strings.Contains(n, text) {
				t.Fatalf("notice %q", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %q notice", subject)
		}
	}
	fresh := func(user userAccount) *http.Cookie {
		c := w.session(user)
		a.mu.Lock()
		s := a.sessions[secretDigest(c.Value)]
		s.AuthAt = time.Now()
		a.sessions[secretDigest(c.Value)] = s
		a.mu.Unlock()
		return c
	}
	oid := "0000aaaa-1111-2222-3333-444455556666"

	// A Microsoft account no one linked, whose tenant claims the owner's
	// address, signs no one in: it only waits in this browser.
	cookie, q := microsoftStart(t, w, f, nil)
	res := microsoftReturn(w, f, cookie, q, oid, w.owner.Email)
	if res.Code != 200 || responseCookie(res, sessionCookie) != nil || !strings.Contains(res.Body.String(), `url=/login"`) {
		t.Fatalf("unlinked return: %d session=%v %s", res.Code, responseCookie(res, sessionCookie), res.Body)
	}
	waiting := responseCookie(res, "__Host-metatrash-microsoft")
	// The same reply again: the attempt is spent.
	if replay := oauthCall(w.h, "GET", "/login/microsoft/callback?code=good-code&state="+q.Get("state"), nil, waiting); replay.Code != 400 {
		t.Fatalf("replayed reply: %d", replay.Code)
	}
	login := oauthCall(w.h, "GET", "/login", nil, waiting)
	if !strings.Contains(login.Body.String(), "no Metatrash account has linked it yet") || !strings.Contains(login.Body.String(), `value="`+w.owner.Email+`"`) {
		t.Fatalf("sign-in page does not explain the waiting account: %s", login.Body)
	}

	// After signing in another way (a fresh session), Security asks first.
	session := fresh(w.owner)
	if res := oauthCall(w.h, "GET", "/login", nil, session, waiting); res.Code != 303 || res.Header().Get("Location") != "/account/security" {
		t.Fatalf("signed in with a waiting account: %d %q", res.Code, res.Header().Get("Location"))
	}
	page := oauthCall(w.h, "GET", "/account/security", nil, session, waiting)
	if !strings.Contains(page.Body.String(), "Link this Microsoft account") {
		t.Fatalf("no link offer: %s", page.Body)
	}
	res = oauthCall(w.h, "POST", "/account/microsoft/confirm", url.Values{"csrf": {a.signInCSRF("/account/microsoft/confirm", session.Value)}}, session, waiting)
	if res.Code != 303 {
		t.Fatalf("confirm: %d %s", res.Code, res.Body)
	}
	expect("Sign-in method added on your Metatrash account", "Microsoft account "+w.owner.Email)
	linked, found, err := w.db.identityBySubject(context.Background(), "microsoft", testTenant, oid)
	if err != nil || !found || linked.UserID != w.owner.ID {
		t.Fatalf("not linked: %+v %v %v", linked, found, err)
	}

	// Now it signs in.
	cookie, q = microsoftStart(t, w, f, nil)
	res = microsoftReturn(w, f, cookie, q, oid, "renamed@work.example")
	signedIn := responseCookie(res, sessionCookie)
	if res.Code != 200 || signedIn == nil {
		t.Fatalf("linked sign-in: %d %s", res.Code, res.Body)
	}
	if user, ok, _ := a.currentUser(context.Background(), signedIn.Value); !ok || user.ID != w.owner.ID {
		t.Fatalf("signed in as %+v", user)
	}
	if id, _, _ := w.db.identityBySubject(context.Background(), "microsoft", testTenant, oid); id.Email != "renamed@work.example" || id.LastUsedAt == 0 {
		t.Fatalf("labels not refreshed: %+v", id)
	}

	// A reply for another browser's attempt, a wrong state, a cancelled
	// sign-in and a refused code all fail without signing in.
	cookie, q = microsoftStart(t, w, f, nil)
	if res := oauthCall(w.h, "GET", "/login/microsoft/callback?code=good-code&state=wrong", nil, cookie); res.Code != 400 || responseCookie(res, sessionCookie) != nil {
		t.Fatalf("wrong state: %d", res.Code)
	}
	cookie, q = microsoftStart(t, w, f, nil)
	if res := oauthCall(w.h, "GET", "/login/microsoft/callback?error=access_denied&state="+q.Get("state"), nil, cookie); res.Code != 200 || !strings.Contains(res.Body.String(), "cancelled") {
		t.Fatalf("cancelled: %d", res.Code)
	}
	cookie, q = microsoftStart(t, w, f, nil)
	if res := oauthCall(w.h, "GET", "/login/microsoft/callback?code=bad-code&state="+q.Get("state"), nil, cookie); res.Code != 400 || responseCookie(res, sessionCookie) != nil {
		t.Fatalf("refused code: %d", res.Code)
	}

	// Link from Security: straight to the signed-in account, once fresh.
	stale := w.session(w.member)
	if res := oauthCall(w.h, "POST", "/account/microsoft/link", url.Values{"csrf": {a.signInCSRF("/account/microsoft/link", stale.Value)}}, stale); res.Code != 403 {
		t.Fatalf("link without confirming: %d", res.Code)
	}
	memberSession := fresh(w.member)
	other := "0000bbbb-1111-2222-3333-444455556666"
	cookie, q = microsoftStart(t, w, f, memberSession)
	res = microsoftReturn(w, f, cookie, q, other, "")
	if res.Code != 200 || !strings.Contains(res.Body.String(), `url=/account/security"`) {
		t.Fatalf("link return: %d %s", res.Code, res.Body)
	}
	expect("Sign-in method added on your Metatrash account", "Microsoft account Test Person")
	// The owner cannot take the member's Microsoft account.
	cookie, q = microsoftStart(t, w, f, fresh(w.owner))
	if res := microsoftReturn(w, f, cookie, q, other, ""); res.Code != 409 {
		t.Fatalf("taking another account's Microsoft account: %d", res.Code)
	}

	// Remove it again (with a fresh session), with a notice.
	ids, err := w.db.identities(context.Background(), w.member.ID)
	if err != nil || len(ids) != 1 {
		t.Fatalf("member identities %v %v", ids, err)
	}
	res = oauthCall(w.h, "POST", "/account/microsoft/remove", url.Values{"csrf": {a.signInCSRF("/account/microsoft/remove", memberSession.Value)}, "identity": {ids[0].ID}}, memberSession)
	if res.Code != 303 {
		t.Fatalf("remove: %d %s", res.Code, res.Body)
	}
	expect("Sign-in method removed on your Metatrash account", "Microsoft account Test Person")
	if _, found, _ := w.db.identityBySubject(context.Background(), "microsoft", testTenant, other); found {
		t.Fatal("still linked after removing")
	}
}
