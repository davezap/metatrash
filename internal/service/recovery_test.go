package service

import (
	"context"
	"crypto/rand"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRecoveryCodeFormat(t *testing.T) {
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^[2-9a-hjkmnp-z]{4}-[2-9a-hjkmnp-z]{4}-[2-9a-hjkmnp-z]{4}$`)
	for range 200 {
		code, err := newRecoveryCode()
		if err != nil || !pattern.MatchString(code) || seen[code] {
			t.Fatalf("code %q %v", code, err)
		}
		seen[code] = true
		normal, ok := normalRecoveryCode(" " + strings.ToUpper(code) + " ")
		if !ok || normal != strings.ReplaceAll(code, "-", "") {
			t.Fatalf("normalize %q: %q %v", code, normal, ok)
		}
		if spaced, ok := normalRecoveryCode(strings.ReplaceAll(code, "-", " ")); !ok || spaced != normal {
			t.Fatal("spaces instead of hyphens")
		}
	}
	for _, bad := range []string{"", "abcd-efgh-jkm", "abcd-efgh-jkmnp", "abcd-efgh-jkm0", "abcd-efgh-jkml", "abcd-efgh-jkm!"} {
		if _, ok := normalRecoveryCode(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if recoveryDigest("user-a", "abcdefghjkmn") == recoveryDigest("user-b", "abcdefghjkmn") {
		t.Fatal("digest not salted by account")
	}
}

var recoveryCodePattern = regexp.MustCompile(`<li><code>([2-9a-z]{4}-[2-9a-z]{4}-[2-9a-z]{4})</code></li>`)

func TestRecoveryCodesAndEmailSwitchAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	a.totpKey = testTOTPKey(t)
	notices := make(chan string, 16)
	a.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
		return nil
	}
	expectNotice := func(want string) {
		t.Helper()
		select {
		case n := <-notices:
			if !strings.HasPrefix(n, w.owner.Email+"|") || !strings.Contains(n, want) {
				t.Fatalf("notice %q, want %q", n, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no notice email (%s)", want)
		}
	}
	csrf := func(path string, session *http.Cookie) string { return a.signInCSRF(path, session.Value) }
	post := func(path string, form url.Values, session *http.Cookie) *http.Response {
		t.Helper()
		form.Set("csrf", csrf(path, session))
		return oauthCall(w.h, "POST", path, form, session).Result()
	}
	page := func(session *http.Cookie) string {
		return html.UnescapeString(oauthCall(w.h, "GET", "/account/security", nil, session).Body.String())
	}

	// A new account: email on, no codes, and nothing to turn it off with.
	stale := w.session(w.owner)
	body := page(stale)
	if !strings.Contains(body, "Email codes <span class=\"pill\">on</span>") || !strings.Contains(body, "first add a passkey or an authenticator app, and create recovery codes") || !strings.Contains(body, "To create recovery codes, confirm it’s you above.") {
		t.Fatal("initial security page")
	}
	if r := post("/account/recovery/create", url.Values{}, stale); r.StatusCode != 403 {
		t.Fatalf("create without confirming: %d", r.StatusCode)
	}
	session := w.session(w.owner)
	a.markConfirmed(session.Value, w.owner.ID, time.Now())
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, session); r.StatusCode != 409 {
		t.Fatalf("off with nothing else: %d", r.StatusCode)
	}
	if r := post("/account/email-login", url.Values{"email_login": {"maybe"}}, session); r.StatusCode != 400 {
		t.Fatalf("bad value: %d", r.StatusCode)
	}

	// Create codes: shown once, stored only as digests, with a notice.
	r := post("/account/recovery/create", url.Values{}, session)
	if r.StatusCode != 303 || r.Header.Get("Location") != "/account/security" {
		t.Fatalf("create: %d", r.StatusCode)
	}
	expectNotice("a new set of 10 recovery codes")
	body = page(session)
	found := recoveryCodePattern.FindAllStringSubmatch(body, -1)
	if len(found) != recoveryCodeCount || !strings.Contains(body, "shown only this once") {
		t.Fatalf("codes shown: %d", len(found))
	}
	codes := make([]string, len(found))
	for i, m := range found {
		codes[i] = m[1]
	}
	if again := page(session); recoveryCodePattern.MatchString(again) || !strings.Contains(again, "10 of 10 left") {
		t.Fatal("codes shown twice, or no count")
	}
	rows, err := w.db.db.QueryContext(ctx, "SELECT code_hash FROM metatrash_recovery_codes WHERE user_id = ?", w.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored := 0
	for rows.Next() {
		var digest string
		_ = rows.Scan(&digest)
		for _, code := range codes {
			if strings.Contains(digest, strings.ReplaceAll(code, "-", "")) {
				t.Fatal("plain code stored")
			}
		}
		stored++
	}
	rows.Close()
	if stored != recoveryCodeCount {
		t.Fatalf("stored %d", stored)
	}
	// Another session never sees them.
	other := w.session(w.owner)
	a.markConfirmed(other.Value, w.owner.ID, time.Now())
	if recoveryCodePattern.MatchString(page(other)) {
		t.Fatal("codes shown to another session")
	}

	// Codes alone are not enough to turn email off; an authenticator app too is.
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, session); r.StatusCode != 409 {
		t.Fatalf("off with codes only: %d", r.StatusCode)
	}
	secret := make([]byte, totpSecretBytes)
	_, _ = rand.Read(secret)
	sealed, _ := sealTOTP(a.totpKey, w.owner.ID, secret)
	if err := w.db.startTOTP(ctx, w.owner.ID, sealed, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w.db.enableTOTP(ctx, w.owner.ID, time.Now().Unix()/totpPeriod-10, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page(session), `name="email_login" value="off"`) {
		t.Fatal("turn off not offered")
	}
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, stale); r.StatusCode != 403 {
		t.Fatalf("off without confirming: %d", r.StatusCode)
	}
	r = post("/account/email-login", url.Values{"email_login": {"off"}}, session)
	if r.StatusCode != 303 || noticeOf(r) != "email-login-off" {
		t.Fatalf("off: %d", r.StatusCode)
	}
	expectNotice("A sign-in method was removed on your Metatrash account: sign-in with a code emailed to this address")
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, session); r.StatusCode != 303 || noticeOf(r) != "" {
		t.Fatalf("off again: %d", r.StatusCode)
	}
	body = page(stale)
	if !strings.Contains(body, "Email codes <span class=\"pill\">off</span>") || strings.Contains(body, "/account/confirm/send") || !strings.Contains(body, "Confirm with your authenticator app.") {
		t.Fatal("security page with email off")
	}

	// With email off: no code by email, no confirming by email, and the
	// authenticator app cannot be removed.
	if r := post("/account/confirm/send", url.Values{}, stale); r.StatusCode != 403 {
		t.Fatalf("confirm by email: %d", r.StatusCode)
	}
	if r := post("/account/totp/remove", url.Values{}, session); r.StatusCode != 409 {
		t.Fatalf("removed the last method: %d", r.StatusCode)
	}
	if _, ok, _ := w.db.totp(ctx, w.owner.ID); !ok {
		t.Fatal("authenticator app removed")
	}
	codesSent := 0
	a.send = func(context.Context, string, string) error { codesSent++; return nil }
	loginPage := oauthCall(w.h, "GET", "/login", nil)
	login := responseCookie(loginPage, loginCookie)
	sendRes := oauthCall(w.h, "POST", "/login/send", loginSendForm(t, loginPage.Body.String(), w.owner.Email), login)
	if sendRes.Code != 303 || codesSent != 0 {
		t.Fatalf("send with email off: %d, %d codes", sendRes.Code, codesSent)
	}
	expectNotice("you have turned off sign-in by emailed code")
	waiting := oauthCall(w.h, "GET", "/login", nil, login).Body.String()
	if !strings.Contains(waiting, "Check your email.") || !strings.Contains(waiting, `action="/login/restart"`) {
		t.Fatal("login page differs for an account with email off, or no Try another way")
	}
	// Try another way goes back to every sign-in method.
	if r := oauthCall(w.h, "POST", "/login/restart", url.Values{"csrf": {strings.Repeat("0", 64)}}, login); r.Code != 403 {
		t.Fatalf("restart with a bad CSRF: %d", r.Code)
	}
	if r := oauthCall(w.h, "POST", "/login/restart", url.Values{"csrf": {login.Value}}, login); r.Code != 303 || r.Header().Get("Location") != "/login" {
		t.Fatalf("restart: %d", r.Code)
	}
	if again := oauthCall(w.h, "GET", "/login", nil, login).Body.String(); strings.Contains(again, "Check your email.") || !strings.Contains(again, `action="/login/recovery"`) {
		t.Fatal("still waiting for a code after Try another way")
	}
	// A code sent before email was turned off no longer signs in.
	browser, _ := randomHex(32)
	var sent string
	a.send = func(_ context.Context, _, code string) error { sent = code; return nil }
	if err := a.issue(ctx, browser, w.owner.Email); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := a.verify(ctx, browser, sent); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Fatalf("verify with email off: %v", err)
	}

	// Sign in with a recovery code: as typed, once, with a notice.
	loginPage = oauthCall(w.h, "GET", "/login", nil)
	login = responseCookie(loginPage, loginCookie)
	if !strings.Contains(loginPage.Body.String(), `action="/login/recovery"`) {
		t.Fatal("login page does not offer recovery codes")
	}
	signIn := func(email, code string) *http.Response {
		return oauthCall(w.h, "POST", "/login/recovery", url.Values{"csrf": {login.Value}, "email": {email}, "code": {code}}, login).Result()
	}
	answers := map[string]string{}
	for name, try := range map[string][2]string{
		"wrong":   {w.owner.Email, "2222-2222-2222"},
		"garbage": {w.owner.Email, "not a code"},
		"member":  {w.member.Email, codes[0]},
		"unknown": {randomName(t, "nobody-") + "@example.com", codes[0]},
	} {
		res := oauthCall(w.h, "POST", "/login/recovery", url.Values{"csrf": {login.Value}, "email": {try[0]}, "code": {try[1]}}, login)
		answers[name] = res.Body.String()
		if res.Code != 400 || !strings.Contains(html.UnescapeString(answers[name]), "That recovery code did not work for that email address") || !strings.Contains(answers[name], `<details class="resend" open><summary>Use a recovery code`) {
			t.Fatalf("%s: %d", name, res.Code)
		}
	}
	res := signIn(strings.ToUpper(w.owner.Email), " "+strings.ToUpper(strings.ReplaceAll(codes[3], "-", ""))+" ")
	var signedIn *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			signedIn = c
		}
	}
	if res.StatusCode != 303 || signedIn == nil || signedIn.Value == "" {
		t.Fatalf("recovery sign-in: %d", res.StatusCode)
	}
	if user, ok, _ := a.currentUser(ctx, signedIn.Value); !ok || user.ID != w.owner.ID {
		t.Fatal("wrong session")
	}
	if fresh, _ := a.sessionFresh(signedIn.Value, time.Now()); !fresh {
		t.Fatal("recovery sign-in is not fresh")
	}
	expectNotice("9 recovery codes are left")
	if r := signIn(w.owner.Email, codes[3]); r.StatusCode != 400 {
		t.Fatalf("code used twice: %d", r.StatusCode)
	}
	if !strings.Contains(page(signedIn), "9 of 10 left") {
		t.Fatal("count after use")
	}
	// Five attempts per address per 15 minutes.
	limited := randomName(t, "limited-") + "@example.com"
	for i := range 6 {
		r := signIn(limited, "2222-2222-2222")
		if (i < 5 && r.StatusCode != 400) || (i == 5 && r.StatusCode != 429) {
			t.Fatalf("attempt %d: %d", i+1, r.StatusCode)
		}
	}

	// New codes replace the old ones.
	if r := post("/account/recovery/create", url.Values{}, signedIn); r.StatusCode != 303 {
		t.Fatalf("recreate: %d", r.StatusCode)
	}
	expectNotice("any older codes no longer work")
	if newCodes := recoveryCodePattern.FindAllStringSubmatch(page(signedIn), -1); len(newCodes) != recoveryCodeCount {
		t.Fatal("new codes not shown")
	}
	login = responseCookie(oauthCall(w.h, "GET", "/login", nil), loginCookie)
	if r := signIn(w.owner.Email, codes[5]); r.StatusCode != 400 {
		t.Fatalf("old code after recreating: %d", r.StatusCode)
	}

	// Turning email back on lets the last method go again.
	r = post("/account/email-login", url.Values{"email_login": {"on"}}, signedIn)
	if r.StatusCode != 303 || noticeOf(r) != "email-login-on" {
		t.Fatalf("on: %d", r.StatusCode)
	}
	expectNotice("A sign-in method was added on your Metatrash account: sign-in with a code emailed to this address")
	if r := post("/account/totp/remove", url.Values{}, signedIn); r.StatusCode != 303 {
		t.Fatalf("remove with email on: %d", r.StatusCode)
	}
	expectNotice("authenticator app")
}

func noticeOf(r *http.Response) string {
	for _, c := range r.Cookies() {
		if c.Name == noticeCookie {
			return c.Value
		}
	}
	return ""
}
