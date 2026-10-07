package service

import (
	"context"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Sign-in methods on Your account (0.22.0): confirming it's you (step-up)
// and managing passkeys; and signing in with a passkey. The authenticator
// app (0.24.0) is in totp.go.
//
// Adding or removing a passkey needs a session that signed in or confirmed
// within stepUpWindow. Confirming uses one of the user's passkeys, their
// authenticator app or a code emailed to the account's address.

// signInRoutes are the POST routes of the Sign-in methods section. Each has
// its own session-bound CSRF token.
var signInRoutes = map[string]bool{
	"/account/confirm/send":    true,
	"/account/confirm/verify":  true,
	"/account/confirm/passkey": true,
	"/account/passkeys/add":    true,
	"/account/passkeys/rename": true,
	"/account/passkeys/remove": true,
	"/account/confirm/totp":    true,
	"/account/totp/start":      true,
	"/account/totp/enable":     true,
	"/account/totp/remove":     true,
}

// signInFormFields are the fields each route accepts besides csrf.
var signInFormFields = map[string][]string{
	"/account/confirm/send":    nil,
	"/account/confirm/verify":  {"code"},
	"/account/confirm/passkey": passkeyAssertionFields,
	"/account/passkeys/add":    {"name", "client_data", "attestation", "transports"},
	"/account/passkeys/rename": {"passkey", "name"},
	"/account/passkeys/remove": {"passkey"},
	"/login/passkey":           passkeyAssertionFields,
	"/account/confirm/totp":    {"code"},
	"/account/totp/start":      nil,
	"/account/totp/enable":     {"code"},
	"/account/totp/remove":     nil,
	"/login/totp":              {"email", "code"},
}

var passkeyAssertionFields = []string{"credential", "client_data", "authenticator_data", "signature", "user_handle"}

type passkeyView struct {
	ID, Name          string
	Created, LastUsed string
	Synced            bool
}

type signInPage struct {
	CSRF map[string]string
	// Fresh: the session signed in or confirmed within stepUpWindow.
	Fresh        bool
	FreshMinutes int
	ConfirmSent  bool
	Passkeys     []passkeyView
	Unavailable  bool
	// ConfirmOptions and CreateOptions are WebAuthn options (JSON) for
	// passkey.js; empty when that form is not shown.
	ConfirmOptions, CreateOptions string
	MaxPasskeys                   bool
	// TOTP is the Authenticator app card; nil when the server has no
	// totpKeyFile.
	TOTP *totpView
}

func (a *accounts) signInCSRF(path, session string) string {
	return a.mac("signin:" + path + ":" + session)
}

// signInSection builds the Sign-in methods section of Your account.
func (h *httpAdapter) signInSection(r *http.Request, user userAccount) (*signInPage, bool) {
	a := h.service.accounts
	session := cookieToken(r, h.sessionCookieName())
	now := time.Now()
	page := &signInPage{CSRF: map[string]string{}}
	for path := range signInRoutes {
		page.CSRF[strings.TrimPrefix(path, "/account/")] = a.signInCSRF(path, session)
	}
	var until time.Time
	page.Fresh, until = a.sessionFresh(session, now)
	if page.Fresh {
		page.FreshMinutes = int(math.Ceil(until.Sub(now).Minutes()))
	}
	if h.service.ownedDB == nil {
		page.Unavailable = true
		return page, false
	}
	list, err := h.service.ownedDB.passkeys(r.Context(), user.ID)
	if err != nil {
		page.Unavailable = true
		return page, false
	}
	for _, p := range list {
		view := passkeyView{ID: p.ID, Name: p.Name, Created: time.Unix(p.CreatedAt, 0).UTC().Format("2 Jan 2006"), LastUsed: "never", Synced: p.BackupEligible}
		if p.LastUsedAt > 0 {
			view.LastUsed = time.Unix(p.LastUsedAt, 0).UTC().Format("2 Jan 2006")
		}
		page.Passkeys = append(page.Passkeys, view)
	}
	if a.totpKey != nil {
		if page.TOTP, err = h.totpSection(r.Context(), user); err != nil {
			page.Unavailable = true
			return page, false
		}
	}
	a.mu.Lock()
	c, waiting := a.challenges[confirmKey(session)]
	page.ConfirmSent = waiting && c.Ready && now.Before(c.Expires)
	a.mu.Unlock()
	page.MaxPasskeys = len(list) >= maxPasskeysPerUser
	if page.Fresh && !page.MaxPasskeys {
		page.CreateOptions = a.creationOptions(session, user, list, now)
	}
	if !page.Fresh && len(list) > 0 {
		page.ConfirmOptions = a.requestOptions("confirm", session, list, now)
	}
	return page, true
}

// formBytes decodes a base64url form field.
func formBytes(r *http.Request, field string, limit int, required bool) ([]byte, error) {
	value := r.PostForm.Get(field)
	if value == "" && !required {
		return nil, nil
	}
	b, err := b64.DecodeString(value)
	if err != nil || len(b) == 0 || len(b) > limit {
		if value == "" {
			return nil, problem(400, "invalid_passkey", "Passkeys need JavaScript and a browser that supports them.")
		}
		return nil, passkeyFailed()
	}
	return b, nil
}

type passkeyAssertion struct {
	credential, clientData, authData, signature, handle []byte
}

func readPasskeyAssertion(r *http.Request) (passkeyAssertion, error) {
	var p passkeyAssertion
	var err error
	if p.credential, err = formBytes(r, "credential", maxCredentialIDBytes, true); err != nil {
		return p, err
	}
	if p.clientData, err = formBytes(r, "client_data", 4096, true); err != nil {
		return p, err
	}
	if p.authData, err = formBytes(r, "authenticator_data", 1024, true); err != nil {
		return p, err
	}
	if p.signature, err = formBytes(r, "signature", 1024, true); err != nil {
		return p, err
	}
	p.handle, err = formBytes(r, "user_handle", 64, false)
	return p, err
}

var errConfirmFirst = problem(403, "confirm_required", "Confirm it’s you first. Adding or removing a sign-in method needs a sign-in or confirmation within the last 10 minutes.")

// submitSignIn handles the Sign-in methods forms after the form, Origin and
// CSRF checks.
func (h *httpAdapter) submitSignIn(w http.ResponseWriter, r *http.Request, session, path, client string) {
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
	db := h.service.ownedDB
	if db == nil {
		sendError(w, problem(503, "unavailable", "Sign-in methods are temporarily unavailable."))
		return
	}
	ctx, now := r.Context(), time.Now()
	notice := ""
	err = h.service.rates.take(allowance{"signin:user:" + user.ID, 30, 600})
	if err == nil {
		switch path {
		case "/account/confirm/send":
			emailKey := a.mac("email:" + user.Email)
			err = h.service.rates.reserve(allowance{"mail:global:day", 100, 86400}, allowance{"mail:ip:" + client, 10, 3600}, allowance{"mail:email:minute:" + emailKey, 1, 60}, allowance{"mail:email:hour:" + emailKey, 3, 3600})
			if err == nil {
				err = a.issueCode(ctx, confirmKey(session), user.Email, a.sendConfirm)
			}
		case "/account/confirm/verify":
			a.mu.Lock()
			a.cleanup(now)
			var email string
			email, err = a.checkCodeLocked(confirmKey(session), strings.TrimSpace(r.PostForm.Get("code")))
			a.mu.Unlock()
			if err == nil && email != user.Email {
				err = problem(400, "invalid_code", "That code is invalid or expired. Try again, or request a new code.")
			}
			if err == nil {
				a.markConfirmed(session, user.ID, now)
				notice = "signin-confirmed"
			}
		case "/account/confirm/passkey":
			var in passkeyAssertion
			if in, err = readPasskeyAssertion(r); err == nil {
				err = h.confirmWithPasskey(ctx, session, user, in, now)
			}
			if err == nil {
				notice = "signin-confirmed"
			}
		case "/account/confirm/totp", "/account/totp/start", "/account/totp/enable", "/account/totp/remove":
			notice, err = h.submitTOTP(ctx, path, session, user, r.PostForm.Get("code"), now)
		case "/account/passkeys/add":
			err = h.addPasskey(r, session, user, now)
			notice = "passkey-added"
		case "/account/passkeys/rename":
			var name string
			if name, err = validPasskeyName(r.PostForm.Get("name")); err == nil {
				err = db.renamePasskey(ctx, user.ID, r.PostForm.Get("passkey"), name)
			}
			notice = "passkey-renamed"
		case "/account/passkeys/remove":
			if fresh, _ := a.sessionFresh(session, now); !fresh {
				err = errConfirmFirst
				break
			}
			var name string
			if name, err = db.removePasskey(ctx, user.ID, r.PostForm.Get("passkey")); err == nil {
				h.notifySignInChange(user, "removed", "passkey \""+name+"\"")
			}
			notice = "passkey-removed"
		}
	}
	if err == nil {
		if notice != "" {
			accountCookie(w, h.noticeCookieName(), notice, 60)
		}
		http.Redirect(w, r, h.basePath+"/account/security", http.StatusSeeOther)
		return
	}
	page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "That change could not be saved. Reload the page and try again."}
	status := http.StatusServiceUnavailable
	var p *Error
	if errors.As(err, &p) {
		status, page.Message = p.Status, p.Message
		if p.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
		}
	}
	h.renderAccount(w, r, status, page)
}

