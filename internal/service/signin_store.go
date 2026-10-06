package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
)

// Sign-in methods (schema v8): passkeys. The email-code switch, the
// authenticator-app and the recovery-code tables exist from v8 but are not
// used yet.

const maxPasskeysPerUser = 20
const maxPasskeyNameRunes = 64

// passkey is one registered WebAuthn credential.
type passkey struct {
	ID             string
	UserID         string
	CredentialID   []byte
	PublicKey      []byte
	Algorithm      int
	SignCount      uint32
	AAGUID         string
	Transports     string
	BackupEligible bool
	BackedUp       bool
	Name           string
	CreatedAt      int64
	LastUsedAt     int64
}

var transportsPattern = regexp.MustCompile(`^([a-z-]{1,16}(,[a-z-]{1,16}){0,6})?$`)

const passkeyColumns = "passkey_id, user_id, credential_id, public_key, algorithm, sign_count, aaguid, transports, backup_eligible, backed_up, name, created_at, last_used_at"

func (db *accountDatabase) checkSignInSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v8 required; follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('metatrash_passkeys', 'metatrash_totp', 'metatrash_recovery_codes') AND engine = 'InnoDB'").Scan(&count); err != nil || count != 3 {
		return bad
	}
	var columns sql.NullString
	var nonUnique, prefixes int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_passkeys' AND index_name = 'metatrash_passkeys_credential'").Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != "credential_hash" || nonUnique != 0 || prefixes != 0 {
		return bad
	}
	for _, query := range []string{
		"SELECT " + passkeyColumns + ", credential_hash FROM metatrash_passkeys LIMIT 0",
		"SELECT user_id, secret_sealed, created_at, enabled_at, last_used_step FROM metatrash_totp LIMIT 0",
		"SELECT user_id, code_hash, created_at FROM metatrash_recovery_codes LIMIT 0",
		"SELECT email_login FROM metatrash_users LIMIT 0",
	} {
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			return bad
		}
		rows.Close()
	}
	return nil
}

// validPasskeyName trims a user-chosen name; empty names get a default.
func validPasskeyName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Passkey", nil
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > maxPasskeyNameRunes {
		return "", invalid("Use a passkey name of up to 64 characters.")
	}
	for _, c := range name {
		if c < 32 || c == 127 || (c >= 0x80 && c < 0xa0) || c == 0x2028 || c == 0x2029 {
			return "", invalid("The passkey name contains characters that are not allowed.")
		}
	}
	return name, nil
}

func scanPasskey(row accountScanner) (passkey, error) {
	var p passkey
	var credential string
	var count uint64
	var eligible, backed int
	if err := row.Scan(&p.ID, &p.UserID, &credential, &p.PublicKey, &p.Algorithm, &count, &p.AAGUID, &p.Transports, &eligible, &backed, &p.Name, &p.CreatedAt, &p.LastUsedAt); err != nil {
		return p, err
	}
	raw, err := b64.DecodeString(credential)
	if err != nil || len(raw) == 0 || len(raw) > maxCredentialIDBytes || count > 0xffffffff || !idPattern.MatchString(p.ID) || !idPattern.MatchString(p.UserID) || !aaguidPattern.MatchString(p.AAGUID) || !transportsPattern.MatchString(p.Transports) {
		return p, fmt.Errorf("invalid stored passkey")
	}
	if _, err := parseCOSEKey(p.PublicKey, p.Algorithm); err != nil {
		return p, fmt.Errorf("invalid stored passkey")
	}
	p.CredentialID, p.SignCount, p.BackupEligible, p.BackedUp = raw, uint32(count), eligible == 1, backed == 1
	return p, nil
}

