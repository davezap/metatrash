package service

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func formCSRF(t *testing.T, body, action string) string {
	t.Helper()
	m := regexp.MustCompile(`action="` + regexp.QuoteMeta(action) + `"[^>]*><input type="hidden" name="csrf" value="([0-9a-f]{64})"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no form for %s in page", action)
	}
	return m[1]
}

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
	page := oauthCall(w.h, "GET", "/account/security", nil, session)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, `id="sign-in"`) || !strings.Contains(body, "Confirm it’s you") || strings.Contains(body, "data-passkey-create") || strings.Contains(body, "/account/confirm/passkey") {
		t.Fatalf("account page before confirming: %d %v %v %v %v", page.Code, strings.Contains(body, `id="sign-in"`), strings.Contains(body, "Confirm it’s you"), strings.Contains(body, "data-passkey-create"), strings.Contains(body, "/account/confirm/passkey"))
	}
	if !strings.Contains(body, "/assets/passkey.js") {
		t.Fatal("passkey.js not loaded")
	}
	sendCSRF := formCSRF(t, body, "/account/confirm/send")
	res := oauthCall(w.h, "POST", "/account/passkeys/add", url.Values{"csrf": {w.s.accounts.signInCSRF("/account/passkeys/add", session.Value)}, "name": {"x"}, "client_data": {"e30"}, "attestation": {"oA"}}, session)
	if res.Code != 403 || !strings.Contains(res.Body.String(), "Confirm it’s you first") {
		t.Fatalf("add without confirming: %d %s", res.Code, res.Body)
	}
	res = oauthCall(w.h, "POST", "/account/confirm/send", url.Values{"csrf": {"0" + sendCSRF[1:]}}, session)
	if res.Code != 403 {
		t.Fatalf("bad CSRF: %d", res.Code)
	}

	// Confirm with an emailed code.
	res = oauthCall(w.h, "POST", "/account/confirm/send", url.Values{"csrf": {sendCSRF}}, session)
	if res.Code != 303 || confirmEmail != w.owner.Email || len(confirmCode) != 6 {
		t.Fatalf("send confirmation: %d %s", res.Code, res.Body)
	}
	body = oauthCall(w.h, "GET", "/account/security", nil, session).Body.String()
	verifyCSRF := formCSRF(t, body, "/account/confirm/verify")
	wrong := "000000"
	if confirmCode == wrong {
		wrong = "111111"
	}
	res = oauthCall(w.h, "POST", "/account/confirm/verify", url.Values{"csrf": {verifyCSRF}, "code": {wrong}}, session)
	if res.Code != 400 {
		t.Fatalf("wrong confirmation code: %d", res.Code)
	}
	// Another session cannot use this session's code.
	other := w.session(w.owner)
	res = oauthCall(w.h, "POST", "/account/confirm/verify", url.Values{"csrf": {w.s.accounts.signInCSRF("/account/confirm/verify", other.Value)}, "code": {confirmCode}}, other)
	if res.Code != 400 {
		t.Fatalf("code used from another session: %d", res.Code)
	}
	res = oauthCall(w.h, "POST", "/account/confirm/verify", url.Values{"csrf": {verifyCSRF}, "code": {confirmCode}}, session)
	if res.Code != 303 || res.Header().Get("Location") != "/account/security" {
		t.Fatalf("confirm: %d %s", res.Code, res.Body)
	}

	// Now the page offers to add a passkey.
	body = oauthCall(w.h, "GET", "/account/security", nil, session, &http.Cookie{Name: noticeCookie, Value: "signin-confirmed"}).Body.String()
	if !strings.Contains(body, "Confirmed. For the next 10 minutes") || !strings.Contains(body, "for the next 10 minutes") {
		t.Fatal("confirmation not shown")
	}
	soft := newSoftAuthenticator(t, coseES256)
	cd, att := soft.create(t, dataOptions(t, body, "data-passkey-create"))
	addForm := url.Values{"csrf": {formCSRF(t, body, "/account/passkeys/add")}, "name": {"  Pixel phone  "}, "client_data": {b64.EncodeToString(cd)}, "attestation": {b64.EncodeToString(att)}, "transports": {"hybrid,internal"}}
	res = oauthCall(w.h, "POST", "/account/passkeys/add", addForm, session)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "passkey-added" {
		t.Fatalf("add passkey: %d %s", res.Code, res.Body)
	}
	expectNotice(`added on your Metatrash account|A sign-in method was added on your Metatrash account: passkey "Pixel phone"`)
	list, err := w.db.passkeys(ctx, w.owner.ID)
	if err != nil || len(list) != 1 || list[0].Name != "Pixel phone" || list[0].Transports != "hybrid,internal" || list[0].LastUsedAt != 0 {
		t.Fatal(list, err)
	}
	// Replaying the same registration fails (challenge spent); a fresh
	// challenge with the same credential is a duplicate.
	if res = oauthCall(w.h, "POST", "/account/passkeys/add", addForm, session); res.Code != 400 {
		t.Fatalf("replayed registration: %d", res.Code)
	}
	body = oauthCall(w.h, "GET", "/account/security", nil, session).Body.String()
	options := dataOptions(t, body, "data-passkey-create")
	if !strings.Contains(options, b64.EncodeToString(soft.id)) {
		t.Fatal("existing passkey not excluded")
	}
	cd, att = soft.create(t, options)
	res = oauthCall(w.h, "POST", "/account/passkeys/add", url.Values{"csrf": {addForm.Get("csrf")}, "client_data": {b64.EncodeToString(cd)}, "attestation": {b64.EncodeToString(att)}}, session)
	if res.Code != 409 {
		t.Fatalf("duplicate passkey: %d %s", res.Code, res.Body)
	}

	// Sign in with the passkey, without an email address.
	login := oauthCall(w.h, "GET", "/login", nil)
	browser := responseCookie(login, loginCookie)
	body = login.Body.String()
	if browser == nil || !strings.Contains(body, "Sign in with a passkey") || !strings.Contains(body, `autocomplete="email webauthn"`) {
		t.Fatalf("login page: %d", login.Code)
	}
	in := soft.get(t, dataOptions(t, body, "data-passkey-get"))
	res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, in), browser)
	signedIn := responseCookie(res, sessionCookie)
	if res.Code != 303 || res.Header().Get("Location") != "/account" || signedIn == nil {
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
	elsewhere := oauthCall(w.h, "GET", "/login", nil)
	in = soft.get(t, dataOptions(t, elsewhere.Body.String(), "data-passkey-get"))
	if res = oauthCall(w.h, "POST", "/login/passkey", assertionForm(browser.Value, in), browser); res.Code != 400 {
		t.Fatalf("challenge from another browser: %d", res.Code)
	}

	// Confirm with the passkey on a session that is not fresh.
	stale := w.session(w.owner)
	body = oauthCall(w.h, "GET", "/account/security", nil, stale).Body.String()
	in = soft.get(t, dataOptions(t, body, "data-passkey-get"))
	res = oauthCall(w.h, "POST", "/account/confirm/passkey", assertionForm(formCSRF(t, body, "/account/confirm/passkey"), in), stale)
	if res.Code != 303 {
		t.Fatalf("confirm with passkey: %d %s", res.Code, res.Body)
	}
	if fresh, _ := w.s.accounts.sessionFresh(stale.Value, time.Now()); !fresh {
		t.Fatal("passkey confirmation did not refresh the session")
	}
	// Another account cannot confirm with the owner's passkey.
	memberSession := w.session(w.member)
	body = oauthCall(w.h, "GET", "/account/security", nil, memberSession).Body.String()
	if strings.Contains(body, "/account/confirm/passkey") {
		t.Fatal("confirm with passkey offered without passkeys")
	}
	in = soft.get(t, w.s.accounts.requestOptions("confirm", memberSession.Value, nil, time.Now()))
	res = oauthCall(w.h, "POST", "/account/confirm/passkey", assertionForm(w.s.accounts.signInCSRF("/account/confirm/passkey", memberSession.Value), in), memberSession)
	if res.Code != 400 {
		t.Fatalf("confirm with another account's passkey: %d", res.Code)
	}

	// Rename (no confirmation needed) and remove (confirmation needed).
	renameCSRF := w.s.accounts.signInCSRF("/account/passkeys/rename", memberSession.Value)
	if res = oauthCall(w.h, "POST", "/account/passkeys/rename", url.Values{"csrf": {renameCSRF}, "passkey": {list[0].ID}, "name": {"mine now"}}, memberSession); res.Code != 404 {
		t.Fatalf("rename another account's passkey: %d", res.Code)
	}
	unconfirmed := w.session(w.owner)
	body = oauthCall(w.h, "GET", "/account/security", nil, unconfirmed).Body.String()
	if strings.Contains(body, "/account/passkeys/remove") {
		t.Fatal("remove offered without confirming")
	}
	res = oauthCall(w.h, "POST", "/account/passkeys/rename", url.Values{"csrf": {formCSRF(t, body, "/account/passkeys/rename")}, "passkey": {list[0].ID}, "name": {"Work laptop"}}, unconfirmed)
	if res.Code != 303 {
		t.Fatalf("rename: %d %s", res.Code, res.Body)
	}
	if res = oauthCall(w.h, "POST", "/account/passkeys/rename", url.Values{"csrf": {formCSRF(t, body, "/account/passkeys/rename")}, "passkey": {list[0].ID}, "name": {"bad\nname"}}, unconfirmed); res.Code != 400 {
		t.Fatalf("rename with a control character: %d", res.Code)
	}
	removeCSRF := w.s.accounts.signInCSRF("/account/passkeys/remove", unconfirmed.Value)
	if res = oauthCall(w.h, "POST", "/account/passkeys/remove", url.Values{"csrf": {removeCSRF}, "passkey": {list[0].ID}}, unconfirmed); res.Code != 403 {
		t.Fatalf("remove without confirming: %d", res.Code)
	}
	body = oauthCall(w.h, "GET", "/account/security", nil, signedIn).Body.String()
	if !strings.Contains(body, "Work laptop") {
		t.Fatal("renamed passkey not listed")
	}
	res = oauthCall(w.h, "POST", "/account/passkeys/remove", url.Values{"csrf": {formCSRF(t, body, "/account/passkeys/remove")}, "passkey": {list[0].ID}}, signedIn)
	if res.Code != 303 {
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
