package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Owner-run focused checks use fake mail and storage; SQL cutover checks are
// documented separately in docs/account-database.md.
type memoryAccountStore struct {
	users   map[string]userAccount
	failure error
}

func (s *memoryAccountStore) FindOrCreate(_ context.Context, email string) (userAccount, error) {
	if s.failure != nil {
		return userAccount{}, s.failure
	}
	if user, ok := s.users[email]; ok {
		return user, nil
	}
	id, err := randomHex(16)
	if err != nil {
		return userAccount{}, err
	}
	user := userAccount{ID: id, Email: email, CreatedAt: time.Now().UTC(), MaxPrivateSpaces: 1}
	s.users[email] = user
	return user, nil
}

func (s *memoryAccountStore) ByID(_ context.Context, id string) (userAccount, bool, error) {
	if s.failure != nil {
		return userAccount{}, false, s.failure
	}
	for _, user := range s.users {
		if user.ID == id {
			return user, true, nil
		}
	}
	return userAccount{}, false, nil
}

func (s *memoryAccountStore) Close() error { return nil }

func (s *memoryAccountStore) ChooseUsername(_ context.Context, id, value string) error {
	if s.failure != nil {
		return s.failure
	}
	username, err := normalizeUsername(value)
	if err != nil {
		return err
	}
	for _, user := range s.users {
		if user.Username == username {
			return problem(409, "conflict", "Username taken.")
		}
	}
	for email, user := range s.users {
		if user.ID == id && user.Username == "" {
			user.Username = username
			s.users[email] = user
			return nil
		}
	}
	return problem(409, "conflict", "Username already selected.")
}

func testAccounts(t *testing.T) (*Service, *string, *memoryAccountStore) {
	t.Helper()
	store := &memoryAccountStore{users: map[string]userAccount{}}
	s := &Service{rates: limiter{buckets: map[string]bucket{}}}
	s.accounts = &accounts{config: accountConfig{Origin: "https://metatrash.com"}, store: store,
		challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{},
		secret: []byte("test-only-secret"), mailSlots: make(chan struct{}, 2)}
	code := new(string)
	s.accounts.send = func(_ context.Context, _, value string) error { *code = value; return nil }
	return s, code, store
}

