package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Renaming a space (0.95.29): a new name, optionally a new address under the
// same owner; the old address keeps leading to it for apps, and renaming back
// drops that alias. Taken addresses are refused.
func TestRenameSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	owner := w.user("owner")
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 10 WHERE user_id = ?", owner.ID); err != nil {
		t.Fatal(err)
	}
	slug, other := randomName(t, "docs-"), randomName(t, "other-")
	space, err := w.s.createOwnedSpace(ctx, owner.ID, "Old docs", slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.createOwnedSpace(ctx, owner.ID, "Other", other); err != nil {
		t.Fatal(err)
	}
	token := w.connect(owner, map[string]string{space.ID: "read_write"}, "")
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(`{"databaseConfigFile":"`+os.Getenv("METATRASH_TEST_DB_CONFIG")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdminDB(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	find := func(ref string) AdminSpace {
		t.Helper()
		s, err := admin.FindSpace(ctx, ref)
		if err != nil {
			t.Fatalf("find %s: %v", ref, err)
		}
		return s
	}

	// Name only: the address stays.
	renamed, err := admin.RenameSpace(ctx, find(owner.Username+"/"+slug), "  Collumii docs ", "")
	if err != nil || renamed.AddressChanged || renamed.Name != "Collumii docs" || renamed.Slug != slug {
		t.Fatalf("rename name only: %+v %v", renamed, err)
	}
	if got := find(space.ID); got.Name != "Collumii docs" || got.Slug != slug {
		t.Fatalf("after name change: %+v", got)
	}

	// A taken address is refused.
	if _, err := admin.RenameSpace(ctx, find(space.ID), "Collumii docs", other); !errors.Is(err, ErrSpaceAddressTaken) {
		t.Fatalf("rename onto another space: %v", err)
	}
	if _, err := admin.RenameSpace(ctx, find(space.ID), "Collumii docs", "Bad Slug"); err == nil {
		t.Fatal("accepted an invalid slug")
	}

	// A new address: both the new and the old one reach the space.
	fresh := randomName(t, "collumii-")
	renamed, err = admin.RenameSpace(ctx, find(space.ID), "Collumii docs", fresh)
	if err != nil || !renamed.AddressChanged || renamed.OldSlug != slug || renamed.Slug != fresh {
		t.Fatalf("rename address: %+v %v", renamed, err)
	}
	w.tool(token, "read", map[string]any{"space": owner.Username + "/" + fresh, "path": "AGENTS.md"}, "")
	w.tool(token, "read", map[string]any{"space": owner.Username + "/" + slug, "path": "AGENTS.md"}, "")
	if id, found, err := w.db.spaceByAlias(ctx, owner.Username, slug); err != nil || !found || id != space.ID {
		t.Fatalf("old address alias: %s %v %v", id, found, err)
	}
	// The owner can't make a new space at the old address while it leads there.
	if _, err := w.s.createOwnedSpace(ctx, owner.ID, "Reuse", slug); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("new space at the old address: %v", err)
	}

	// Back to the old address: it is real again and the alias goes.
	if _, err := admin.RenameSpace(ctx, find(space.ID), "Collumii docs", slug); err != nil {
		t.Fatalf("rename back: %v", err)
	}
	if _, found, err := w.db.spaceByAlias(ctx, owner.Username, slug); err != nil || found {
		t.Fatalf("alias left at the space's own address: %v %v", found, err)
	}
	var n int
	if err := w.db.db.QueryRow("SELECT COUNT(*) FROM metatrash_space_aliases WHERE space_id = ?", space.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("aliases after renaming back: %d %v", n, err)
	}
	w.tool(token, "read", map[string]any{"space": owner.Username + "/" + slug, "path": "AGENTS.md"}, "")
}