func (h *httpAdapter) confirmWithPasskey(ctx context.Context, session string, user userAccount, in passkeyAssertion, now time.Time) error {
	a, db := h.service.accounts, h.service.ownedDB
	p, found, err := db.passkeyByCredential(ctx, in.credential)
	if err != nil {
		return err
	}
	if !found || p.UserID != user.ID {
		return problem(400, "invalid_passkey", "Use a passkey that belongs to this account, or confirm with an emailed code.")
	}
	count, backedUp, err := a.verifyPasskeyAssertion("confirm", session, p, in.clientData, in.authData, in.signature, in.handle, now)
	if err != nil {
		return err
	}
	if err := db.usePasskey(ctx, p, count, backedUp, now); err != nil {
		return err
	}
	a.markConfirmed(session, user.ID, now)
	return nil
}

func (h *httpAdapter) addPasskey(r *http.Request, session string, user userAccount, now time.Time) error {
	a, db := h.service.accounts, h.service.ownedDB
	if fresh, _ := a.sessionFresh(session, now); !fresh {
		return errConfirmFirst
	}
	name, err := validPasskeyName(r.PostForm.Get("name"))
	if err != nil {
		return err
	}
	clientData, err := formBytes(r, "client_data", 4096, true)
	if err != nil {
		return err
	}
	attestation, err := formBytes(r, "attestation", 6144, true)
	if err != nil {
		return err
	}
	p, err := a.verifyPasskeyRegistration(session, user.ID, clientData, attestation, r.PostForm.Get("transports"), now)
	if err != nil {
		return err
	}
	if p.ID, err = randomHex(16); err != nil {
		return err
	}
	p.Name, p.CreatedAt = name, now.Unix()
	if err := db.addPasskey(r.Context(), p); err != nil {
		return err
	}
	h.notifySignInChange(user, "added", "passkey \""+name+"\"")
	return nil
}

