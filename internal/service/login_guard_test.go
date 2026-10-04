package service

import (
	"bytes"
	"context"
	"html"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	csrfFieldPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)
	powFieldPattern  = regexp.MustCompile(`name="pow" value="([^"]+)"`)
)

// solvePow does what login.js does in the browser.
func solvePow(challenge string) string {
	for n := 0; ; n++ {
		nonce := strconv.Itoa(n)
		if powZeroBits(challenge, nonce) >= loginPowBits {
			return nonce
		}
	}
}

// loginSendForm fills the "email me a code" form found in a rendered login
// page the way a browser with JavaScript would.
func loginSendForm(t *testing.T, page, email string) url.Values {
	t.Helper()
	csrf, pow := csrfFieldPattern.FindStringSubmatch(page), powFieldPattern.FindStringSubmatch(page)
	if csrf == nil || pow == nil || !strings.Contains(page, `name="website"`) || !strings.Contains(page, "/assets/login.js") {
		t.Fatal("login page lacks the bot-check fields")
	}
	challenge := html.UnescapeString(pow[1])
	return url.Values{"email": {email}, "csrf": {html.UnescapeString(csrf[1])}, "pow": {challenge}, "nonce": {solvePow(challenge)}, "website": {""}}
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	output, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(output); log.SetFlags(flags) })
	return &buf
}

func TestLoginPowChallenge(t *testing.T) {
	s, _, _ := testAccounts(t)
	a := s.accounts
	browser, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	now := time.Now()
	challenge := a.newPowChallenge(browser, now)
	nonce := solvePow(challenge)
	wrong := nonce + "1"
	for powZeroBits(challenge, wrong) >= loginPowBits {
		wrong += "1"
	}
	check := func(want, browser, challenge, nonce string, at time.Time) {
		t.Helper()
		if got, _ := a.checkPow(browser, challenge, nonce, at); got != want {
			t.Fatalf("checkPow = %s, want %s", got, want)
		}
	}
	check("missing", browser, challenge, "", now)
	check("missing", browser, "", nonce, now)
	check("malformed", browser, challenge, "12a", now)
	check("malformed", browser, "x.y", nonce, now)
	check("forged", other, challenge, nonce, now)
	check("forged", "", challenge, nonce, now)
	check("expired", browser, challenge, nonce, now.Add(loginPowLifetime+time.Second))
	check("pass", browser, challenge, nonce, now.Add(3*time.Second))
	check("reused", browser, challenge, nonce, now)
	// A wrong nonce spends the challenge too.
	second := a.newPowChallenge(browser, now)
	check("wrong", browser, second, wrong, now)
	check("reused", browser, second, solvePow(second), now)
	if _, age := a.checkPow(browser, a.newPowChallenge(browser, now.Add(-7*time.Second)), "1", now); age < 6*time.Second || age > 8*time.Second {
		t.Fatalf("age %v", age)
	}
	// Spent challenges are forgotten once they could no longer pass anyway.
	a.mu.Lock()
	a.cleanup(now.Add(loginPowLifetime + time.Minute))
	left := len(a.powUsed)
	a.mu.Unlock()
	if left != 0 {
		t.Fatal("spent challenges kept after expiry")
	}
}

