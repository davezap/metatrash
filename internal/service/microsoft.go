package service

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Sign in with Microsoft (0.77.0): an OpenID Connect code flow against the
// Microsoft identity platform's "common" endpoint, so work, school and
// personal Microsoft accounts can all sign in. Design: the Claude Doc "Sign in
// with Microsoft and Google: design" and docs/security-review-2026-10-31.md.
//
// The rules that keep it safe:
//   - An identity is the tenant ID and object ID (tid + oid), never the email
//     claim, which a tenant's admin can set to anyone's address ("nOAuth").
//   - The ID token is checked in full: RS256 signature against Microsoft's
//     published keys, issuer for the token's own tenant, audience, times and
//     the nonce.
//   - Each attempt is bound to the browser that started it (a Lax cookie, so
//     it survives the return from login.microsoftonline.com), single-use,
//     ten minutes, with a random state, nonce and PKCE verifier.
//   - A Microsoft account no Metatrash account has linked never signs anyone
//     in. It waits in that browser until the person signs in another way and
//     confirms linking it on Security, or links it from Security directly.

const microsoftAuthority = "https://login.microsoftonline.com"
const oidcLifetime = 10 * time.Minute
const maxOIDCPending = 1024
const maxIdentitiesPerUser = 10

// oidcClockSkew is how far token times may be off from ours.
const oidcClockSkew = 2 * time.Minute

// oidcKeysFor is how long fetched signing keys are used; an unknown key ID
// fetches again, at most every oidcKeysRetry.
const oidcKeysFor = 24 * time.Hour
const oidcKeysRetry = 5 * time.Minute

var guidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// microsoftConfig is the "microsoft" part of accounts.json.
type microsoftConfig struct {
	Enabled bool `json:"enabled"`
	// ClientID is the Application (client) ID of the app registration.
	ClientID string `json:"clientId"`
	// ClientSecretFile holds the client secret's value, never accounts.json.
	ClientSecretFile string `json:"clientSecretFile"`
}

func (c *microsoftConfig) settings() (*oidcProvider, error) {
	clientID := strings.ToLower(strings.TrimSpace(c.ClientID))
	if !guidPattern.MatchString(clientID) {
		return nil, fmt.Errorf("microsoft.clientId must be the app registration's Application (client) ID")
	}
	if c.ClientSecretFile == "" {
		return nil, fmt.Errorf("microsoft.clientSecretFile is required")
	}
	b, err := os.ReadFile(c.ClientSecretFile)
	if err != nil {
		return nil, fmt.Errorf("read Microsoft client secret file: %w", err)
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" || len(secret) > 512 || strings.ContainsAny(secret, "\r\n") {
		return nil, fmt.Errorf("Microsoft client secret file must hold one secret value")
	}
	return newOIDCProvider(clientID, secret, microsoftAuthority), nil
}

// oidcProvider is one sign-in provider (only Microsoft so far) and the
// attempts in progress.
type oidcProvider struct {
	clientID, secret string
	authority        string
	http             *http.Client
	now              func() time.Time

	mu      sync.Mutex
	pending map[string]oidcPending // by digest of the browser cookie
	waiting map[string]oidcWaiting // unlinked accounts, by the same digest
	keys    map[string]*rsa.PublicKey
	keysAt  time.Time
	triedAt time.Time
}

func newOIDCProvider(clientID, secret, authority string) *oidcProvider {
	return &oidcProvider{clientID: clientID, secret: secret, authority: authority,
		http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		now:  time.Now, pending: map[string]oidcPending{}, waiting: map[string]oidcWaiting{}}
}

type oidcPending struct {
	State, Nonce, Verifier string
	Expires                time.Time
	// UserID and Session are set when Link a Microsoft account on Security
	// started the attempt: the account is linked to that user on return.
	UserID, Session string
}

// oidcIdentity is what a checked ID token says.
type oidcIdentity struct {
	Tenant, Object string
	// Email and Name only label the account (never a key or a match).
	Email, Name string
}

// Label names the account for people: its address or name.
func (id oidcIdentity) Label() string {
	if id.Email != "" {
		return id.Email
	}
	if id.Name != "" {
		return id.Name
	}
	return "a Microsoft account"
}

