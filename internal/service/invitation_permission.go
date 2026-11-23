package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// What a pending invitation's connected apps may do (0.101.0). A member's
// connected AI apps may read and write their space, or only read it
// (metatrash_memberships.agent_permission, Settings → Spaces → Members). An
// owner may choose it on the invitation too, before it's accepted: the
// membership starts with it.
//
// The choice is the column metatrash_invitations.agent_permission
// ('read_write' by default), added by deploy/account-invitation-permission.sql,
// an additive change any earlier executable ignores. Without it the server
// works as before: invitations carry no choice and the Members page offers
// none.

// invitationPermissionsReady reports whether the account database has the
// column. Asked once; a failed look counts as no.
func (db *accountDatabase) invitationPermissionsReady(ctx context.Context) bool {
	if db == nil {
		return false
	}
	db.invitePermissionOnce.Do(func() {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var n int
		err := db.db.QueryRowContext(lookup, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'metatrash_invitations' AND column_name = 'agent_permission'").Scan(&n)
		db.invitePermission = err == nil && n == 1
		if !db.invitePermission {
			log.Printf("sharing: invitations take no app permission until deploy/account-invitation-permission.sql is applied")
		}
	})
	return db.invitePermission
}

func validAgentPermission(permission string) bool {
	return permission == "read_only" || permission == "read_write"
}

// setInvitationAgentPermission sets what a pending invitation's apps may do
// once it is accepted.
func (db *accountDatabase) setInvitationAgentPermission(ctx context.Context, ownerID, spaceID, invitationID, permission string) error {
	if !idPattern.MatchString(invitationID) || !validAgentPermission(permission) {
		return invalid("Invalid app permission.")
	}
	if !db.invitationPermissionsReady(ctx) {
		return problem(503, "unavailable", "Choosing what an invitation's apps may do isn't set up on this server yet.")
	}
	return db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, _ ownedSpace) error {
		var status string
		err := tx.QueryRowContext(ctx, "SELECT status FROM metatrash_invitations WHERE space_id = ? AND invitation_id = ?", spaceID, invitationID).Scan(&status)
		if err == sql.ErrNoRows {
			return missing()
		}
		if err != nil {
			return fmt.Errorf("cannot read invitation")
		}
		if status != "pending" {
			return problem(409, "conflict", "This invitation was accepted or cancelled. Change what a member's apps may do on their row under Members.")
		}
		if _, err := tx.ExecContext(ctx, "UPDATE metatrash_invitations SET agent_permission = ? WHERE space_id = ? AND invitation_id = ? AND status = 'pending'", permission, spaceID, invitationID); err != nil {
			return fmt.Errorf("cannot change app permission")
		}
		return nil
	})
}

// invitedAgentPermission is what an invitation chose for its apps, inside
// the transaction that accepts it: read_write when the server keeps no
// choice.
func (db *accountDatabase) invitedAgentPermission(ctx context.Context, tx *sql.Tx, spaceID, invitationID string) (string, error) {
	if !db.invitationPermissionsReady(ctx) {
		return "read_write", nil
	}
	var permission string
	if err := tx.QueryRowContext(ctx, "SELECT agent_permission FROM metatrash_invitations WHERE space_id = ? AND invitation_id = ?", spaceID, invitationID).Scan(&permission); err != nil {
		return "", fmt.Errorf("cannot read invitation")
	}
	if !validAgentPermission(permission) {
		return "read_write", nil
	}
	return permission, nil
}
