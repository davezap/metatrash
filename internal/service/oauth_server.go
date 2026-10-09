package service

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web/oauth.html
var oauthHTML string
var oauthTemplate = template.Must(template.New("oauth").Parse(oauthHTML))

const oauthCookie = "__Host-metatrash-oauth"
const pendingLifetime = 10 * time.Minute
const maxPendingAuthorizations = 2048
const maxAuthorizationCodes = 2048

var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
var codeVerifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var spaceFieldPattern = regexp.MustCompile(`^s-[0-9a-f]{32}$`)

// oauthPending is a validated authorization request waiting for sign-in and
// consent. It is bound to one browser by the oauth cookie and kept in memory.
type oauthPending struct {
	ID                                      string
	Client                                  oauthClient
	RedirectURI, State, Challenge, Resource string
	WantWrite                               bool
	Expires                                 time.Time
	// NewSpaceID is a space created from this request's setup step; consent
	// preselects it.
	NewSpaceID string
}

// oauthCode is a single-use authorization code, kept in memory until expiry so
// that a second use can be detected and the connection's tokens revoked.
type oauthCode struct {
	GrantID, UserID, ClientID, RedirectURI, Resource, Challenge, Scope string
	Expires                                                            time.Time
	Used                                                               bool
}

// oauthServer is the authorization server. It exists only when accounts are
// enabled with oauth.enabled; every route below is absent otherwise.
type oauthServer struct {
	settings     oauthSettings
	issuer       string
	mcpResource  string
	restResource string
	clients      *oauthClients
	mu           sync.Mutex
	pending      map[string]*oauthPending
	codes        map[string]*oauthCode
	lastPurge    time.Time
	now          func() time.Time
}

func newOAuthServer(settings oauthSettings, publicURL string) *oauthServer {
	issuer := strings.TrimSuffix(publicURL, "/")
	return &oauthServer{settings: settings, issuer: issuer, mcpResource: issuer + "/mcp/account", restResource: issuer + "/api/v1/account",
		clients: newOAuthClients(settings.clientHosts), pending: map[string]*oauthPending{}, codes: map[string]*oauthCode{}, now: time.Now}
}

// canonicalResource accepts the resource exactly, or with one trailing slash
// or an upper-case scheme/host, and returns the configured identifier.
func (o *oauthServer) canonicalResource(raw string) (string, bool) {
	if raw == "" {
		return o.mcpResource, true
	}
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", false
	}
	candidate := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host) + strings.TrimSuffix(u.EscapedPath(), "/")
	for _, resource := range []string{o.mcpResource, o.restResource} {
		if candidate == resource {
			return resource, true
		}
	}
	return "", false
}

// cleanup removes expired pending requests and codes. Callers hold mu.
func (o *oauthServer) cleanup(now time.Time) {
	for key, p := range o.pending {
		if !now.Before(p.Expires) {
			delete(o.pending, key)
		}
	}
	for key, c := range o.codes {
		if !now.Before(c.Expires) {
			delete(o.codes, key)
		}
	}
}

