package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// RFC 6238 appendix B (SHA-1), last six digits.
func TestTOTPCodeVectors(t *testing.T) {
	secret := []byte("12345678901234567890")
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		if got := totpCode(secret, unix/totpPeriod); got != want {
			t.Errorf("T=%d: %s, want %s", unix, got, want)
		}
	}
}

func TestTOTPMatchWindow(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1234567890, 0)
	step := now.Unix() / totpPeriod
	for _, d := range []int64{-1, 0, 1} {
		if got, ok := totpMatch(secret, totpCode(secret, step+d), now, 0); !ok || got != step+d {
			t.Fatalf("step %+d: %d %v", d, got, ok)
		}
	}
	for _, d := range []int64{-2, 2} {
		if _, ok := totpMatch(secret, totpCode(secret, step+d), now, 0); ok {
			t.Fatalf("step %+d accepted", d)
		}
	}
	// A step already used, or an earlier one, is refused.
	if _, ok := totpMatch(secret, totpCode(secret, step), now, step); ok {
		t.Fatal("used step accepted")
	}
	if _, ok := totpMatch(secret, totpCode(secret, step-1), now, step); ok {
		t.Fatal("earlier step accepted")
	}
	if got, ok := totpMatch(secret, totpCode(secret, step+1), now, step); !ok || got != step+1 {
		t.Fatal("next step refused")
	}
	for _, bad := range []string{"", "12345", "1234567", "12345a", " 12345", "١٢٣٤٥٦"} {
		if _, ok := totpMatch(secret, bad, now, 0); ok {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func testTOTPKey(t *testing.T) cipher.AEAD {
	t.Helper()
	block, _ := aes.NewCipher(make([]byte, 32))
	aead, _ := cipher.NewGCM(block)
	return aead
}

func TestTOTPSealing(t *testing.T) {
	aead := testTOTPKey(t)
	secret := []byte("abcdefghijklmnopqrst")
	sealed, err := sealTOTP(aead, "user-a", secret)
	if err != nil || len(sealed) > 256 {
		t.Fatal(err, len(sealed))
	}
	if got, err := openTOTP(aead, "user-a", sealed); err != nil || string(got) != string(secret) {
		t.Fatal("round trip", err)
	}
	if _, err := openTOTP(aead, "user-b", sealed); err == nil {
		t.Fatal("opened for another account")
	}
	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := openTOTP(aead, "user-a", tampered); err == nil {
		t.Fatal("tampered secret opened")
	}
	tampered = append([]byte{}, sealed...)
	tampered[0] = 2
	if _, err := openTOTP(aead, "user-a", tampered); err == nil {
		t.Fatal("unknown version opened")
	}
	if _, err := openTOTP(aead, "user-a", sealed[:10]); err == nil {
		t.Fatal("short value opened")
	}
	again, _ := sealTOTP(aead, "user-a", secret)
	if string(again) == string(sealed) {
		t.Fatal("nonce reused")
	}
}

func TestTOTPKeyFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	_ = os.WriteFile(good, []byte(strings.Repeat("ab", 32)+"\n"), 0600)
	if _, err := loadTOTPKey(good); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"short": strings.Repeat("ab", 16), "nothex": strings.Repeat("zz", 32), "empty": ""} {
		path := filepath.Join(dir, name)
		_ = os.WriteFile(path, []byte(content), 0600)
		if _, err := loadTOTPKey(path); err == nil {
			t.Fatalf("%s key accepted", name)
		}
	}
	if _, err := loadTOTPKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key file accepted")
	}
}

func TestTOTPURIAndSecret(t *testing.T) {
	secret := []byte("12345678901234567890")
	if got := totpURI("dave+x@example.com", secret); got != "otpauth://totp/Metatrash:dave+x@example.com?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Metatrash" {
		t.Fatal(got)
	}
	if got := groupedSecret(secret); got != "GEZD GNBV GY3T QOJQ GEZD GNBV GY3T QOJQ" {
		t.Fatal(got)
	}
}