// passkeys lists an account's passkeys, oldest first.
func (db *accountDatabase) passkeys(ctx context.Context, userID string) ([]passkey, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.db.QueryContext(ctx, "SELECT "+passkeyColumns+" FROM metatrash_passkeys WHERE user_id = ? ORDER BY created_at, passkey_id LIMIT ?", userID, maxPasskeysPerUser+1)
	if err != nil {
		return nil, fmt.Errorf("account database unavailable")
	}
	defer rows.Close()
	list := []passkey{}
	for rows.Next() {
		p, err := scanPasskey(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("account database unavailable")
	}
	return list, nil
}

// passkeyByCredential finds a passkey by its raw credential ID.
func (db *accountDatabase) passkeyByCredential(ctx context.Context, credentialID []byte) (passkey, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := scanPasskey(db.db.QueryRowContext(ctx, "SELECT "+passkeyColumns+" FROM metatrash_passkeys WHERE credential_hash = ?", secretDigest(string(credentialID))))
	if err == sql.ErrNoRows {
		return passkey{}, false, nil
	}
	if err != nil {
		return passkey{}, false, fmt.Errorf("account database unavailable")
	}
	return p, true, nil
}

// addPasskey stores a newly registered credential. A credential ID that is
// already registered (to anyone) is refused.
func (db *accountDatabase) addPasskey(ctx context.Context, p passkey) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	// Lock the user row so the per-account cap holds under concurrent adds.
	var one int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM metatrash_users WHERE user_id = ? FOR UPDATE", p.UserID).Scan(&one); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_passkeys WHERE user_id = ?", p.UserID).Scan(&count); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if count >= maxPasskeysPerUser {
		return problem(409, "conflict", "You have the maximum of 20 passkeys. Remove one before adding another.")
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO metatrash_passkeys (passkey_id, user_id, credential_hash, credential_id, public_key, algorithm, sign_count, aaguid, transports, backup_eligible, backed_up, name, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)",
		p.ID, p.UserID, secretDigest(string(p.CredentialID)), b64.EncodeToString(p.CredentialID), p.PublicKey, p.Algorithm, p.SignCount, p.AAGUID, p.Transports, boolInt(p.BackupEligible), boolInt(p.BackedUp), p.Name, p.CreatedAt)
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return problem(409, "conflict", "This passkey is already registered.")
	}
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}

// usePasskey records a successful sign-in: the new counter, backup state and
// time. The counter only moves forward (checked again here, so two concurrent
// sign-ins with one counter value cannot both succeed).
func (db *accountDatabase) usePasskey(ctx context.Context, p passkey, signCount uint32, backedUp bool, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := db.db.ExecContext(ctx, "UPDATE metatrash_passkeys SET sign_count = ?, backed_up = ?, last_used_at = ? WHERE passkey_id = ? AND user_id = ? AND (sign_count = 0 OR sign_count < ?)",
		signCount, boolInt(backedUp), now.Unix(), p.ID, p.UserID, signCount)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		// MariaDB reports 0 when nothing changed, which happens for a counter
		// of 0 used twice in one second; anything else is a counter replay.
		if signCount == 0 && p.SignCount == 0 {
			return nil
		}
		return problem(400, "invalid_passkey", "This passkey could not be verified.")
	}
	return nil
}

// renamePasskey changes a passkey's name; it must belong to userID.
func (db *accountDatabase) renamePasskey(ctx context.Context, userID, passkeyID, name string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !idPattern.MatchString(passkeyID) {
		return missing()
	}
	var one int
	if err := db.db.QueryRowContext(ctx, "SELECT 1 FROM metatrash_passkeys WHERE passkey_id = ? AND user_id = ?", passkeyID, userID).Scan(&one); err == sql.ErrNoRows {
		return problem(404, "not_found", "That passkey no longer exists. Reload the page.")
	} else if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_passkeys SET name = ? WHERE passkey_id = ? AND user_id = ?", name, passkeyID, userID); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}

// removePasskey deletes a passkey that belongs to userID and returns its name.
func (db *accountDatabase) removePasskey(ctx context.Context, userID, passkeyID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !idPattern.MatchString(passkeyID) {
		return "", missing()
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	var name string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM metatrash_passkeys WHERE passkey_id = ? AND user_id = ? FOR UPDATE", passkeyID, userID).Scan(&name); err == sql.ErrNoRows {
		return "", problem(404, "not_found", "That passkey no longer exists. Reload the page.")
	} else if err != nil {
		return "", fmt.Errorf("account database unavailable")
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_passkeys WHERE passkey_id = ? AND user_id = ?", passkeyID, userID); err != nil {
		return "", fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("account database unavailable")
	}
	return name, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
