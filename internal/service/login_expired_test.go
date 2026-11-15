package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A sign-in page left open past its login cookie (20 minutes) used to answer
// its forms with a JSON error. It now shows the sign-in page again, with a
// note and a new cookie, so sending again works (0.97.0).
func TestLoginExpiredPage(t *testing.T) {
	s, code, _ := testAccounts(t)
	h := &httpAdapter{service: s}
	call := func(method, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "https://metatrash.com"+path, strings.NewReader(form.Encode()))
		if method == "POST" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://metatrash.com")
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
	loginCookieOf := func(w *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range w.Result().Cookies() {
			if c.Name == loginCookie && c.MaxAge > 0 {
				return c
			}
		}
		return nil
	}
	expiredPage := func(w *httptest.ResponseRecorder, note string) *http.Cookie {
		t.Helper()
		body := w.Body.String()
		if w.Code != 403 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, note) || !strings.Contains(body, `class="problem" role="alert"`) {
			t.Fatalf("expired form: %d %s\n%s", w.Code, w.Header().Get("Content-Type"), body)
		}
		fresh := loginCookieOf(w)
		if fresh == nil {
			t.Fatal("expired form got no new login cookie")
		}
		return fresh
	}

	// The "email me a code" form, sent after the cookie ran out.
	first := call("GET", "/login", nil)
	form := loginSendForm(t, first.Body.String(), "late@example.com")
	w := call("POST", "/login/send", form)
	fresh := expiredPage(w, "We’ve refreshed it: please try again.")
	if *code != "" {
		t.Fatal("expired form sent a code")
	}
	if !strings.Contains(w.Body.String(), `value="late@example.com"`) {
		t.Fatal("typed address not kept")
	}
	// Sending the refreshed page's form works.
	if w := call("POST", "/login/send", loginSendForm(t, w.Body.String(), "late@example.com"), fresh); w.Code != 303 || *code == "" {
		t.Fatalf("refreshed form: %d %s", w.Code, w.Body.String())
	}

	// The code form, sent after the cookie ran out: the code can't work in
	// a new browser, so the page asks for a new one.
	w = call("POST", "/login/verify", url.Values{"code": {*code}, "csrf": {fresh.Value}})
	expiredPage(w, "its code no longer works. Ask for a new code below.")
	if !strings.Contains(w.Body.String(), "Email me a code") {
		t.Fatal("expired code form does not offer a new code")
	}

	// A code form from an older cookie, while this browser's code waits:
	// the code card again, and the right csrf signs in.
	w = call("POST", "/login/verify", url.Values{"code": {*code}, "csrf": {"stale"}}, fresh)
	expiredPage(w, "We’ve refreshed it: please try again.")
	if !strings.Contains(w.Body.String(), "Check your email.") || !strings.Contains(w.Body.String(), `value="`+fresh.Value+`"`) {
		t.Fatalf("waiting code card not shown:\n%s", w.Body.String())
	}
	if w := call("POST", "/login/verify", url.Values{"code": {*code}, "csrf": {fresh.Value}}, fresh); w.Code != 303 {
		t.Fatalf("sign-in after refresh: %d %s", w.Code, w.Body.String())
	}

	// The recovery form keeps its address and stays open.
	w = call("POST", "/login/recovery", url.Values{"email": {"late@example.com"}, "code": {"aaaa-bbbb-cccc"}, "csrf": {"old"}})
	expiredPage(w, "We’ve refreshed it")
	if !strings.Contains(w.Body.String(), `id="recovery-email" name="email" type="email" maxlength="254" autocomplete="email" value="late@example.com"`) {
		t.Fatal("recovery form not reopened with its address")
	}

	// Signing out when the session already ended goes to sign in.
	if w := call("POST", "/logout", url.Values{"csrf": {"x"}}); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Fatalf("logout without session: %d %s", w.Code, w.Body.String())
	}
	// A form from another site is still refused before any of this.
	r := httptest.NewRequest("POST", "https://metatrash.com/login/send", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.serveAccounts(w, r, "test-client")
	if w.Code != 403 || strings.Contains(w.Body.String(), "<html") {
		t.Fatalf("cross-site form: %d", w.Code)
	}
}
