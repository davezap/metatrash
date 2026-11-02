package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The counting rules on their own: objects count once, a removed object
// comes off, and a listing keeps what was stored while it ran.
func TestSpaceUsageCounting(t *testing.T) {
	var u spaceUsage
	if u.addObject("a", 10) {
		t.Fatal("counted before the first listing")
	}
	u.startListing()
	u.addObject("b", 5) // stored while listing, not in the listing
	u.finishListing(map[string]int64{"a": 10, "c": 7}, time.Now(), true)
	if s := u.snapshot(); s.FileBytes != 22 || s.FileObjects != 3 || s.FilesAt.IsZero() {
		t.Fatalf("after listing: %+v", s)
	}
	if u.addObject("a", 10) {
		t.Fatal("an object counted twice")
	}
	if !u.addObject("d", 1) {
		t.Fatal("a new object not counted")
	}
	if !u.removeObject("c") || u.removeObject("c") {
		t.Fatal("remove")
	}
	if s := u.snapshot(); s.FileBytes != 16 || s.FileObjects != 3 {
		t.Fatalf("after add and remove: %+v", s)
	}
	// A failed listing keeps the counts.
	u.startListing()
	u.finishListing(map[string]int64{}, time.Now(), false)
	if s := u.snapshot(); s.FileBytes != 16 || s.FileObjects != 3 {
		t.Fatalf("after a failed listing: %+v", s)
	}
	// Seeding never overwrites a measurement.
	u.seed(usageSnapshot{GitBytes: 99, GitAt: time.Now(), FileBytes: 99})
	if s := u.snapshot(); s.GitBytes != 99 || s.FileBytes != 16 {
		t.Fatalf("seeded: %+v", s)
	}
	u.seed(usageSnapshot{GitBytes: 1, GitAt: time.Now()})
	if s := u.snapshot(); s.GitBytes != 99 {
		t.Fatalf("seeded twice: %+v", s)
	}
}

