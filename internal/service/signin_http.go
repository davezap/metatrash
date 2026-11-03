package service

import (
	"context"
	"log"
	"math"
	"net/http"
	"strings"
	"time"
)

// Sign-in methods (0.22.0; Settings → Security since 0.81.0, site_security.go):
// confirming it's you (step-up) and managing passkeys; and signing in with a
// passkey. The authenticator
// app (0.24.0) is in totp.go; recovery codes and the email sign-in switch
// (0.25.0) are in recovery.go.
//
// Adding or removing a passkey needs a session that signed in or confirmed
// within stepUpWindow. Confirming uses one of the user's passkeys, their
// authenticator app or a code emailed to the account's address (unless the
// account turned email sign-in off).

// signInRoutes name the Security changes. They were the old account page's
// form routes; Settings posts each one to /api/v1/site/account/security/...
// (with /account/ dropped). Confirming it's you is site_account.go's
// /api/v1/site/account/confirm/....
var signInRoutes = map[string]bool{
	"/account/passkeys/add":    true,
	"/account/passkeys/rename": true,
	"/account/passkeys/remove": true,
	"/account/totp/start":      true,
	"/account/totp/enable":     true,
	"/account/totp/remove":     true,
	"/account/recovery/create": true,
	"/account/email-login":     true,
	"/account/email/change":    true,
	"/account/email/verify":    true,
	"/account/email/cancel":    true,
	"/account/sessions/end":    true,
	"/account/sessions/remove": true,
}

var passkeyAssertionFields = []string{"credential", "client_data", "authenticator_data", "signature", "user_handle"}

type passkeyView struct {
	ID, Name          string
	Created, LastUsed string
	Synced            bool
}

type signInPage struct {
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
	// EmailLogin: emailed codes sign in and confirm. CanTurnOffEmail: the
	// account has what turning it off needs (recovery.go).
	EmailLogin, CanTurnOffEmail, Strong bool
	Recovery                            int
	RecoveryCreated                     string
	// NewRecoveryCodes are shown once, right after they are created.
	NewRecoveryCodes []string
	// EmailChangeTo is the new address a code was sent to, while it waits.
	EmailChangeTo string
	// Sessions are the signed-in devices, this one first; OtherSessions
	// counts the rest.
	Sessions      []sessionView
	OtherSessions int
}

// signInSection is the state Settings → Security shows (siteSecurityOf).
func (h *httpAdapter) signInSection(r *http.Request, user userAccount) (*signInPage, bool) {
	a := h.service.accounts
	session := cookieToken(r, h.sessionCookieName())
	now := time.Now()
	page := &signInPage{}
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
	state, err := h.service.ownedDB.signInState(r.Context(), user.ID)
	if err != nil {
		page.Unavailable = true
		return page, false
	}
	page.EmailLogin, page.Strong, page.Recovery = state.EmailLogin, state.Strong(), state.Recovery
	page.CanTurnOffEmail = state.Strong() && state.Recovery > 0
	if state.RecoveryCreated > 0 {
		page.RecoveryCreated = time.Unix(state.RecoveryCreated, 0).UTC().Format("2 Jan 2006")
	}
	page.NewRecoveryCodes = a.takeRecoveryReveal(session, user.ID, now)
	page.EmailChangeTo = a.pendingEmailChange(session, now)
	page.Sessions = a.userSessions(user.ID, session, now)
	page.OtherSessions = a.otherSessions(user.ID, session, now)
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
	return fieldBytes(r.PostForm.Get(field), limit, required)
}

// fieldBytes decodes one base64url passkey field, from a form or JSON.
func fieldBytes(value string, limit int, required bool) ([]byte, error) {
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
	return passkeyAssertionFrom(r.PostForm.Get)
}

// passkeyAssertionFrom reads an assertion's fields by their form names.
func passkeyAssertionFrom(get func(string) string) (passkeyAssertion, error) {
	var p passkeyAssertion
	var err error
	if p.credential, err = fieldBytes(get("credential"), maxCredentialIDBytes, true); err != nil {
		return p, err
	}
	if p.clientData, err = fieldBytes(get("client_data"), 4096, true); err != nil {
		return p, err
	}
	if p.authData, err = fieldBytes(get("authenticator_data"), 1024, true); err != nil {
		return p, err
	}
	if p.signature, err = fieldBytes(get("signature"), 1024, true); err != nil {
		return p, err
	}
	p.handle, err = fieldBytes(get("user_handle"), 64, false)
	return p, err
}

// signsOutOthers are the changes after which the account's other sessions
// are signed out: removing a sign-in method or changing the address.
var signsOutOthers = map[string]bool{
	"passkey-removed": true,
	"totp-removed":    true,
	"email-login-off": true,
	"email-changed":   true,
}

var errConfirmFirst = problem(403, "confirm_required", "Confirm it’s you first. Adding or removing a sign-in method needs a sign-in or confirmation within the last 10 minutes.")

