package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Testing an invitation in one browser: signed in as the owner, open the
// invited address's email in webmail, follow its link, ask for a code.
func TestCrossSiteLinkAndSwitchingAccounts(t *testing.T) {
	s, code, _ := testAccounts(t)
	h := &httpAdapter{service: s}
	call := func(method, path, fetchSite string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://metatrash.com"+path, strings.NewReader(form.Encode()))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://metatrash.com")
		}
		if fetchSite != "" {
			r.Header.Set("Sec-Fetch-Site", fetchSite)
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
	cookie := func(w *httptest.ResponseRecorder, name string) *http.Cookie {
		for _, c := range w.Result().Cookies() {
			if c.Name == name {
				return c
			}
		}
		return nil
	}
	signIn := func(browser *http.Cookie, email string, extra ...*http.Cookie) {
		t.Helper()
		form := loginSendForm(t, call("GET", "/login", "", nil, browser).Body.String(), email)
		if w := call("POST", "/login/send", "", form, append([]*http.Cookie{browser}, extra...)...); w.Code != 303 || w.Header().Get("Location") != "/login" {
			t.Fatalf("send to %s: %d", email, w.Code)
		}
	}
	browser := cookie(call("GET", "/login", "", nil), loginCookie)
	signIn(browser, "owner@example.com")
	w := call("POST", "/login/verify", "", url.Values{"code": {*code}, "csrf": {browser.Value}}, browser)
	owner := cookie(w, sessionCookie)
	if w.Code != 303 || owner == nil {
		t.Fatalf("owner sign-in: %d", w.Code)
	}

	// A link from another site arrives without the Strict cookies. Instead
	// of sending a signed-in browser to sign in, the page reloads itself from
	// this site, which sends them.
	for _, path := range []string{"/account", "/account/shared", "/login"} {
		w := call("GET", path, "cross-site", nil)
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(body, `http-equiv="refresh"`) || !strings.Contains(body, `href="`+path+`">Continue`) {
			t.Fatalf("cross-site %s: %d\n%s", path, w.Code, body)
		}
	}
	// The reload is same-origin: with the cookie it is the account, without
	// it sign-in as before. Cookies sent cross-site (a future Lax cookie)
	// skip the reload.
	if w := call("GET", "/account", "same-origin", nil, owner); (w.Code != 200 && w.Code != 503) || !strings.Contains(w.Body.String(), "owner@example.com") {
		t.Fatalf("reloaded account: %d", w.Code)
	}
	if w := call("GET", "/account", "same-origin", nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("signed-out reload: %d", w.Code)
	}
	if w := call("GET", "/account", "cross-site", nil, owner); strings.Contains(w.Body.String(), `http-equiv="refresh"`) {
		t.Fatal("reload with the session cookie present")
	}
	// Without a code waiting, a signed-in browser goes from sign-in to its account.
	if w := call("GET", "/login", "", nil, browser, owner); w.Code != 303 || w.Header().Get("Location") != "/account" {
		t.Fatalf("signed-in /login: %d", w.Code)
	}

	// Asking for a code for another address while signed in shows where to
	// enter it, saying it switches accounts, instead of the old account.
	signIn(browser, "guest@example.com", owner)
	w = call("GET", "/login", "", nil, browser, owner)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `action="/login/verify"`) || !strings.Contains(body, "This browser is signed in as") || !strings.Contains(body, "owner@example.com") || !strings.Contains(body, "guest@example.com") {
		t.Fatalf("switch page: %d\n%s", w.Code, body)
	}
	w = call("POST", "/login/verify", "", url.Values{"code": {*code}, "csrf": {browser.Value}}, browser, owner)
	guest := cookie(w, sessionCookie)
	if w.Code != 303 || guest == nil || guest.Value == owner.Value {
		t.Fatalf("switch verify: %d", w.Code)
	}
	if user, ok, _ := s.accounts.currentUser(t.Context(), guest.Value); !ok || user.Email != "guest@example.com" {
		t.Fatalf("switched to %q", user.Email)
	}
	if _, ok, _ := s.accounts.currentUser(t.Context(), owner.Value); ok {
		t.Fatal("previous session still signed in after switching")
	}
}
