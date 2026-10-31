package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// identity is an outside account (Microsoft so far) linked to a Metatrash
// account as a way to sign in (schema v22, metatrash_identities).
type identity struct {
	ID, UserID, Provider  string
	Issuer, Subject       string
	Email, Name           string
	CreatedAt, LastUsedAt int64
}

// Label names the linked account for people.
func (id identity) Label() string {
	return oidcIdentity{Email: id.Email, Name: id.Name}.Label()
}

const identityColumns = "identity_id, user_id, provider, issuer, subject, email_hint, display_name, created_at, last_used_at"

func scanIdentity(row accountScanner) (identity, error) {
	var id identity
	err := row.Scan(&id.ID, &id.UserID, &id.Provider, &id.Issuer, &id.Subject, &id.Email, &id.Name, &id.CreatedAt, &id.LastUsedAt)
	return id, err
}

func (db *accountDatabase) checkIdentitySchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v22 required (sign in with Microsoft); follow docs/deployment.md")
	var columns sql.NullString
	var nonUnique int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_identities' AND index_name = 'metatrash_identities_subject'").Scan(&columns, &nonUnique); err != nil || columns.String != "provider,issuer,subject" || nonUnique != 0 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT "+identityColumns+" FROM metatrash_identities LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	return nil
}

var errIdentityStore = problem(503, "account_unavailable", "Your account is temporarily unavailable. Please try again later.")

func (db *accountDatabase) identityBySubject(ctx context.Context, provider, issuer, subject string) (identity, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id, err := scanIdentity(db.db.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM metatrash_identities WHERE provider = ? AND issuer = ? AND subject = ?", provider, issuer, subject))
	if err == sql.ErrNoRows {
		return identity{}, false, nil
	}
	if err != nil {
		return identity{}, false, errIdentityStore
	}
	return id, true, nil
}

func (db *accountDatabase) identities(ctx context.Context, userID string) ([]identity, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.db.QueryContext(ctx, "SELECT "+identityColumns+" FROM metatrash_identities WHERE user_id = ? ORDER BY created_at, identity_id", userID)
	if err != nil {
		return nil, errIdentityStore
	}
	defer rows.Close()
	var list []identity
	for rows.Next() {
		id, err := scanIdentity(rows)
		if err != nil {
			return nil, errIdentityStore
		}
		list = append(list, id)
	}
	if rows.Err() != nil {
		return nil, errIdentityStore
	}
	return list, nil
}

// addIdentity links an outside account to userID. The unique key on
// provider, issuer and subject is the final check that no other account has
// it.
func (db *accountDatabase) addIdentity(ctx context.Context, userID, provider string, in oidcIdentity, now time.Time) error {
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return errIdentityStore
	}
	defer tx.Rollback()
	// Lock the user row so two links at once cannot pass the limit.
	var one int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM metatrash_users WHERE user_id = ? AND deleted_at IS NULL FOR UPDATE", userID).Scan(&one); err != nil {
		return errIdentityStore
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_identities WHERE user_id = ?", userID).Scan(&count); err != nil {
		return errIdentityStore
	}
	if count >= maxIdentitiesPerUser {
		return problem(409, "conflict", fmt.Sprintf("You can link at most %d Microsoft accounts. Remove one first.", maxIdentitiesPerUser))
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO metatrash_identities ("+identityColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)",
		id, userID, provider, in.Tenant, in.Object, in.Email, in.Name, now.Unix())
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return problem(409, "microsoft_taken", "That Microsoft account is already linked to another Metatrash account. Remove it there first.")
	}
	if err != nil || tx.Commit() != nil {
		return errIdentityStore
	}
	return nil
}

// useIdentity records a sign-in and refreshes the labels.
func (db *accountDatabase) useIdentity(ctx context.Context, identityID string, in oidcIdentity, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := db.db.ExecContext(ctx, "UPDATE metatrash_identities SET email_hint = ?, display_name = ?, last_used_at = ? WHERE identity_id = ?", in.Email, in.Name, now.Unix(), identityID)
	return err
}

// removeIdentity unlinks one of the user's outside accounts and returns its
// label.
func (db *accountDatabase) removeIdentity(ctx context.Context, userID, identityID string) (string, error) {
	if !isLowerHex(identityID) || len(identityID) != 32 {
		return "", problem(404, "not_found", "That Microsoft account is not linked any more. Reload the page.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	id, err := scanIdentity(db.db.QueryRowContext(ctx, "SELECT "+identityColumns+" FROM metatrash_identities WHERE identity_id = ? AND user_id = ?", identityID, userID))
	if err == sql.ErrNoRows {
		return "", problem(404, "not_found", "That Microsoft account is not linked any more. Reload the page.")
	}
	if err != nil {
		return "", errIdentityStore
	}
	if _, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_identities WHERE identity_id = ? AND user_id = ?", identityID, userID); err != nil {
		return "", errIdentityStore
	}
	return id.Label(), nil
}
