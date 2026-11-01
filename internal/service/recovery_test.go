package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
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

var recoveryCodePattern = regexp.MustCompile(`^[2-9a-z]{4}-[2-9a-z]{4}-[2-9a-z]{4}$`)

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
	post := func(path string, form url.Values, session *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		return securityPost(w, path, form, session)
	}
	// What Settings → Security shows about email sign-in and recovery codes.
	type view struct {
		EmailLogin      bool     `json:"emailLogin"`
		CanTurnOffEmail bool     `json:"canTurnOffEmail"`
		Recovery        int      `json:"recovery"`
		NewCodes        []string `json:"newRecoveryCodes"`
	}
	page := func(session *http.Cookie) view {
		var v view
		_ = json.Unmarshal([]byte(jsonOf(securityState(t, w, session))), &v)
		return v
	}

	// A new account: email on, no codes, and nothing to turn it off with.
	stale := w.session(w.owner)
	if v := page(stale); !v.EmailLogin || v.CanTurnOffEmail || v.Recovery != 0 {
		t.Fatalf("initial security: %+v", v)
	}
	if r := post("/account/recovery/create", url.Values{}, stale); r.Code != 403 {
		t.Fatalf("create without confirming: %d", r.Code)
	}
	session := w.session(w.owner)
	a.markConfirmed(session.Value, w.owner.ID, time.Now())
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, session); r.Code != 409 {
		t.Fatalf("off with nothing else: %d", r.Code)
	}
	if r := post("/account/email-login", url.Values{"email_login": {"maybe"}}, session); r.Code != 400 {
		t.Fatalf("bad value: %d", r.Code)
	}

	// Create codes: shown once, stored only as digests, with a notice.
	r := post("/account/recovery/create", url.Values{}, session)
	if r.Code != 200 || noticeOf(r) != "recovery-created" {
		t.Fatalf("create: %d", r.Code)
	}
	expectNotice("a new set of 10 recovery codes")
	codes := page(session).NewCodes
	if len(codes) != recoveryCodeCount {
		t.Fatalf("codes shown: %d", len(codes))
	}
	for _, code := range codes {
		if !recoveryCodePattern.MatchString(code) {
			t.Fatalf("code %q", code)
		}
	}
	if again := page(session); len(again.NewCodes) != 0 || again.Recovery != 10 {
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
	if len(page(other).NewCodes) != 0 {
		t.Fatal("codes shown to another session")
	}

	// Codes alone are not enough to turn email off; an authenticator app too is.
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, session); r.Code != 409 {
		t.Fatalf("off with codes only: %d", r.Code)
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
	if !page(session).CanTurnOffEmail {
		t.Fatal("turn off not offered")
	}
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, stale); r.Code != 403 {
		t.Fatalf("off without confirming: %d", r.Code)
	}
	r = post("/account/email-login", url.Values{"email_login": {"off"}}, session)
	if r.Code != 200 || noticeOf(r) != "email-login-off" {
		t.Fatalf("off: %d", r.Code)
	}
	expectNotice("A sign-in method was removed on your Metatrash account: sign-in with a code emailed to this address")
	if r := post("/account/email-login", url.Values{"email_login": {"off"}}, session); r.Code != 200 || noticeOf(r) != "" {
		t.Fatalf("off again: %d", r.Code)
	}
	// Turning email sign-in off signs out the account's other sessions.
	if _, ok, _ := a.currentUser(ctx, stale.Value); ok {
		t.Fatal("other session still signed in after email sign-in was turned off")
	}
	if _, ok, _ := a.currentUser(ctx, session.Value); !ok {
		t.Fatal("this session signed out")
	}
	stale = w.session(w.owner)
	if ways := confirmWays(t, w, stale); page(stale).EmailLogin || ways["email"] == true || ways["totp"] != true {
		t.Fatal("security with email off")
	}

	// With email off: no code by email, no confirming by email, and the
	// authenticator app cannot be removed.
	if r := post("/account/confirm/send", url.Values{}, stale); r.Code != 403 {
		t.Fatalf("confirm by email: %d", r.Code)
	}
	if r := post("/account/totp/remove", url.Values{}, session); r.Code != 409 {
		t.Fatalf("removed the last method: %d", r.Code)
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
	// A recovery code signs out every other session of the account.
	for _, c := range []*http.Cookie{session, stale} {
		if _, ok, _ := a.currentUser(ctx, c.Value); ok {
			t.Fatal("session survived a recovery code sign-in")
		}
	}
	expectNotice("9 recovery codes are left")
	if r := signIn(w.owner.Email, codes[3]); r.StatusCode != 400 {
		t.Fatalf("code used twice: %d", r.StatusCode)
	}
	if page(signedIn).Recovery != 9 {
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
	if r := post("/account/recovery/create", url.Values{}, signedIn); r.Code != 200 {
		t.Fatalf("recreate: %d", r.Code)
	}
	expectNotice("any older codes no longer work")
	if len(page(signedIn).NewCodes) != recoveryCodeCount {
		t.Fatal("new codes not shown")
	}
	login = responseCookie(oauthCall(w.h, "GET", "/login", nil), loginCookie)
	if r := signIn(w.owner.Email, codes[5]); r.StatusCode != 400 {
		t.Fatalf("old code after recreating: %d", r.StatusCode)
	}

	// Turning email back on lets the last method go again.
	r = post("/account/email-login", url.Values{"email_login": {"on"}}, signedIn)
	if r.Code != 200 || noticeOf(r) != "email-login-on" {
		t.Fatalf("on: %d", r.Code)
	}
	expectNotice("A sign-in method was added on your Metatrash account: sign-in with a code emailed to this address")
	if r := post("/account/totp/remove", url.Values{}, signedIn); r.Code != 200 {
		t.Fatalf("remove with email on: %d", r.Code)
	}
	expectNotice("authenticator app")
}
