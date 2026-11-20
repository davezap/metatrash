package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash/extension"
)

func TestSpaceLevelsCombineAndMemberAccess(t *testing.T) {
	const (
		user  = "11111111111111111111111111111111"
		org   = "22222222222222222222222222222222"
		other = "33333333333333333333333333333333"
		open  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		read  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		hid   = "cccccccccccccccccccccccccccccccc"
	)
	calls := 0
	fail := false
	s := &Service{extensions: []extension.Hooks{
		{Name: "one", SpaceLevels: func(ctx context.Context, userID string) (extension.SpaceLevels, error) {
			calls++
			if fail {
				return extension.SpaceLevels{}, errors.New("down")
			}
			return extension.SpaceLevels{Owners: []string{org, userID, "bad"}, Spaces: map[string]string{open: "read_write", read: "read_write", "x": "read_write"}}, nil
		}},
		{Name: "two", SpaceLevels: func(ctx context.Context, userID string) (extension.SpaceLevels, error) {
			return extension.SpaceLevels{Spaces: map[string]string{read: "read_only", hid: "owner"}}, nil
		}},
	}}
	db := &accountDatabase{levels: s.spaceLevelsFunc()}
	ctx := context.Background()
	levels, err := db.levelsFor(ctx, user)
	if err != nil || !levels.managed(org) || levels.managed(user) || levels.managed(other) || len(levels.owners) != 1 {
		t.Fatalf("owners = %v, %v", levels.owners, err)
	}
	if levels.spaces[open] != "read_write" || levels.spaces[read] != "read_only" || levels.spaces[hid] != "" || len(levels.spaces) != 2 {
		t.Fatalf("spaces = %v", levels.spaces)
	}
	if _, err := db.levelsFor(ctx, user); err != nil || calls != 1 {
		t.Fatalf("not cached: %d calls, %v", calls, err)
	}
	null := sql.NullString{}
	active := sql.NullString{String: "active", Valid: true}
	suspended := sql.NullString{String: "suspended", Valid: true}
	rw := sql.NullString{String: "read_write", Valid: true}
	ro := sql.NullString{String: "read_only", Valid: true}
	cases := []struct {
		name              string
		owner, space      string
		status, perm      sql.NullString
		wantStatus, wantP string
	}{
		{"level alone", org, open, null, null, "active", "read_write"},
		{"read-only level alone", org, read, null, null, "active", "read_only"},
		{"share lowers", org, open, active, ro, "active", "read_only"},
		{"share can't raise", org, read, active, rw, "active", "read_only"},
		{"suspended stays", org, open, suspended, rw, "suspended", "read_write"},
		{"hidden despite share", org, hid, active, rw, "", ""},
		{"hidden alone", org, hid, null, null, "", ""},
		{"other owner unchanged", other, hid, active, rw, "active", "read_write"},
		{"other owner no share", other, open, null, null, "", ""},
	}
	for _, c := range cases {
		status, perm, err := db.memberAccess(ctx, user, c.owner, c.space, c.status, c.perm)
		if err != nil || status.String != c.wantStatus || perm.String != c.wantP {
			t.Errorf("%s: got %q %q %v", c.name, status.String, perm.String, err)
		}
	}
	if st, p, err := db.memberAccess(ctx, "", org, open, null, null); err != nil || st.Valid || p.Valid {
		t.Fatalf("signed out: %v %v %v", st, p, err)
	}
	fail = true
	const second = "44444444444444444444444444444444"
	if _, _, err := db.memberAccess(ctx, second, org, open, active, rw); err == nil {
		t.Fatal("an extension error must refuse")
	}
	if got := lowerLevel("read_write", "read_only"); got != "read_only" {
		t.Fatal(got)
	}
	if (&Service{}).spaceLevelsFunc() != nil {
		t.Fatal("no hooks should give nil")
	}
	var none *accountDatabase
	if st, _, err := none.memberAccess(ctx, user, org, open, active, ro); err != nil || st.String != "active" {
		t.Fatal("nil database must leave the membership as it is")
	}
}

