package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"strings"
	"time"
)

// Authenticator app (TOTP, RFC 6238): 0.24.0. SHA-1, six digits, 30-second
// steps, the current step and one either side accepted, each step at most
// once. The secret is 20 random bytes, stored sealed with AES-256-GCM under
// a server key read from totpKeyFile in accounts.json. Without that setting
// the authenticator app is not offered.

const totpPeriod = 30
const totpSecretBytes = 20

// totpAttempts limits wrong and right codes alike per account (and per
// address at sign-in, whether or not it has an account), so the attempt
// budget says nothing about which methods an address has.
var totpAttempts = func(key string) allowance { return allowance{"totp:" + key, 5, 900} }

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// loadTOTPKey reads a key file holding 64 hex digits (32 bytes).
func loadTOTPKey(path string) (cipher.AEAD, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read TOTP key file: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("TOTP key file must hold 64 hex digits (openssl rand -hex 32)")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealTOTP encrypts a secret for one account: version byte 1, nonce, then
// the ciphertext. The user ID is authenticated, so a sealed secret copied to
// another account does not open.
func sealTOTP(aead cipher.AEAD, userID string, secret []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{1}, nonce...)
	return aead.Seal(out, nonce, secret, []byte("metatrash-totp:"+userID)), nil
}

func openTOTP(aead cipher.AEAD, userID string, sealed []byte) ([]byte, error) {
	n := aead.NonceSize()
	if len(sealed) < 1+n+aead.Overhead() || sealed[0] != 1 {
		return nil, fmt.Errorf("invalid sealed TOTP secret")
	}
	secret, err := aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte("metatrash-totp:"+userID))
	if err != nil || len(secret) != totpSecretBytes {
		return nil, fmt.Errorf("invalid sealed TOTP secret")
	}
	return secret, nil
}

// totpCode is the six-digit code for one time step (RFC 4226 HOTP).
func totpCode(secret []byte, step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000)
}

// totpMatch finds the step a code belongs to among the current step and one
// either side, ignoring steps at or before after (already used). It checks
// every candidate, so the time taken does not depend on which one matched.
func totpMatch(secret []byte, code string, now time.Time, after int64) (int64, bool) {
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	found := int64(0)
	for step := current - 1; step <= current+1; step++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 && step > after && found == 0 {
			found = step
		}
	}
	return found, found != 0
}

// totpURI is what the QR code holds. The label shows the account's email in
// the app, so the user can tell accounts apart.
func totpURI(email string, secret []byte) string {
	label := url.PathEscape("Metatrash:" + email)
	return "otpauth://totp/" + label + "?secret=" + totpEncoding.EncodeToString(secret) + "&issuer=Metatrash"
}

// groupedSecret shows the base32 secret in groups of four for typing.
func groupedSecret(secret []byte) string {
	s := totpEncoding.EncodeToString(secret)
	var groups []string
	for i := 0; i < len(s); i += 4 {
		groups = append(groups, s[i:min(i+4, len(s))])
	}
	return strings.Join(groups, " ")
}

// totpRecord is a row of metatrash_totp. EnabledAt is 0 while setup waits for
// the first code.
type totpRecord struct {
	UserID       string
	Sealed       []byte
	CreatedAt    int64
	EnabledAt    int64
	LastUsedStep int64
}

func (db *accountDatabase) totp(ctx context.Context, userID string) (totpRecord, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var r totpRecord
	err := db.db.QueryRowContext(ctx, "SELECT user_id, secret_sealed, created_at, enabled_at, last_used_step FROM metatrash_totp WHERE user_id = ?", userID).Scan(&r.UserID, &r.Sealed, &r.CreatedAt, &r.EnabledAt, &r.LastUsedStep)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("account database unavailable")
	}
	return r, true, nil
}

func (db *accountDatabase) userIDByEmail(ctx context.Context, email string) (string, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id string
	err := db.db.QueryRowContext(ctx, "SELECT user_id FROM metatrash_users WHERE email = ?", email).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("account database unavailable")
	}
	return id, true, nil
}

// startTOTP stores a new secret waiting for its first code, replacing a
// setup that was never finished. An enabled authenticator app is kept: it
// must be removed first.
func (db *accountDatabase) startTOTP(ctx context.Context, userID string, sealed []byte, now time.Time) error {
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
	var enabled int64
	err = tx.QueryRowContext(ctx, "SELECT enabled_at FROM metatrash_totp WHERE user_id = ?", userID).Scan(&enabled)
	if err == nil && enabled > 0 {
		return problem(409, "conflict", "You already have an authenticator app. Remove it before setting up another.")
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("account database unavailable")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_totp WHERE user_id = ? AND enabled_at = 0", userID); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_totp (user_id, secret_sealed, created_at, enabled_at, last_used_step) VALUES (?, ?, ?, 0, 0)", userID, sealed, now.Unix()); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}

