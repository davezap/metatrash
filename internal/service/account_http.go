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
	OAuthEnabled                    bool
	AppConnectURL, AppsCSRF         string
	AppSpacesCSRF                   string
	Apps                            []connectedApp
	AppSpaces                       *appSpacesPage
	AppsUnavailable                 bool
	Sharing                         *humanSharingPage
	Invitations                     []humanInvitation
	Joined                          []joinedHumanSpace
	MembershipUnavailable           bool
	MembershipCSRF                  map[string]string
	SpaceCSRF, SpaceName, SpaceSlug string
	Spaces                          []accountSpace
	SpacesUnavailable               bool
	CanCreate                       bool
	BasePath                        string
	Disabled, Verify, SignedIn      bool
	CSRF, Email, Message            string
	UsernameCSRF, Username          string
	User                            userAccount
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
	if page.SignedIn {
		page.MembershipCSRF = make(map[string]string)
		for _, action := range []string{"invite", "cancel", "accept", "suspend", "restore", "remove", "agent"} {
			page.MembershipCSRF[action] = h.service.accounts.mac("membership:" + action + ":" + cookieToken(r, h.sessionCookieName()))
		}
		if page.Sharing == nil && page.AppSpaces == nil {
			var membershipErr error
			page.Invitations, page.Joined, membershipErr = h.accountMemberships(r.Context(), page.User)
			if membershipErr != nil {
				page.MembershipUnavailable = true
				status = http.StatusServiceUnavailable
			}
		}
		if h.oauth != nil {
			page.OAuthEnabled = true
			page.AppConnectURL = h.oauth.mcpResource
			page.AppsCSRF = h.service.accounts.mac("apps-revoke:" + cookieToken(r, h.sessionCookieName()))
			page.AppSpacesCSRF = h.service.accounts.mac("apps-spaces:" + cookieToken(r, h.sessionCookieName()))
			if page.Sharing == nil && page.AppSpaces == nil {
				var appsErr error
				if page.Apps, appsErr = h.connectedApps(r.Context(), page.User); appsErr != nil {
					page.AppsUnavailable = true
					status = http.StatusServiceUnavailable
				}
			}
		}
		page.SpaceCSRF = h.service.accounts.mac("space-create:" + cookieToken(r, h.sessionCookieName()))
		var err error
		page.Spaces, err = h.accountSpaces(r.Context(), page.User)
		if err != nil {
			page.SpacesUnavailable = true
			status = http.StatusServiceUnavailable
		} else {
			page.CanCreate = page.User.Username != "" && len(page.Spaces) < page.User.MaxPrivateSpaces
		}
	}
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
	sharingID := strings.TrimPrefix(path, "/account/sharing/")
	sharingRoute := strings.HasPrefix(path, "/account/sharing/") && idPattern.MatchString(sharingID)
	membershipAction := strings.TrimPrefix(path, "/account/membership/")
	membershipRoute := strings.HasPrefix(path, "/account/membership/") && (membershipAction == "invite" || membershipAction == "cancel" || membershipAction == "accept" || membershipAction == "suspend" || membershipAction == "restore" || membershipAction == "remove" || membershipAction == "agent")
	appID := strings.TrimPrefix(path, "/account/apps/")
	appRoute := strings.HasPrefix(path, "/account/apps/") && idPattern.MatchString(appID)
	loginCookie, sessionCookie := loginCookie, sessionCookie
	if h.basePath != "" {
		suffix := "-" + secretDigest(h.basePath)[:16]
		loginCookie += suffix
		sessionCookie += suffix
	}
	if !sharingRoute && !membershipRoute && !appRoute && path != "/login" && path != "/login/send" && path != "/login/verify" && path != "/logout" && path != "/account" && path != "/account/username" && path != "/account/spaces" && path != "/account/apps/revoke" && path != "/account/apps/spaces" {
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
	if path == "/login" || path == "/account" || sharingRoute || appRoute {
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
		if path == "/account" || sharingRoute || appRoute {
			if !signedIn {
				http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
				return true
			}
			page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session)}
			if sharingRoute {
				page.Sharing, err = h.humanSharing(r.Context(), user, sharingID)
				if err != nil {
					sendError(w, err)
					return true
				}
			}
			if appRoute {
				page.AppSpaces, err = h.appSpaces(r.Context(), user, appID)
				if err != nil {
					sendError(w, err)
					return true
				}
			}
			h.renderAccount(w, r, 200, page)
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
	formLimit := int64(2048)
	if path == "/account/apps/spaces" {
		formLimit = 16384 // one field per space
	}
	r.Body = http.MaxBytesReader(w, r.Body, formLimit)
	if err := r.ParseForm(); err != nil {
		sendError(w, invalid("Invalid or oversized form."))
		return true
	}
	allowed := map[string]bool{"csrf": true}
	if membershipRoute {
		allowed["space"] = true
		switch membershipAction {
		case "invite":
			allowed["email"] = true
		case "cancel", "accept":
			allowed["invitation"] = true
		case "agent":
			allowed["member"], allowed["permission"] = true, true
		default:
			allowed["member"] = true
		}
	}
	if path == "/account/spaces" {
		allowed["name"], allowed["slug"] = true, true
	}
	if path == "/account/username" {
		allowed["username"] = true
	}
	if path == "/account/apps/revoke" || path == "/account/apps/spaces" {
		allowed["grant"] = true
	}
	if path == "/login/send" {
		allowed["email"] = true
	}
	if path == "/login/verify" {
		allowed["code"] = true
	}
	for key, values := range r.PostForm {
		if !(allowed[key] || (path == "/account/apps/spaces" && spaceFieldPattern.MatchString(key))) || len(values) != 1 {
			sendError(w, invalid("Unknown or duplicate form field."))
			return true
		}
	}
	browser := cookieToken(r, loginCookie)
	csrf := browser
	session := cookieToken(r, sessionCookie)
	if membershipRoute || path == "/logout" || path == "/account/username" || path == "/account/spaces" || path == "/account/apps/revoke" || path == "/account/apps/spaces" {
		if session == "" {
			sendError(w, problem(403, "forbidden", "Please sign in again."))
			return true
		}
		csrf = a.mac("logout:" + session)
		if membershipRoute {
			csrf = a.mac("membership:" + membershipAction + ":" + session)
		}
		if path == "/account/username" {
			csrf = a.mac("username:" + session)
		}
		if path == "/account/spaces" {
			csrf = a.mac("space-create:" + session)
		}
		if path == "/account/apps/revoke" {
			csrf = a.mac("apps-revoke:" + session)
		}
		if path == "/account/apps/spaces" {
			csrf = a.mac("apps-spaces:" + session)
		}
	}
	if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(r.PostForm.Get("csrf"))) != 1 {
		sendError(w, problem(403, "forbidden", "This form expired. Reload the page and try again."))
		return true
	}
	if membershipRoute {
		h.submitHumanMembership(w, r, session, membershipAction)
		return true
	}
	if path == "/account/apps/spaces" {
		h.submitAppSpaces(w, r, session)
		return true
	}
	if path == "/account/apps/revoke" {
		user, signedIn, err := a.currentUser(r.Context(), session)
		if err != nil {
			sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
			return true
		}
		if !signedIn {
			http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
			return true
		}
		if h.oauth == nil || h.service.ownedDB == nil {
			sendError(w, missing())
			return true
		}
		err = h.service.rates.take(allowance{"apps:user:" + user.ID, 30, 600})
		if err == nil {
			err = h.service.ownedDB.revokeConnection(r.Context(), user.ID, r.PostForm.Get("grant"))
		}
		if err != nil {
			page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "The connection could not be revoked. Reload before retrying."}
			status := http.StatusServiceUnavailable
			var p *Error
			if errors.As(err, &p) {
				status, page.Message = p.Status, p.Message
				if p.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
				}
			}
			h.renderAccount(w, r, status, page)
			return true
		}
		http.Redirect(w, r, h.basePath+"/account#connected-apps", http.StatusSeeOther)
		return true
	}
	if path == "/account/spaces" {
		user, signedIn, err := a.currentUser(r.Context(), session)
		if err != nil {
			sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
			return true
		}
		if !signedIn {
			http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
			return true
		}
		page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), SpaceName: r.PostForm.Get("name"), SpaceSlug: r.PostForm.Get("slug")}
		err = h.service.rates.take(allowance{"space-create:user:" + user.ID, 10, 600})
		var space ownedSpace
		if err == nil {
			space, err = h.service.createOwnedSpace(r.Context(), user.ID, page.SpaceName, page.SpaceSlug)
		}
		if err != nil {
			status := http.StatusServiceUnavailable
			page.Message = "Your space is not ready yet. Reload your account page and retry the same name and URL slug."
			var p *Error
			if errors.As(err, &p) {
				status, page.Message = p.Status, p.Message
				if p.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
				}
			}
			h.renderAccount(w, r, status, page)
			return true
		}
		http.Redirect(w, r, h.ownedSpaceURL(user.Username, space.Slug), http.StatusSeeOther)
		return true
	}
	if path == "/account/username" {
		user, signedIn, err := a.currentUser(r.Context(), session)
		if err != nil {
			sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable. Please try again later."))
			return true
		}
		if !signedIn {
			http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
			return true
		}
		page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: csrf, Username: r.PostForm.Get("username")}
		err = h.service.rates.take(allowance{"username:user:" + user.ID, 20, 600})
		if err == nil {
			err = a.store.ChooseUsername(r.Context(), user.ID, page.Username)
		}
		if err != nil {
			status := http.StatusServiceUnavailable
			page.Message = "Your username could not be saved. Reload your account page before trying again."
			var p *Error
			if errors.As(err, &p) {
				status, page.Message = p.Status, p.Message
				if p.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
				}
			}
			h.renderAccount(w, r, status, page)
			return true
		}
		http.Redirect(w, r, h.basePath+"/account", http.StatusSeeOther)
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
	// Continue an app connection that sent this browser to sign in. The pending
	// request lives server-side; the browser only holds its random cookie.
	if h.oauth != nil && h.oauth.pendingFor(cookieToken(r, h.oauthCookieName())) != nil {
		http.Redirect(w, r, h.basePath+"/oauth/consent", http.StatusSeeOther)
		return true
	}
	http.Redirect(w, r, h.basePath+"/account", http.StatusSeeOther)
	return true
}