// signInChange makes one Security change (path is its form route,
// /account/...) for the session's user, reading its fields with get: for the
// Security forms and the Settings dialog's Security tab (0.81.0). It answers
// the notice to show, or where to send the browser next.
func (h *httpAdapter) signInChange(w http.ResponseWriter, r *http.Request, session, client, path string, user userAccount, get func(string) string) (string, string, error) {
	a, db := h.service.accounts, h.service.ownedDB
	if db == nil {
		return "", "", problem(503, "unavailable", "Sign-in methods are temporarily unavailable.")
	}
	ctx, now := r.Context(), time.Now()
	notice := ""
	err := h.service.rates.take(allowance{"signin:user:" + user.ID, 30, 600})
	if err == nil {
		switch path {
		case "/account/totp/start", "/account/totp/enable", "/account/totp/remove":
			notice, err = h.submitTOTP(ctx, path, session, user, get("code"), now)
		case "/account/recovery/create":
			err = h.createRecoveryCodes(ctx, session, user, now)
			notice = "recovery-created"
		case "/account/email-login":
			notice, err = h.switchEmailLogin(ctx, session, user, get("email_login"), now)
		case "/account/email/change":
			err = h.startEmailChange(ctx, session, client, user, get("email"), now)
			notice = "email-change-sent"
		case "/account/email/verify":
			_, err = h.finishEmailChange(ctx, session, user, strings.TrimSpace(get("code")), now)
			notice = "email-changed"
		case "/account/email/cancel":
			a.cancelEmailChange(session)
			notice = "email-change-cancelled"
		case "/account/passkeys/add":
			err = h.addPasskey(ctx, session, user, get, now)
			notice = "passkey-added"
		case "/account/passkeys/rename":
			var name string
			if name, err = validPasskeyName(get("name")); err == nil {
				err = db.renamePasskey(ctx, user.ID, get("passkey"), name)
			}
			notice = "passkey-renamed"
		case "/account/passkeys/remove":
			if fresh, _ := a.sessionFresh(session, now); !fresh {
				err = errConfirmFirst
				break
			}
			var name string
			if name, err = db.removePasskey(ctx, user.ID, get("passkey")); err == nil {
				h.notifySignInChange(user, "removed", "passkey \""+name+"\"")
			}
			notice = "passkey-removed"
		case "/account/sessions/end":
			var n int
			n, err = a.endOtherSessions(user.ID, session)
			log.Printf("account sessions-end user=%s ended=%d stored=%v", user.ID, n, err == nil)
			if err != nil {
				err = problem(503, "sessions_not_stored", "The other browsers are signed out for now, but that could not be saved and they may be signed in again after the next restart. Try again shortly.")
			}
			notice = "sessions-ended"
		case "/account/sessions/remove":
			var found bool
			found, err = a.endSessionByID(user.ID, session, get("session"))
			log.Printf("account session-end user=%s found=%v stored=%v", user.ID, found, err == nil)
			switch {
			case err != nil:
				err = problem(503, "sessions_not_stored", "That device is signed out for now, but that could not be saved and it may be signed in again after the next restart. Try again shortly.")
			case !found:
				err = problem(404, "session_not_found", "That device is already signed out. Reload the page to see the current list.")
			}
			notice = "session-ended"
		}
	}
	if err == nil && signsOutOthers[notice] {
		// Whoever may have been signed in elsewhere loses the account along
		// with the method or address they could have used to get back in.
		a.endOtherSessions(user.ID, session)
	}
	return notice, "", err
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

func (h *httpAdapter) addPasskey(ctx context.Context, session string, user userAccount, get func(string) string, now time.Time) error {
	a, db := h.service.accounts, h.service.ownedDB
	if fresh, _ := a.sessionFresh(session, now); !fresh {
		return errConfirmFirst
	}
	name, err := validPasskeyName(get("name"))
	if err != nil {
		return err
	}
	clientData, err := fieldBytes(get("client_data"), 4096, true)
	if err != nil {
		return err
	}
	attestation, err := fieldBytes(get("attestation"), 6144, true)
	if err != nil {
		return err
	}
	p, err := a.verifyPasskeyRegistration(session, user.ID, clientData, attestation, get("transports"), now)
	if err != nil {
		return err
	}
	if p.ID, err = randomHex(16); err != nil {
		return err
	}
	p.Name, p.CreatedAt = name, now.Unix()
	if err := db.addPasskey(ctx, p); err != nil {
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
	token, err := a.startSessionLocked(ctx, user.ID, now)
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
	body := signInMethodMail(h.service.accounts.config.Origin, change, what, time.Now())
	h.sendSecurityNotice(user, "Sign-in method "+change+" on your Metatrash account", body)
}

// noticeBudget is the day's allowance for security notices. It is separate
// from the codes' mail:global:day, so whoever spends that one cannot also
// keep the owner from hearing about a new sign-in method or a recovery code
// used (security review 2026-10-31, F2).
var noticeBudget = allowance{"mail:notice:day", 200, 86400}

// codeMailBudget is the day's allowance for emailed codes (sign-in,
// confirmation and new-address codes), across the whole site.
var codeMailBudget = allowance{"mail:global:day", 100, 86400}

// logMailBudget writes one journal line an hour while the day's code budget
// is used up, so a spent budget shows in the journal instead of only as
// people not getting codes.
func (h *httpAdapter) logMailBudget() {
	if h.service.rates.full(codeMailBudget) && h.service.rates.take(allowance{"log:mail-budget", 1, 3600}) == nil {
		log.Printf("mail budget used up: no more emailed codes are sent until 00:00 UTC")
	}
}

// sendSecurityNotice emails the account a security notice in the background.
func (h *httpAdapter) sendSecurityNotice(user userAccount, subject, body string) {
	a := h.service.accounts
	if a.sendNotice == nil {
		return
	}
	if h.service.rates.take(noticeBudget) != nil {
		log.Printf("security notice not sent user=%s: the day's notice budget is used up", user.ID)
		return
	}
	go func() {
		if err := a.sendNotice(context.Background(), user.Email, subject, body); err != nil {
			// SMTP responses may contain addresses; never log them.
			log.Print("sign-in method notice not sent")
		}
	}()
}