// The encoder's output was checked with a reference decoder (zxing-cpp) for
// 1 to 2000 bytes, versions 1 to 38. These pin the version choice and the
// fixed parts of the symbol.
func TestQRCodeStructure(t *testing.T) {
	for n, version := range map[int]int{1: 1, 14: 1, 15: 2, 100: 6, 213: 10, 331: 13, 2000: 38} {
		q, err := encodeQR([]byte(strings.Repeat("x", n)))
		if err != nil || q.size != version*4+17 {
			t.Fatalf("%d bytes: version %d, want %d (%v)", n, (q.size-17)/4, version, err)
		}
		// Finder pattern corners and the always-dark module.
		for _, c := range [][2]int{{0, 0}, {q.size - 7, 0}, {0, q.size - 7}} {
			if !q.dark[c[1]][c[0]] || !q.dark[c[1]+3][c[0]+3] || q.dark[c[1]+1][c[0]+1] {
				t.Fatalf("%d bytes: finder at %v", n, c)
			}
		}
		if !q.dark[q.size-8][8] {
			t.Fatal("dark module missing")
		}
	}
	if _, err := encodeQR(make([]byte, 2400)); err == nil {
		t.Fatal("oversized data encoded")
	}
	q, _ := encodeQR([]byte("otpauth://totp/Metatrash:a@b.c?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Metatrash"))
	svg := q.svg()
	if !strings.HasPrefix(svg, `<svg class="qr"`) || !strings.HasSuffix(svg, `"/></svg>`) || strings.ContainsAny(svg[strings.Index(svg, ` d="`)+4:len(svg)-9], `"<>`) {
		t.Fatal(svg)
	}
}

var setupKeyPattern = regexp.MustCompile(`<code class="totp-secret">([A-Z2-7 ]+)</code>`)

func TestAuthenticatorAppAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	a.totpKey = testTOTPKey(t)
	notices := make(chan string, 8)
	a.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
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
	csrf := func(path string, session *http.Cookie) string { return a.signInCSRF(path, session.Value) }

	// Without a recent confirmation, setup is not offered or accepted.
	stale := w.session(w.owner)
	body := oauthCall(w.h, "GET", "/account/security", nil, stale).Body.String()
	if !strings.Contains(body, "To set up an authenticator app, confirm it’s you above.") || strings.Contains(body, "/account/totp/start") || strings.Contains(body, "/account/confirm/totp") {
		t.Fatal("setup offered without confirming")
	}
	if res := oauthCall(w.h, "POST", "/account/totp/start", url.Values{"csrf": {csrf("/account/totp/start", stale)}}, stale); res.Code != 403 {
		t.Fatalf("start without confirming: %d", res.Code)
	}

	// Start setup on a fresh session: the page shows a QR code and the key.
	session := w.session(w.owner)
	a.markConfirmed(session.Value, w.owner.ID, time.Now())
	body = oauthCall(w.h, "GET", "/account/security", nil, session).Body.String()
	res := oauthCall(w.h, "POST", "/account/totp/start", url.Values{"csrf": {formCSRF(t, body, "/account/totp/start")}}, session)
	if res.Code != 303 || res.Header().Get("Location") != "/account/security" {
		t.Fatalf("start: %d %s", res.Code, res.Body)
	}
	page := oauthCall(w.h, "GET", "/account/security", nil, session)
	body = page.Body.String()
	m := setupKeyPattern.FindStringSubmatch(body)
	if m == nil || !strings.Contains(body, `<svg class="qr"`) || page.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("setup page without QR code or key, or cacheable")
	}
	secret, err := totpEncoding.DecodeString(strings.ReplaceAll(m[1], " ", ""))
	if err != nil || len(secret) != totpSecretBytes {
		t.Fatal("setup key", err)
	}
	record, ok, err := w.db.totp(ctx, w.owner.ID)
	if err != nil || !ok || record.EnabledAt != 0 || strings.Contains(string(record.Sealed), string(secret)) {
		t.Fatal("stored setup", err)
	}
	// Starting again replaces the unfinished setup with a new key.
	res = oauthCall(w.h, "POST", "/account/totp/start", url.Values{"csrf": {csrf("/account/totp/start", session)}}, session)
	if res.Code != 303 {
		t.Fatalf("restart: %d", res.Code)
	}
	body = oauthCall(w.h, "GET", "/account/security", nil, session).Body.String()
	m = setupKeyPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no setup key after restart")
	}
	newSecret, _ := totpEncoding.DecodeString(strings.ReplaceAll(m[1], " ", ""))
	if string(newSecret) == string(secret) {
		t.Fatal("restart kept the key")
	}
	secret = newSecret

	// A wrong code does not enable it; the right one does, with a notice.
	now := time.Now()
	step := now.Unix() / totpPeriod
	wrong := totpCode(secret, step+5)
	enable := url.Values{"csrf": {formCSRF(t, body, "/account/totp/enable")}, "code": {wrong}}
	if res = oauthCall(w.h, "POST", "/account/totp/enable", enable, session); res.Code != 400 || !strings.Contains(res.Body.String(), "That code is not right") {
		t.Fatalf("wrong code: %d", res.Code)
	}
	enable.Set("code", totpCode(secret, step))
	if res = oauthCall(w.h, "POST", "/account/totp/enable", enable, stale); res.Code != 403 {
		t.Fatalf("enable from a stale session: %d", res.Code)
	}
	res = oauthCall(w.h, "POST", "/account/totp/enable", enable, session)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "totp-added" {
		t.Fatalf("enable: %d %s", res.Code, res.Body)
	}
	expectNotice("A sign-in method was added on your Metatrash account: authenticator app")
	body = oauthCall(w.h, "GET", "/account/security", nil, session).Body.String()
	if setupKeyPattern.MatchString(body) || strings.Contains(body, `<svg class="qr"`) || !strings.Contains(body, "/account/totp/remove") {
		t.Fatal("key still shown after setup, or no remove")
	}
	if res = oauthCall(w.h, "POST", "/account/totp/start", url.Values{"csrf": {csrf("/account/totp/start", session)}}, session); res.Code != 409 {
		t.Fatalf("second setup: %d", res.Code)
	}

	// Sign in with the email address and a code. The first code is spent.
	login := oauthCall(w.h, "GET", "/login", nil)
	browser := responseCookie(login, loginCookie)
	if !strings.Contains(login.Body.String(), `action="/login/totp"`) {
		t.Fatal("login page does not offer the authenticator app")
	}
	signIn := func(email, code string) *http.Response {
		r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {email}, "code": {code}}, browser)
		return r.Result()
	}
	used := signIn(strings.ToUpper(w.owner.Email), totpCode(secret, step))
	if used.StatusCode != 400 {
		t.Fatalf("spent code signed in: %d", used.StatusCode)
	}
	// The member has no authenticator app and an unknown address has no
	// account: both get the same answer as a wrong code.
	answers := map[string]string{}
	for name, email := range map[string]string{"none": w.member.Email, "unknown": randomName(t, "nobody-") + "@example.com"} {
		r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {email}, "code": {totpCode(secret, step+1)}}, browser)
		answers[name] = r.Body.String()
		if r.Code != 400 || !strings.Contains(answers[name], "That code did not work for that email address") || !strings.Contains(answers[name], `<details class="resend" open>`) {
			t.Fatalf("%s: %d", name, r.Code)
		}
		if strings.Contains(answers[name], w.member.ID) || strings.Contains(answers[name], w.owner.ID) {
			t.Fatal("answer names an account")
		}
	}
	ok2 := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {w.owner.Email}, "code": {totpCode(secret, step+1)}}, browser)
	signedIn := responseCookie(ok2, sessionCookie)
	if ok2.Code != 303 || ok2.Header().Get("Location") != "/account" || signedIn == nil {
		t.Fatalf("sign in: %d %s", ok2.Code, ok2.Body)
	}
	if user, ok, _ := a.currentUser(ctx, signedIn.Value); !ok || user.ID != w.owner.ID {
		t.Fatal("wrong session")
	}
	if fresh, _ := a.sessionFresh(signedIn.Value, time.Now()); !fresh {
		t.Fatal("authenticator sign-in is not fresh")
	}
	// Bad CSRF and an invalid address.
	if r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {strings.Repeat("0", 64)}, "email": {w.owner.Email}, "code": {"123456"}}, browser); r.Code != 403 {
		t.Fatalf("bad CSRF: %d", r.Code)
	}
	if r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {"not an address"}, "code": {"123456"}}, browser); r.Code != 400 || !strings.Contains(r.Body.String(), "valid email") {
		t.Fatalf("invalid address: %d", r.Code)
	}

	// Five attempts per address per 15 minutes, counted whether or not the
	// address has an authenticator app.
	limited := randomName(t, "limited-") + "@example.com"
	for i := range 6 {
		r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {limited}, "code": {"000000"}}, browser)
		if (i < 5 && r.Code != 400) || (i == 5 && r.Code != 429) {
			t.Fatalf("attempt %d: %d", i+1, r.Code)
		}
	}

	// Confirm it's you with the app on a stale session. Give it an unused
	// step first (both codes in the window were spent above).
	if _, err := w.db.db.ExecContext(ctx, "UPDATE metatrash_totp SET last_used_step = ? WHERE user_id = ?", step-5, w.owner.ID); err != nil {
		t.Fatal(err)
	}
	body = oauthCall(w.h, "GET", "/account/security", nil, stale).Body.String()
	if !strings.Contains(body, "your authenticator app or a code we email") {
		t.Fatal("confirm with the app not offered")
	}
	if res = oauthCall(w.h, "POST", "/account/totp/remove", url.Values{"csrf": {csrf("/account/totp/remove", stale)}}, stale); res.Code != 403 {
		t.Fatalf("remove without confirming: %d", res.Code)
	}
	if res = oauthCall(w.h, "POST", "/account/confirm/totp", url.Values{"csrf": {formCSRF(t, body, "/account/confirm/totp")}, "code": {wrong}}, stale); res.Code != 400 {
		t.Fatalf("confirm with a wrong code: %d", res.Code)
	}
	res = oauthCall(w.h, "POST", "/account/confirm/totp", url.Values{"csrf": {formCSRF(t, body, "/account/confirm/totp")}, "code": {totpCode(secret, time.Now().Unix()/totpPeriod)}}, stale)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "signin-confirmed" {
		t.Fatalf("confirm with the app: %d %s", res.Code, res.Body)
	}
	// The member cannot confirm with an app they do not have.
	member := w.session(w.member)
	if res = oauthCall(w.h, "POST", "/account/confirm/totp", url.Values{"csrf": {csrf("/account/confirm/totp", member)}, "code": {"123456"}}, member); res.Code != 400 {
		t.Fatalf("member confirm: %d", res.Code)
	}

	// Remove it: a notice, and its codes no longer sign in.
	res = oauthCall(w.h, "POST", "/account/totp/remove", url.Values{"csrf": {csrf("/account/totp/remove", stale)}}, stale)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "totp-removed" {
		t.Fatalf("remove: %d %s", res.Code, res.Body)
	}
	expectNotice("A sign-in method was removed on your Metatrash account: authenticator app")
	if _, ok, _ := w.db.totp(ctx, w.owner.ID); ok {
		t.Fatal("not removed")
	}
	login = oauthCall(w.h, "GET", "/login", nil)
	browser = responseCookie(login, loginCookie)
	if r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {w.owner.Email}, "code": {totpCode(secret, time.Now().Unix()/totpPeriod+1)}}, browser); r.Code != 400 {
		t.Fatalf("removed app signed in: %d", r.Code)
	}

	// An unfinished setup can be cancelled without confirming, quietly.
	a.markConfirmed(member.Value, w.member.ID, time.Now())
	if res = oauthCall(w.h, "POST", "/account/totp/start", url.Values{"csrf": {csrf("/account/totp/start", member)}}, member); res.Code != 303 {
		t.Fatalf("member start: %d", res.Code)
	}
	staleMember := w.session(w.member)
	res = oauthCall(w.h, "POST", "/account/totp/remove", url.Values{"csrf": {csrf("/account/totp/remove", staleMember)}}, staleMember)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "totp-cancelled" {
		t.Fatalf("cancel: %d %s", res.Code, res.Body)
	}
	select {
	case n := <-notices:
		t.Fatalf("notice for a cancelled setup: %q", n)
	case <-time.After(200 * time.Millisecond):
	}
	if res = oauthCall(w.h, "POST", "/account/totp/remove", url.Values{"csrf": {csrf("/account/totp/remove", member)}}, member); res.Code != 404 {
		t.Fatalf("remove when there is none: %d", res.Code)
	}
}