// An extension's levels open an owner's spaces to a person without a
// membership, a membership only lowers them, and a space without a level
// is hidden even from a member, on the website, in the spaces list and
// through apps (0.100.0).
func TestSpaceLevelsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	org, person, sharer := w.user("org"), w.user("person"), w.user("sharer")
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 10 WHERE user_id = ?", org.ID); err != nil {
		t.Fatal(err)
	}
	slugA, slugB := randomName(t, "open-"), randomName(t, "hidden-")
	open, err := w.s.createOwnedSpace(ctx, org.ID, "Open", slugA)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := w.s.createOwnedSpace(ctx, org.ID, "Hidden", slugB)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	level := "read_only"
	w.db.levels = func(_ context.Context, id string) (spaceLevels, error) {
		mu.Lock()
		defer mu.Unlock()
		l := spaceLevels{owners: map[string]bool{org.ID: true}, spaces: map[string]string{}}
		if id == person.ID && level != "" {
			l.spaces[open.ID] = level
		}
		return l, nil
	}
	t.Cleanup(func() { w.db.levels = nil })
	setLevel := func(l string) {
		mu.Lock()
		level = l
		mu.Unlock()
	}
	// The sharer has a membership in the hidden space, which grants nothing.
	now := time.Now().Unix()
	if _, err := w.db.db.Exec("INSERT INTO metatrash_memberships (space_id, user_id, role, status, joined_at, updated_at, agent_permission) VALUES (?, ?, 'member', 'active', ?, ?, 'read_write')", hidden.ID, sharer.ID, now, now); err != nil {
		t.Fatal(err)
	}
	nameA, nameB := org.Username+"/"+slugA, org.Username+"/"+slugB
	personSession, sharerSession := w.session(person), w.session(sharer)

	if res := siteCall(w, "GET", "/api/v1/site/spaces/"+nameA+"/file?path=AGENTS.md", "", personSession, ""); res.Code != 200 {
		t.Fatalf("level read: %d %s", res.Code, res.Body)
	}
	state := siteJSON(t, siteCall(w, "GET", "/api/v1/site/spaces/"+nameA+"/files", "", personSession, ""))["state"].(string)
	csrf := siteToken(t, w, personSession)
	if res := siteCall(w, "PUT", "/api/v1/site/spaces/"+nameA+"/file?path=x.md", `{"text": "x\n", "ifInState": "`+state+`"}`, personSession, csrf); res.Code != 403 {
		t.Fatalf("read-only level wrote: %d %s", res.Code, res.Body)
	}
	if res := siteCall(w, "GET", "/spaces/"+nameA+"/", "", personSession, ""); res.Code != 200 {
		t.Fatalf("level space page: %d", res.Code)
	}
	if session := siteCall(w, "GET", "/api/v1/site/session", "", personSession, "").Body.String(); !strings.Contains(session, `"space":"`+nameA+`"`) || strings.Contains(session, nameB) {
		t.Fatalf("person's spaces: %s", session)
	}
	if res := siteCall(w, "GET", "/api/v1/site/spaces/"+nameB+"/file?path=AGENTS.md", "", sharerSession, ""); res.Code != 404 {
		t.Fatalf("membership opened a hidden space: %d", res.Code)
	}
	if session := siteCall(w, "GET", "/api/v1/site/session", "", sharerSession, "").Body.String(); strings.Contains(session, nameB) {
		t.Fatalf("hidden space listed: %s", session)
	}

	// Apps: consent up to the level, and access follows it.
	token := w.connect(person, map[string]string{open.ID: "read_only"}, "")
	w.tool(token, "read", map[string]any{"space": nameA, "path": "AGENTS.md"}, "")
	setLevel("read_write")
	if res := siteCall(w, "PUT", "/api/v1/site/spaces/"+nameA+"/file?path=x.md", `{"text": "x\n", "ifInState": "`+state+`"}`, personSession, csrf); res.Code != 201 {
		t.Fatalf("read-write level write: %d %s", res.Code, res.Body)
	}
	// A personal share lowers the level.
	if _, err := w.db.db.Exec("INSERT INTO metatrash_memberships (space_id, user_id, role, status, joined_at, updated_at, agent_permission) VALUES (?, ?, 'member', 'active', ?, ?, 'read_only')", open.ID, person.ID, now, now); err != nil {
		t.Fatal(err)
	}
	state = siteJSON(t, siteCall(w, "GET", "/api/v1/site/spaces/"+nameA+"/files", "", personSession, ""))["state"].(string)
	if res := siteCall(w, "PUT", "/api/v1/site/spaces/"+nameA+"/file?path=y.md", `{"text": "y\n", "ifInState": "`+state+`"}`, personSession, csrf); res.Code != 403 {
		t.Fatalf("share didn't lower: %d %s", res.Code, res.Body)
	}
	// No level: hidden everywhere, membership or not.
	setLevel("")
	if res := siteCall(w, "GET", "/api/v1/site/spaces/"+nameA+"/file?path=AGENTS.md", "", personSession, ""); res.Code != 404 {
		t.Fatalf("hidden read: %d", res.Code)
	}
	w.tool(token, "read", map[string]any{"space": nameA, "path": "AGENTS.md"}, "not_found")
}
