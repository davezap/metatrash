package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash/extension"
)

func TestSpaceGateFunc(t *testing.T) {
	const person, org, other = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210", "00000000000000000000000000000001"
	calls := 0
	fail := false
	held := problem(403, "held", "Sign in with Microsoft first.")
	s := &Service{extensions: []extension.Hooks{
		{Name: "none"},
		{Name: "orgs", SpaceGate: func(_ context.Context, userID, ownerID string) error {
			calls++
			if fail {
				return errors.New("database down")
			}
			if ownerID == org && userID == person {
				return held
			}
			return nil
		}},
	}}
	if (&Service{extensions: []extension.Hooks{{Name: "none"}}}).spaceGateFunc() != nil {
		t.Fatal("no SpaceGate hook should mean no function")
	}
	db := &accountDatabase{gate: s.spaceGateFunc()}
	ctx := context.Background()
	var p *Error
	if err := db.gateFor(ctx, person, org); !errors.As(err, &p) || p.Code != "held" {
		t.Fatalf("held: %v", err)
	}
	if err := db.gateFor(ctx, other, org); err != nil {
		t.Fatalf("other person: %v", err)
	}
	before := calls
	if db.gateFor(ctx, person, org) == nil || db.gateFor(ctx, other, org) != nil || calls != before {
		t.Fatal("answers not cached")
	}
	// Owners and signed-out visitors are never asked.
	if db.gateFor(ctx, org, org) != nil || db.gateFor(ctx, "", org) != nil || calls != before {
		t.Fatal("owner or nobody asked")
	}
	// Any other error refuses as unavailable, and is asked again next time.
	fail = true
	failing := &accountDatabase{gate: s.spaceGateFunc()}
	if err := failing.gateFor(ctx, other, org); !errors.As(err, &p) || p.Status != 503 {
		t.Fatalf("failure: %v", err)
	}
	fail = false
	if err := failing.gateFor(ctx, other, org); err != nil {
		t.Fatalf("failure cached: %v", err)
	}
	var none *accountDatabase
	if none.gateFor(ctx, person, org) != nil {
		t.Fatal("no database, no gate")
	}
}