// Without totpKeyFile the authenticator app is neither offered nor accepted.
func TestAuthenticatorAppOffWithoutKey(t *testing.T) {
	w := newOAuthWorld(t)
	session := w.session(w.owner)
	w.s.accounts.markConfirmed(session.Value, w.owner.ID, time.Now())
	body := oauthCall(w.h, "GET", "/account/security", nil, session).Body.String()
	if strings.Contains(body, "Authenticator app") || strings.Contains(body, "/account/totp/") {
		t.Fatal("authenticator app offered without a key")
	}
	if res := oauthCall(w.h, "POST", "/account/totp/start", url.Values{"csrf": {w.s.accounts.signInCSRF("/account/totp/start", session.Value)}}, session); res.Code != 404 {
		t.Fatalf("start without a key: %d", res.Code)
	}
	login := oauthCall(w.h, "GET", "/login", nil)
	browser := responseCookie(login, loginCookie)
	if strings.Contains(login.Body.String(), "/login/totp") {
		t.Fatal("login offers the authenticator app without a key")
	}
	if r := oauthCall(w.h, "POST", "/login/totp", url.Values{"csrf": {browser.Value}, "email": {w.owner.Email}, "code": {"123456"}}, browser); r.Code != 503 {
		t.Fatalf("sign in without a key: %d", r.Code)
	}
}