func TestAccountsCodes(t *testing.T) {
	s, code, store := testAccounts(t)
	a := s.accounts
	browser := strings.Repeat("a", 64)
	email := "person@example.com"
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	if len(store.users) != 0 {
		t.Fatal("unverified email created an account")
	}
	original := *code
	for i := 0; i < 5; i++ {
		if _, err := a.verify(context.Background(), browser, "wrong"); err == nil {
			t.Fatal("bad code accepted")
		}
	}
	if _, err := a.verify(context.Background(), browser, original); err == nil {
		t.Fatal("code accepted after attempt limit")
	}
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	c := a.challenges[secretDigest(browser)]
	c.Expires = time.Now().Add(-time.Second)
	a.challenges[secretDigest(browser)] = c
	if _, err := a.verify(context.Background(), browser, *code); err == nil {
		t.Fatal("expired code accepted")
	}
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	token, err := a.verify(context.Background(), browser, *code)
	if err != nil {
		t.Fatal(err)
	}
	user, ok, err := a.currentUser(context.Background(), token)
	if err != nil || !ok || user.Email != email || user.MaxPrivateSpaces != 1 {
		t.Fatal("missing verified account or incorrect allowance")
	}
	if _, err := a.verify(context.Background(), browser, *code); err == nil {
		t.Fatal("code replay accepted")
	}
	restarted, _, _ := testAccounts(t)
	restarted.accounts.store = store
	if _, ok, _ := restarted.accounts.currentUser(context.Background(), token); ok {
		t.Fatal("session survived restart")
	}
	// Signing in again must preserve admin overrides and account identity.
	saved := store.users[email]
	saved.MaxPrivateSpaces = 3
	store.users[email] = saved
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	if _, err := a.verify(context.Background(), browser, *code); err != nil {
		t.Fatal(err)
	}
	if store.users[email].ID != user.ID || store.users[email].MaxPrivateSpaces != 3 {
		t.Fatal("existing account overwritten")
	}
	// A session follows the immutable ID even if the account email changes.
	delete(store.users, email)
	saved.Email = "renamed@example.com"
	store.users[saved.Email] = saved
	if current, ok, err := a.currentUser(context.Background(), token); err != nil || !ok || current.ID != user.ID || current.Email != saved.Email {
		t.Fatal("session identity depends on email")
	}
	store.failure = context.DeadlineExceeded
	if _, ok, err := a.currentUser(context.Background(), token); err == nil || ok {
		t.Fatal("database outage treated as a successful account read")
	}
	if err := a.issue(context.Background(), browser, email); err != nil {
		t.Fatal(err)
	}
	if _, err := a.verify(context.Background(), browser, *code); err == nil {
		t.Fatal("database failure issued a session")
	}
	if _, exists := a.challenges[secretDigest(browser)]; exists {
		t.Fatal("database failure left a reusable code")
	}
	store.failure = nil
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
	if s.rates.buckets["mail:global:day"].count != 1 {
		t.Fatal("rejected resend consumed shared mail budget")
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
	// This harness has no owned-space database, so the page reports spaces as
	// temporarily unavailable (503) while still rendering the signed-in account.
	if w := call("GET", "/account", "", nil, session); (w.Code != 200 && w.Code != 503) || !strings.Contains(w.Body.String(), "person@example.com") {
		t.Fatal("account unavailable")
	}
	usernameForm := url.Values{"username": {"Dave-C"}, "csrf": {s.accounts.mac("username:" + session.Value)}}
	if w := call("POST", "/account/username", "https://other.example", usernameForm, session); w.Code != 403 {
		t.Fatal("cross-origin username selection accepted")
	}
	usernameForm.Set("csrf", s.accounts.mac("logout:"+session.Value))
	if w := call("POST", "/account/username", "https://metatrash.com", usernameForm, session); w.Code != 403 {
		t.Fatal("logout token accepted for username selection")
	}
	usernameForm.Set("csrf", s.accounts.mac("username:"+session.Value))
	if w := call("POST", "/account/username", "https://metatrash.com", usernameForm, session); w.Code != 303 {
		t.Fatal("username selection failed")
	}
	if w := call("GET", "/account", "", nil, session); !strings.Contains(w.Body.String(), "<strong>dave-c</strong>") || strings.Contains(w.Body.String(), "Save permanent username") {
		t.Fatal("selected username not shown as fixed")
	}
	usernameForm.Set("username", "another-name")
	// 409 in production; this harness reports 503 because spaces are unavailable.
	if w := call("POST", "/account/username", "https://metatrash.com", usernameForm, session); (w.Code != 409 && w.Code != 503) || !strings.Contains(w.Body.String(), "Username already selected.") {
		t.Fatal("permanent username replaced")
	}
	if w := call("POST", "/logout", "https://metatrash.com", url.Values{"csrf": {s.accounts.mac("logout:" + session.Value)}}, session); w.Code != 303 {
		t.Fatal("logout failed")
	}
	if w := call("GET", "/account", "", nil, session); w.Code != 303 {
		t.Fatal("logged-out session accepted")
	}
	if w := call("POST", "/account/username", "https://metatrash.com", usernameForm, session); w.Code != 303 {
		t.Fatal("logged-out username request not redirected")
	}
}

func TestUsernameValidation(t *testing.T) {
	for _, value := range []string{"ab", "a--b", "-abc", "abc-", "a_b", "a/b", "dávé", "public", " ADMIN ", strings.Repeat("a", 33)} {
		if _, err := normalizeUsername(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
	if value, err := normalizeUsername(" Dave-C "); err != nil || value != "dave-c" {
		t.Fatal("normalization failed")
	}
}