// A gated owner's spaces: a member and an acting admin who don't pass the
// gate can't open them on the website, through apps or the account-wide
// lists, can't manage or move them, and a space readable on the web stays
// readable. Passing again restores everything.
func TestSpaceGateAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	org, admin, member, stranger := w.user("org"), w.user("admin"), w.user("member"), w.user("stranger")
	var mu sync.Mutex
	open := false
	w.db.actingFor = func(_ context.Context, id string) []string {
		if id == admin.ID {
			return []string{org.ID}
		}
		return nil
	}
	held := problem(403, "microsoft_sign_in_needed", "Sign in with Microsoft first.")
	w.db.gate = func(_ context.Context, userID, ownerID string) error {
		mu.Lock()
		defer mu.Unlock()
		if ownerID == org.ID && !open {
			return held
		}
		return nil
	}
	t.Cleanup(func() { w.db.actingFor, w.db.gate = nil, nil })
	setOpen := func(v bool) {
		mu.Lock()
		open = v
		mu.Unlock()
	}
	slug := randomName(t, "intranet-")
	space, err := w.s.createOwnedSpace(ctx, org.ID, "Intranet", slug)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := w.db.db.Exec("INSERT INTO metatrash_memberships (space_id, user_id, role, status, joined_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)", space.ID, member.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.db.Exec("UPDATE metatrash_memberships SET agent_permission = 'read_write' WHERE space_id = ? AND user_id = ?", space.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	name := org.Username + "/" + slug
	site := "/api/v1/site/spaces/" + name
	adminSession, memberSession, strangerSession := w.freshSession(admin), w.session(member), w.session(stranger)

	// Open: both get in, and connect apps.
	setOpen(true)
	for who, session := range map[string]*http.Cookie{"admin": adminSession, "member": memberSession} {
		if res := siteCall(w, "GET", site+"/file?path=AGENTS.md", "", session, ""); res.Code != 200 {
			t.Fatalf("%s read while open: %d %s", who, res.Code, res.Body)
		}
	}
	memberToken := w.connect(member, map[string]string{space.ID: "read_write"}, "")
	adminToken := w.connect(admin, map[string]string{space.ID: "read_write"}, "")
	w.tool(memberToken, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "")

	// Held back: told why everywhere, not "not found".
	setOpen(false)
	wantCode(t, siteCall(w, "GET", site+"/file?path=AGENTS.md", "", memberSession, ""), 403, "microsoft_sign_in_needed")
	wantCode(t, siteCall(w, "GET", site+"/file?path=AGENTS.md", "", adminSession, ""), 403, "microsoft_sign_in_needed")
	wantCode(t, siteCall(w, "GET", "/spaces/"+name+"/AGENTS.md", "", memberSession, ""), 403, "microsoft_sign_in_needed")
	wantCode(t, siteCall(w, "GET", "/spaces/"+name+"/?export", "", memberSession, ""), 403, "microsoft_sign_in_needed")
	w.tool(memberToken, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "microsoft_sign_in_needed")
	w.tool(adminToken, "read", map[string]any{"space": name, "path": "AGENTS.md"}, "microsoft_sign_in_needed")
	if strings.Contains(jsonOf(w.tool(memberToken, "spaces", map[string]any{}, "")), name) {
		t.Fatal("spaces tool lists a held space")
	}
	// Strangers still see nothing at all.
	wantCode(t, siteCall(w, "GET", site+"/file?path=AGENTS.md", "", strangerSession, ""), 404, "")
	// Managing and moving out need the same.
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account/spaces/"+space.ID+"/sharing", "", adminSession, ""), 403, "microsoft_sign_in_needed")
	wantCode(t, accountPost(w, adminSession, "/spaces/"+space.ID+"/visibility", jsonOf(map[string]string{"visibility": spaceWeb})), 403, "microsoft_sign_in_needed")
	wantCode(t, accountPost(w, adminSession, "/spaces/"+space.ID+"/move", jsonOf(map[string]string{"owner": admin.Username})), 403, "microsoft_sign_in_needed")
	// The account-wide to-do count leaves it out.
	if todo := siteCall(w, "GET", "/api/v1/site/todo", "", memberSession, "").Body.String(); strings.Contains(todo, name) {
		t.Fatalf("todo lists a held space: %s", todo)
	}

	// Readable on the web: read by anyone, still not written by the held member.
	if _, err := w.db.db.Exec("UPDATE metatrash_spaces SET visibility = ? WHERE space_id = ?", spaceWeb, space.ID); err != nil {
		t.Fatal(err)
	}
	if res := siteCall(w, "GET", site+"/file?path=AGENTS.md", "", memberSession, ""); res.Code != 200 {
		t.Fatalf("held member reading a web space: %d %s", res.Code, res.Body)
	}
	if res := siteCall(w, "GET", "/spaces/"+name+"/AGENTS.md", "", memberSession, ""); res.Code != 200 {
		t.Fatalf("held member's page of a web space: %d", res.Code)
	}
	state := siteJSON(t, siteCall(w, "GET", site+"/files", "", memberSession, ""))["state"].(string)
	wantCode(t, siteCall(w, "PUT", site+"/file?path=note.md", `{"text": "x\n", "ifInState": "`+state+`"}`, memberSession, siteToken(t, w, memberSession)), 403, "")

	// Passing the gate again restores access.
	setOpen(true)
	if res := siteCall(w, "PUT", site+"/file?path=note.md", `{"text": "x\n", "ifInState": "`+state+`"}`, memberSession, siteToken(t, w, memberSession)); res.Code != 201 {
		t.Fatalf("member write after passing: %d %s", res.Code, res.Body)
	}
	w.tool(adminToken, "read", map[string]any{"space": name, "path": "note.md"}, "")
}