// Measuring storage against MariaDB: a write updates the repository's size
// without a walk, virtual files' objects count once, the sweep takes off
// what it removes, a full measurement agrees, and the numbers reach the
// database, Settings and the console.
func TestStorageAgainstDatabase(t *testing.T) {
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

	alice := w.user("alice")
	slug := randomName(t, "sized-")
	space, err := w.s.createOwnedSpace(ctx, alice.ID, "Sized", slug)
	if err != nil {
		t.Fatal(err)
	}
	r := w.s.ownedRepository(space.ID)
	name := alice.Username + "/" + slug
	token := w.connect(alice, map[string]string{space.ID: "read_write"}, "")

	// A write: the commit knows the new size, which matches a walk.
	state, _ := w.tool(token, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "")["state"].(string)
	before := r.usage.snapshot()
	w.tool(token, "write", map[string]any{"space": name, "path": "notes.md", "text": strings.Repeat("words, more words. ", 400), "ifInState": state}, "")
	after := r.usage.snapshot()
	walked, err := dirBytes(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if after.GitBytes <= before.GitBytes || after.GitBytes != walked || after.GitAt.IsZero() {
		t.Fatalf("after a write: before %+v after %+v walked %d", before, after, walked)
	}

	// Virtual files: none counted until the bucket is listed once, then
	// each object once.
	body := []byte("\x00binary content\x00")
	if _, err := w.s.storeVirtual(ctx, r, "a.bin", body, "application/octet-stream", ""); err != nil {
		t.Fatal(err)
	}
	if s := r.usage.snapshot(); s.FileObjects != 0 || !s.FilesAt.IsZero() {
		t.Fatalf("counted before listing: %+v", s)
	}
	if err := w.s.measureSpace(ctx, r, objects); err != nil {
		t.Fatal(err)
	}
	if s := r.usage.snapshot(); s.FileObjects != 1 || s.FileBytes != int64(len(body)) || s.FilesAt.IsZero() {
		t.Fatalf("after listing: %+v", s)
	}
	if _, err := w.s.storeVirtual(ctx, r, "copy.bin", body, "application/octet-stream", ""); err != nil {
		t.Fatal(err)
	}
	other := []byte("\x00other\x00")
	if _, err := w.s.storeVirtual(ctx, r, "b.bin", other, "application/octet-stream", ""); err != nil {
		t.Fatal(err)
	}
	if s := r.usage.snapshot(); s.FileObjects != 2 || s.FileBytes != int64(len(body)+len(other)) {
		t.Fatalf("after storing the same and another: %+v", s)
	}
	// No pointer names them, so the sweep removes both (memoryObjects lists
	// them as old) and the counts come off.
	if _, err := sweepSpace(ctx, r, objects, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s := r.usage.snapshot(); s.FileObjects != 0 || s.FileBytes != 0 {
		t.Fatalf("after the sweep: %+v", s)
	}
	// A virtual file kept by a pointer stays counted, and a full
	// measurement agrees with the running count.
	state, _ = w.tool(token, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "")["state"].(string)
	if _, err := w.s.putVirtual(ctx, r, name, "kept.bin", state, body, "application/octet-stream", "", ""); err != nil {
		t.Fatal(err)
	}
	running := r.usage.snapshot()
	if err := w.s.measureSpace(ctx, r, objects); err != nil {
		t.Fatal(err)
	}
	measured := r.usage.snapshot()
	if running.FileObjects != 1 || measured.FileObjects != 1 || measured.FileBytes != running.FileBytes || measured.GitBytes != running.GitBytes {
		t.Fatalf("running %+v, measured %+v", running, measured)
	}

	// The database gets the numbers.
	w.s.flushStorage()
	rows, err := w.db.spaceUsageRows(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := rows[space.ID]
	if !ok || row.GitBytes != measured.GitBytes || row.FileBytes != measured.FileBytes || row.FileObjects != 1 || row.FilesAt.IsZero() {
		t.Fatalf("stored row: %+v %v", row, ok)
	}

	// Settings: each space's usage and the account's total.
	res := siteCall(w, "GET", "/api/v1/site/account", "", w.session(alice), "")
	var account siteAccount
	if err := json.Unmarshal(res.Body.Bytes(), &account); err != nil || res.Code != 200 {
		t.Fatalf("account: %d %s", res.Code, res.Body.String())
	}
	total := measured.Total()
	if account.Storage == nil || account.Storage.Bytes != total || account.Storage.Git != measured.GitBytes || account.Storage.Measured == "" {
		t.Fatalf("account storage: %+v want %d", account.Storage, total)
	}
	var item *siteAccountSpace
	for i := range account.Spaces {
		if account.Spaces[i].ID == space.ID {
			item = &account.Spaces[i]
		}
	}
	if item == nil || item.Usage == nil || item.Usage.Bytes != total || item.Usage.Files != measured.FileBytes {
		t.Fatalf("space usage: %+v", item)
	}

	// The console.
	admin := &AdminDB{store: w.db}
	found, err := admin.FindUser(ctx, alice.Email)
	if err != nil {
		t.Fatal(err)
	}
	users, err := admin.UserUsage(ctx, &found)
	if err != nil || len(users) != 1 || users[0].Bytes != total || users[0].Spaces != 1 || users[0].Unmeasured != 0 || users[0].OldestMeasuredAt == nil {
		t.Fatalf("users usage: %+v %v", users, err)
	}
	spaces, err := admin.SpaceUsage(ctx, alice.ID)
	if err != nil || len(spaces) != 1 || spaces[0].Bytes != total || !spaces[0].Measured || spaces[0].FileObjects != 1 {
		t.Fatalf("spaces usage: %+v %v", spaces, err)
	}
	all, err := admin.UserUsage(ctx, nil)
	if err != nil || len(all) < 1 {
		t.Fatalf("every account: %d %v", len(all), err)
	}

	// A deleted space counts apart until it is purged, which drops its row.
	w.s.accounts.markConfirmed(w.session(alice).Value, alice.ID, time.Now())
	session := w.session(alice)
	w.s.accounts.markConfirmed(session.Value, alice.ID, time.Now())
	if res := accountPost(w, session, "/spaces/"+space.ID+"/delete", jsonOf(map[string]string{"name": "Sized"})); res.Code != 200 {
		t.Fatalf("delete: %d %s", res.Code, res.Body.String())
	}
	storage, err := w.s.accountStorage(ctx, alice.ID)
	if err != nil || storage.Total != 0 || storage.Deleted != total {
		t.Fatalf("after deleting: %+v %v", storage, err)
	}
	w.s.purgeDeletedSpaces(ctx, time.Now().Add(deletedSpaceHold+time.Hour))
	var count int
	if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_space_usage WHERE space_id = ?", space.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("row after purge: %d %v", count, err)
	}
}
