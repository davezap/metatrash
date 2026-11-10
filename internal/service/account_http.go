package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
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
const noticeCookie = "__Host-metatrash-notice"
const returnCookie = "__Host-metatrash-return"

// The sign-in page (/login). Everything else about an account is in the site
// app's Settings dialog (0.79.0-0.81.0); the old /account pages only redirect
// there (0.82.0).
//
//go:embed web/login.html
var loginHTML string
var loginTemplate = template.Must(template.New("login").Parse(loginHTML))

type loginProviderView struct{ Path, Label, Hint string }
type loginWaitingView struct{ Provider, Label, Path string }
type loginView struct {
	Providers []loginProviderView
	Waiting   []loginWaitingView
	BasePath  string
	Legal     bool
	Disabled  bool
	Verify    bool
	CSRF      string
	Email     string
	Message   string
	Pow       string // login proof-of-work challenge
	PowBits   int
	// LoginPasskey is WebAuthn request options for passkey sign-in.
	LoginPasskey string
	// ForgetPasskey is a passkey (credential ID, base64url) the site has no
	// record of, which the page asks the browser to stop offering
	// (signalUnknownCredential) on the relying party PasskeyRPID.
	ForgetPasskey, PasskeyRPID string
	// TOTPLogin offers sign-in with an authenticator app; TOTPOpen and
	// TOTPEmail show that form again after it failed.
	TOTPLogin, TOTPOpen bool
	TOTPEmail           string
	// RecoveryOpen and RecoveryEmail show the recovery-code form again
	// after it failed.
	RecoveryOpen  bool
	RecoveryEmail string
	// ContinueURL makes the page reload itself from this site: a link from
	// another site arrives without the SameSite=Strict session cookie.
	ContinueURL string
	// SignedInAs is the account this browser is signed in to while a code
	// for another address waits: entering it switches accounts.
	SignedInAs string
	Notice     string
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

func (h *httpAdapter) returnCookieName() string {
	if h.basePath == "" {
		return returnCookie
	}
	return returnCookie + "-" + secretDigest(h.basePath)[:16]
}

// returnTarget reports whether target, a path and query on this site with
// the base path removed, is a page signing in may go back to: a space page or
// an account page. Nothing
// else, so the cookie cannot send anyone off-site.
func returnTarget(target string) bool {
	return len(target) <= 2048 && (strings.HasPrefix(target, "/spaces/") || strings.HasPrefix(target, "/app/") || strings.HasPrefix(target, "/account/")) && !strings.ContainsAny(target, "\\\r\n")
}

// redirectToLogin sends a signed-out browser to sign in and remembers the
// page it asked for, so signing in comes back to it (a link from a printed
// PDF or an email) instead of going to Settings.
func (h *httpAdapter) redirectToLogin(w http.ResponseWriter, r *http.Request) {
	if target := r.URL.RequestURI(); r.Method == http.MethodGet && h.returnTarget(target) {
		accountCookie(w, h.returnCookieName(), base64.RawURLEncoding.EncodeToString([]byte(target)), 1200)
	}
	http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
}

// takeReturn gives the page remembered by redirectToLogin, or "", and
// forgets it.
func (h *httpAdapter) takeReturn(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(h.returnCookieName())
	if err != nil {
		return ""
	}
	accountCookie(w, h.returnCookieName(), "", -1)
	target, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || !h.returnTarget(string(target)) {
		return ""
	}
	return string(target)
}

func cookieToken(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil || !digestPattern.MatchString(cookie.Value) {
		return ""
	}
	return cookie.Value
}

// signOut ends this browser's session and forgets its sign-in challenges:
// the account pages' Sign out and the site app's (POST /api/v1/site/logout).
func (a *accounts) signOut(w http.ResponseWriter, browser, session string) {
	a.endSession(session)
	a.mu.Lock()
	delete(a.challenges, secretDigest(browser))
	delete(a.challenges, confirmKey(session))
	a.mu.Unlock()
	accountCookie(w, sessionCookie, "", -1)
	accountCookie(w, loginCookie, "", -1)
}

// accountPages are the old account pages, which moved to the site app's
// Settings dialog. Their addresses still work (links in older emails and
// bookmarks): settingsAddress is where each one went.
var accountPages = map[string]bool{
	"/account":          true,
	"/account/mine":     true,
	"/account/shared":   true,
	"/account/security": true,
	"/account/services": true,
	"/account/profile":  true,
}

// settingsAddress is where an old account page moved to in the site app's
// Settings dialog, or "" for none.
func settingsAddress(path string) string {
	switch path {
	case "/account", "/account/mine", "/account/shared":
		return "/spaces/#settings/spaces"
	case "/account/profile":
		return "/spaces/#settings/profile"
	case "/account/services":
		return "/spaces/#settings/services"
	case "/account/security":
		return "/spaces/#settings/security"
	}
	if id, ok := strings.CutPrefix(path, "/account/sharing/"); ok && idPattern.MatchString(id) {
		return "/spaces/#settings/spaces/" + id
	}
	if id, ok := strings.CutPrefix(path, "/account/apps/"); ok && idPattern.MatchString(id) {
		return "/spaces/#settings/services/apps/" + id
	}
	if id, ok := strings.CutPrefix(path, "/account/actions/"); ok && idPattern.MatchString(id) {
		return "/spaces/#settings/services/actions/" + id
	}
	return ""
}

// loginPosts are the sign-in page's forms.
var loginPosts = map[string]bool{
	"/login/send":     true,
	"/login/verify":   true,
	"/login/passkey":  true,
	"/login/totp":     true,
	"/login/recovery": true,
	"/login/restart":  true,
}

func (h *httpAdapter) renderLogin(w http.ResponseWriter, r *http.Request, status int, page loginView) {
	page.BasePath = h.basePath
	for _, hooks := range h.service.extensions {
		if provider := hooks.Login; provider != nil {
			if provider.Waiting != nil && page.ContinueURL == "" {
				label, email, _ := provider.Waiting(r, h.extensionContext(userAccount{}, ""))
				if label != "" {
					// The provider's sign-in worked but no account has
					// linked it: the page asks for another way (an emailed
					// code), so it doesn't offer that provider's button again.
					page.Waiting = append(page.Waiting, loginWaitingView{provider.Label, label, provider.Path})
					if !page.Verify && page.Email == "" {
						page.Email = email
					}
					continue
				}
			}
			page.Providers = append(page.Providers, loginProviderView{provider.Path, provider.Label, provider.Hint})
		}
	}
	page.Legal = h.docsSpace() != ""
	var b bytes.Buffer
	if err := loginTemplate.Execute(&b, page); err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b.Bytes())
	}
}

