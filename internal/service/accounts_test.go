package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Owner-run focused checks, with fake mail only; no SMTP network connection.
func testAccounts(t *testing.T) (*Service, *string, string) {
	t.Helper()
	dir := t.TempDir()
	passwordPath := filepath.Join(dir, "smtp-password")
	if err := os.WriteFile(passwordPath, []byte("test-only"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := accountConfig{Origin: "https://metatrash.com", SMTPHost: "smtp.gmail.com", SMTPPort: 587, SMTPUsername: "sender@example.com", SMTPFrom: "sender@example.com", SMTPPasswordFile: passwordPath}
	b, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := &Service{lockPath: filepath.Join(dir, ".service-lock"), rates: limiter{buckets: map[string]bucket{}}}
	if err := s.EnableAccounts(path); err != nil {
		t.Fatal(err)
	}
	code := new(string)
	s.accounts.send = func(_ context.Context, _, value string) error { *code = value; return nil }
	return s, code, path
}

func TestAccountsCodes(t *testing.T) {
	s, code, configPath := testAccounts(t)
	a := s.accounts
	browser := strings.Repeat("a", 64)
	email := "person@example.com"
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	if len(a.users) != 0 {
		t.Fatal("unverified email created an account")
	}
	original := *code
	for i := 0; i < 5; i++ {
		if _, err := a.verify(browser, "wrong"); err == nil {
			t.Fatal("bad code accepted")
		}
	}
	if _, err := a.verify(browser, original); err == nil {
		t.Fatal("code accepted after attempt limit")
	}
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	c := a.challenges[secretDigest(browser)]
	c.Expires = time.Now().Add(-time.Second)
	a.challenges[secretDigest(browser)] = c
	if _, err := a.verify(browser, *code); err == nil {
		t.Fatal("expired code accepted")
	}
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	token, err := a.verify(browser, *code)
	if err != nil {
		t.Fatal(err)
	}
	user, ok := a.currentUser(token)
	if !ok || user.Email != email || user.MaxPrivateSpaces != 1 {
		t.Fatal("missing verified account or incorrect allowance")
	}
	if _, err := a.verify(browser, *code); err == nil {
		t.Fatal("code replay accepted")
	}
	restarted := &Service{lockPath: s.lockPath}
	if err := restarted.EnableAccounts(configPath); err != nil {
		t.Fatal(err)
	}
	if restarted.accounts.users[email].ID != user.ID {
		t.Fatal("account not persisted")
	}
	if _, ok := restarted.accounts.currentUser(token); ok {
		t.Fatal("session survived restart")
	}
	// Signing in again must preserve admin overrides and account identity.
	saved := a.users[email]
	saved.MaxPrivateSpaces = 3
	a.users[email] = saved
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	if _, err := a.verify(browser, *code); err != nil {
		t.Fatal(err)
	}
	if a.users[email].ID != user.ID || a.users[email].MaxPrivateSpaces != 3 {
		t.Fatal("existing account overwritten")
	}
	a.send = func(context.Context, string, string) error { return context.DeadlineExceeded }
	if err := a.issue(context.Background(), browser, email); err == nil {
		t.Fatal("mail failure hidden")
	}
	if _, exists := a.challenges[secretDigest(browser)]; exists {
		t.Fatal("failed mail left usable challenge")
	}
}

func TestAccountsHTTP(t *testing.T) {
	s, code, _ := testAccounts(t)
	h := &httpAdapter{service: s}
	call := func(method, path, origin string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://metatrash.com"+path, strings.NewReader(form.Encode()))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", origin)
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		if !h.serveAccounts(w, r, "test-client") {
			t.Fatal("route not handled")
		}
		return w
	}
	w := call("GET", "/login", "", nil)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("login page policy prevents browser form origins")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing login cookie")
	}
	browser := cookies[0]
	if !browser.Secure || !browser.HttpOnly || browser.SameSite != http.SameSiteStrictMode || browser.Path != "/" || browser.Domain != "" {
		t.Fatal("unsafe cookie")
	}
	form := url.Values{"email": {"person@example.com"}, "csrf": {browser.Value}}
	if w := call("POST", "/login/send", "https://other.example", form, browser); w.Code != 403 || *code != "" {
		t.Fatal("cross-origin send accepted")
	}
	for _, origin := range []string{"", "null"} {
		if w := call("POST", "/login/send", origin, form, browser); w.Code != 403 || *code != "" {
			t.Fatal("missing or null origin accepted")
		}
	}
	form.Set("csrf", "wrong")
	if w := call("POST", "/login/send", "https://metatrash.com", form, browser); w.Code != 403 {
		t.Fatal("invalid CSRF accepted")
	}
	form.Set("csrf", browser.Value)
	if w := call("POST", "/login/send", "https://metatrash.com", form, browser); w.Code != 303 || *code == "" {
		t.Fatal("code not sent")
	}
	if w := call("POST", "/login/send", "https://metatrash.com", form, browser); w.Code != 429 {
		t.Fatal("resend not throttled")
	}
	w = call("POST", "/login/verify", "https://metatrash.com", url.Values{"code": {*code}, "csrf": {browser.Value}}, browser)
	if w.Code != 303 || w.Header().Get("Location") != "/account" {
		t.Fatal("login failed")
	}
	var session *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == sessionCookie {
			session = cookie
		}
	}
	if session == nil {
		t.Fatal("missing session")
	}
	if w := call("GET", "/account", "", nil, session); w.Code != 200 || !strings.Contains(w.Body.String(), "person@example.com") {
		t.Fatal("account unavailable")
	}
	if w := call("POST", "/logout", "https://metatrash.com", url.Values{"csrf": {s.accounts.mac("logout:" + session.Value)}}, session); w.Code != 303 {
		t.Fatal("logout failed")
	}
	if w := call("GET", "/account", "", nil, session); w.Code != 303 {
		t.Fatal("logged-out session accepted")
	}
}