func (o *oauthServer) pendingFor(browser string) *oauthPending {
	if browser == "" {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cleanup(o.now())
	p := o.pending[secretDigest(browser)]
	if p == nil {
		return nil
	}
	copy := *p
	return &copy
}

func (h *httpAdapter) oauthCookieName() string {
	if h.basePath == "" {
		return oauthCookie
	}
	return oauthCookie + "-" + secretDigest(h.basePath)[:16]
}

type oauthConsentSpace struct {
	accessibleSpace
	Choice       string
	WriteAllowed bool
}

type oauthPage struct {
	BasePath, Mode, Title, Message string
	ContinueURL                    string
	ClientName, ClientHost, Email  string
	CSRF, LogoutCSRF               string
	WantWrite, Legal               bool
	Spaces                         []oauthConsentSpace
	// First-space setup on the consent page, for accounts with no space yet.
	Setup, NeedUsername       bool
	SetupCSRF, AddressBase    string
	Username, UsernameError   string
	SpaceName, SpaceNameError string
	NewSpaceName, NewSpaceRef string
}

func (h *httpAdapter) renderOAuth(w http.ResponseWriter, status int, page oauthPage, formTarget string) {
	page.BasePath = h.basePath
	page.Legal = h.docsSpace() != ""
	formAction := "'self'"
	if formTarget != "" {
		formAction += " " + formTarget
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action "+formAction)
	var b bytes.Buffer
	if err := oauthTemplate.Execute(&b, page); err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes())
}

func (h *httpAdapter) oauthErrorPage(w http.ResponseWriter, status int, title, message string) {
	h.renderOAuth(w, status, oauthPage{Mode: "error", Title: title, Message: message}, "")
}

// serveOAuth handles discovery, authorization, consent, token and revocation
// routes. It returns false for other paths or when OAuth is disabled.
func (h *httpAdapter) serveOAuth(w http.ResponseWriter, r *http.Request, client string) bool {
	o := h.oauth
	path := r.URL.Path
	if o == nil || !(strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/.well-known/")) {
		return false
	}
	switch path {
	case "/.well-known/oauth-authorization-server", "/.well-known/oauth-protected-resource/mcp/account", "/.well-known/oauth-protected-resource/api/v1/account":
	case "/oauth/authorize", "/oauth/consent", "/oauth/setup", "/oauth/token", "/oauth/revoke":
	default:
		return false
	}
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if !strings.EqualFold(r.Host, h.publicHost) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return true
	}
	switch path {
	case "/oauth/authorize":
		h.oauthAuthorize(w, r, client)
	case "/oauth/consent":
		if r.Method == http.MethodPost {
			h.oauthConsentSubmit(w, r)
		} else {
			h.oauthConsentPage(w, r)
		}
	case "/oauth/setup":
		h.oauthSetupSubmit(w, r)
	case "/oauth/token":
		h.oauthToken(w, r, client)
	case "/oauth/revoke":
		h.oauthRevoke(w, r, client)
	default:
		h.oauthMetadata(w, r)
	}
	return true
}

// Discovery documents are public; CORS lets browser-based MCP clients read them.
func (h *httpAdapter) oauthMetadata(w http.ResponseWriter, r *http.Request) {
	o := h.oauth
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.Header().Set("Access-Control-Allow-Headers", "MCP-Protocol-Version")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	scopes := []string{"spaces:read", "spaces:write"}
	switch r.URL.Path {
	case "/.well-known/oauth-authorization-server":
		sendJSON(w, 200, map[string]any{
			"issuer":                                         o.issuer,
			"authorization_endpoint":                         o.issuer + "/oauth/authorize",
			"token_endpoint":                                 o.issuer + "/oauth/token",
			"revocation_endpoint":                            o.issuer + "/oauth/revoke",
			"response_types_supported":                       []string{"code"},
			"response_modes_supported":                       []string{"query"},
			"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":               []string{"S256"},
			"token_endpoint_auth_methods_supported":          []string{"none"},
			"revocation_endpoint_auth_methods_supported":     []string{"none"},
			"scopes_supported":                               scopes,
			"client_id_metadata_document_supported":          true,
			"authorization_response_iss_parameter_supported": true,
			"service_documentation":                          o.issuer + "/",
		})
	case "/.well-known/oauth-protected-resource/mcp/account":
		sendJSON(w, 200, map[string]any{"resource": o.mcpResource, "authorization_servers": []string{o.issuer}, "scopes_supported": scopes, "bearer_methods_supported": []string{"header"}, "resource_name": "Metatrash spaces (MCP)"})
	default:
		sendJSON(w, 200, map[string]any{"resource": o.restResource, "authorization_servers": []string{o.issuer}, "scopes_supported": scopes, "bearer_methods_supported": []string{"header"}, "resource_name": "Metatrash spaces (REST)"})
	}
}

// oauthRedirect sends an authorization response to the client's validated
// redirect URI, preserving any registered query and adding iss (RFC 9207).
func (h *httpAdapter) oauthRedirect(w http.ResponseWriter, r *http.Request, redirectURI string, params url.Values) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		h.oauthErrorPage(w, 400, "This request cannot continue", "The app's return address is invalid.")
		return
	}
	query := u.Query()
	for key, values := range params {
		query[key] = values
	}
	query.Set("iss", h.oauth.issuer)
	u.RawQuery = query.Encode()
	w.Header().Set("Location", u.String())
	w.WriteHeader(http.StatusSeeOther)
}