func (a *accounts) loginPage(browser string) loginView {
	page := loginView{CSRF: browser, Pow: a.newPowChallenge(browser, time.Now()), PowBits: loginPowBits, LoginPasskey: a.requestOptions("login", browser, nil, time.Now()), TOTPLogin: a.totpKey != nil}
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
	oldPage := accountPages[path] || settingsAddress(path) != ""
	if !oldPage && path != "/login" && !loginPosts[path] && h.extensionLogin(path) == nil && path != "/logout" {
		return false
	}
	loginCookie, sessionCookie := loginCookie, sessionCookie
	if h.basePath != "" {
		suffix := "-" + secretDigest(h.basePath)[:16]
		loginCookie += suffix
		sessionCookie += suffix
	}
	formAction := "'self'"
	for _, hooks := range h.service.extensions {
		if provider := hooks.Login; provider != nil && provider.FormOrigin != "" {
			formAction += " " + provider.FormOrigin
		}
	}
	// script-src 'self' is for theme.js (the saved theme), login.js (the
	// login form's proof of work) and passkey.js (the browser's passkey prompt).
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action "+formAction)
	// no-referrer makes browsers send Origin: null on HTML form POSTs.
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	a := h.service.accounts
	if a == nil {
		h.renderLogin(w, r, 503, loginView{Disabled: true})
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
	if path == "/login" || oldPage {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
			return true
		}
		session := cookieToken(r, sessionCookie)
		// The session cookie is SameSite=Strict, so a link from another site
		// (an invitation email open in webmail) arrives without it even when
		// this browser is signed in. Reload once from this site, which sends
		// it, before treating the browser as signed out. The reload is
		// same-origin, so it cannot loop.
		if session == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			h.renderLogin(w, r, 200, loginView{ContinueURL: h.basePath + path})
			return true
		}
		user, signedIn, err := a.currentUser(r.Context(), session)
		if err != nil {
			h.renderLogin(w, r, 503, loginView{Message: "Sign-in is temporarily unavailable. Please reload this page shortly."})
			return true
		}
		if oldPage {
			// The old account pages live in the site app's Settings dialog.
			if !signedIn {
				h.redirectToLogin(w, r)
				return true
			}
			http.Redirect(w, r, h.basePath+settingsAddress(path), http.StatusSeeOther)
			return true
		}
		browser := cookieToken(r, loginCookie)
		if signedIn {
			// A code was sent for another address from this signed-in
			// browser: show where to enter it, saying that it switches
			// accounts, rather than going back to the account signed in.
			if browser != "" {
				if page := a.loginPage(browser); page.Verify && !strings.EqualFold(page.Email, user.Email) {
					page.SignedInAs = user.Email
					accountCookie(w, loginCookie, browser, 1200)
					h.renderLogin(w, r, 200, page)
					return true
				}
			}
			h.afterSignIn(w, r)
			return true
		}
		if browser == "" {
			var err error
			browser, err = randomHex(32)
			if err != nil {
				sendError(w, err)
				return true
			}
		}
		accountCookie(w, loginCookie, browser, 1200)
		page := a.loginPage(browser)
		page.Notice = h.takeNotice(w, r)
		h.renderLogin(w, r, 200, page)
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return true
	}
	// Every login request writes one journal line (login_guard.go). result is
	// set to the failure each check would cause before that check runs.
	var entry *loginLog
	note := func(string) {}
	if path == "/login/send" || path == "/login/verify" || path == "/login/passkey" || path == "/login/totp" || path == "/login/recovery" {
		entry = &loginLog{kind: strings.TrimPrefix(path, "/login/"), ip: client, ua: r.UserAgent(), result: "bad_origin"}
		defer entry.write()
		note = func(result string) { entry.result = result }
	}
	// Exact Origin and __Host- cookies prevent cross-site login/logout and cookie injection.
	if r.Header.Get("Origin") != a.config.Origin {
		sendError(w, problem(403, "forbidden", "Please submit the form from this site."))
		return true
	}
	note("bad_form")
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/x-www-form-urlencoded" {
		sendError(w, problem(415, "invalid_request", "Use a form submission."))
		return true
	}
	formLimit := int64(2048)
	if path == "/login/passkey" {
		formLimit = 16384 // WebAuthn responses in base64url
	}
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	if err := r.ParseForm(); err != nil {
		sendError(w, invalid("Invalid or oversized form."))
		return true
	}
	allowed := map[string]bool{"csrf": true}
	switch path {
	case "/login/send":
		allowed["email"], allowed["pow"], allowed["nonce"], allowed[loginHoneypotField] = true, true, true, true
	case "/login/verify":
		allowed["code"] = true
	case "/login/passkey":
		for _, field := range passkeyAssertionFields {
			allowed[field] = true
		}
	case "/login/totp", "/login/recovery":
		allowed["email"], allowed["code"] = true, true
	}
	for key, values := range r.PostForm {
		if !allowed[key] || len(values) != 1 {
			sendError(w, invalid("Unknown or duplicate form field."))
			return true
		}
	}
	browser := cookieToken(r, loginCookie)
	if path == "/login/send" {
		// Run both bot checks before anything else can stop the request, so
		// the log line always says which of them it passed.
		a.logEmail(r.Context(), entry, r.PostForm.Get("email"))
		entry.honeypot = honeypotStatus(r)
		entry.pow, entry.age = a.checkPow(browser, r.PostForm.Get("pow"), r.PostForm.Get("nonce"), time.Now())
	}
	note("bad_csrf")
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
	if provider := h.extensionLogin(path); provider != nil {
		provider.Start(w, r, h.extensionContext(userAccount{}, session))
		return true
	}
	if path == "/login/restart" {
		// Try another way: forget the code waiting for this browser, so the
		// sign-in page offers every method again.
		a.mu.Lock()
		delete(a.challenges, secretDigest(browser))
		a.mu.Unlock()
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return true
	}
	if path == "/logout" {
		a.signOut(w, browser, session)
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return true
	}
	fail := func(err error) {
		page := a.loginPage(browser)
		if path == "/login/totp" {
			page.TOTPOpen, page.TOTPEmail = true, r.PostForm.Get("email")
		}
		if path == "/login/recovery" {
			page.RecoveryOpen, page.RecoveryEmail = true, r.PostForm.Get("email")
		}
		var p *Error
		if errors.As(err, &p) {
			note(p.Code)
			page.Message = p.Message
			if p.Code == "unknown_passkey" {
				if id, err := formBytes(r, "credential", maxCredentialIDBytes, true); err == nil {
					page.ForgetPasskey, page.PasskeyRPID = b64.EncodeToString(id), a.rpID()
				}
			}
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
			h.renderLogin(w, r, p.Status, page)
		} else {
			note("error")
			page.Message = "Sign-in is temporarily unavailable. Please try again later."
			h.renderLogin(w, r, 503, page)
		}
	}
	if path == "/login/send" {
		email, err := normalizeEmail(r.PostForm.Get("email"))
		if err != nil {
			fail(invalid("Enter a valid email address."))
			note("invalid_email")
			return true
		}
		emailKey := a.mac("email:" + email)
		// Attempt limits remain independent; rejected delivery admission consumes no send budget.
		if err := h.service.rates.take(allowance{"mail:attempt:" + client, 30, 600}); err != nil {
			fail(err)
			return true
		}
		// Bot checks come before the mail budgets, so a blocked request costs
		// real users nothing. The message does not say which check failed.
		if entry.honeypot != "pass" || entry.pow != "pass" {
			message := "We could not send a code. Reload this page and try again."
			if entry.pow == "missing" && entry.honeypot == "pass" {
				message = "Sending a code needs JavaScript. Turn it on for this site, reload and try again."
			}
			fail(problem(400, "blocked", message))
			return true
		}
		if err := h.service.rates.reserve(codeMailBudget, allowance{"mail:ip:" + client, 10, 3600}, allowance{"mail:email:minute:" + emailKey, 1, 60}, allowance{"mail:email:hour:" + emailKey, 3, 3600}); err != nil {
			h.logMailBudget()
			fail(err)
			return true
		}
		// An account that turned email sign-in off gets a notice instead of
		// a code. The page looks the same either way, so it does not tell
		// anyone how an address signs in.
		send, result := a.send, "sent"
		if h.service.ownedDB != nil {
			allowed, err := h.service.ownedDB.emailLoginAllowed(r.Context(), email)
			if err != nil {
				fail(problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later."))
				return true
			}
			if !allowed {
				send = func(ctx context.Context, email, _ string) error {
					if a.sendNotice == nil {
						return errors.New("no mail")
					}
					return a.sendNotice(ctx, email, "Metatrash sign-in by email is turned off", emailLoginOffMail(a.config.Origin))
				}
				result = "email_off"
			}
		}
		if err := a.issueCode(r.Context(), secretDigest(browser), email, send); err != nil {
			fail(err)
			return true
		}
		note(result)
		accountCookie(w, loginCookie, browser, 1200)
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return true
	}
	if err := h.service.rates.take(allowance{"verify:ip:" + client, 30, 600}); err != nil {
		fail(err)
		return true
	}
	// The session about to start records this browser and address.
	r = r.WithContext(withSessionOrigin(r.Context(), sessionOrigin{Device: deviceLabel(r.UserAgent()), IP: client}))
	var token string
	if path == "/login/passkey" {
		var user userAccount
		token, user, err = h.passkeySignIn(r, browser)
		if user.Email != "" {
			entry.email, entry.account = user.Email, "existing"
		}
		if err != nil {
			fail(err)
			return true
		}
	} else if path == "/login/totp" {
		token, _, err = h.totpSignIn(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("code"), entry)
		if err != nil {
			fail(err)
			return true
		}
	} else if path == "/login/recovery" {
		token, _, err = h.recoverySignIn(r.Context(), r.PostForm.Get("email"), r.PostForm.Get("code"), entry)
		if err != nil {
			fail(err)
			return true
		}
	} else {
		var email string
		var created bool
		token, email, created, err = a.verify(r.Context(), browser, strings.TrimSpace(r.PostForm.Get("code")))
		a.logEmail(r.Context(), entry, email)
		if err != nil {
			fail(err)
			return true
		}
		if created {
			entry.account = "new" // this sign-in created it
		}
	}
	note("ok")
	// Revoke this browser's previous session when replacing it.
	a.endSession(session)
	accountCookie(w, sessionCookie, token, int(sessionLifetime/time.Second))
	accountCookie(w, loginCookie, "", -1)
	h.afterSignIn(w, r)
	return true
}

