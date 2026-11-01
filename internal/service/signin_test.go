package service

import (
	"context"
	"html"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func dataOptions(t *testing.T, body, attribute string) string {
	t.Helper()
	m := regexp.MustCompile(attribute + `="([^"]*)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no %s in page", attribute)
	}
	return html.UnescapeString(m[1])
}

func assertionForm(csrf string, in softAssertion) url.Values {
	return url.Values{"csrf": {csrf}, "credential": {b64.EncodeToString(in.credential)}, "client_data": {b64.EncodeToString(in.clientData)},
		"authenticator_data": {b64.EncodeToString(in.authData)}, "signature": {b64.EncodeToString(in.signature)}, "user_handle": {b64.EncodeToString(in.handle)}}
}

func TestPasskeysAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	notices := make(chan string, 8)
	w.s.accounts.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
		return nil
	}
	var confirmCode, confirmEmail string
	w.s.accounts.sendConfirm = func(_ context.Context, email, code string) error {
		confirmEmail, confirmCode = email, code
		return nil
	}
	expectNotice := func(want string) {
		t.Helper()
		select {
		case n := <-notices:
			if !strings.HasPrefix(n, w.owner.Email+"|") || !strings.Contains(n, want) {
				t.Fatalf("notice %q", n)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no notice email")
		}
	}

	// A session that signed in long ago must confirm before adding a passkey.
	session := w.session(w.owner)
	state := securityState(t, w, session)
	if state["fresh"] == true || state["createOptions"] != nil {
		t.Fatalf("security before confirming: %v", state)
	}
	if ways := confirmWays(t, w, session); ways["email"] != true || ways["passkey"] != nil {
		t.Fatalf("ways to confirm: %v", ways)
	}
	res := securityPost(w, "/account/passkeys/add", url.Values{"name": {"x"}, "client_data": {"e30"}, "attestation": {"oA"}}, session)
	if res.Code != 403 || !strings.Contains(res.Body.String(), "step_up_needed") {
		t.Fatalf("add without confirming: %d %s", res.Code, res.Body)
	}
	// Change the token's first character (one that starts with 0 gets a 1).
	csrf := siteCSRF(w, session)
	tampered := "0" + csrf[1:]
	if tampered == csrf {
		tampered = "1" + csrf[1:]
	}
	res = siteCall(w, "POST", "/api/v1/site/account/confirm/send", "{}", session, tampered)
	if res.Code != 403 || confirmEmail != "" {
		t.Fatalf("bad CSRF: %d", res.Code)
	}

	// Confirm with an emailed code.
	res = securityPost(w, "/account/confirm/send", url.Values{}, session)
	if res.Code != 200 || confirmEmail != w.owner.Email || len(confirmCode) != 6 {
		t.Fatalf("send confirmation: %d %s", res.Code, res.Body)
	}
	if ways := confirmWays(t, w, session); ways["sent"] != true {
		t.Fatalf("code not shown as sent: %v", ways)
	}
	wrong := "000000"
	if confirmCode == wrong {
		wrong = "111111"
	}
	res = securityPost(w, "/account/confirm/verify", url.Values{"code": {wrong}}, session)
	if res.Code != 400 {
		t.Fatalf("wrong confirmation code: %d", res.Code)
	}
	// Another session cannot use this session's code.
	other := w.session(w.owner)
	res = securityPost(w, "/account/confirm/verify", url.Values{"code": {confirmCode}}, other)
	if res.Code != 400 {
		t.Fatalf("code used from another session: %d", res.Code)
	}
	res = securityPost(w, "/account/confirm/verify", url.Values{"code": {confirmCode}}, session)
	if res.Code != 200 || !strings.HasPrefix(jsonNotice(t, res), "Confirmed. For the next 10 minutes") {
		t.Fatalf("confirm: %d %s", res.Code, res.Body)
	}

	// Now Security offers to add a passkey.
	state = securityState(t, w, session)
	options, _ := state["createOptions"].(string)
	if state["fresh"] != true || options == "" {
		t.Fatalf("security after confirming: %v", state)
	}
	soft := newSoftAuthenticator(t, coseES256)
	cd, att := soft.create(t, options)
	addForm := url.Values{"name": {"  Pixel phone  "}, "client_data": {b64.EncodeToString(cd)}, "attestation": {b64.EncodeToString(att)}, "transports": {"hybrid,internal"}}
	res = securityPost(w, "/account/passkeys/add", addForm, session)
	if res.Code != 200 || noticeOf(res) != "passkey-added" {
		t.Fatalf("add passkey: %d %s", res.Code, res.Body)
	}
	expectNotice(`added on your Metatrash account|A sign-in method was added on your Metatrash account: passkey "Pixel phone"`)
	list, err := w.db.passkeys(ctx, w.owner.ID)
	if err != nil || len(list) != 1 || list[0].Name != "Pixel phone" || list[0].Transports != "hybrid,internal" || list[0].LastUsedAt != 0 {
		t.Fatal(list, err)
	}
	// Replaying the same registration fails (challenge spent); a fresh
	// challenge with the same credential is a duplicate.
	if res = securityPost(w, "/account/passkeys/add", addForm, session); res.Code != 400 {
		t.Fatalf("replayed registration: %d", res.Code)
	}
	options, _ = securityState(t, w, session)["createOptions"].(string)
	if !strings.Contains(options, b64.EncodeToString(soft.id)) {
		t.Fatal("existing passkey not excluded")
	}
	cd, att = soft.create(t, options)
	res = securityPost(w, "/account/passkeys/add", url.Values{"client_data": {b64.EncodeToString(cd)}, "attestation": {b64.EncodeToString(att)}}, session)
	if res.Code != 409 {
		t.Fatalf("duplicate passkey: %d %s", res.Code, res.Body)
	}

	// Sign in with the passkey, without an email address.
	login := oauthCall(w.h, "GET", "/login", nil)
	browser := responseCookie(login, loginCookie)
	body := login.Body.String()
	if browser == nil || !strings.Contains(body, "Sign in with a passkey") || !strings.Contains(body, `autocomplete="email webauthn"`) {
		t.Fatalf("login page: %d", login.Code)
	}
	in := soft.get(t, dataOptions(t, body, "data-passkey-get"))
	res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, in), browser)
	signedIn := responseCookie(res, sessionCookie)
	if res.Code != 303 || res.Header().Get("Location") != "/app/#settings/spaces" || signedIn == nil {
		t.Fatalf("passkey sign-in: %d %s", res.Code, res.Body)
	}
	if user, ok, _ := w.s.accounts.currentUser(ctx, signedIn.Value); !ok || user.ID != w.owner.ID {
		t.Fatal("passkey sign-in created the wrong session")
	}
	if fresh, _ := w.s.accounts.sessionFresh(signedIn.Value, time.Now()); !fresh {
		t.Fatal("passkey sign-in is not fresh")
	}
	if list, _ = w.db.passkeys(ctx, w.owner.ID); list[0].LastUsedAt == 0 {
		t.Fatal("last used not recorded")
	}
	if res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, in), browser); res.Code != 400 {
		t.Fatalf("replayed sign-in: %d", res.Code)
	}
	// A passkey the site does not know, and a challenge from another browser.
	login = oauthCall(w.h, "GET", "/login", nil)
	browser = responseCookie(login, loginCookie)
	stranger := newSoftAuthenticator(t, coseEdDSA)
	res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, stranger.get(t, dataOptions(t, login.Body.String(), "data-passkey-get"))), browser)
	if res.Code != 400 || !strings.Contains(res.Body.String(), "not registered with Metatrash") {
		t.Fatalf("unknown passkey: %d", res.Code)
	}
	// The page asks the browser to forget that passkey (0.63.0).
	if body := res.Body.String(); !strings.Contains(body, `data-passkey-forget="`+b64.EncodeToString(stranger.id)+`"`) || !strings.Contains(body, `data-rp-id="`+w.s.accounts.rpID()+`"`) {
		t.Fatalf("unknown passkey not signalled: %s", body)
	}
	// A known passkey that fails, or no failure at all, signals nothing.
	if body := oauthCall(w.h, "GET", "/login", nil).Body.String(); strings.Contains(body, "data-passkey-forget") {
		t.Fatal("login page signals a passkey")
	}
	elsewhere := oauthCall(w.h, "GET", "/login", nil)
	in = soft.get(t, dataOptions(t, elsewhere.Body.String(), "data-passkey-get"))
	if res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, in), browser); res.Code != 400 {
		t.Fatalf("challenge from another browser: %d", res.Code)
	}

	// Confirm with the passkey on a session that is not fresh.
	stale := w.session(w.owner)
	ways, _ := confirmWays(t, w, stale)["passkey"].(string)
	in = soft.get(t, ways)
	res = securityPost(w, "/account/confirm/passkey", assertionForm("", in), stale)
	if res.Code != 200 {
		t.Fatalf("confirm with passkey: %d %s", res.Code, res.Body)
	}
	if fresh, _ := w.s.accounts.sessionFresh(stale.Value, time.Now()); !fresh {
		t.Fatal("passkey confirmation did not refresh the session")
	}
	// Another account cannot confirm with the owner's passkey.
	memberSession := w.session(w.member)
	if confirmWays(t, w, memberSession)["passkey"] != nil {
		t.Fatal("confirm with passkey offered without passkeys")
	}
	in = soft.get(t, w.s.accounts.requestOptions("confirm", memberSession.Value, nil, time.Now()))
	res = securityPost(w, "/account/confirm/passkey", assertionForm("", in), memberSession)
	if res.Code != 400 {
		t.Fatalf("confirm with another account's passkey: %d", res.Code)
	}

	// Rename (no confirmation needed) and remove (confirmation needed).
	if res = securityPost(w, "/account/passkeys/rename", url.Values{"passkey": {list[0].ID}, "name": {"mine now"}}, memberSession); res.Code != 404 {
		t.Fatalf("rename another account's passkey: %d", res.Code)
	}
	unconfirmed := w.session(w.owner)
	res = securityPost(w, "/account/passkeys/rename", url.Values{"passkey": {list[0].ID}, "name": {"Work laptop"}}, unconfirmed)
	if res.Code != 200 {
		t.Fatalf("rename: %d %s", res.Code, res.Body)
	}
	if res = securityPost(w, "/account/passkeys/rename", url.Values{"passkey": {list[0].ID}, "name": {"bad\nname"}}, unconfirmed); res.Code != 400 {
		t.Fatalf("rename with a control character: %d", res.Code)
	}
	if res = securityPost(w, "/account/passkeys/remove", url.Values{"passkey": {list[0].ID}}, unconfirmed); res.Code != 403 {
		t.Fatalf("remove without confirming: %d", res.Code)
	}
	if !strings.Contains(jsonOf(securityState(t, w, signedIn)["passkeys"]), `"name":"Work laptop"`) {
		t.Fatal("renamed passkey not listed")
	}
	res = securityPost(w, "/account/passkeys/remove", url.Values{"passkey": {list[0].ID}}, signedIn)
	if res.Code != 200 {
		t.Fatalf("remove: %d %s", res.Code, res.Body)
	}
	expectNotice(`removed on your Metatrash account|A sign-in method was removed on your Metatrash account: passkey "Work laptop"`)
	if list, _ = w.db.passkeys(ctx, w.owner.ID); len(list) != 0 {
		t.Fatal("passkey not removed")
	}
	// Removing a sign-in method signs out the account's other sessions.
	if _, ok, _ := w.s.accounts.currentUser(ctx, unconfirmed.Value); ok {
		t.Fatal("other session still signed in after removing a passkey")
	}
	if _, ok, _ := w.s.accounts.currentUser(ctx, signedIn.Value); !ok {
		t.Fatal("this session signed out by removing a passkey")
	}
	// A removed passkey can no longer sign in.
	login = oauthCall(w.h, "GET", "/login", nil)
	browser = responseCookie(login, loginCookie)
	res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, soft.get(t, dataOptions(t, login.Body.String(), "data-passkey-get"))), browser)
	if res.Code != 400 {
		t.Fatalf("removed passkey signed in: %d", res.Code)
	}
}

// Signing in with an emailed code makes the session fresh; the login page
// without a database still renders.
func TestAccountsSessionFreshness(t *testing.T) {
	s, code, _ := testAccounts(t)
	a := s.accounts
	browser := strings.Repeat("c", 64)
	if err := a.issue(context.Background(), browser, "fresh@example.com"); err != nil {
		t.Fatal(err)
	}
	token, _, _, err := a.verify(context.Background(), browser, *code)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if fresh, until := a.sessionFresh(token, now); !fresh || until.Sub(now) > stepUpWindow {
		t.Fatal("new session is not fresh")
	}
	if fresh, _ := a.sessionFresh(token, now.Add(stepUpWindow+time.Second)); fresh {
		t.Fatal("session still fresh after the window")
	}
	if fresh, _ := a.sessionFresh("unknown", now); fresh {
		t.Fatal("unknown session fresh")
	}
	h := &httpAdapter{service: s}
	r := httptest.NewRequest("GET", "https://metatrash.com/login", nil)
	rec := httptest.NewRecorder()
	h.serveAccounts(rec, r, "test-client")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data-passkey-get") {
		t.Fatalf("login page: %d", rec.Code)
	}
}
