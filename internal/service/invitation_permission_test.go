package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDatabaseInvitationPermission(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	if !w.db.invitationPermissionsReady(ctx) {
		t.Fatal("the test database has no metatrash_invitations.agent_permission: apply deploy/account-invitation-permission.sql")
	}
	dora := w.user("dora")
	id, err := w.db.inviteHuman(ctx, w.owner.ID, w.ownedID, dora.Email)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := w.db.db.QueryRowContext(ctx, "SELECT agent_permission FROM metatrash_invitations WHERE invitation_id = ?", id).Scan(&stored); err != nil || stored != "read_write" {
		t.Fatalf("default %q %v", stored, err)
	}
	if err := w.db.setInvitationAgentPermission(ctx, w.owner.ID, w.ownedID, id, "admin"); err == nil {
		t.Error("accepted an unknown permission")
	}
	if err := w.db.setInvitationAgentPermission(ctx, dora.ID, w.ownedID, id, "read_only"); err == nil {
		t.Error("someone other than the owner changed the invitation")
	}
	if err := w.db.setInvitationAgentPermission(ctx, w.owner.ID, w.ownedID, id, "read_only"); err != nil {
		t.Fatal(err)
	}
	// Inviting the same address again keeps the pending invitation and its choice.
	if again, err := w.db.inviteHuman(ctx, w.owner.ID, w.ownedID, dora.Email); err != nil || again != id {
		t.Fatalf("invite again: %s %v", again, err)
	}
	if err := w.db.acceptHumanInvitation(ctx, dora.ID, w.ownedID, id); err != nil {
		t.Fatal(err)
	}
	var member string
	if err := w.db.db.QueryRowContext(ctx, "SELECT agent_permission FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", w.ownedID, dora.ID).Scan(&member); err != nil || member != "read_only" {
		t.Fatalf("membership starts with %q %v, want read_only", member, err)
	}
	if err := w.db.setInvitationAgentPermission(ctx, w.owner.ID, w.ownedID, id, "read_write"); err == nil || !strings.Contains(err.Error(), "accepted or cancelled") {
		t.Errorf("changed an accepted invitation: %v", err)
	}

	// A re-run's jobs don't count toward the folder's hour (rerun.go).
	store := dbJobStore{w.db.db}
	now := time.Now()
	for i, commit := range []string{strings.Repeat("a", 40), rerunCommit, rerunCommit} {
		j := agentJob{ID: strings.Repeat(string(rune('1'+i)), 32), SpaceID: w.ownedID, Folder: "hr/", Action: "tidy", File: "hr/a.md", Commit: commit, Status: jobDone, QueuedAt: now}
		if err := store.add(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := store.queuedSince(ctx, w.ownedID, "hr/", now.Add(-time.Hour)); err != nil || n != 1 {
		t.Errorf("queuedSince %d %v, want 1", n, err)
	}
}
