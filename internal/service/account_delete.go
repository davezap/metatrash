package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Deleting an account (0.71.0). The account asks from Profile, after a
// recent sign-in and typing its email address, once it owns no space
// (deleted ones aside). At once it is signed out everywhere, its connected
// apps and the actions it pays for are disconnected, ownership offers to it
// are withdrawn, and it is emailed. For AccountDeletionGrace, signing in
// again cancels the deletion. Then the daily housekeeping purges it: sign-in
// methods, sessions, memberships (its accepted invitations end), GitHub
// connections, agents and their keys, web references, transfer rows and old
// addresses go, and the row's email and username are replaced so the
// address can sign up afresh. The row stays, marked deleted, because other
// people's records still name it (acknowledgements, form receipts, purged
// spaces). A final email goes to the old address.

// AccountDeletionGrace is how long an account waits, signed out, before it is
// purged.
const AccountDeletionGrace = 30 * 24 * time.Hour

// deletedEmail is the address a purged account keeps: unique, valid, and
// on a domain that can never receive mail.
func deletedEmail(userID string) string { return "deleted-" + userID + "@deleted.invalid" }

// ownedLiveSpaces counts the spaces the account owns that are not deleted.
func (db *accountDatabase) ownedLiveSpaces(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ? AND provisioning_state IN ('provisioning', 'ready')", userID).Scan(&n)
	return n, err
}

// accountDeletion is when the account asked to be deleted, or zero.
func (db *accountDatabase) accountDeletion(ctx context.Context, userID string) (time.Time, error) {
	var at sql.NullInt64
	if err := db.db.QueryRowContext(ctx, "SELECT deletion_requested_at FROM metatrash_users WHERE user_id = ? AND deleted_at IS NULL", userID).Scan(&at); err != nil {
		return time.Time{}, err
	}
	if !at.Valid {
		return time.Time{}, nil
	}
	return time.Unix(at.Int64, 0).UTC(), nil
}

// requestAccountDeletion marks the account for deletion and disconnects what
// acts for it. The caller signs it out.
func (db *accountDatabase) requestAccountDeletion(ctx context.Context, userID string, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	// Lock the account as creating a space or accepting one does, so neither
	// can slip in while it is checked.
	var id string
	if err := tx.QueryRowContext(ctx, "SELECT user_id FROM metatrash_users WHERE user_id = ? AND deleted_at IS NULL FOR UPDATE", userID).Scan(&id); err != nil {
		return fmt.Errorf("cannot read your account")
	}
	owned, err := db.ownedLiveSpaces(ctx, tx, userID)
	if err != nil {
		return fmt.Errorf("cannot check your spaces")
	}
	if owned > 0 {
		what := "a space"
		if owned > 1 {
			what = fmt.Sprintf("%d spaces", owned)
		}
		return problem(409, "conflict", "You still own "+what+". Delete each one, or transfer it to a member, first (on the space's Manage sharing page).")
	}
	for _, step := range []string{
		"UPDATE metatrash_users SET deletion_requested_at = ? WHERE user_id = ?",
		// Connected apps, with their consent and tokens (cascade).
		"DELETE FROM metatrash_oauth_grants WHERE ? > 0 AND user_id = ?",
		// Actions this account pays for stop.
		"DELETE FROM metatrash_action_optins WHERE ? > 0 AND user_id = ?",
		"DELETE FROM metatrash_space_transfers WHERE ? > 0 AND to_user_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, step, now.Unix(), userID); err != nil {
			return fmt.Errorf("cannot delete your account")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot delete your account")
	}
	return nil
}

// cancelAccountDeletion clears a pending deletion; cancelled says there was one.
func (db *accountDatabase) cancelAccountDeletion(ctx context.Context, userID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := db.db.ExecContext(ctx, "UPDATE metatrash_users SET deletion_requested_at = NULL WHERE user_id = ? AND deletion_requested_at IS NOT NULL AND deleted_at IS NULL", userID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// cancelDeletionOnSignIn keeps an account that signs in while waiting to be
// deleted, and tells it so in the background. A failure is logged and
// never stops the sign-in.
func (a *accounts) cancelDeletionOnSignIn(userID string, now time.Time) {
	db := a.sessionDB()
	if db == nil {
		return
	}
	cancelled, err := db.cancelAccountDeletion(context.Background(), userID)
	if err != nil {
		log.Printf("account deletion-cancel failed user=%s", userID)
		return
	}
	if !cancelled || a.sendNotice == nil {
		return
	}
	log.Printf("account deletion cancelled by sign-in user=%s", userID)
	go func() {
		var email string
		if err := db.db.QueryRow("SELECT email FROM metatrash_users WHERE user_id = ?", userID).Scan(&email); err != nil {
			return
		}
		if err := a.sendNotice(context.Background(), email, "Your Metatrash account will not be deleted", accountDeletionCancelledMail(a.config.Origin, now)); err != nil {
			log.Print("deletion-cancelled notice not sent")
		}
	}()
}

// purgeAccount removes what is personal to an account whose grace period
// ended and returns the address it had. ok is false when there is nothing to
// do (cancelled meanwhile, or it owns a space again).
func (db *accountDatabase) purgeAccount(ctx context.Context, userID string, before time.Time, now time.Time) (email string, ok bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", false, fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, "SELECT email FROM metatrash_users WHERE user_id = ? AND deletion_requested_at <= ? AND deleted_at IS NULL FOR UPDATE", userID, before.Unix()).Scan(&email)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("cannot read account")
	}
	if owned, err := db.ownedLiveSpaces(ctx, tx, userID); err != nil || owned > 0 {
		if err == nil {
			log.Printf("deleted accounts: %s owns a space again; not purged", userID)
		}
		return "", false, err
	}
	steps := []string{
		"DELETE FROM metatrash_oauth_grants WHERE user_id = ?",
		"DELETE FROM metatrash_action_optins WHERE user_id = ? OR agent_id IN (SELECT agent_id FROM metatrash_agents WHERE user_id = ?)",
		"DELETE FROM metatrash_agents WHERE user_id = ?",
		"DELETE FROM metatrash_memberships WHERE user_id = ?",
		// Invitations it accepted end; the address stays in the inviter's list.
		"UPDATE metatrash_invitations SET status = 'cancelled', accepted_user_id = NULL WHERE accepted_user_id = ?",
		"DELETE FROM metatrash_space_transfers WHERE from_user_id = ? OR to_user_id = ?",
		"DELETE FROM metatrash_space_aliases WHERE owner_user_id = ?",
		"DELETE FROM metatrash_github_installations WHERE user_id = ?",
		"DELETE FROM metatrash_passkeys WHERE user_id = ?",
		"DELETE FROM metatrash_totp WHERE user_id = ?",
		"DELETE FROM metatrash_recovery_codes WHERE user_id = ?",
		"DELETE FROM metatrash_sessions WHERE user_id = ?",
		"DELETE FROM metatrash_web_refs WHERE user_id = ?",
	}
	for _, step := range steps {
		args := []any{userID}
		if n := countPlaceholders(step); n == 2 {
			args = append(args, userID)
		}
		if _, err := tx.ExecContext(ctx, step, args...); err != nil {
			return "", false, fmt.Errorf("cannot purge account: %s", firstWords(step))
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE metatrash_users SET email = ?, username = NULL, deleted_at = ? WHERE user_id = ?", deletedEmail(userID), now.Unix(), userID); err != nil {
		return "", false, fmt.Errorf("cannot purge account")
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("cannot purge account")
	}
	return email, true, nil
}

func countPlaceholders(query string) int {
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
		}
	}
	return n
}

