package service

import (
	"bytes"
	"context"
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
const noticeCookie = "__Host-metatrash-notice"

//go:embed web/account.html
var accountHTML string
var accountTemplate = template.Must(template.New("account").Parse(accountHTML))

type accountPage struct {
	// Section is the account page shown: spaces, security, services or
	// profile (accountSection). Filter is which spaces Spaces lists: all,
	// mine or shared (accountSpaceFilters).
	Section                    string
	Filter                     string
	InvitationCount            int
	OAuthEnabled               bool
	AppConnectURL, AppsCSRF    string
	AppSpacesCSRF              string
	Apps                       []connectedApp
	AppSpaces                  *appSpacesPage
	AppsUnavailable            bool
	GitHubEnabled              bool
	GitHubConnectCSRF          string
	GitHubLinkCSRF             string
	GitHubDisconnectCSRF       string
	GitHub                     []githubInstallationView
	GitHubUnavailable          bool
	Sharing                    *humanSharingPage
	Invitations                []humanInvitation
	Joined                     []joinedHumanSpace
	MembershipUnavailable      bool
	MembershipCSRF             map[string]string
	SpaceCSRF, SpaceName       string
	VisibilityCSRF             string
	SpaceNameError             string
	Spaces                     []accountSpace
	SpacesUnavailable          bool
	CanCreate                  bool
	BasePath                   string
	Legal                      bool
	Disabled, Verify, SignedIn bool
	CSRF, Email, Message       string
	Pow                        string // login proof-of-work challenge
	LoginPasskey               string // WebAuthn request options for passkey sign-in
	// TOTPLogin offers sign-in with an authenticator app; TOTPOpen and
	// TOTPEmail show that form again after it failed.
	TOTPLogin, TOTPOpen bool
	TOTPEmail           string
	// RecoveryOpen and RecoveryEmail show the recovery-code form again
	// after it failed.
	RecoveryOpen           bool
	RecoveryEmail          string
	SignIn                 *signInPage
	PowBits                int
	Notice                 string
	UsernameCSRF, Username string
	User                   userAccount
}

// SpaceTotal counts the spaces Spaces lists under All: owned and joined.
func (p accountPage) SpaceTotal() int { return len(p.Spaces) + len(p.Joined) }

// AnyWeb reports whether any of the user's spaces is readable on the web, so
// Spaces explains public spaces once instead of on every row.
func (p accountPage) AnyWeb() bool {
	for _, space := range p.Spaces {
		if space.Web && space.URL != "" {
			return true
		}
	}
	return false
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

// accountSectionPaths are the account pages. Each is one section of Your
// account, with the sections listed down the left like a space's explorer.
var accountSectionPaths = map[string]string{
	"/account":          "spaces",
	"/account/mine":     "spaces",
	"/account/shared":   "spaces",
	"/account/security": "security",
	"/account/services": "services",
	"/account/profile":  "profile",
}

// accountSpaceFilters are the filters of Spaces, one table of the spaces the
// user owns and the spaces they joined. Each filter is its own address, so
// it works without JavaScript and an old link to Shared with me still works.
var accountSpaceFilters = map[string]string{
	"/account":        "all",
	"/account/mine":   "mine",
	"/account/shared": "shared",
}

// accountSection is the section a request belongs to, so a form that fails
// is shown again on its own page.
func accountSection(path string) string {
	if section, ok := accountSectionPaths[path]; ok {
		return section
	}
	switch {
	case path == "/account/membership/accept":
		return "spaces"
	case signInRoutes[path]:
		return "security"
	case strings.HasPrefix(path, "/account/apps/"), strings.HasPrefix(path, "/account/github/"):
		return "services"
	case path == "/account/username":
		return "profile"
	}
	return "spaces"
}

func (h *httpAdapter) renderAccount(w http.ResponseWriter, r *http.Request, status int, page accountPage) {
	page.BasePath = h.basePath
	page.Legal = h.docsSpace() != ""
	if page.SignedIn {
		if page.Section == "" {
			page.Section = accountSection(r.URL.Path)
		}
		if page.Filter = accountSpaceFilters[r.URL.Path]; page.Filter == "" {
			page.Filter = "all"
		}
		if page.AppSpaces != nil {
			page.Section = "services"
		}
		if page.Sharing != nil {
			page.Section = "spaces"
		}
		overview := page.Sharing == nil && page.AppSpaces == nil
		page.MembershipCSRF = make(map[string]string)
		for _, action := range []string{"invite", "cancel", "accept", "suspend", "restore", "remove", "agent"} {
			page.MembershipCSRF[action] = h.service.accounts.mac("membership:" + action + ":" + cookieToken(r, h.sessionCookieName()))
		}
		// Memberships load on every page: the navigation counts invitations.
		var membershipErr error
		page.Invitations, page.Joined, membershipErr = h.accountMemberships(r.Context(), page.User)
		page.InvitationCount = len(page.Invitations)
		if membershipErr != nil {
			page.MembershipUnavailable = true
			if overview && page.Section == "spaces" && page.Filter != "mine" {
				status = http.StatusServiceUnavailable
			}
		}
		if h.oauth != nil {
			page.OAuthEnabled = true
			page.AppConnectURL = h.oauth.mcpResource
			page.AppsCSRF = h.service.accounts.mac("apps-revoke:" + cookieToken(r, h.sessionCookieName()))
			page.AppSpacesCSRF = h.service.accounts.mac("apps-spaces:" + cookieToken(r, h.sessionCookieName()))
			if overview && page.Section == "services" {
				var appsErr error
				if page.Apps, appsErr = h.connectedApps(r.Context(), page.User); appsErr != nil {
					page.AppsUnavailable = true
					status = http.StatusServiceUnavailable
				}
			}
		}
		if h.github != nil {
			page.GitHubEnabled = true
			page.GitHubConnectCSRF = h.service.accounts.mac("github-connect:" + cookieToken(r, h.sessionCookieName()))
			page.GitHubLinkCSRF = h.service.accounts.mac("github-link:" + cookieToken(r, h.sessionCookieName()))
			page.GitHubDisconnectCSRF = h.service.accounts.mac("github-disconnect:" + cookieToken(r, h.sessionCookieName()))
			if overview && page.Section == "services" {
				var githubErr error
				if page.GitHub, githubErr = h.githubConnections(r, page.User); githubErr != nil {
					page.GitHubUnavailable = true
					status = http.StatusServiceUnavailable
				}
			}
		}
		if overview && page.Section == "security" && h.service.ownedDB != nil {
			var ok bool
			if page.SignIn, ok = h.signInSection(r, page.User); !ok {
				status = http.StatusServiceUnavailable
			}
		}
		page.SpaceCSRF = h.service.accounts.mac("space-create:" + cookieToken(r, h.sessionCookieName()))
		page.VisibilityCSRF = h.service.accounts.mac("space-visibility:" + cookieToken(r, h.sessionCookieName()))
		var err error
		page.Spaces, err = h.accountSpaces(r.Context(), page.User)
		if err != nil {
			page.SpacesUnavailable = true
			if overview && page.Section == "spaces" && page.Filter != "shared" {
				status = http.StatusServiceUnavailable
			}
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
	page := accountPage{CSRF: browser, Pow: a.newPowChallenge(browser, time.Now()), PowBits: loginPowBits, LoginPasskey: a.requestOptions("login", browser, nil, time.Now()), TOTPLogin: a.totpKey != nil}
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
	githubRoute := path == "/account/github/connect" || path == "/account/github/link" || path == "/account/github/disconnect"
	loginCookie, sessionCookie := loginCookie, sessionCookie
	if h.basePath != "" {
		suffix := "-" + secretDigest(h.basePath)[:16]
		loginCookie += suffix
		sessionCookie += suffix
	}
	if !sharingRoute && !membershipRoute && !appRoute && !githubRoute && !signInRoutes[path] && path != "/login" && path != "/login/send" && path != "/login/verify" && path != "/login/passkey" && path != "/login/totp" && path != "/login/recovery" && path != "/login/restart" && path != "/logout" && accountSectionPaths[path] == "" && path != "/account/username" && path != "/account/spaces" && path != "/account/spaces/visibility" && path != "/account/apps/revoke" && path != "/account/apps/spaces" {
		return false
	}
	formAction := "'self'"
	if h.github != nil {
		// Connect GitHub posts here and is redirected to GitHub; browsers check
		// form-action against the redirect target too.
		formAction += " " + h.github.settings.webBase
	}
	// script-src 'self' is for login.js (the login form's proof of work) and
	// passkey.js (the browser's passkey prompts).
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action "+formAction)
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
	if path == "/login" || accountSectionPaths[path] != "" || sharingRoute || appRoute {
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
		if accountSectionPaths[path] != "" || sharingRoute || appRoute {
			if !signedIn {
				http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
				return true
			}
			page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session)}
			if accountSectionPaths[path] != "" {
				page.Notice = h.takeNotice(w, r)
			}
			if sharingRoute {
				page.Sharing, err = h.humanSharing(r.Context(), user, sharingID)
				if err != nil {
					sendError(w, err)
					return true
				}
				page.Notice = h.takeNotice(w, r)
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
	if path == "/account/apps/spaces" {
		formLimit = 16384 // one field per space
	}
	if path == "/login/passkey" || path == "/account/confirm/passkey" || path == "/account/passkeys/add" {
		formLimit = 16384 // WebAuthn responses in base64url
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
	if path == "/account/spaces/visibility" {
		allowed["space"], allowed["visibility"] = true, true
	}
	if path == "/account/username" {
		allowed["username"] = true
	}
	if path == "/account/apps/revoke" || path == "/account/apps/spaces" {
		allowed["grant"] = true
	}
	if path == "/account/github/disconnect" {
		allowed["installation"] = true
	}
	if path == "/login/send" {
		allowed["email"], allowed["pow"], allowed["nonce"], allowed[loginHoneypotField] = true, true, true, true
	}
	if path == "/login/verify" {
		allowed["code"] = true
	}
	for _, field := range signInFormFields[path] {
		allowed[field] = true
	}
	for key, values := range r.PostForm {
		if !(allowed[key] || (path == "/account/apps/spaces" && spaceFieldPattern.MatchString(key))) || len(values) != 1 {
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
	if membershipRoute || githubRoute || signInRoutes[path] || path == "/logout" || path == "/account/username" || path == "/account/spaces" || path == "/account/spaces/visibility" || path == "/account/apps/revoke" || path == "/account/apps/spaces" {
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
		if path == "/account/spaces/visibility" {
			csrf = a.mac("space-visibility:" + session)
		}
		if path == "/account/apps/revoke" {
			csrf = a.mac("apps-revoke:" + session)
		}
		if path == "/account/apps/spaces" {
			csrf = a.mac("apps-spaces:" + session)
		}
		if githubRoute {
			csrf = a.mac("github-" + strings.TrimPrefix(path, "/account/github/") + ":" + session)
		}
		if signInRoutes[path] {
			csrf = a.signInCSRF(path, session)
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
	if signInRoutes[path] {
		h.submitSignIn(w, r, session, path, client)
		return true
	}
	if path == "/account/apps/spaces" {
		h.submitAppSpaces(w, r, session)
		return true
	}
	if githubRoute {
		h.submitGitHub(w, r, session, path)
		return true
	}
	if path == "/account/spaces/visibility" {
		h.submitSpaceVisibility(w, r, session)
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
		http.Redirect(w, r, h.basePath+"/account/services", http.StatusSeeOther)
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
		page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), SpaceName: r.PostForm.Get("name")}
		// The create form sends only a name and the slug is derived from it.
		// Retry preparation sends the stored slug so an interrupted space
		// always finishes at the address it reserved.
		slug, derived := r.PostForm.Get("slug"), !r.PostForm.Has("slug")
		err = h.service.rates.take(allowance{"space-create:user:" + user.ID, 10, 600})
		if err == nil && derived {
			slug, err = h.deriveNewSpaceSlug(r.Context(), user, page.SpaceName)
		}
		var space ownedSpace
		if err == nil {
			space, err = h.service.createOwnedSpace(r.Context(), user.ID, page.SpaceName, slug)
		}
		if err != nil {
			status := http.StatusServiceUnavailable
			message := "Your space is not ready yet. Reload your account page and retry."
			var p *Error
			if errors.As(err, &p) {
				status, message = p.Status, p.Message
				if p.RetryAfterSeconds > 0 {
					w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
				}
			}
			if derived && p != nil && p.Code == "conflict" {
				message = "You already have a space at " + h.basePath + "/spaces/" + user.Username + "/" + slug + ". Choose a different name."
			}
			if derived && p != nil && (p.Code == "invalid_request" || p.Code == "conflict") {
				page.SpaceNameError = message
			} else {
				page.Message = message
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
		a.mu.Lock()
		delete(a.sessions, secretDigest(session))
		delete(a.challenges, secretDigest(browser))
		delete(a.challenges, confirmKey(session))
		a.mu.Unlock()
		accountCookie(w, sessionCookie, "", -1)
		accountCookie(w, loginCookie, "", -1)
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
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
			h.renderAccount(w, r, p.Status, page)
		} else {
			note("error")
			page.Message = "Sign-in is temporarily unavailable. Please try again later."
			h.renderAccount(w, r, 503, page)
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
		if err := h.service.rates.reserve(allowance{"mail:global:day", 100, 86400}, allowance{"mail:ip:" + client, 10, 3600}, allowance{"mail:email:minute:" + emailKey, 1, 60}, allowance{"mail:email:hour:" + emailKey, 3, 3600}); err != nil {
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
		http.Redirect(w, r, h.basePath+"/account/services", http.StatusSeeOther)
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

// deriveNewSpaceSlug derives the slug for the create form and refuses a name
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

// submitSpaceVisibility handles POST /account/spaces/visibility: the owner
// makes a space readable by anyone in the website explorer ("web") or private
// again. Agent access is not affected.
func (h *httpAdapter) submitSpaceVisibility(w http.ResponseWriter, r *http.Request, session string) {
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
	if h.service.ownedDB == nil {
		sendError(w, problem(503, "unavailable", "Your spaces are temporarily unavailable."))
		return
	}
	visibility := r.PostForm.Get("visibility")
	err = h.service.rates.take(allowance{"space-visibility:user:" + user.ID, 30, 600})
	if err == nil {
		err = h.service.ownedDB.setSpaceVisibility(r.Context(), user.ID, r.PostForm.Get("space"), visibility)
	}
	if err != nil {
		page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "The space setting could not be saved. Reload before retrying."}
		status := http.StatusServiceUnavailable
		var p *Error
		if errors.As(err, &p) {
			status, page.Message = p.Status, p.Message
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
		}
		h.renderAccount(w, r, status, page)
		return
	}
	notice := "space-private"
	if visibility == spaceWeb {
		notice = "space-web"
	}
	accountCookie(w, h.noticeCookieName(), notice, 60)
	http.Redirect(w, r, h.basePath+"/account", http.StatusSeeOther)
}
