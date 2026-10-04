package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

// githubInstallation is one Metatrash GitHub App installation linked to an
// account (schema v6).
type githubInstallation struct {
	InstallationID int64
	UserID         string
	GitHubUserID   int64
	GitHubLogin    string
	AccountLogin   string
	AccountType    string // User or Organization
	Status         string // active or suspended
	CreatedAt      int64
	UpdatedAt      int64
}

const maxGitHubInstallationsPerUser = 20

// GitHub logins: 1-39 letters, digits or single hyphens, not at either end.
var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)

func validGitHubInstallation(inst githubInstallation) bool {
	return inst.InstallationID > 0 && inst.GitHubUserID > 0 && githubLoginPattern.MatchString(inst.GitHubLogin) &&
		githubLoginPattern.MatchString(inst.AccountLogin) && (inst.AccountType == "User" || inst.AccountType == "Organization") &&
		(inst.Status == "active" || inst.Status == "suspended")
}

// checkGitHubSchema is the schema v6 readiness check, run at startup.
func (db *accountDatabase) checkGitHubSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v6 required; follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_github_installations' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return bad
	}
	var columns sql.NullString
	var nonUnique, prefixes int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_github_installations' AND index_name = 'PRIMARY'").Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != "installation_id" || nonUnique != 0 || prefixes != 0 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT installation_id, user_id, github_user_id, github_login, account_login, account_type, status, created_at, updated_at FROM metatrash_github_installations LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	return nil
}

func scanGitHubInstallation(row accountScanner) (githubInstallation, error) {
	var inst githubInstallation
	if err := row.Scan(&inst.InstallationID, &inst.UserID, &inst.GitHubUserID, &inst.GitHubLogin, &inst.AccountLogin, &inst.AccountType, &inst.Status, &inst.CreatedAt, &inst.UpdatedAt); err != nil {
		return inst, err
	}
	if !validGitHubInstallation(inst) || !idPattern.MatchString(inst.UserID) {
		return inst, fmt.Errorf("invalid stored GitHub installation")
	}
	return inst, nil
}

const githubInstallationColumns = "installation_id, user_id, github_user_id, github_login, account_login, account_type, status, created_at, updated_at"

// githubInstallations lists an account's linked installations, oldest first.
func (db *accountDatabase) githubInstallations(ctx context.Context, userID string) ([]githubInstallation, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.db.QueryContext(ctx, "SELECT "+githubInstallationColumns+" FROM metatrash_github_installations WHERE user_id = ? ORDER BY created_at, installation_id LIMIT ?", userID, maxGitHubInstallationsPerUser)
	if err != nil {
		return nil, fmt.Errorf("account database unavailable")
	}
	defer rows.Close()
	list := []githubInstallation{}
	for rows.Next() {
		inst, err := scanGitHubInstallation(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, inst)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("account database unavailable")
	}
	return list, nil
}

// linkGitHubInstallation records a verified installation for an account, or
// refreshes the names of one the account already has. An installation linked
// to another account is refused; that account must disconnect it first.
func (db *accountDatabase) linkGitHubInstallation(ctx context.Context, inst githubInstallation, now time.Time) error {
	if !validGitHubInstallation(inst) || !idPattern.MatchString(inst.UserID) {
		return invalid("GitHub returned an installation Metatrash cannot use.")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	// Serialise per account so the per-account cap holds.
	var userID string
	if err := tx.QueryRowContext(ctx, "SELECT user_id FROM metatrash_users WHERE user_id = ? FOR UPDATE", inst.UserID).Scan(&userID); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	var owner string
	err = tx.QueryRowContext(ctx, "SELECT user_id FROM metatrash_github_installations WHERE installation_id = ? FOR UPDATE", inst.InstallationID).Scan(&owner)
	switch {
	case err == nil && owner != inst.UserID:
		return problem(409, "conflict", "This GitHub installation is already connected to another Metatrash account. Disconnect it there first.")
	case err == nil:
		if _, err := tx.ExecContext(ctx, "UPDATE metatrash_github_installations SET github_user_id = ?, github_login = ?, account_login = ?, account_type = ?, status = ?, updated_at = ? WHERE installation_id = ?",
			inst.GitHubUserID, inst.GitHubLogin, inst.AccountLogin, inst.AccountType, inst.Status, now.Unix(), inst.InstallationID); err != nil {
			return fmt.Errorf("cannot update GitHub installation")
		}
	case err == sql.ErrNoRows:
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_github_installations WHERE user_id = ?", inst.UserID).Scan(&count); err != nil {
			return fmt.Errorf("account database unavailable")
		}
		if count >= maxGitHubInstallationsPerUser {
			return problem(409, "limit_reached", "You have connected the most GitHub accounts allowed. Disconnect one first.")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_github_installations ("+githubInstallationColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			inst.InstallationID, inst.UserID, inst.GitHubUserID, inst.GitHubLogin, inst.AccountLogin, inst.AccountType, inst.Status, now.Unix(), now.Unix()); err != nil {
			return fmt.Errorf("cannot save GitHub installation")
		}
	default:
		return fmt.Errorf("account database unavailable")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot save GitHub installation")
	}
	return nil
}

// unlinkGitHubInstallation removes one of the account's installations. The app
// stays installed on GitHub until the user uninstalls it there.
func (db *accountDatabase) unlinkGitHubInstallation(ctx context.Context, userID string, installationID int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_github_installations WHERE installation_id = ? AND user_id = ?", installationID, userID)
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		return problem(404, "not_found", "That GitHub connection no longer exists. Reload Your account.")
	}
	return nil
}

// forgetGitHubInstallation deletes an installation removed on GitHub (webhook).
func (db *accountDatabase) forgetGitHubInstallation(ctx context.Context, installationID int64) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_github_installations WHERE installation_id = ?", installationID); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}

// setGitHubInstallationStatus records a suspension or its end (webhook).
// Unknown installations are ignored.
func (db *accountDatabase) setGitHubInstallationStatus(ctx context.Context, installationID int64, status string, now time.Time) error {
	if status != "active" && status != "suspended" {
		return fmt.Errorf("invalid installation status")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_github_installations SET status = ?, updated_at = ? WHERE installation_id = ?", status, now.Unix(), installationID); err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}