func (h *httpAdapter) oauthAuthorize(w http.ResponseWriter, r *http.Request, client string) {
	o := h.oauth
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return
	}
	if err := h.service.rates.take(allowance{"oauth:authorize:" + client, 60, 600}); err != nil {
		h.oauthErrorPage(w, 429, "Too many requests", "Please wait a few minutes before connecting again.")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		h.oauthErrorPage(w, 400, "This request cannot continue", "The app sent an invalid sign-in request.")
		return
	}
	for key, values := range query {
		if len(values) != 1 {
			h.oauthErrorPage(w, 400, "This request cannot continue", "The app repeated the parameter "+strconv.Quote(key)+".")
			return
		}
	}
	// Client and redirect URI are verified before anything is sent to the redirect URI.
	clientID, redirectURI := query.Get("client_id"), query.Get("redirect_uri")
	app, err := o.clients.lookup(r.Context(), clientID)
	if err != nil {
		h.oauthErrorPage(w, 400, "This app is not recognized", "Metatrash only connects apps it can verify. The app's identity could not be confirmed.")
		return
	}
	if !app.matchRedirectURI(redirectURI) {
		h.oauthErrorPage(w, 400, "This request cannot continue", "The app's return address is not registered for "+app.Name+".")
		return
	}
	fail := func(code, description string) {
		params := url.Values{"error": {code}, "error_description": {description}}
		if state, ok := query["state"]; ok {
			params["state"] = state
		}
		h.oauthRedirect(w, r, redirectURI, params)
	}
	if query.Get("response_type") != "code" {
		fail("unsupported_response_type", "Only the authorization code flow is supported.")
		return
	}
	challenge := query.Get("code_challenge")
	if query.Get("code_challenge_method") != "S256" || !codeChallengePattern.MatchString(challenge) {
		fail("invalid_request", "PKCE with code_challenge_method S256 is required.")
		return
	}
	resource, ok := o.canonicalResource(query.Get("resource"))
	if !ok {
		fail("invalid_target", "Unknown resource.")
		return
	}
	scope, _ := parseScope(query.Get("scope"), true)
	if len(query.Get("state")) > 2048 {
		fail("invalid_request", "state is too long.")
		return
	}
	if query.Get("prompt") == "none" {
		fail("consent_required", "Metatrash always asks the user to choose spaces.")
		return
	}
	for _, unsupported := range []string{"request", "request_uri"} {
		if _, present := query[unsupported]; present {
			fail(unsupported+"_not_supported", "Request objects are not supported.")
			return
		}
	}
	browser := cookieToken(r, h.oauthCookieName())
	if browser == "" {
		browser, err = randomHex(32)
		if err != nil {
			sendError(w, err)
			return
		}
	}
	id, err := randomHex(16)
	if err != nil {
		sendError(w, err)
		return
	}
	pending := &oauthPending{ID: id, Client: app, RedirectURI: redirectURI, State: query.Get("state"), Challenge: challenge, Resource: resource, WantWrite: scope == scopeReadWrite, Expires: o.now().Add(pendingLifetime)}
	o.mu.Lock()
	o.cleanup(o.now())
	if len(o.pending) >= maxPendingAuthorizations {
		o.mu.Unlock()
		h.oauthErrorPage(w, 503, "Metatrash is busy", "Please try connecting again in a few minutes.")
		return
	}
	// One pending request per browser: a newer request replaces the older one.
	o.pending[secretDigest(browser)] = pending
	o.mu.Unlock()
	// Lax so the cookie is set on this cross-site navigation and sent on the
	// same-site continuation below; Strict session cookies are not sent on a
	// cross-site navigation, so consent happens on the next, same-site page.
	http.SetCookie(w, &http.Cookie{Name: h.oauthCookieName(), Value: browser, Path: "/", MaxAge: int(pendingLifetime / time.Second), Expires: o.now().Add(pendingLifetime), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	h.renderOAuth(w, 200, oauthPage{Mode: "continue", Title: "Continue", ClientName: app.Name, ClientHost: app.Host, ContinueURL: h.basePath + "/oauth/consent"}, "")
}

func (h *httpAdapter) consentCSRF(session string, pending *oauthPending) string {
	return h.service.accounts.mac("oauth-consent:" + session + ":" + pending.ID)
}

func (h *httpAdapter) oauthConsentPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, POST")
		sendError(w, problem(405, "invalid_request", "Use GET or POST."))
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("This page does not accept query parameters."))
		return
	}
	pending := h.oauth.pendingFor(cookieToken(r, h.oauthCookieName()))
	if pending == nil {
		h.oauthErrorPage(w, 400, "This connection request expired", "Connection requests last ten minutes and work only in the browser where they started.")
		return
	}
	session := cookieToken(r, h.sessionCookieName())
	user, signedIn, err := h.service.accounts.currentUser(r.Context(), session)
	if err != nil {
		h.oauthErrorPage(w, 503, "Sign-in is temporarily unavailable", "Please try again shortly.")
		return
	}
	if !signedIn {
		h.renderOAuth(w, 200, oauthPage{Mode: "signin", Title: "Sign in", ClientName: pending.Client.Name, ClientHost: pending.Client.Host}, "")
		return
	}
	h.renderConsent(w, r, 200, user, session, pending, "")
}