// firstWords names a statement in an error without its arguments.
func firstWords(query string) string {
	if len(query) > 40 {
		return query[:40]
	}
	return query
}

// endUserSessions signs the account out everywhere.
func (a *accounts) endUserSessions(userID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	var keys []string
	for k, session := range a.sessions {
		if session.UserID == userID {
			keys = append(keys, k)
		}
	}
	return a.endSessionsLocked(keys...)
}

// purgeDeletedAccounts purges every account whose grace period ended, and
// emails each its final notice.
func (s *Service) purgeDeletedAccounts(ctx context.Context, now time.Time) {
	db := s.ownedDB
	if db == nil {
		return
	}
	before := now.Add(-AccountDeletionGrace)
	var ids []string
	err := eachRow(ctx, db.db, func(scan func(...any) error) error {
		var id string
		if err := scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}, "SELECT user_id FROM metatrash_users WHERE deletion_requested_at <= ? AND deleted_at IS NULL ORDER BY deletion_requested_at LIMIT 100", before.Unix())
	if err != nil {
		log.Printf("deleted accounts: %v", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		email, ok, err := db.purgeAccount(ctx, id, before, now)
		if err != nil {
			log.Printf("deleted accounts: purge %s: %v", id, err)
			continue
		}
		if !ok {
			continue
		}
		log.Printf("deleted accounts: purged %s", id)
		if a := s.accounts; a != nil && a.sendNotice != nil {
			if err := a.sendNotice(ctx, email, "Your Metatrash account has been deleted", accountDeletedMail(a.config.Origin)); err != nil {
				log.Printf("deleted accounts: final notice for %s not sent", id)
			}
		}
	}
}

// submitAccountDelete handles POST /account/delete from Profile: after a
// recent sign-in, the account types its email address and is marked for
// deletion, signed out everywhere and emailed.
func (h *httpAdapter) submitAccountDelete(w http.ResponseWriter, r *http.Request, session string) {
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
	db := a.sessionDB()
	err = h.service.rates.take(allowance{"account-delete:user:" + user.ID, 10, 600})
	if err == nil && db == nil {
		err = problem(503, "unavailable", "Deleting an account is not available on this server.")
	}
	if err == nil {
		if fresh, _ := a.sessionFresh(session, time.Now()); !fresh {
			err = errConfirmFirst
		}
	}
	if err == nil && !strings.EqualFold(strings.TrimSpace(r.PostForm.Get("email")), user.Email) {
		err = problem(400, "invalid_request", "That is not your email address. Type it exactly as shown under Email.")
	}
	now := time.Now()
	if err == nil {
		err = db.requestAccountDeletion(r.Context(), user.ID, now)
	}
	if err == nil {
		log.Printf("account deletion requested user=%s", user.ID)
		if endErr := a.endUserSessions(user.ID); endErr != nil {
			log.Printf("account deletion: sessions of %s not all ended in the database", user.ID)
		}
		h.sendSecurityNotice(user, "Your Metatrash account will be deleted", accountDeletionRequestedMail(a.config.Origin, now, now.Add(AccountDeletionGrace)))
		accountCookie(w, h.sessionCookieName(), "", -1)
		accountCookie(w, h.noticeCookieName(), "account-deletion-requested", 60)
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return
	}
	page := accountPage{SignedIn: true, User: user, Section: "profile", CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Message: "Your account could not be deleted. Reload before retrying."}
	status := http.StatusServiceUnavailable
	var p *Error
	if errors.As(err, &p) {
		status, page.Message = p.Status, p.Message
		if p.RetryAfterSeconds > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
		}
	}
	if err == errConfirmFirst {
		page.Message = "Deleting your account needs a sign-in within the last 10 minutes. Confirm it’s you in Security, then delete it again."
	}
	h.renderAccount(w, r, status, page)
}
