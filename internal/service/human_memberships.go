package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Human operations only. The HTTP handler supplies actorID from the
// verified session, with Origin/CSRF checks and rate limits before calling here.
// Every mutation locks the space first, including acceptance and revocation.
// Browser access checks current active membership; agent access remains separate.
func (db *accountDatabase) membershipTransaction(ctx context.Context, spaceID, actorID string, ownerOnly bool, work func(context.Context, *sql.Tx, ownedSpace) error) error {
	if !idPattern.MatchString(spaceID) || !idPattern.MatchString(actorID) {
		return missing()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("membership database unavailable")
	}
	defer tx.Rollback()
	space, err := scanOwnedSpace(tx.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE space_id = ? FOR UPDATE", spaceID))
	if err == sql.ErrNoRows {
		return missing()
	}
	if err != nil {
		return fmt.Errorf("cannot read membership space")
	}
	if space.State != "ready" || (ownerOnly && space.OwnerID != actorID) {
		return missing()
	}
	if err := work(ctx, tx, space); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot confirm membership change; reload before retrying")
	}
	return nil
}

// A duplicate live invitation returns the same ID without extending its expiry.
// Expired, cancelled and accepted slots can be reused only for non-members.
func (db *accountDatabase) inviteHuman(ctx context.Context, ownerID, spaceID, email string) (string, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return "", invalid("Enter a valid email address.")
	}
	id, err := randomHex(16)
	if err != nil {
		return "", err
	}
	err = db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		var targetID string
		err := tx.QueryRowContext(ctx, "SELECT user_id FROM metatrash_users WHERE email = ?", email).Scan(&targetID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("cannot check invitation recipient")
		}
		if targetID == space.OwnerID {
			return invalid("The owner already belongs to this space.")
		}
		if targetID != "" {
			var status string
			err := tx.QueryRowContext(ctx, "SELECT status FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", spaceID, targetID).Scan(&status)
			if err == nil {
				return problem(409, "conflict", "This person already has a membership; restore suspended access explicitly.")
			}
			if err != sql.ErrNoRows {
				return fmt.Errorf("cannot check existing membership")
			}
		}
		var previousID, status string
		var expires int64
		err = tx.QueryRowContext(ctx, "SELECT invitation_id, status, expires_at FROM metatrash_invitations WHERE space_id = ? AND email = ?", spaceID, email).Scan(&previousID, &status, &expires)
		now := time.Now().UTC().Unix()
		if err == nil && status == "pending" && expires > now {
			id = previousID
			return nil
		}
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("cannot read invitation")
		}
		if err == sql.ErrNoRows {
			_, err = tx.ExecContext(ctx, "INSERT INTO metatrash_invitations (invitation_id, space_id, email, status, created_at, expires_at) VALUES (?, ?, ?, 'pending', ?, ?)", id, spaceID, email, now, now+7*24*60*60)
		} else {
			_, err = tx.ExecContext(ctx, "UPDATE metatrash_invitations SET invitation_id = ?, status = 'pending', created_at = ?, expires_at = ?, accepted_user_id = NULL WHERE space_id = ? AND email = ?", id, now, now+7*24*60*60, spaceID, email)
		}
		if err != nil {
			return fmt.Errorf("cannot save invitation")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (db *accountDatabase) cancelHumanInvitation(ctx context.Context, ownerID, spaceID, invitationID string) error {
	if !idPattern.MatchString(invitationID) {
		return missing()
	}
	return db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, _ ownedSpace) error {
		// Conditional update makes cancellation retryable and prevents a stale ID
		// from cancelling a replacement invitation or undoing acceptance.
		_, err := tx.ExecContext(ctx, "UPDATE metatrash_invitations SET status = 'cancelled' WHERE space_id = ? AND invitation_id = ? AND status = 'pending'", spaceID, invitationID)
		if err != nil {
			return fmt.Errorf("cannot cancel invitation")
		}
		return nil
	})
}

func (db *accountDatabase) acceptHumanInvitation(ctx context.Context, userID, spaceID, invitationID string) error {
	if !idPattern.MatchString(invitationID) {
		return missing()
	}
	return db.membershipTransaction(ctx, spaceID, userID, false, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		// Read verified email from the authoritative account, never submitted text.
		var email string
		if err := tx.QueryRowContext(ctx, "SELECT email FROM metatrash_users WHERE user_id = ?", userID).Scan(&email); err != nil {
			return fmt.Errorf("cannot read invitation account")
		}
		var status string
		var expires int64
		var acceptedID sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT status, expires_at, accepted_user_id FROM metatrash_invitations WHERE space_id = ? AND invitation_id = ? AND email = ?", spaceID, invitationID, email).Scan(&status, &expires, &acceptedID)
		if err == sql.ErrNoRows || userID == space.OwnerID {
			return missing()
		}
		if err != nil {
			return fmt.Errorf("cannot read invitation")
		}
		var membershipStatus string
		err = tx.QueryRowContext(ctx, "SELECT status FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", spaceID, userID).Scan(&membershipStatus)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("cannot read membership")
		}
		if status == "accepted" && acceptedID.Valid && acceptedID.String == userID && membershipStatus == "active" {
			return nil // Response-lost retry; never recreate or restore membership.
		}
		now := time.Now().UTC().Unix()
		if status != "pending" || expires <= now || membershipStatus != "" {
			return problem(409, "conflict", "Invitation unavailable or membership already exists. Ask the owner to review access.")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_memberships (space_id, user_id, role, status, joined_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)", spaceID, userID, now, now); err != nil {
			return fmt.Errorf("cannot create membership")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE metatrash_invitations SET status = 'accepted', accepted_user_id = ? WHERE space_id = ? AND invitation_id = ?", userID, spaceID, invitationID); err != nil {
			return fmt.Errorf("cannot accept invitation")
		}
		return nil
	})
}

func (db *accountDatabase) manageHumanMember(ctx context.Context, ownerID, spaceID, userID, action string) error {
	if !idPattern.MatchString(userID) || (action != "suspend" && action != "restore" && action != "remove") {
		return invalid("Invalid membership action.")
	}
	return db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		if userID == space.OwnerID {
			return invalid("The owner cannot be changed through member management.")
		}
		var err error
		if action == "remove" {
			_, err = tx.ExecContext(ctx, "DELETE FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", spaceID, userID)
		} else {
			from, to := "active", "suspended"
			if action == "restore" {
				from, to = "suspended", "active"
			}
			_, err = tx.ExecContext(ctx, "UPDATE metatrash_memberships SET status = ?, updated_at = ? WHERE space_id = ? AND user_id = ? AND status = ?", to, time.Now().UTC().Unix(), spaceID, userID, from)
		}
		if err != nil {
			return fmt.Errorf("cannot change membership")
		}
		// An ownership offer goes only to an active member.
		if action != "restore" {
			if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_space_transfers WHERE space_id = ? AND to_user_id = ?", spaceID, userID); err != nil {
				return fmt.Errorf("cannot change membership")
			}
		}
		return nil
	})
}