func (h *httpAdapter) setupCSRF(session string, pending *oauthPending) string {
	return h.service.accounts.mac("oauth-setup:" + session + ":" + pending.ID)
}

func (h *httpAdapter) renderConsent(w http.ResponseWriter, r *http.Request, status int, user userAccount, session string, pending *oauthPending, message string) {
	h.renderConsentPage(w, r, status, user, session, pending, oauthPage{Message: message})
}

// renderConsentPage renders consent; page carries any message and the setup
// form's values and field errors.
func (h *httpAdapter) renderConsentPage(w http.ResponseWriter, r *http.Request, status int, user userAccount, session string, pending *oauthPending, page oauthPage) {
	db := h.service.ownedDB
	if db == nil {
		h.oauthErrorPage(w, 503, "Spaces are temporarily unavailable", "Please try again shortly.")
		return
	}
	spaces, err := db.accessibleSpaces(r.Context(), user)
	if err != nil {
		h.oauthErrorPage(w, 503, "Spaces are temporarily unavailable", "Please try again shortly.")
		return
	}
	previous, err := db.grantConsent(r.Context(), user.ID, pending.Client.ID, pending.Resource)
	if err != nil {
		h.oauthErrorPage(w, 503, "Spaces are temporarily unavailable", "Please try again shortly.")
		return
	}
	page.Mode, page.Title, page.ClientName, page.ClientHost, page.Email = "consent", "Connect "+pending.Client.Name, pending.Client.Name, pending.Client.Host, user.Email
	page.CSRF, page.LogoutCSRF, page.WantWrite = h.consentCSRF(session, pending), h.service.accounts.mac("logout:"+session), pending.WantWrite
	owned := 0
	for _, space := range spaces {
		item := oauthConsentSpace{accessibleSpace: space, Choice: "none", WriteAllowed: space.CanWrite && pending.WantWrite}
		if choice, ok := previous[space.ID]; ok {
			item.Choice = choice
			if choice == "read_write" && !item.WriteAllowed {
				item.Choice = "read_only"
			}
		}
		if space.ID == pending.NewSpaceID {
			page.NewSpaceName, page.NewSpaceRef = space.Name, space.Owner+"/"+space.Slug
			if item.Choice == "none" {
				item.Choice = "read_only"
				if item.WriteAllowed {
					item.Choice = "read_write"
				}
			}
		}
		if space.Owned {
			owned++
		}
		page.Spaces = append(page.Spaces, item)
	}
	// New accounts can choose a username and create their first space here.
	if owned == 0 && user.MaxPrivateSpaces > 0 {
		page.Setup, page.NeedUsername, page.SetupCSRF = true, user.Username == "", h.setupCSRF(session, pending)
		page.AddressBase = h.basePath + "/spaces/your-username"
		if user.Username != "" {
			page.AddressBase = h.basePath + "/spaces/" + user.Username
		}
	}
	h.renderOAuth(w, status, page, redirectSource(pending.RedirectURI))
}