// enableTOTP finishes setup with the step of the first code.
func (db *accountDatabase) enableTOTP(ctx context.Context, userID string, step int64, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := db.db.ExecContext(ctx, "UPDATE metatrash_totp SET enabled_at = ?, last_used_step = ? WHERE user_id = ? AND enabled_at = 0", now.Unix(), step, userID)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return problem(409, "conflict", "Setup changed in another window. Reload the page.")
	}
	return nil
}

// useTOTP records the step of a code just accepted. It fails when that step
// (or a later one) was already used, so two requests with one code cannot
// both succeed.
func (db *accountDatabase) useTOTP(ctx context.Context, userID string, step int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := db.db.ExecContext(ctx, "UPDATE metatrash_totp SET last_used_step = ? WHERE user_id = ? AND enabled_at > 0 AND last_used_step < ?", step, userID, step)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return errTOTPCode
	}
	return nil
}

// removeTOTP deletes the account's authenticator app, set up or not, and
// reports whether it was set up.
func (db *accountDatabase) removeTOTP(ctx context.Context, userID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	var enabled int64
	if err := tx.QueryRowContext(ctx, "SELECT enabled_at FROM metatrash_totp WHERE user_id = ? FOR UPDATE", userID).Scan(&enabled); err == sql.ErrNoRows {
		return false, problem(404, "not_found", "There is no authenticator app on this account. Reload the page.")
	} else if err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_totp WHERE user_id = ?", userID); err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("account database unavailable")
	}
	return enabled > 0, nil
}

var errTOTPCode = problem(400, "invalid_code", "That code is not right. Check the time on your phone and enter the code your authenticator app shows now.")

// checkTOTP verifies a code against the account's enabled authenticator app
// and spends it. found is false when the account has none (the caller decides
// what to say).
func (h *httpAdapter) checkTOTP(ctx context.Context, userID, code string, now time.Time) (found bool, err error) {
	a, db := h.service.accounts, h.service.ownedDB
	r, ok, err := db.totp(ctx, userID)
	if err != nil {
		return false, err
	}
	if !ok || r.EnabledAt == 0 || a.totpKey == nil {
		return false, nil
	}
	secret, err := openTOTP(a.totpKey, userID, r.Sealed)
	if err != nil {
		return true, fmt.Errorf("account database unavailable")
	}
	step, match := totpMatch(secret, code, now, r.LastUsedStep)
	if !match {
		return true, errTOTPCode
	}
	return true, db.useTOTP(ctx, userID, step)
}