type oidcWaiting struct {
	oidcIdentity
	Expires time.Time
}

func (p *oidcProvider) sweepLocked(now time.Time) {
	for k, v := range p.pending {
		if !now.Before(v.Expires) {
			delete(p.pending, k)
		}
	}
	for k, v := range p.waiting {
		if !now.Before(v.Expires) {
			delete(p.waiting, k)
		}
	}
}

// begin records a new attempt for browser.
func (p *oidcProvider) begin(browser string, attempt oidcPending) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.sweepLocked(now)
	if len(p.pending) >= maxOIDCPending {
		return problem(503, "busy", "Metatrash is busy. Please try again in a few minutes.")
	}
	attempt.Expires = now.Add(oidcLifetime)
	p.pending[secretDigest(browser)] = attempt
	return nil
}

// take removes and returns the browser's attempt (one use only).
func (p *oidcProvider) take(browser string) (oidcPending, bool) {
	if browser == "" {
		return oidcPending{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	key := secretDigest(browser)
	attempt, ok := p.pending[key]
	delete(p.pending, key)
	if !ok || !p.now().Before(attempt.Expires) {
		return oidcPending{}, false
	}
	return attempt, true
}

// wait keeps an unlinked account for the browser for ten minutes.
func (p *oidcProvider) wait(browser string, id oidcIdentity) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.sweepLocked(now)
	if len(p.waiting) >= maxOIDCPending {
		return problem(503, "busy", "Metatrash is busy. Please try again in a few minutes.")
	}
	p.waiting[secretDigest(browser)] = oidcWaiting{oidcIdentity: id, Expires: now.Add(oidcLifetime)}
	return nil
}

// waitingFor reports the unlinked account waiting in this browser.
func (p *oidcProvider) waitingFor(browser string) (oidcIdentity, bool) {
	if p == nil || browser == "" {
		return oidcIdentity{}, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	w, ok := p.waiting[secretDigest(browser)]
	if !ok || !p.now().Before(w.Expires) {
		return oidcIdentity{}, false
	}
	return w.oidcIdentity, true
}

// takeWaiting removes and returns the account waiting in this browser.
func (p *oidcProvider) takeWaiting(browser string) (oidcIdentity, bool) {
	id, ok := p.waitingFor(browser)
	if ok {
		p.mu.Lock()
		delete(p.waiting, secretDigest(browser))
		p.mu.Unlock()
	}
	return id, ok
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// authorizeURL is where the browser goes to sign in with Microsoft.
func (p *oidcProvider) authorizeURL(redirect string, attempt oidcPending) string {
	q := url.Values{
		"client_id":             {p.clientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"response_mode":         {"query"},
		"scope":                 {"openid profile email"},
		"state":                 {attempt.State},
		"nonce":                 {attempt.Nonce},
		"code_challenge":        {pkceChallenge(attempt.Verifier)},
		"code_challenge_method": {"S256"},
		"prompt":                {"select_account"},
	}
	return p.authority + "/common/oauth2/v2.0/authorize?" + q.Encode()
}

var errMicrosoftUnavailable = problem(503, "microsoft_unavailable", "Microsoft sign-in could not be finished. Please try again, or sign in another way.")

// exchange swaps the authorization code for an ID token.
func (p *oidcProvider) exchange(ctx context.Context, code, redirect, verifier string) (string, error) {
	form := url.Values{"client_id": {p.clientID}, "client_secret": {p.secret}, "grant_type": {"authorization_code"},
		"code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "scope": {"openid profile email"}}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.authority+"/common/oauth2/v2.0/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errMicrosoftUnavailable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return "", errMicrosoftUnavailable
	}
	defer res.Body.Close()
	var body struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return "", errMicrosoftUnavailable
	}
	if res.StatusCode != http.StatusOK || body.IDToken == "" {
		if body.Error == "invalid_grant" {
			return "", problem(400, "microsoft_expired", "That Microsoft sign-in expired or was already used. Please try again.")
		}
		return "", errMicrosoftUnavailable
	}
	return body.IDToken, nil
}

// signingKey returns Microsoft's key with this ID, fetching the published
// keys when they are old or the ID is new to us.
func (p *oidcProvider) signingKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	p.mu.Lock()
	now := p.now()
	key, ok := p.keys[kid]
	fresh := now.Sub(p.keysAt) < oidcKeysFor
	retry := now.Sub(p.triedAt) >= oidcKeysRetry
	if ok && fresh {
		p.mu.Unlock()
		return key, nil
	}
	if !retry && fresh {
		p.mu.Unlock()
		return nil, errBadIDToken
	}
	p.triedAt = now
	p.mu.Unlock()
	keys, err := p.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.keys, p.keysAt = keys, now
	p.mu.Unlock()
	if key, ok := keys[kid]; ok {
		return key, nil
	}
	return nil, errBadIDToken
}

func (p *oidcProvider) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.authority+"/common/discovery/v2.0/keys", nil)
	if err != nil {
		return nil, errMicrosoftUnavailable
	}
	res, err := p.http.Do(req)
	if err != nil {
		return nil, errMicrosoftUnavailable
	}
	defer res.Body.Close()
	var set struct {
		Keys []struct {
			Kty, Use, Kid, N, E string
		} `json:"keys"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set) != nil {
		return nil, errMicrosoftUnavailable
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || k.Kid == "" || len(keys) >= 64 {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		modulus := new(big.Int).SetBytes(n)
		exponent := int(new(big.Int).SetBytes(e).Int64())
		if bits := modulus.BitLen(); bits < 2048 || bits > 4096 || exponent < 3 || exponent%2 == 0 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: modulus, E: exponent}
	}
	if len(keys) == 0 {
		return nil, errMicrosoftUnavailable
	}
	return keys, nil
}

var errBadIDToken = problem(400, "microsoft_token", "Microsoft’s reply could not be checked. Please try again, or sign in another way.")

// verifyIDToken checks a Microsoft ID token and returns who it names.
func (p *oidcProvider) verifyIDToken(ctx context.Context, raw, nonce string) (oidcIdentity, error) {
	parts := strings.Split(raw, ".")
	if len(raw) > 16384 || len(parts) != 3 {
		return oidcIdentity{}, errBadIDToken
	}
	headerJSON, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	claimsJSON, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	signature, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return oidcIdentity{}, errBadIDToken
	}
	var header struct {
		Alg, Kid string
	}
	if json.Unmarshal(headerJSON, &header) != nil || header.Alg != "RS256" || header.Kid == "" {
		return oidcIdentity{}, errBadIDToken
	}
	key, err := p.signingKey(ctx, header.Kid)
	if err != nil {
		return oidcIdentity{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) != nil {
		return oidcIdentity{}, errBadIDToken
	}
	var c struct {
		Iss               string          `json:"iss"`
		Aud               json.RawMessage `json:"aud"`
		Exp               *float64        `json:"exp"`
		Nbf               *float64        `json:"nbf"`
		Iat               *float64        `json:"iat"`
		Nonce             string          `json:"nonce"`
		Tid               string          `json:"tid"`
		Oid               string          `json:"oid"`
		Email             string          `json:"email"`
		PreferredUsername string          `json:"preferred_username"`
		Name              string          `json:"name"`
	}
	if json.Unmarshal(claimsJSON, &c) != nil {
		return oidcIdentity{}, errBadIDToken
	}
	var aud string
	if json.Unmarshal(c.Aud, &aud) != nil {
		var list []string
		if json.Unmarshal(c.Aud, &list) != nil || len(list) != 1 {
			return oidcIdentity{}, errBadIDToken
		}
		aud = list[0]
	}
	now := p.now()
	at := func(v *float64) time.Time { return time.Unix(int64(*v), 0) }
	switch {
	case !guidPattern.MatchString(c.Tid), !guidPattern.MatchString(c.Oid):
	case c.Iss != p.authority+"/"+c.Tid+"/v2.0":
	case aud != p.clientID:
	case c.Exp == nil || !now.Before(at(c.Exp).Add(oidcClockSkew)):
	case c.Nbf != nil && now.Add(oidcClockSkew).Before(at(c.Nbf)):
	case c.Iat == nil || now.Add(oidcClockSkew).Before(at(c.Iat)):
	case nonce == "" || subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
	default:
		id := oidcIdentity{Tenant: c.Tid, Object: c.Oid, Name: clipRunes(strings.TrimSpace(c.Name), 128)}
		for _, candidate := range []string{c.Email, c.PreferredUsername} {
			if email, err := normalizeEmail(candidate); err == nil && strings.Contains(email, "@") {
				id.Email = email
				break
			}
		}
		return id, nil
	}
	return oidcIdentity{}, errBadIDToken
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (h *httpAdapter) microsoftCookieName() string {
	name := "__Host-metatrash-microsoft"
	if h.basePath != "" {
		name += "-" + secretDigest(h.basePath)[:16]
	}
	return name
}

func (h *httpAdapter) microsoftRedirect() string {
	return h.publicOrigin + h.basePath + "/login/microsoft/callback"
}

// setMicrosoftCookie keeps the browser's attempt (or waiting account) for
// ten minutes. Lax: it must come back on the return from Microsoft.
func (h *httpAdapter) setMicrosoftCookie(w http.ResponseWriter, value string) {
	age := int(oidcLifetime / time.Second)
	expires := time.Now().Add(oidcLifetime)
	if value == "" {
		age, expires = -1, time.Unix(1, 0)
	}
	http.SetCookie(w, &http.Cookie{Name: h.microsoftCookieName(), Value: value, Path: "/", MaxAge: age, Expires: expires, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// microsoftWaiting is the unlinked Microsoft account waiting in this
// browser, if any.
func (h *httpAdapter) microsoftWaiting(r *http.Request) (oidcIdentity, bool) {
	a := h.service.accounts
	if a == nil || a.microsoft == nil {
		return oidcIdentity{}, false
	}
	return a.microsoft.waitingFor(cookieToken(r, h.microsoftCookieName()))
}

// startMicrosoft sends the browser to Microsoft to sign in: from the sign-in
// page (userID empty) or to link an account from Security.
func (h *httpAdapter) startMicrosoft(w http.ResponseWriter, r *http.Request, client, userID, session string) {
	p := h.service.accounts.microsoft
	if p == nil {
		sendError(w, missing())
		return
	}
	if err := h.service.rates.take(allowance{"microsoft:start:" + client, 30, 600}); err != nil {
		sendError(w, err)
		return
	}
	attempt := oidcPending{UserID: userID, Session: session}
	values := make([]string, 4)
	for i := range values {
		v, err := randomHex(32)
		if err != nil {
			sendError(w, err)
			return
		}
		values[i] = v
	}
	browser := values[0]
	attempt.State, attempt.Nonce, attempt.Verifier = values[1], values[2], values[3]
	if err := p.begin(browser, attempt); err != nil {
		sendError(w, err)
		return
	}
	h.setMicrosoftCookie(w, browser)
	http.Redirect(w, r, p.authorizeURL(h.microsoftRedirect(), attempt), http.StatusSeeOther)
}

// serveMicrosoft handles /login/microsoft/callback, Microsoft's return. It
// returns false for other paths or when Microsoft sign-in is off. Account
// routes reject queries, hence this separate route.
func (h *httpAdapter) serveMicrosoft(w http.ResponseWriter, r *http.Request, client string) bool {
	a := h.service.accounts
	if r.URL.Path != "/login/microsoft/callback" || a == nil || a.microsoft == nil {
		return false
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return true
	}
	origin, _ := url.Parse(a.config.Origin)
	if !strings.EqualFold(r.Host, origin.Host) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return true
	}
	entry := &loginLog{kind: "microsoft", ip: client, ua: r.UserAgent(), email: "-", result: "error"}
	defer entry.write()
	h.microsoftCallback(w, r, client, entry)
	return true
}

func (h *httpAdapter) renderMicrosoft(w http.ResponseWriter, status int, title, message, back string) {
	h.renderGitHub(w, status, githubPage{Eyebrow: "Microsoft", Title: title, Message: message, ContinueURL: back, Button: "Continue →"})
}

func (h *httpAdapter) microsoftCallback(w http.ResponseWriter, r *http.Request, client string, entry *loginLog) {
	a := h.service.accounts
	p := a.microsoft
	login := h.basePath + "/login"
	fail := func(err error) {
		status, message := 503, "Microsoft sign-in could not be finished. Please try again, or sign in another way."
		var e *Error
		if errors.As(err, &e) {
			status, message = e.Status, e.Message
			entry.result = e.Code
		}
		h.renderMicrosoft(w, status, "Not signed in with Microsoft", message, login)
	}
	if err := h.service.rates.take(allowance{"microsoft:callback:" + client, 30, 600}); err != nil {
		fail(err)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	bad := problem(400, "bad_reply", "Microsoft sent an unexpected reply. Start again from the sign-in page.")
	if err != nil {
		fail(bad)
		return
	}
	for _, key := range []string{"code", "state", "error"} {
		if len(query[key]) > 1 {
			fail(bad)
			return
		}
	}
	// The attempt is single-use whatever happens next.
	browser := cookieToken(r, h.microsoftCookieName())
	attempt, ok := p.take(browser)
	if !ok || query.Get("state") == "" || subtle.ConstantTimeCompare([]byte(attempt.State), []byte(query.Get("state"))) != 1 {
		h.setMicrosoftCookie(w, "")
		fail(problem(400, "bad_state", "This Microsoft reply did not match a sign-in started in this browser in the last ten minutes. Start again from the sign-in page."))
		return
	}
	if attempt.UserID != "" {
		login = h.basePath + "/account/security"
	}
	if query.Has("error") {
		h.setMicrosoftCookie(w, "")
		entry.result = "cancelled"
		h.renderMicrosoft(w, 200, "Not signed in with Microsoft", "The Microsoft sign-in was cancelled or refused. You can try again, or sign in another way.", login)
		return
	}
	code := query.Get("code")
	if code == "" || len(code) > 4096 {
		h.setMicrosoftCookie(w, "")
		fail(bad)
		return
	}
	raw, err := p.exchange(r.Context(), code, h.microsoftRedirect(), attempt.Verifier)
	var id oidcIdentity
	if err == nil {
		id, err = p.verifyIDToken(r.Context(), raw, attempt.Nonce)
	}
	if err != nil {
		h.setMicrosoftCookie(w, "")
		fail(err)
		return
	}
	db := h.service.ownedDB
	if db == nil {
		fail(problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later."))
		return
	}
	ctx, now := r.Context(), time.Now()
	linked, found, err := db.identityBySubject(ctx, "microsoft", id.Tenant, id.Object)
	if err != nil {
		fail(problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later."))
		return
	}
	entry.email = id.Email
	if attempt.UserID != "" {
		h.setMicrosoftCookie(w, "")
		h.finishMicrosoftLink(w, r, attempt, id, linked, found, entry)
		return
	}
	if !found {
		// Never signs anyone in: it waits for this browser to sign in
		// another way and confirm, whatever address the token names.
		if err := p.wait(browser, id); err != nil {
			fail(err)
			return
		}
		h.setMicrosoftCookie(w, browser)
		entry.account, entry.result = "unlinked", "waiting"
		h.continueTo(w, r, login)
		return
	}
	h.setMicrosoftCookie(w, "")
	user, ok, err := a.store.ByID(ctx, linked.UserID)
	if err != nil || !ok {
		fail(problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later."))
		return
	}
	entry.email, entry.account = user.Email, "existing"
	ctx = withSessionOrigin(ctx, sessionOrigin{Device: deviceLabel(r.UserAgent()), IP: client})
	a.mu.Lock()
	token, err := a.startSessionLocked(ctx, user.ID, now)
	if err == nil {
		for k, pending := range a.challenges {
			if pending.Email == user.Email {
				delete(a.challenges, k)
			}
		}
	}
	a.mu.Unlock()
	if err != nil {
		fail(err)
		return
	}
	if err := db.useIdentity(ctx, linked.ID, id, now); err != nil {
		// Only the labels and last-used time; the sign-in stands.
		entry.result = "ok_unrecorded"
	} else {
		entry.result = "ok"
	}
	// This cross-site request did not carry the browser's Strict session
	// cookie, so a session it had before is replaced in the browser but not
	// ended on the server; it expires within a day, or Signed-in devices on
	// Security ends it.
	accountCookie(w, h.sessionCookieName(), token, int(sessionLifetime/time.Second))
	h.continueTo(w, r, login)
}

// finishMicrosoftLink ends Link a Microsoft account, started on Security.
func (h *httpAdapter) finishMicrosoftLink(w http.ResponseWriter, r *http.Request, attempt oidcPending, id oidcIdentity, linked identity, found bool, entry *loginLog) {
	a := h.service.accounts
	back := h.basePath + "/account/security"
	entry.kind = "microsoft-link"
	user, signedIn, err := a.currentUser(r.Context(), attempt.Session)
	fresh, _ := a.sessionFresh(attempt.Session, time.Now())
	switch {
	case err != nil:
		err = problem(503, "account_unavailable", "Your account is temporarily unavailable. Please try again later.")
	case !signedIn || user.ID != attempt.UserID:
		err = problem(403, "signed_out", "You were signed out before Microsoft replied. Sign in and link the account again.")
	case found && linked.UserID == user.ID:
		entry.result = "already"
		accountCookie(w, h.noticeCookieName(), "microsoft-already", 60)
		h.continueTo(w, r, back)
		return
	case found:
		err = problem(409, "microsoft_taken", "That Microsoft account is already linked to another Metatrash account. Remove it there first.")
	case !fresh:
		err = errConfirmFirst
	default:
		err = h.service.ownedDB.addIdentity(r.Context(), user.ID, "microsoft", id, time.Now())
	}
	if err != nil {
		status, message := 503, "The Microsoft account could not be linked. Please try again."
		var e *Error
		if errors.As(err, &e) {
			status, message = e.Status, e.Message
			entry.result = e.Code
		}
		h.renderMicrosoft(w, status, "Microsoft account not linked", message, back)
		return
	}
	entry.email, entry.account, entry.result = user.Email, "existing", "linked"
	h.notifySignInChange(user, "added", "Microsoft account "+id.Label())
	accountCookie(w, h.noticeCookieName(), "microsoft-linked", 60)
	h.continueTo(w, r, back)
}

// continueTo shows a page that reloads itself at target from this site: the
// browser arrived from Microsoft, so it did not send the Strict cookies.
func (h *httpAdapter) continueTo(w http.ResponseWriter, r *http.Request, target string) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	h.renderAccount(w, r, 200, accountPage{ContinueURL: target})
}

// confirmMicrosoftLink links the account waiting in this browser to the
// signed-in user (Link it on Security, after signing in another way).
func (h *httpAdapter) confirmMicrosoftLink(r *http.Request, session string, user userAccount, now time.Time) error {
	a := h.service.accounts
	if a.microsoft == nil {
		return missing()
	}
	if fresh, _ := a.sessionFresh(session, now); !fresh {
		return errConfirmFirst
	}
	id, ok := a.microsoft.takeWaiting(cookieToken(r, h.microsoftCookieName()))
	if !ok {
		return problem(404, "nothing_waiting", "No Microsoft account is waiting to be linked in this browser any more. Choose Link a Microsoft account to start again.")
	}
	db := h.service.ownedDB
	linked, found, err := db.identityBySubject(r.Context(), "microsoft", id.Tenant, id.Object)
	if err != nil {
		return problem(503, "account_unavailable", "Your account is temporarily unavailable. Please try again later.")
	}
	if found {
		if linked.UserID == user.ID {
			return nil
		}
		return problem(409, "microsoft_taken", "That Microsoft account is already linked to another Metatrash account. Remove it there first.")
	}
	if err := db.addIdentity(r.Context(), user.ID, "microsoft", id, now); err != nil {
		return err
	}
	h.notifySignInChange(user, "added", "Microsoft account "+id.Label())
	return nil
}