func (h *httpAdapter) oauthConsentSubmit(w http.ResponseWriter, r *http.Request) {
	o := h.oauth
	a := h.service.accounts
	if r.Header.Get("Origin") != h.publicOrigin {
		sendError(w, problem(403, "forbidden", "Please submit the form from this site."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		sendError(w, problem(415, "invalid_request", "Use a form submission."))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := r.ParseForm(); err != nil {
		sendError(w, invalid("Invalid or oversized form."))
		return
	}
	for key, values := range r.PostForm {
		if len(values) != 1 || !(key == "csrf" || key == "decision" || spaceFieldPattern.MatchString(key)) {
			sendError(w, invalid("Unknown or duplicate form field."))
			return
		}
	}
	browser := cookieToken(r, h.oauthCookieName())
	pending := o.pendingFor(browser)
	if pending == nil {
		h.oauthErrorPage(w, 400, "This connection request expired", "Connection requests last ten minutes and work only in the browser where they started.")
		return
	}
	session := cookieToken(r, h.sessionCookieName())
	if session == "" || subtle.ConstantTimeCompare([]byte(h.consentCSRF(session, pending)), []byte(r.PostForm.Get("csrf"))) != 1 {
		sendError(w, problem(403, "forbidden", "This form expired. Return to your app and connect again."))
		return
	}
	user, signedIn, err := a.currentUser(r.Context(), session)
	if err != nil || !signedIn {
		h.oauthErrorPage(w, 403, "Please sign in again", "Your session ended. Return to your app and connect again.")
		return
	}
	if err := h.service.rates.take(allowance{"oauth:consent:user:" + user.ID, 30, 600}); err != nil {
		h.oauthErrorPage(w, 429, "Too many requests", "Please wait a few minutes before connecting again.")
		return
	}
	// The pending request is consumed by either decision; a resubmission cannot reuse it.
	consume := func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		key := secretDigest(browser)
		if current := o.pending[key]; current == nil || current.ID != pending.ID {
			return false
		}
		delete(o.pending, key)
		return true
	}
	clearCookie := func() {
		http.SetCookie(w, &http.Cookie{Name: h.oauthCookieName(), Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(1, 0), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	stateParams := url.Values{}
	if pending.State != "" {
		stateParams.Set("state", pending.State)
	}
	switch r.PostForm.Get("decision") {
	case "deny":
		if !consume() {
			h.oauthErrorPage(w, 400, "This connection request expired", "Return to your app and connect again.")
			return
		}
		clearCookie()
		stateParams.Set("error", "access_denied")
		stateParams.Set("error_description", "The user declined to connect.")
		h.oauthRedirect(w, r, pending.RedirectURI, stateParams)
		return
	case "approve":
	default:
		sendError(w, invalid("Choose Connect or Cancel."))
		return
	}
	choices := []oauthSpaceChoice{}
	for key, values := range r.PostForm {
		if !spaceFieldPattern.MatchString(key) {
			continue
		}
		switch values[0] {
		case "none":
		case "read_only":
			choices = append(choices, oauthSpaceChoice{SpaceID: key[2:], Permission: "read_only"})
		case "read_write":
			if !pending.WantWrite {
				h.renderConsent(w, r, 400, user, session, pending, "This app asked for read-only access.")
				return
			}
			choices = append(choices, oauthSpaceChoice{SpaceID: key[2:], Permission: "read_write"})
		default:
			sendError(w, invalid("Invalid access choice."))
			return
		}
	}
	db := h.service.ownedDB
	if db == nil {
		h.oauthErrorPage(w, 503, "Spaces are temporarily unavailable", "Please try again shortly.")
		return
	}
	now := o.now()
	grantID, scope, err := db.saveGrant(r.Context(), user.ID, pending.Client, pending.Resource, choices, now, o.settings.grantIdle)
	if err != nil {
		var p *Error
		if errors.As(err, &p) {
			h.renderConsent(w, r, p.Status, user, session, pending, p.Message)
			return
		}
		log.Printf("oauth consent failed (%T)", err)
		h.renderConsent(w, r, 503, user, session, pending, "The connection could not be saved. Please try again.")
		return
	}
	code, err := newOAuthToken("mt_ac_")
	if err != nil {
		sendError(w, err)
		return
	}
	o.mu.Lock()
	o.cleanup(now)
	if len(o.codes) >= maxAuthorizationCodes {
		o.mu.Unlock()
		h.oauthErrorPage(w, 503, "Metatrash is busy", "Your choices were saved. Please connect again in a few minutes.")
		return
	}
	o.codes[secretDigest(code)] = &oauthCode{GrantID: grantID, UserID: user.ID, ClientID: pending.Client.ID, RedirectURI: pending.RedirectURI, Resource: pending.Resource, Challenge: pending.Challenge, Scope: scope, Expires: now.Add(o.settings.codeTTL)}
	o.mu.Unlock()
	if !consume() {
		o.mu.Lock()
		delete(o.codes, secretDigest(code))
		o.mu.Unlock()
		h.oauthErrorPage(w, 400, "This connection request expired", "Your choices were saved. Return to your app and connect again.")
		return
	}
	clearCookie()
	stateParams.Set("code", code)
	h.oauthRedirect(w, r, pending.RedirectURI, stateParams)
}

// Token endpoint responses (RFC 6749 section 5) are never cached.
func oauthJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	sendJSON(w, status, value)
}

func oauthFailure(w http.ResponseWriter, err error) {
	var p *Error
	if !errors.As(err, &p) {
		log.Printf("oauth token request failed (%T)", err)
		oauthJSON(w, 503, map[string]string{"error": "temporarily_unavailable", "error_description": "Please try again shortly."})
		return
	}
	if p.Code == "invalid_client" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="metatrash"`)
	}
	if p.RetryAfterSeconds > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
	}
	oauthJSON(w, p.Status, map[string]string{"error": p.Code, "error_description": p.Message})
}

func oauthRequestError(code, description string) *Error {
	status := 400
	if code == "invalid_client" {
		status = 401
	}
	return &Error{Status: status, Code: code, Message: description}
}

// parseOAuthForm reads a bounded, single-valued form body for token and
// revocation requests. Client secrets and client authentication are refused.
func parseOAuthForm(w http.ResponseWriter, r *http.Request) (url.Values, error) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		return nil, &Error{Status: 405, Code: "invalid_request", Message: "Use POST."}
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/x-www-form-urlencoded" {
		return nil, oauthRequestError("invalid_request", "Use application/x-www-form-urlencoded.")
	}
	if r.URL.RawQuery != "" {
		return nil, oauthRequestError("invalid_request", "Parameters belong in the request body.")
	}
	if r.Header.Get("Authorization") != "" {
		return nil, oauthRequestError("invalid_client", "This server supports public clients only (token_endpoint_auth_method none).")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if err := r.ParseForm(); err != nil {
		return nil, oauthRequestError("invalid_request", "Invalid or oversized form.")
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			return nil, oauthRequestError("invalid_request", "Parameters must not repeat.")
		}
	}
	if _, present := r.PostForm["client_secret"]; present {
		return nil, oauthRequestError("invalid_client", "This server supports public clients only (token_endpoint_auth_method none).")
	}
	return r.PostForm, nil
}

func tokenCORS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func (h *httpAdapter) oauthToken(w http.ResponseWriter, r *http.Request, client string) {
	o := h.oauth
	if tokenCORS(w, r) {
		return
	}
	if err := h.service.rates.take(allowance{"oauth:token:" + client, 60, 60}, allowance{"oauth:token", 1200, 60}); err != nil {
		oauthFailure(w, err)
		return
	}
	form, err := parseOAuthForm(w, r)
	if err != nil {
		oauthFailure(w, err)
		return
	}
	db := h.service.ownedDB
	now := o.now()
	o.mu.Lock()
	purge := db != nil && now.Sub(o.lastPurge) > 10*time.Minute
	if purge {
		o.lastPurge = now
	}
	o.mu.Unlock()
	if purge {
		if err := db.purgeExpired(r.Context(), now); err != nil {
			log.Printf("oauth purge failed (%T)", err)
		}
	}
	clientID := form.Get("client_id")
	if clientID == "" {
		oauthFailure(w, oauthRequestError("invalid_request", "client_id is required."))
		return
	}
	resource := ""
	if raw, present := form["resource"]; present {
		var ok bool
		if resource, ok = o.canonicalResource(raw[0]); !ok || raw[0] == "" {
			oauthFailure(w, oauthRequestError("invalid_target", "Unknown resource."))
			return
		}
	}
	var pair oauthTokenPair
	switch form.Get("grant_type") {
	case "authorization_code":
		verifier, redirectURI := form.Get("code_verifier"), form.Get("redirect_uri")
		digest := secretDigest(form.Get("code"))
		o.mu.Lock()
		code := o.codes[digest]
		var snapshot oauthCode
		reused := false
		if code != nil && now.Before(code.Expires) {
			snapshot = *code
			reused = code.Used
			code.Used = true
		}
		o.mu.Unlock()
		if snapshot.GrantID == "" {
			oauthFailure(w, oauthGrantError("The authorization code is invalid or expired."))
			return
		}
		if reused {
			// RFC 6749 section 4.1.2: revoke tokens issued with a reused code.
			if err := db.revokeGrantTokens(r.Context(), snapshot.GrantID); err != nil {
				log.Printf("oauth code reuse revocation failed (%T)", err)
			}
			oauthFailure(w, oauthGrantError("The authorization code was already used."))
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		computed := base64.RawURLEncoding.EncodeToString(sum[:])
		if snapshot.ClientID != clientID || snapshot.RedirectURI != redirectURI || (resource != "" && resource != snapshot.Resource) ||
			!codeVerifierPattern.MatchString(verifier) || subtle.ConstantTimeCompare([]byte(computed), []byte(snapshot.Challenge)) != 1 {
			oauthFailure(w, oauthGrantError("The authorization code does not match this request."))
			return
		}
		if db == nil {
			oauthFailure(w, errors.New("account database unavailable"))
			return
		}
		pair, err = db.issueForCode(r.Context(), snapshot, now, o.settings)
	case "refresh_token":
		refresh := form.Get("refresh_token")
		if !strings.HasPrefix(refresh, "mt_rt_") || len(refresh) != 49 {
			oauthFailure(w, oauthGrantError("Refresh token is invalid or expired."))
			return
		}
		if db == nil {
			oauthFailure(w, errors.New("account database unavailable"))
			return
		}
		pair, err = db.refreshTokens(r.Context(), refresh, clientID, resource, form.Get("scope"), now, o.settings)
	case "":
		err = oauthRequestError("invalid_request", "grant_type is required.")
	default:
		err = oauthRequestError("unsupported_grant_type", "Use authorization_code or refresh_token.")
	}
	if err != nil {
		oauthFailure(w, err)
		return
	}
	oauthJSON(w, 200, map[string]any{"access_token": pair.Access, "token_type": "Bearer", "expires_in": int(pair.AccessTTL / time.Second), "refresh_token": pair.Refresh, "scope": pair.Scope})
}

func (h *httpAdapter) oauthRevoke(w http.ResponseWriter, r *http.Request, client string) {
	if tokenCORS(w, r) {
		return
	}
	if err := h.service.rates.take(allowance{"oauth:revoke:" + client, 60, 60}); err != nil {
		oauthFailure(w, err)
		return
	}
	form, err := parseOAuthForm(w, r)
	if err != nil {
		oauthFailure(w, err)
		return
	}
	token, clientID := form.Get("token"), form.Get("client_id")
	if token == "" || clientID == "" {
		oauthFailure(w, oauthRequestError("invalid_request", "token and client_id are required."))
		return
	}
	db := h.service.ownedDB
	if db == nil {
		oauthFailure(w, errors.New("account database unavailable"))
		return
	}
	if len(token) == 49 && (strings.HasPrefix(token, "mt_at_") || strings.HasPrefix(token, "mt_rt_")) {
		if err := db.revokeToken(r.Context(), token, clientID); err != nil {
			oauthFailure(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
}

// oauthSetupSubmit is the consent page's first-space step for new accounts:
// choose a public username if needed, then create a space whose slug is
// derived from its name. It uses the same rules and limits as Your account,
// is bound to the pending request and session, and returns to consent with
// the new space preselected and the request's ten minutes restarted.
func (h *httpAdapter) oauthSetupSubmit(w http.ResponseWriter, r *http.Request) {
	o := h.oauth
	a := h.service.accounts
	if r.Header.Get("Origin") != h.publicOrigin {
		sendError(w, problem(403, "forbidden", "Please submit the form from this site."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" || r.Method != http.MethodPost {
		sendError(w, problem(415, "invalid_request", "Use a form submission."))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	if err := r.ParseForm(); err != nil {
		sendError(w, invalid("Invalid or oversized form."))
		return
	}
	for key, values := range r.PostForm {
		if len(values) != 1 || (key != "csrf" && key != "username" && key != "name") {
			sendError(w, invalid("Unknown or duplicate form field."))
			return
		}
	}
	browser := cookieToken(r, h.oauthCookieName())
	pending := o.pendingFor(browser)
	if pending == nil {
		h.oauthErrorPage(w, 400, "This connection request expired", "Connection requests last ten minutes and work only in the browser where they started. Anything you created was saved.")
		return
	}
	session := cookieToken(r, h.sessionCookieName())
	if session == "" || subtle.ConstantTimeCompare([]byte(h.setupCSRF(session, pending)), []byte(r.PostForm.Get("csrf"))) != 1 {
		sendError(w, problem(403, "forbidden", "This form expired. Return to your app and connect again."))
		return
	}
	user, signedIn, err := a.currentUser(r.Context(), session)
	if err != nil || !signedIn {
		h.oauthErrorPage(w, 403, "Please sign in again", "Your session ended. Return to your app and connect again.")
		return
	}
	page := oauthPage{Username: r.PostForm.Get("username"), SpaceName: r.PostForm.Get("name")}
	fail := func(err error, field *string) {
		status, message := http.StatusServiceUnavailable, "Your space could not be created. Please try again."
		var p *Error
		if errors.As(err, &p) {
			status, message = p.Status, p.Message
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
		}
		if field != nil && p != nil && (p.Code == "invalid_request" || p.Code == "conflict" || p.Code == "reserved") {
			*field = message
		} else {
			page.Message = message
		}
		if current, ok, err := a.currentUser(r.Context(), session); err == nil && ok {
			user = current
		}
		h.renderConsentPage(w, r, status, user, session, pending, page)
	}
	if err := h.service.rates.take(allowance{"space-create:user:" + user.ID, 10, 600}); err != nil {
		fail(err, nil)
		return
	}
	if user.Username == "" {
		err := h.service.rates.take(allowance{"username:user:" + user.ID, 20, 600})
		if err == nil {
			err = a.store.ChooseUsername(r.Context(), user.ID, page.Username)
		}
		if err != nil {
			fail(err, &page.UsernameError)
			return
		}
		if user, signedIn, err = a.currentUser(r.Context(), session); err != nil || !signedIn || user.Username == "" {
			fail(fmt.Errorf("cannot reload account"), nil)
			return
		}
	}
	slug, err := h.deriveNewSpaceSlug(r.Context(), user, page.SpaceName)
	if err == nil {
		var space ownedSpace
		if space, err = h.service.createOwnedSpace(r.Context(), user.ID, page.SpaceName, slug); err == nil {
			now := o.now()
			o.mu.Lock()
			if current := o.pending[secretDigest(browser)]; current != nil && current.ID == pending.ID {
				current.NewSpaceID = space.ID
				current.Expires = now.Add(pendingLifetime)
			}
			o.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: h.oauthCookieName(), Value: browser, Path: "/", MaxAge: int(pendingLifetime / time.Second), Expires: now.Add(pendingLifetime), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			http.Redirect(w, r, h.basePath+"/oauth/consent", http.StatusSeeOther)
			return
		}
	}
	if p := (*Error)(nil); errors.As(err, &p) && p.Code == "conflict" {
		err = problem(409, "conflict", "You already have a space at "+h.basePath+"/spaces/"+user.Username+"/"+slug+". Choose a different name.")
	}
	fail(err, &page.SpaceNameError)
}