// submitAppSpaces saves Change spaces for one connection after the form,
// Origin and CSRF checks. Each s-{space} field is none, read_only or
// read_write; errors re-render the page with the user's current state.
func (h *httpAdapter) submitAppSpaces(w http.ResponseWriter, r *http.Request, session string) {
	a := h.service.accounts
	user, signedIn, err := a.currentUser(r.Context(), session)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return
	}
	if !signedIn {
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return
	}
	if h.oauth == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	grantID := r.PostForm.Get("grant")
	choices := []oauthSpaceChoice{}
	for key, values := range r.PostForm {
		if !spaceFieldPattern.MatchString(key) {
			continue
		}
		switch values[0] {
		case "none", "read_only", "read_write":
			choices = append(choices, oauthSpaceChoice{SpaceID: key[2:], Permission: values[0]})
		default:
			sendError(w, invalid("Invalid access choice."))
			return
		}
	}
	err = h.service.rates.take(allowance{"apps:user:" + user.ID, 30, 600})
	if err == nil {
		err = h.service.ownedDB.changeGrantSpaces(r.Context(), user.ID, grantID, choices, time.Now())
	}
	if err == nil {
		http.Redirect(w, r, h.basePath+"/account#connected-apps", http.StatusSeeOther)
		return
	}
	status, message := http.StatusServiceUnavailable, "Your changes could not be saved. Reload before retrying."
	var p *Error
	if errors.As(err, &p) {
		status, message = p.Status, p.Message
		if p.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
		}
	}
	page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: message}
	if page.AppSpaces, err = h.appSpaces(r.Context(), user, grantID); err != nil {
		// The connection itself is gone: show Your account with the message.
		page.AppSpaces = nil
	}
	h.renderAccount(w, r, status, page)
}
