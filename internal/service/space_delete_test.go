package service

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Deleting a space against MariaDB: the sharing page's step-up and typed
// name, who may delete, what the delete hides at once (website, apps,
// account lists, invitations), the held address, restoring it, and the purge
// after 30 days (repository, the space's virtual files, the row and address).
func TestDeleteSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	settings := testAgentSettings(t)
	objects := &memoryObjects{objects: map[string][]byte{}}
	settings.downloads = objects
	runner, err := newAgentRunner(w.s, settings, &memoryJobStore{jobs: map[string]agentJob{}})
	if err != nil {
		t.Fatal(err)
	}
	w.s.agents.Store(runner)
	t.Cleanup(func() { w.s.agents.Store(nil) })

	alice, bob := w.user("alice"), w.user("bob")
	slug := randomName(t, "old-")
	space, err := w.s.createOwnedSpace(ctx, alice.ID, "Old notes", slug)
	if err != nil {
		t.Fatal(err)
	}
	invite := func(email string) string {
		t.Helper()
		id, err := w.db.inviteHuman(ctx, alice.ID, space.ID, email)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := w.db.acceptHumanInvitation(ctx, bob.ID, space.ID, invite(bob.Email)); err != nil {
		t.Fatal(err)
	}
	pending := invite(randomName(t, "later-") + "@example.com")
	aliceToken := w.connect(alice, map[string]string{space.ID: "read_write"}, "")
	bobToken := w.connect(bob, map[string]string{space.ID: "read_write"}, "")
	name := alice.Username + "/" + slug
	w.tool(aliceToken, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "")

	aliceSession, bobSession := w.session(alice), w.session(bob)
	remove := func(typed string, session *http.Cookie) int {
		t.Helper()
		res := oauthCall(w.h, "POST", "/account/spaces/delete", url.Values{"csrf": {w.s.accounts.mac("space-delete:" + session.Value)}, "space": {space.ID}, "name": {typed}}, session)
		return res.Code
	}
	page := oauthCall(w.h, "GET", "/account/sharing/"+space.ID, nil, aliceSession).Body.String()
	if !strings.Contains(page, `id="delete"`) || !strings.Contains(page, `href="/spaces/`+name+`/?export"`) || strings.Contains(page, `action="/account/spaces/delete"`) {
		t.Fatal("sharing page offers Delete without a fresh session, or no Download")
	}
	if code := remove("Old notes", aliceSession); code != 403 {
		t.Fatalf("delete without confirmation: %d", code)
	}
	w.s.accounts.markConfirmed(aliceSession.Value, alice.ID, time.Now())
	w.s.accounts.markConfirmed(bobSession.Value, bob.ID, time.Now())
	if page := oauthCall(w.h, "GET", "/account/sharing/"+space.ID, nil, aliceSession).Body.String(); !strings.Contains(page, `action="/account/spaces/delete"`) {
		t.Fatal("no Delete form after confirming")
	}
	if code := remove("Old note", aliceSession); code != 400 {
		t.Fatalf("wrong name: %d", code)
	}
	if code := remove("Old notes", bobSession); code != 404 {
		t.Fatalf("member deleting: %d", code)
	}
	if res := oauthCall(w.h, "POST", "/account/spaces/delete", url.Values{"csrf": {w.s.accounts.mac("space-visibility:" + aliceSession.Value)}, "space": {space.ID}, "name": {"Old notes"}}, aliceSession); res.Code != 403 {
		t.Fatalf("another form's token: %d", res.Code)
	}
	if code := remove("  old NOTES ", aliceSession); code != 303 {
		t.Fatalf("delete: %d", code)
	}

	// Gone for everyone at once.
	if w.s.ownedRepository(space.ID) != nil {
		t.Fatal("repository still open")
	}
	for who, session := range map[string]*http.Cookie{"owner": aliceSession, "member": bobSession} {
		if res := oauthCall(w.h, "GET", "/spaces/"+name+"/", nil, session); res.Code != 404 {
			t.Fatalf("%s opens it: %d", who, res.Code)
		}
		if strings.Contains(oauthCall(w.h, "GET", "/account", nil, session).Body.String(), slug) {
			t.Fatalf("%s's account still lists it", who)
		}
	}
	for who, token := range map[string]string{"owner": aliceToken, "member": bobToken} {
		if res := w.mcp(token, "tools/call", map[string]any{"name": "read", "arguments": map[string]any{"space": name, "path": "AGENTS.md"}}, ""); !res.isError {
			t.Fatalf("%s's app still reads it", who)
		}
	}
	var status string
	if err := w.db.db.QueryRowContext(ctx, "SELECT status FROM metatrash_invitations WHERE invitation_id = ?", pending).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("pending invitation: %s %v", status, err)
	}
	if code := remove("Old notes", aliceSession); code != 404 {
		t.Fatalf("deleting again: %d", code)
	}
	// The address is held; the allowance is not.
	if _, err := w.s.createOwnedSpace(ctx, alice.ID, "Old notes", slug); err == nil || !strings.Contains(err.Error(), "is held until") {
		t.Fatalf("reusing the address: %v", err)
	}
	var count int
	if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ? AND provisioning_state IN ('provisioning', 'ready')", alice.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("counted spaces: %d %v", count, err)
	}

	// The operator restores it; the service opens it on its next start.
	admin := &AdminDB{store: w.db}
	found, err := admin.FindSpace(ctx, name)
	if err != nil || found.State != "deleted" || found.PurgeAt == nil || found.PurgeAt.Sub(*found.DeletedAt) != deletedSpaceHold {
		t.Fatalf("operator's view: %+v %v", found, err)
	}
	if err := admin.RestoreSpace(ctx, found); err != nil {
		t.Fatal(err)
	}
	if err := admin.RestoreSpace(ctx, found); err != ErrNotDeleted {
		t.Fatalf("restoring twice: %v", err)
	}
	restored, err := scanOwnedSpace(w.db.db.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE space_id = ?", space.ID))
	if err != nil || restored.State != "ready" {
		t.Fatalf("restored: %+v %v", restored, err)
	}
	if err := w.s.prepareOwnedSpace(ctx, restored); err != nil {
		t.Fatal(err)
	}
	w.tool(bobToken, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "")

	// Deleted again and purged after 30 days, with its own virtual files only.
	if code := remove("Old notes", aliceSession); code != 303 {
		t.Fatalf("delete again: %d", code)
	}
	mine, other := "files/"+space.ID+"/"+strings.Repeat("a", 64), "files/"+w.ownedID+"/"+strings.Repeat("b", 64)
	objects.objects[mine], objects.objects[other] = []byte("x"), []byte("y")
	repoPath := filepath.Join(w.s.ownedRoot, space.ID+".git")
	w.s.purgeDeletedSpaces(ctx, time.Now().Add(deletedSpaceHold-time.Hour))
	if _, err := os.Stat(repoPath); err != nil || objects.objects[mine] == nil {
		t.Fatal("purged before 30 days")
	}
	w.s.purgeDeletedSpaces(ctx, time.Now().Add(deletedSpaceHold+time.Hour))
	if _, err := os.Stat(repoPath); !os.IsNotExist(err) {
		t.Fatalf("repository kept: %v", err)
	}
	if objects.objects[mine] != nil || objects.objects[other] == nil {
		t.Fatalf("objects after purge: %v", objects.removed)
	}
	purged, err := scanOwnedSpace(w.db.db.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE space_id = ?", space.ID))
	if err != nil || purged.State != "purged" || purged.Slug != "deleted-"+space.ID {
		t.Fatalf("purged row: %+v %v", purged, err)
	}
	if err := admin.RestoreSpace(ctx, AdminSpace{ID: space.ID}); err != ErrNotDeleted {
		t.Fatalf("restoring a purged space: %v", err)
	}
	// The address is free again.
	if _, err := w.s.createOwnedSpace(ctx, alice.ID, "Old notes", slug); err != nil {
		t.Fatal(err)
	}
}