// afterSignIn sends a browser that just signed in (or came back to /login
// signed in) on to where it was going.
func (h *httpAdapter) afterSignIn(w http.ResponseWriter, r *http.Request) {
	// Continue an app connection that sent this browser to sign in. The pending
	// request lives server-side; the browser only holds its random cookie.
	if h.oauth != nil && h.oauth.pendingFor(cookieToken(r, h.oauthCookieName())) != nil {
		http.Redirect(w, r, h.basePath+"/oauth/consent", http.StatusSeeOther)
		return
	}
	for _, hooks := range h.service.extensions {
		if provider := hooks.Login; provider != nil && provider.Waiting != nil {
			label, _, target := provider.Waiting(r, h.extensionContext(userAccount{}, cookieToken(r, h.sessionCookieName())))
			if label != "" && localReturnAddress(target) {
				http.Redirect(w, r, h.basePath+target, http.StatusSeeOther)
				return
			}
		}
	}
	// Go back to the page that sent this browser to sign in.
	if target := h.takeReturn(w, r); target != "" {
		http.Redirect(w, r, h.basePath+target, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, h.basePath+"/spaces/#settings/spaces", http.StatusSeeOther)
}

// deriveNewSpaceSlug derives the slug for a new space's name and refuses a name
// whose address the user already has. Retrying an interrupted space goes
// through the stored slug instead, so a ready space here is always a clash.
func (h *httpAdapter) deriveNewSpaceSlug(ctx context.Context, user userAccount, name string) (string, error) {
	slug, err := deriveSpaceSlug(name)
	if err != nil {
		return "", err
	}
	spaces, err := h.accountSpaces(ctx, user)
	if err != nil {
		return "", err
	}
	for _, space := range spaces {
		if space.Slug == slug && space.Ready {
			return slug, problem(409, "conflict", "That address is already in use.")
		}
	}
	return slug, nil
}

// changeSpaceVisibility makes an owner's space readable on the web or private
// again and returns the notice. Opening a space to everyone needs a recent
// sign-in or confirmation (0.79.0), like handing it to someone else: a
// script that got into the page can't publish a space on its own. user owns
// the space; actor is the person changing it, who hears about it.
func (h *httpAdapter) changeSpaceVisibility(ctx context.Context, user, actor userAccount, session, spaceID, visibility string) (string, error) {
	a := h.service.accounts
	if err := h.service.rates.take(allowance{"space-visibility:user:" + actor.ID, 30, 600}); err != nil {
		return "", err
	}
	if visibility == spaceWeb {
		if fresh, _ := a.sessionFresh(session, time.Now()); !fresh {
			return "", errConfirmFirst
		}
	}
	if err := h.service.ownedDB.setSpaceVisibility(ctx, user.ID, spaceID, visibility); err != nil {
		return "", err
	}
	if visibility != spaceWeb {
		return "space-private", nil
	}
	// Making a space readable keeps it open after the session ends, so
	// the owner hears about it (security review 2026-10-31, F5).
	name := "One of your spaces"
	if spaces, err := h.accountSpaces(ctx, user); err == nil {
		for _, s := range spaces {
			if s.ID == spaceID {
				name = "Your space \"" + mailText(s.Name) + "\" (" + user.Username + "/" + s.Slug + ")"
				if actor.ID != user.ID {
					name = "The space \"" + mailText(s.Name) + "\" (" + user.Username + "/" + s.Slug + "), which you manage,"
				}
			}
		}
	}
	h.sendSecurityNotice(actor, "A Metatrash space is now readable on the web", spaceWebMail(a.config.Origin, name, time.Now()))
	return "space-web", nil
}

func (h *httpAdapter) returnTarget(target string) bool {
	if returnTarget(target) {
		return true
	}
	for _, hooks := range h.service.extensions {
		for _, route := range hooks.PublicRoutes {
			if route.Method == http.MethodGet && route.Path == target {
				return true
			}
		}
	}
	return false
}