// passkeySignIn signs in with a passkey (POST /login/passkey) and returns the
// new session token and its account.
func (h *httpAdapter) passkeySignIn(r *http.Request, browser string) (string, userAccount, error) {
	a, db := h.service.accounts, h.service.ownedDB
	if db == nil {
		return "", userAccount{}, problem(503, "unavailable", "Passkey sign-in is temporarily unavailable. Use an emailed code.")
	}
	in, err := readPasskeyAssertion(r)
	if err != nil {
		return "", userAccount{}, err
	}
	ctx, now := r.Context(), time.Now()
	p, found, err := db.passkeyByCredential(ctx, in.credential)
	if err != nil {
		return "", userAccount{}, problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	}
	if !found {
		return "", userAccount{}, problem(400, "unknown_passkey", "That passkey is not registered with Metatrash (it may have been removed from your account). Sign in with an emailed code instead.")
	}
	user, ok, err := a.store.ByID(ctx, p.UserID)
	if err != nil || !ok {
		return "", userAccount{}, problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	}
	count, backedUp, err := a.verifyPasskeyAssertion("login", browser, p, in.clientData, in.authData, in.signature, in.handle, now)
	if err != nil {
		return "", user, err
	}
	if err := db.usePasskey(ctx, p, count, backedUp, now); err != nil {
		return "", user, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	token, err := a.startSessionLocked(user.ID, now)
	if err != nil {
		return "", user, err
	}
	// As with a code: signing in invalidates outstanding codes for this email.
	for k, pending := range a.challenges {
		if pending.Email == user.Email {
			delete(a.challenges, k)
		}
	}
	return token, user, nil
}

// notifySignInChange emails the account a notice that a sign-in method was
// added or removed. Best effort: it never delays or fails the change.
func (h *httpAdapter) notifySignInChange(user userAccount, change, what string) {
	a := h.service.accounts
	if a.sendNotice == nil || h.service.rates.take(allowance{"mail:global:day", 100, 86400}) != nil {
		return
	}
	body := signInMethodMail(a.config.Origin, change, what, time.Now())
	go func() {
		if err := a.sendNotice(context.Background(), user.Email, "Sign-in method "+change+" on your Metatrash account", body); err != nil {
			// SMTP responses may contain addresses; never log them.
			log.Print("sign-in method notice not sent")
		}
	}()
}
