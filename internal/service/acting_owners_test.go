package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash/extension"
)

// The extensions' ActsFor answers are combined, cached per account, and an
// error grants nothing and is not cached (0.95.23).
func TestActingOwnersFunc(t *testing.T) {
	const admin, org, other = "0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210", "00000000000000000000000000000001"
	calls := 0
	fail := false
	s := &Service{extensions: []extension.Hooks{
		{Name: "none"},
		{Name: "orgs", ActsFor: func(_ context.Context, id string) ([]string, error) {
			calls++
			if fail {
				return nil, errors.New("database down")
			}
			if id == admin {
				return []string{org, admin, "not-an-id"}, nil
			}
			return nil, nil
		}},
	}}
	if (&Service{extensions: []extension.Hooks{{Name: "none"}}}).actingOwnersFunc() != nil {
		t.Fatal("no ActsFor hook should mean no function")
	}
	db := &accountDatabase{actingFor: s.actingOwnersFunc()}
	if !db.actsFor(context.Background(), admin, org) || !db.actsFor(context.Background(), admin, admin) {
		t.Fatal("admin should act for the organisation and themselves")
	}
	if db.actsFor(context.Background(), org, admin) || db.actsFor(context.Background(), other, org) {
		t.Fatal("acting is one way and only for the accounts named")
	}
	if got := db.actingOwners(context.Background(), admin); len(got) != 1 || got[0] != org {
		t.Fatalf("acting owners: %v", got)
	}
	before := calls
	db.actsFor(context.Background(), admin, org)
	if calls != before {
		t.Fatal("answer not cached")
	}
	fail = true
	if db.actsFor(context.Background(), other+"x", org) {
		t.Fatal("malformed id acts")
	}
	failing := &accountDatabase{actingFor: s.actingOwnersFunc()}
	if failing.actsFor(context.Background(), admin, org) {
		t.Fatal("an error must grant nothing")
	}
	fail = false
	if !failing.actsFor(context.Background(), admin, org) {
		t.Fatal("an error must not be cached")
	}
	var none *accountDatabase
	if none.actsFor(context.Background(), admin, org) || !none.actsFor(context.Background(), admin, admin) {
		t.Fatal("without the hook only the owner is the owner")
	}
}