func TestLoginBotChecksHTTP(t *testing.T) {
	logs := captureLog(t)
	s, code, store := testAccounts(t)
	h := &httpAdapter{service: s}
	send := func(form url.Values, ua string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", "https://metatrash.com/login/send", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://metatrash.com")
		r.Header.Set("User-Agent", ua)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		h.serveAccounts(w, r, "198.51.100.7")
		return w
	}
	page := func() (string, *http.Cookie) {
		t.Helper()
		w := httptest.NewRecorder()
		h.serveAccounts(w, httptest.NewRequest("GET", "https://metatrash.com/login", nil), "198.51.100.7")
		if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
			t.Fatal("login.js blocked by CSP: " + csp)
		}
		return w.Body.String(), w.Result().Cookies()[0]
	}
	lastLine := func() string {
		lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
		return lines[len(lines)-1]
	}

	cases := []struct {
		name   string
		edit   func(url.Values)
		pow    string
		hp     string
		status int
	}{
		{"honeypot filled", func(f url.Values) { f.Set("website", "http://spam.example") }, "pass", "filled", 400},
		{"honeypot absent", func(f url.Values) { f.Del("website") }, "pass", "absent", 400},
		{"no javascript", func(f url.Values) { f.Set("nonce", "") }, "missing", "pass", 400},
		{"no pow fields", func(f url.Values) { f.Del("nonce"); f.Del("pow") }, "missing", "pass", 400},
		{"guessed nonce", func(f url.Values) {
			n := 0
			for powZeroBits(f.Get("pow"), strconv.Itoa(n)) >= loginPowBits {
				n++
			}
			f.Set("nonce", strconv.Itoa(n))
		}, "wrong", "pass", 400},
		{"both fail", func(f url.Values) { f.Set("website", "x"); f.Del("nonce") }, "missing", "filled", 400},
	}
	for _, tc := range cases {
		body, cookie := page()
		form := loginSendForm(t, body, "Emily.Bot@Gmail.com")
		tc.edit(form)
		w := send(form, "BotAgent/1.0", cookie)
		if w.Code != tc.status || *code != "" {
			t.Fatalf("%s: status %d, code sent %v", tc.name, w.Code, *code != "")
		}
		line := lastLine()
		for _, want := range []string{"login send ", "ip=198.51.100.7", "email=emily.bot@gmail.com account=none", "honeypot=" + tc.hp, "pow=" + tc.pow, "result=blocked", `ua="BotAgent/1.0"`} {
			if !strings.Contains(line, want) {
				t.Fatalf("%s: log line %q lacks %q", tc.name, line, want)
			}
		}
		if form.Get("pow") != "" && strings.Contains(line, form.Get("pow")) {
			t.Fatalf("%s: log line leaks the challenge: %q", tc.name, line)
		}
		if tc.name == "no javascript" && !strings.Contains(w.Body.String(), "needs JavaScript") {
			t.Fatal("no-JavaScript message missing")
		}
		if tc.name == "honeypot filled" && strings.Contains(w.Body.String(), "Turn it on") {
			t.Fatal("blocked message names a check")
		}
	}
	if s.rates.buckets["mail:global:day"].count != 0 {
		t.Fatal("blocked requests consumed the shared mail budget")
	}

	// Replaying a solved form, and a form from another browser.
	body, cookie := page()
	form := loginSendForm(t, body, "person@example.com")
	if w := send(form, "Mozilla/5.0", cookie); w.Code != 303 || *code == "" {
		t.Fatalf("real browser refused: %d %s", w.Code, w.Body.String())
	}
	if line := lastLine(); !strings.Contains(line, "honeypot=pass pow=pass") || !strings.Contains(line, "result=sent") || !strings.Contains(line, "email=person@example.com account=none ") {
		t.Fatalf("sent line: %q", line)
	}
	sentCode := *code
	*code = ""
	if w := send(form, "Mozilla/5.0", cookie); w.Code != 400 || *code != "" || !strings.Contains(lastLine(), "pow=reused") {
		t.Fatal("replayed proof of work accepted")
	}
	_, stranger := page()
	if w := send(form, "Mozilla/5.0", stranger); !strings.Contains(lastLine(), "pow=forged") || !strings.Contains(lastLine(), "result=bad_csrf") || w.Code != 403 {
		t.Fatalf("other browser: %q", lastLine())
	}
	// Rejections before the checks are logged too.
	r := httptest.NewRequest("POST", "https://metatrash.com/login/send", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.serveAccounts(httptest.NewRecorder(), r, "203.0.113.9")
	if line := lastLine(); !strings.Contains(line, "ip=203.0.113.9") || !strings.Contains(line, "result=bad_origin") || !strings.Contains(line, "honeypot=- pow=-") {
		t.Fatalf("bad origin line: %q", line)
	}
	extra := url.Values{"email": {"a@b.co"}, "csrf": {cookie.Value}, "phone": {"1"}}
	send(extra, "x", cookie)
	if !strings.Contains(lastLine(), "result=bad_form") {
		t.Fatalf("bad form line: %q", lastLine())
	}

	// Verify is logged with the address the code went to.
	verify := func(code string) {
		t.Helper()
		r := httptest.NewRequest("POST", "https://metatrash.com/login/verify", strings.NewReader(url.Values{"code": {code}, "csrf": {cookie.Value}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://metatrash.com")
		r.AddCookie(cookie)
		h.serveAccounts(httptest.NewRecorder(), r, "198.51.100.7")
	}
	verify("000000")
	if sentCode == "000000" {
		t.Skip("random code collided with the wrong guess")
	}
	if line := lastLine(); !strings.HasPrefix(line, "login verify ") || !strings.Contains(line, "email=person@example.com account=none ") || !strings.Contains(line, "result=invalid_code") || strings.Contains(line, "pow=") {
		t.Fatalf("bad verify line: %q", line)
	}
	verify(sentCode)
	if line := lastLine(); !strings.Contains(line, "result=ok") || !strings.Contains(line, "email=person@example.com account=new ") || strings.Contains(line, sentCode) {
		t.Fatalf("verify line: %q", line)
	}
	if _, ok := store.users["person@example.com"]; !ok {
		t.Fatal("verified account missing")
	}
	// The address now has an account; the next request says so (and is
	// throttled by the one-a-minute limit).
	body, cookie = page()
	send(loginSendForm(t, body, "Person@Example.com"), "Mozilla/5.0", cookie)
	if line := lastLine(); !strings.Contains(line, "email=person@example.com account=existing ") || !strings.Contains(line, "result=rate_limited") {
		t.Fatalf("existing account line: %q", line)
	}
	store.failure = context.DeadlineExceeded
	body, cookie = page()
	send(loginSendForm(t, body, "x@example.com"), "Mozilla/5.0", cookie)
	if line := lastLine(); !strings.Contains(line, "account=unknown") {
		t.Fatalf("database outage line: %q", line)
	}
	store.failure = nil
	_, cookie = page() // a browser with no code waiting
	verify("123456")
	if line := lastLine(); !strings.Contains(line, "email=- ") || !strings.Contains(line, "result=invalid_code") {
		t.Fatalf("no-code verify line: %q", line)
	}
}
