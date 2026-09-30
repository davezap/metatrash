package service

import (
	"bytes"
	"crypto/subtle"
	_ "embed"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const loginCookie = "__Host-metatrash-login"
const sessionCookie = "__Host-metatrash-session"

//go:embed web/account.html
var accountHTML string
var accountTemplate = template.Must(template.New("account").Parse(accountHTML))

type accountPage struct {
	BasePath                   string
	Disabled, Verify, SignedIn bool
	CSRF, Email, Message       string
	User                       userAccount
}

func accountCookie(w http.ResponseWriter, name, value string, age int) {
	cookie := &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if age < 0 {
		cookie.Expires = time.Unix(1, 0)
	} else {
		cookie.Expires = time.Now().Add(time.Duration(age) * time.Second)
	}
	http.SetCookie(w, cookie)
}

func cookieToken(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil || !digestPattern.MatchString(cookie.Value) {
		return ""
	}
	return cookie.Value
}

func (h *httpAdapter) renderAccount(w http.ResponseWriter, r *http.Request, status int, page accountPage) {
	page.BasePath = h.basePath
	var b bytes.Buffer
	if err := accountTemplate.Execute(&b, page); err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b.Bytes())
	}
}

func (a *accounts) loginPage(browser string) accountPage {
	page := accountPage{CSRF: browser}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cleanup(time.Now())
	if c, ok := a.challenges[secretDigest(browser)]; ok && c.Ready {
		page.Verify = true
		page.Email = c.Email
	}
	return page
}

func (h *httpAdapter) serveAccounts(w http.ResponseWriter, r *http.Request, client string) bool {
	path := r.URL.Path
	loginCookie, sessionCookie := loginCookie, sessionCookie
	if h.basePath != "" {
		suffix := "-" + secretDigest(h.basePath)[:16]
		loginCookie += suffix
		sessionCookie += suffix
	}
	if path != "/login" && path != "/login/send" && path != "/login/verify" && path != "/logout" && path != "/account" {
		return false
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	// no-referrer makes browsers send Origin: null on HTML form POSTs.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	a := h.service.accounts
	if a == nil {
		h.renderAccount(w, r, 503, accountPage{Disabled: true})
		return true
	}
	origin, _ := url.Parse(a.config.Origin)
	if !strings.EqualFold(r.Host, origin.Host) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return true
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Account routes do not accept query parameters."))
		return true
	}
	if path == "/login" || path == "/account" {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
			return true
		}
		session := cookieToken(r, sessionCookie)
		user, signedIn, err := a.currentUser(r.Context(), session)
		if err != nil {
			h.renderAccount(w, r, 503, accountPage{Message: "Sign-in is temporarily unavailable. Please reload this page shortly."})
			return true
		}
		if path == "/account" {
			if !signedIn {
				http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
				return true
			}
			h.renderAccount(w, r, 200, accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session)})
			return true
		}
		if signedIn {
			http.Redirect(w, r, h.basePath+"/account", http.StatusSeeOther)
			return true
		}
		browser := cookieToken(r, loginCookie)
		if browser == "" {
			var err error
			browser, err = randomHex(32)
			if err != nil {
				sendError(w, err)
				return true
			}
		}
		accountCookie(w, loginCookie, browser, 1200)
		h.renderAccount(w, r, 200, a.loginPage(browser))
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return true
	}
	// Exact Origin and __Host- cookies prevent cross-site login/logout and cookie injection.
	if r.Header.Get("Origin") != a.config.Origin {
		sendError(w, problem(403, "forbidden", "Please submit the form from this site."))
		return true
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/x-www-form-urlencoded" {
		sendError(w, problem(415, "invalid_request", "Use a form submission."))
		return true
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if err := r.ParseForm(); err != nil {
		sendError(w, invalid("Invalid or oversized form."))
		return true
	}
	allowed := map[string]bool{"csrf": true}
	if path == "/login/send" {
		allowed["email"] = true
	}
	if path == "/login/verify" {
		allowed["code"] = true
	}
	for key, values := range r.PostForm {
		if !allowed[key] || len(values) != 1 {
			sendError(w, invalid("Unknown or duplicate form field."))
			return true
		}
	}
	browser := cookieToken(r, loginCookie)
	csrf := browser
	session := cookieToken(r, sessionCookie)
	if path == "/logout" {
		if session == "" {
			sendError(w, problem(403, "forbidden", "Please sign in again."))
			return true
		}
		csrf = a.mac("logout:" + session)
	}
	if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.PostForm.Get("csrf"))) != 1 {
		sendError(w, problem(403, "forbidden", "This form expired. Reload the page and try again."))
		return true
	}
	if path == "/logout" {
		a.mu.Lock()
		delete(a.sessions, secretDigest(session))
		delete(a.challenges, secretDigest(browser))
		a.mu.Unlock()
		accountCookie(w, sessionCookie, "", -1)
		accountCookie(w, loginCookie, "", -1)
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return true
	}
	fail := func(err error) {
		page := a.loginPage(browser)
		var p *Error
		if errors.As(err, &p) {
			page.Message = p.Message
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
			h.renderAccount(w, r, p.Status, page)
		} else {
			page.Message = "Sign-in is temporarily unavailable. Please try again later."
			h.renderAccount(w, r, 503, page)
		}
	}
	if path == "/login/send" {
		email, err := normalizeEmail(r.PostForm.Get("email"))
		if err != nil {
			fail(invalid("Enter a valid email address."))
			return true
		}
		emailKey := a.mac("email:" + email)
		// Attempt limits remain independent; rejected delivery admission consumes no send budget.
		if err := h.service.rates.take(allowance{"mail:attempt:" + client, 30, 600}); err != nil {
			fail(err)
			return true
		}
		if err := h.service.rates.reserve(allowance{"mail:global:day", 100, 86400}, allowance{"mail:ip:" + client, 10, 3600}, allowance{"mail:email:minute:" + emailKey, 1, 60}, allowance{"mail:email:hour:" + emailKey, 3, 3600}); err != nil {
			fail(err)
			return true
		}
		if err := a.issue(r.Context(), browser, email); err != nil {
			fail(err)
			return true
		}
		accountCookie(w, loginCookie, browser, 1200)
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return true
	}
	if err := h.service.rates.take(allowance{"verify:ip:" + client, 30, 600}); err != nil {
		fail(err)
		return true
	}
	token, err := a.verify(r.Context(), browser, strings.TrimSpace(r.PostForm.Get("code")))
	if err != nil {
		fail(err)
		return true
	}
	// Revoke this browser's previous session when replacing it.
	a.mu.Lock()
	delete(a.sessions, secretDigest(session))
	a.mu.Unlock()
	accountCookie(w, sessionCookie, token, int(sessionLifetime/time.Second))
	accountCookie(w, loginCookie, "", -1)
	http.Redirect(w, r, h.basePath+"/account", http.StatusSeeOther)
	return true
}