// An account that acts for an owner account (an organisation's admin) uses
// and manages that owner's spaces as the owner would, on the website, in
// Settings and through apps, and stops the moment it no longer acts for it.
func TestActingOwnerAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	if w.s.ownedDB != w.db {
		t.Fatal("test world uses another database handle")
	}
	var mu sync.Mutex
	var mails []string
	w.s.accounts.sendNotice = func(_ context.Context, to, subject, body string) error {
		mu.Lock()
		defer mu.Unlock()
		mails = append(mails, to+"|"+subject)
		return nil
	}
	mailed := func(to, subject string) bool {
		for i := 0; i < 100; i++ {
			mu.Lock()
			for _, m := range mails {
				if m == to+"|"+subject {
					mu.Unlock()
					return true
				}
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}
	org, admin, stranger := w.user("org"), w.user("admin"), w.user("stranger")
	slug := randomName(t, "handbook-")
	space, err := w.s.createOwnedSpace(ctx, org.ID, "Handbook", slug)
	if err != nil {
		t.Fatal(err)
	}
	name := org.Username + "/" + slug
	site := "/api/v1/site/spaces/" + name
	var actingMu sync.Mutex
	acting := true
	w.db.actingFor = func(_ context.Context, id string) []string {
		actingMu.Lock()
		defer actingMu.Unlock()
		if acting && id == admin.ID {
			return []string{org.ID}
		}
		return nil
	}
	t.Cleanup(func() { w.db.actingFor = nil })
	stop := func() {
		actingMu.Lock()
		acting = false
		actingMu.Unlock()
	}

	adminSession, strangerSession := w.session(admin), w.session(stranger)
	csrf := siteToken(t, w, adminSession)

	// The website: reading and writing as the owner; others see nothing.
	if res := siteCall(w, "GET", site+"/file?path=AGENTS.md", "", adminSession, ""); res.Code != 200 {
		t.Fatalf("admin read: %d %s", res.Code, res.Body)
	}
	if res := siteCall(w, "GET", site+"/file?path=AGENTS.md", "", strangerSession, ""); res.Code != 404 {
		t.Fatalf("stranger read: %d", res.Code)
	}
	state := siteJSON(t, siteCall(w, "GET", site+"/files", "", adminSession, ""))["state"].(string)
	if res := siteCall(w, "PUT", site+"/file?path=policy.md", `{"text": "# Policy\n", "ifInState": "`+state+`"}`, adminSession, csrf); res.Code != 201 {
		t.Fatalf("admin write: %d %s", res.Code, res.Body)
	}
	if res := siteCall(w, "GET", "/spaces/"+name+"/", "", adminSession, ""); res.Code != 200 {
		t.Fatalf("admin space page: %d", res.Code)
	}

	// Listed with the spaces the admin can use, under the owner's name.
	session := siteCall(w, "GET", "/api/v1/site/session", "", adminSession, "").Body.String()
	if !strings.Contains(session, `"space":"`+name+`"`) {
		t.Fatalf("session spaces: %s", session)
	}
	overview := siteCall(w, "GET", "/api/v1/site/account", "", adminSession, "").Body.String()
	if !strings.Contains(overview, `"owner":"`+org.Username+`"`) || !strings.Contains(overview, `"space":"`+name+`"`) {
		t.Fatalf("account overview: %s", overview)
	}
	if strings.Contains(siteCall(w, "GET", "/api/v1/site/account", "", strangerSession, "").Body.String(), name) {
		t.Fatal("stranger's overview lists the space")
	}

	// Sharing, as the owner.
	sharing := siteCall(w, "GET", "/api/v1/site/account/spaces/"+space.ID+"/sharing", "", adminSession, "")
	if sharing.Code != 200 || !strings.Contains(sharing.Body.String(), `"space":"`+name+`"`) {
		t.Fatalf("admin sharing: %d %s", sharing.Code, sharing.Body)
	}
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account/spaces/"+space.ID+"/sharing", "", strangerSession, ""), 404, "")
	if res := accountPost(w, adminSession, "/spaces/"+space.ID+"/invite", jsonOf(map[string]string{"email": stranger.Email})); res.Code != 200 {
		t.Fatalf("admin invite: %d %s", res.Code, res.Body)
	}
	wantCode(t, accountPost(w, strangerSession, "/spaces/"+space.ID+"/invite", jsonOf(map[string]string{"email": "x@example.com"})), 404, "")

	// Making it readable on the web tells the admin, not the owner account.
	w.s.accounts.markConfirmed(adminSession.Value, admin.ID, time.Now())
	if res := accountPost(w, adminSession, "/spaces/"+space.ID+"/visibility", jsonOf(map[string]string{"visibility": spaceWeb})); res.Code != 200 {
		t.Fatalf("admin visibility: %d %s", res.Code, res.Body)
	}
	if !mailed(admin.Email, "A Metatrash space is now readable on the web") {
		t.Fatalf("no notice to the admin: %v", mails)
	}
	if res := accountPost(w, adminSession, "/spaces/"+space.ID+"/visibility", jsonOf(map[string]string{"visibility": spacePrivate})); res.Code != 200 {
		t.Fatalf("admin private: %d", res.Code)
	}

	// Apps: the admin can connect the space with write access.
	token := w.connect(admin, map[string]string{space.ID: "read_write"}, "")
	w.tool(token, "read", map[string]any{"space": name, "path": "policy.md"}, "")
	spaces := w.tool(token, "spaces", map[string]any{}, "")
	if !strings.Contains(jsonOf(spaces), name) {
		t.Fatalf("spaces tool: %v", spaces)
	}

	// No longer acting for the owner: everything stops once the answer
	// changes (the real hook's cache holds it at most actingOwnersTTL).
	stop()
	if res := siteCall(w, "GET", site+"/file?path=AGENTS.md", "", adminSession, ""); res.Code != 404 {
		t.Fatalf("former admin read: %d", res.Code)
	}
	w.tool(token, "read", map[string]any{"space": name, "path": "policy.md"}, "not_found")
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account/spaces/"+space.ID+"/sharing", "", adminSession, ""), 404, "")
	if code := accountPost(w, adminSession, "/spaces/"+space.ID+"/delete", jsonOf(map[string]string{"name": "Handbook"})).Code; code == 200 {
		t.Fatal("former admin deleted the space")
	}
	if res := siteCall(w, "GET", "/spaces/"+name+"/", "", adminSession, ""); res.Code == http.StatusOK && !strings.Contains(res.Body.String(), "Sign in") && strings.Contains(res.Body.String(), "policy.md") {
		t.Fatalf("former admin space page: %d", res.Code)
	}

	// Acting again, the admin can delete it, as the owner could.
	actingMu.Lock()
	acting = true
	actingMu.Unlock()
	if code := accountPost(w, adminSession, "/spaces/"+space.ID+"/delete", jsonOf(map[string]string{"name": "Handbook"})).Code; code != 200 {
		t.Fatalf("admin delete: %d", code)
	}
}

// Moving spaces between your own account and one you act for: needs a recent
// confirmation, keeps the space's ID, apps and old address, leaves you a
// member when your own space moves away, and refuses anyone else.
func TestMoveSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	var mu sync.Mutex
	var mails []string
	w.s.accounts.sendNotice = func(_ context.Context, to, subject, body string) error {
		mu.Lock()
		defer mu.Unlock()
		mails = append(mails, to+"|"+subject)
		return nil
	}
	org, admin, stranger := w.user("org"), w.user("admin"), w.user("stranger")
	var actingMu sync.Mutex
	acting := true
	w.db.actingFor = func(_ context.Context, id string) []string {
		actingMu.Lock()
		defer actingMu.Unlock()
		if acting && id == admin.ID {
			return []string{org.ID}
		}
		return nil
	}
	t.Cleanup(func() { w.db.actingFor = nil })
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 10 WHERE user_id IN (?, ?)", org.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	slug := randomName(t, "notes-")
	space, err := w.s.createOwnedSpace(ctx, admin.ID, "Team notes", slug)
	if err != nil {
		t.Fatal(err)
	}
	token := w.connect(admin, map[string]string{space.ID: "read_write"}, "")
	adminSession, strangerSession := w.session(admin), w.freshSession(stranger)
	move := func(session *http.Cookie, to string) *httptest.ResponseRecorder {
		t.Helper()
		return accountPost(w, session, "/spaces/"+space.ID+"/move", jsonOf(map[string]string{"owner": to}))
	}
	if res := move(adminSession, org.Username); res.Code != 403 {
		t.Fatalf("move without confirmation: %d %s", res.Code, res.Body)
	}
	w.s.accounts.markConfirmed(adminSession.Value, admin.ID, time.Now())
	if res := move(adminSession, stranger.Username); res.Code != 400 {
		t.Fatalf("move to a stranger: %d %s", res.Code, res.Body)
	}
	if res := move(adminSession, "no-such-"+slug); res.Code != 400 {
		t.Fatalf("move to nobody: %d", res.Code)
	}
	if res := move(adminSession, admin.Username); res.Code != 400 {
		t.Fatalf("move to where it is: %d", res.Code)
	}
	if res := move(strangerSession, stranger.Username); res.Code != 404 {
		t.Fatalf("stranger move: %d", res.Code)
	}
	overview := siteCall(w, "GET", "/api/v1/site/account", "", adminSession, "").Body.String()
	if !strings.Contains(overview, `"manages":["`+org.Username+`"]`) {
		t.Fatalf("manages: %s", overview)
	}
	if sharing := siteCall(w, "GET", "/api/v1/site/account/spaces/"+space.ID+"/sharing", "", adminSession, "").Body.String(); !strings.Contains(sharing, `"moveTo":["`+org.Username+`"]`) {
		t.Fatalf("own space moveTo: %s", sharing)
	}

	// Into the organisation.
	res := move(adminSession, strings.ToUpper(org.Username))
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"space":"`+org.Username+"/"+slug+`"`) {
		t.Fatalf("move in: %d %s", res.Code, res.Body)
	}
	var owner, status, permission string
	if err := w.db.db.QueryRow("SELECT owner_user_id FROM metatrash_spaces WHERE space_id = ?", space.ID).Scan(&owner); err != nil || owner != org.ID {
		t.Fatalf("owner %s %v", owner, err)
	}
	if err := w.db.db.QueryRow("SELECT status, agent_permission FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", space.ID, admin.ID).Scan(&status, &permission); err != nil || status != "active" || permission != "read_write" {
		t.Fatalf("admin membership %s %s %v", status, permission, err)
	}
	if w.s.repositoryOwner(w.s.ownedRepository(space.ID)) != org.ID {
		t.Fatal("open repository keeps the old owner")
	}
	w.tool(token, "read", map[string]any{"space": org.Username + "/" + slug, "path": "AGENTS.md"}, "")
	w.tool(token, "read", map[string]any{"space": admin.Username + "/" + slug, "path": "AGENTS.md"}, "")
	mu.Lock()
	sent := strings.Join(mails, "\n")
	mu.Unlock()
	if !strings.Contains(sent, admin.Email+"|A Metatrash space was moved") || strings.Contains(sent, org.Email) {
		t.Fatalf("mails: %s", sent)
	}
	if sharing := siteCall(w, "GET", "/api/v1/site/account/spaces/"+space.ID+"/sharing", "", adminSession, "").Body.String(); !strings.Contains(sharing, `"moveTo":["`+admin.Username+`"]`) {
		t.Fatalf("org space moveTo: %s", sharing)
	}
	// Offering an organisation's space to someone needs it moved out first.
	if res := accountPost(w, adminSession, "/spaces/"+space.ID+"/transfer-offer", jsonOf(map[string]string{"member": stranger.ID})); res.Code != 409 {
		t.Fatalf("offer while acting: %d %s", res.Code, res.Body)
	}

	// A space can't move to an address the organisation already uses.
	slug2 := randomName(t, "plans-")
	if _, err := w.s.createOwnedSpace(ctx, org.ID, "Plans", slug2); err != nil {
		t.Fatal(err)
	}
	other, err := w.s.createOwnedSpace(ctx, admin.ID, "Plans", slug2)
	if err != nil {
		t.Fatal(err)
	}
	if res := accountPost(w, adminSession, "/spaces/"+other.ID+"/move", jsonOf(map[string]string{"owner": org.Username})); res.Code != 409 {
		t.Fatalf("clash: %d %s", res.Code, res.Body)
	}

	// Back out again: the admin owns it, isn't a member, and the
	// organisation isn't made one.
	if res := move(adminSession, admin.Username); res.Code != 200 {
		t.Fatalf("move out: %d %s", res.Code, res.Body)
	}
	var members int
	if err := w.db.db.QueryRow("SELECT COUNT(*) FROM metatrash_memberships WHERE space_id = ?", space.ID).Scan(&members); err != nil || members != 0 {
		t.Fatalf("memberships after moving out: %d %v", members, err)
	}
	w.tool(token, "read", map[string]any{"space": admin.Username + "/" + slug, "path": "AGENTS.md"}, "")

	// Without acting for the organisation, nothing moves in.
	actingMu.Lock()
	acting = false
	actingMu.Unlock()
	if res := move(adminSession, org.Username); res.Code != 400 {
		t.Fatalf("move after acting ended: %d %s", res.Code, res.Body)
	}
}
