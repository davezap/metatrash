package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Recovery codes and the per-account email-code switch: 0.25.0.
//
// An account gets recoveryCodeCount single-use codes at a time, shown once
// and stored as salted SHA-256 digests; creating new ones replaces the old.
// A code signs in like an authenticator code (email address plus code).
//
// Email sign-in (metatrash_users.email_login) can be turned off only while
// the account has a passkey or an authenticator app and unused recovery
// codes. While it is off, emailed codes neither sign in nor confirm changes,
// and the last passkey or authenticator app cannot be removed.

const recoveryCodeCount = 10

// recoveryAlphabet leaves out 0, 1, i, l and o, which are easily confused.
const recoveryAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

// recoveryCodeLength characters give about 59 bits per code.
const recoveryCodeLength = 12

// recoveryShowFor is how long new codes wait to be shown once.
const recoveryShowFor = 10 * time.Minute

var recoveryAttempts = func(key string) allowance { return allowance{"recovery:" + key, 5, 900} }

// newRecoveryCode returns a code formatted for display, like abcd-efgh-jkmn.
func newRecoveryCode() (string, error) {
	var b strings.Builder
	limit := big.NewInt(int64(len(recoveryAlphabet)))
	for i := range recoveryCodeLength {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		b.WriteByte(recoveryAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// normalRecoveryCode accepts a code as typed: any case, with or without
// hyphens and spaces. ok is false for anything that cannot be a code.
func normalRecoveryCode(code string) (string, bool) {
	code = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(code)))
	if len(code) != recoveryCodeLength || strings.Trim(code, recoveryAlphabet) != "" {
		return "", false
	}
	return code, true
}

// recoveryDigest is what the database keeps. The user ID salts it, so one
// digest table cannot be searched for every account at once.
func recoveryDigest(userID, normal string) string {
	return secretDigest("metatrash-recovery:" + userID + ":" + normal)
}

// signInState is what decides which sign-in changes an account may make.
type signInState struct {
	EmailLogin      bool
	Passkeys        int
	TOTP            bool // an authenticator app that is set up
	Recovery        int  // unused recovery codes
	RecoveryCreated int64
}

// Strong reports whether the account has a sign-in method other than email
// and recovery codes.
func (s signInState) Strong() bool { return s.Passkeys > 0 || s.TOTP }

type accountQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSignInState(ctx context.Context, q accountQuerier, userID string, lock bool) (signInState, error) {
	var s signInState
	var email int
	query := "SELECT email_login FROM metatrash_users WHERE user_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	if err := q.QueryRowContext(ctx, query, userID).Scan(&email); err != nil {
		return s, fmt.Errorf("account database unavailable")
	}
	var totp int
	err := q.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM metatrash_passkeys WHERE user_id = ?),
		(SELECT COUNT(*) FROM metatrash_totp WHERE user_id = ? AND enabled_at > 0),
		(SELECT COUNT(*) FROM metatrash_recovery_codes WHERE user_id = ?),
		(SELECT COALESCE(MAX(created_at), 0) FROM metatrash_recovery_codes WHERE user_id = ?)`,
		userID, userID, userID, userID).Scan(&s.Passkeys, &totp, &s.Recovery, &s.RecoveryCreated)
	if err != nil {
		return s, fmt.Errorf("account database unavailable")
	}
	s.EmailLogin, s.TOTP = email == 1, totp > 0
	return s, nil
}

func (db *accountDatabase) signInState(ctx context.Context, userID string) (signInState, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return readSignInState(ctx, db.db, userID, false)
}

// emailLoginAllowed reports whether an emailed code may sign in to the
// account with this address. Addresses without an account may (signing in
// creates one).
func (db *accountDatabase) emailLoginAllowed(ctx context.Context, email string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var on int
	err := db.db.QueryRowContext(ctx, "SELECT email_login FROM metatrash_users WHERE email = ?", email).Scan(&on)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	return on == 1, nil
}

var errKeepSignIn = problem(409, "conflict", "Email sign-in is off, so this is your last way to sign in besides recovery codes. Turn email sign-in back on first, or add another passkey or authenticator app.")

// keepSignInLocked is checked inside a transaction that holds the user row
// and has just removed a passkey or authenticator app.
func keepSignInLocked(ctx context.Context, tx *sql.Tx, userID string) error {
	s, err := readSignInState(ctx, tx, userID, false)
	if err != nil {
		return err
	}
	if !s.EmailLogin && !s.Strong() {
		return errKeepSignIn
	}
	return nil
}

// setEmailLogin turns sign-in by emailed code on or off. Turning it off needs
// a passkey or authenticator app and unused recovery codes. changed is false
// when it was already so.
func (db *accountDatabase) setEmailLogin(ctx context.Context, userID string, on bool) (changed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	s, err := readSignInState(ctx, tx, userID, true)
	if err != nil {
		return false, err
	}
	if s.EmailLogin == on {
		return false, nil
	}
	if !on && (!s.Strong() || s.Recovery == 0) {
		return false, problem(409, "conflict", "To turn off email sign-in, first add a passkey or an authenticator app, and create recovery codes.")
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metatrash_users SET email_login = ? WHERE user_id = ?", boolInt(on), userID); err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	return true, nil
}

// replaceRecoveryCodes stores new code digests in place of any old ones.
func (db *accountDatabase) replaceRecoveryCodes(ctx context.Context, userID string, digests []string, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	var one int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM metatrash_users WHERE user_id = ? FOR UPDATE", userID).Scan(&one); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_recovery_codes WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	for _, digest := range digests {
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)", userID, digest, now.Unix()); err != nil {
			return fmt.Errorf("account database unavailable")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}

// useRecoveryCode spends a code. Deleting the row is the check, so one code
// cannot be spent twice. It returns how many codes are left.
func (db *accountDatabase) useRecoveryCode(ctx context.Context, userID, digest string) (bool, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_recovery_codes WHERE user_id = ? AND code_hash = ?", userID, digest)
	if err != nil {
		return false, 0, fmt.Errorf("account database unavailable")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return false, 0, nil
	}
	var left int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_recovery_codes WHERE user_id = ?", userID).Scan(&left); err != nil {
		return true, 0, nil
	}
	return true, left, nil
}

// recoveryRevealKey is where a session's new codes wait to be shown once.
func recoveryRevealKey(session string) string {
	return secretDigest("recovery:" + session)
}

type recoveryReveal struct {
	UserID  string
	Codes   []string
	Expires time.Time
}

// takeRecoveryReveal returns the new codes waiting for this session and
// forgets them.
func (a *accounts) takeRecoveryReveal(session, userID string, now time.Time) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := recoveryRevealKey(session)
	r, ok := a.recoveryReveals[key]
	delete(a.recoveryReveals, key)
	if !ok || r.UserID != userID || !now.Before(r.Expires) {
		return nil
	}
	return r.Codes
}

// createRecoveryCodes makes a new set of codes for the account and keeps them
// for the session to show once.
func (h *httpAdapter) createRecoveryCodes(ctx context.Context, session string, user userAccount, now time.Time) error {
	a := h.service.accounts
	if fresh, _ := a.sessionFresh(session, now); !fresh {
		return errConfirmFirst
	}
	codes := make([]string, recoveryCodeCount)
	digests := make([]string, recoveryCodeCount)
	for i := range codes {
		code, err := newRecoveryCode()
		if err != nil {
			return err
		}
		normal, _ := normalRecoveryCode(code)
		codes[i], digests[i] = code, recoveryDigest(user.ID, normal)
	}
	a.mu.Lock()
	a.cleanup(now)
	busy := len(a.recoveryReveals) >= 1024
	a.mu.Unlock()
	if busy {
		return problem(503, "busy", "Please try again shortly.")
	}
	if err := h.service.ownedDB.replaceRecoveryCodes(ctx, user.ID, digests, now); err != nil {
		return err
	}
	a.mu.Lock()
	if a.recoveryReveals == nil {
		a.recoveryReveals = map[string]recoveryReveal{}
	}
	a.recoveryReveals[recoveryRevealKey(session)] = recoveryReveal{UserID: user.ID, Codes: codes, Expires: now.Add(recoveryShowFor)}
	a.mu.Unlock()
	h.notifySignInChange(user, "added", "a new set of "+strconv.Itoa(recoveryCodeCount)+" recovery codes (any older codes no longer work)")
	return nil
}

// switchEmailLogin handles the email sign-in switch on Security.
func (h *httpAdapter) switchEmailLogin(ctx context.Context, session string, user userAccount, value string, now time.Time) (string, error) {
	a := h.service.accounts
	if fresh, _ := a.sessionFresh(session, now); !fresh {
		return "", errConfirmFirst
	}
	if value != "on" && value != "off" {
		return "", invalid("Choose on or off.")
	}
	on := value == "on"
	changed, err := h.service.ownedDB.setEmailLogin(ctx, user.ID, on)
	if err != nil || !changed {
		return "", err
	}
	if on {
		h.notifySignInChange(user, "added", "sign-in with a code emailed to this address")
		return "email-login-on", nil
	}
	h.notifySignInChange(user, "removed", "sign-in with a code emailed to this address")
	return "email-login-off", nil
}

// recoverySignIn signs in with an email address and a recovery code (POST
// /login/recovery). Like the authenticator app, every failure says the same
// thing and every attempt counts against the address.
func (h *httpAdapter) recoverySignIn(ctx context.Context, rawEmail, code string, entry *loginLog) (string, userAccount, error) {
	a, db := h.service.accounts, h.service.ownedDB
	failed := problem(400, "invalid_code", "That recovery code did not work for that email address. Check both; each code works only once.")
	if db == nil {
		return "", userAccount{}, problem(503, "unavailable", "Recovery codes are temporarily unavailable.")
	}
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return "", userAccount{}, invalid("Enter a valid email address.")
	}
	a.logEmail(ctx, entry, email)
	if err := h.service.rates.take(recoveryAttempts("email:" + a.mac("email:"+email))); err != nil {
		return "", userAccount{}, err
	}
	normal, valid := normalRecoveryCode(code)
	userID, ok, err := db.userIDByEmail(ctx, email)
	var user userAccount
	if err == nil && ok {
		user, ok, err = a.store.ByID(ctx, userID)
	}
	if err != nil {
		return "", userAccount{}, problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	}
	if !ok || !valid {
		return "", userAccount{}, failed
	}
	used, left, err := db.useRecoveryCode(ctx, user.ID, recoveryDigest(user.ID, normal))
	if err != nil {
		return "", user, problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	}
	if !used {
		return "", user, failed
	}
	now := time.Now()
	a.mu.Lock()
	token, err := a.startSessionLocked(ctx, user.ID, now)
	if err == nil {
		// Someone using a recovery code has usually lost a device: sign out
		// the sessions that device (or whoever has it) may still hold.
		a.endOtherSessionsLocked(user.ID, token)
		for k, pending := range a.challenges {
			if pending.Email == user.Email {
				delete(a.challenges, k)
			}
		}
	}
	a.mu.Unlock()
	if err != nil {
		return "", user, err
	}
	h.sendSecurityNotice(user, "Recovery code used on your Metatrash account", recoveryUsedMail(a.config.Origin, left, now))
	return token, user, nil
}