// totpSignIn signs in with an email address and an authenticator code (POST
// /login/totp). Every failure says the same thing, whether the address has
// no account, no authenticator app, or the code is wrong, and every attempt
// counts against the address's budget.
func (h *httpAdapter) totpSignIn(ctx context.Context, rawEmail, code string, entry *loginLog) (string, userAccount, error) {
	a, db := h.service.accounts, h.service.ownedDB
	failed := problem(400, "invalid_code", "That code did not work for that email address. Check both, or email yourself a code instead.")
	if db == nil || a.totpKey == nil {
		return "", userAccount{}, problem(503, "unavailable", "Authenticator app sign-in is not available. Use an emailed code.")
	}
	email, err := normalizeEmail(rawEmail)
	if err != nil {
		return "", userAccount{}, invalid("Enter a valid email address.")
	}
	a.logEmail(ctx, entry, email)
	if err := h.service.rates.take(totpAttempts("email:" + a.mac("email:"+email))); err != nil {
		return "", userAccount{}, err
	}
	userID, ok, err := db.userIDByEmail(ctx, email)
	var user userAccount
	if err == nil && ok {
		user, ok, err = a.store.ByID(ctx, userID)
	}
	if err != nil {
		return "", userAccount{}, problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	}
	if !ok {
		return "", userAccount{}, failed
	}
	now := time.Now()
	found, err := h.checkTOTP(ctx, user.ID, strings.TrimSpace(code), now)
	if !found || err == errTOTPCode {
		return "", user, failed
	}
	if err != nil {
		return "", user, problem(503, "account_unavailable", "Sign-in is temporarily unavailable. Please try again later.")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	token, err := a.startSessionLocked(user.ID, now)
	if err != nil {
		return "", user, err
	}
	for k, pending := range a.challenges {
		if pending.Email == user.Email {
			delete(a.challenges, k)
		}
	}
	return token, user, nil
}

// totpView is the Authenticator app card on Security.
type totpView struct {
	Enabled         bool
	Added, LastUsed string
	Pending         bool
	QR              template.HTML // SVG generated by encodeQR
	Secret          string
}

func (h *httpAdapter) totpSection(ctx context.Context, user userAccount) (*totpView, error) {
	a, db := h.service.accounts, h.service.ownedDB
	r, ok, err := db.totp(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	view := &totpView{}
	if !ok {
		return view, nil
	}
	if r.EnabledAt > 0 {
		view.Enabled = true
		view.Added = time.Unix(r.EnabledAt, 0).UTC().Format("2 Jan 2006")
		view.LastUsed = "never"
		// Enabling spends the first code's step; later steps are sign-ins
		// or confirmations.
		if r.LastUsedStep*totpPeriod > r.EnabledAt+totpPeriod {
			view.LastUsed = time.Unix(r.LastUsedStep*totpPeriod, 0).UTC().Format("2 Jan 2006")
		}
		return view, nil
	}
	secret, err := openTOTP(a.totpKey, user.ID, r.Sealed)
	if err != nil {
		return nil, err
	}
	q, err := encodeQR([]byte(totpURI(user.Email, secret)))
	if err != nil {
		return nil, err
	}
	view.Pending, view.QR, view.Secret = true, template.HTML(q.svg()), groupedSecret(secret)
	return view, nil
}

// submitTOTP handles the authenticator-app forms on Security. It returns the
// notice to show.
func (h *httpAdapter) submitTOTP(ctx context.Context, path, session string, user userAccount, code string, now time.Time) (string, error) {
	a, db := h.service.accounts, h.service.ownedDB
	if a.totpKey == nil {
		return "", problem(404, "not_found", "Authenticator apps are not available on this server.")
	}
	fresh, _ := a.sessionFresh(session, now)
	switch path {
	case "/account/totp/start":
		if !fresh {
			return "", errConfirmFirst
		}
		secret := make([]byte, totpSecretBytes)
		if _, err := rand.Read(secret); err != nil {
			return "", err
		}
		sealed, err := sealTOTP(a.totpKey, user.ID, secret)
		if err != nil {
			return "", err
		}
		return "", db.startTOTP(ctx, user.ID, sealed, now)
	case "/account/totp/enable":
		if !fresh {
			return "", errConfirmFirst
		}
		if err := h.service.rates.take(totpAttempts("user:" + user.ID)); err != nil {
			return "", err
		}
		r, ok, err := db.totp(ctx, user.ID)
		if err != nil {
			return "", err
		}
		if !ok || r.EnabledAt > 0 {
			return "", problem(409, "conflict", "Setup changed in another window. Reload the page.")
		}
		secret, err := openTOTP(a.totpKey, user.ID, r.Sealed)
		if err != nil {
			return "", fmt.Errorf("account database unavailable")
		}
		step, match := totpMatch(secret, strings.TrimSpace(code), now, 0)
		if !match {
			return "", errTOTPCode
		}
		if err := db.enableTOTP(ctx, user.ID, step, now); err != nil {
			return "", err
		}
		h.notifySignInChange(user, "added", "authenticator app")
		return "totp-added", nil
	case "/account/totp/remove":
		r, ok, err := db.totp(ctx, user.ID)
		if err != nil {
			return "", err
		}
		// Cancelling an unfinished setup needs no confirmation.
		if ok && r.EnabledAt > 0 && !fresh {
			return "", errConfirmFirst
		}
		enabled, err := db.removeTOTP(ctx, user.ID)
		if err != nil {
			return "", err
		}
		if !enabled {
			return "totp-cancelled", nil
		}
		h.notifySignInChange(user, "removed", "authenticator app")
		return "totp-removed", nil
	case "/account/confirm/totp":
		if err := h.service.rates.take(totpAttempts("user:" + user.ID)); err != nil {
			return "", err
		}
		found, err := h.checkTOTP(ctx, user.ID, strings.TrimSpace(code), now)
		if !found && err == nil {
			err = problem(400, "invalid_code", "There is no authenticator app on this account. Confirm another way.")
		}
		if err != nil {
			return "", err
		}
		a.markConfirmed(session, user.ID, now)
		return "signin-confirmed", nil
	}
	return "", missing()
}
