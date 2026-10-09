package service

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Ownership transfer against MariaDB: offer (needs a fresh session and an
// active member with a username), the member's view, refusals at acceptance,
// what acceptance moves (owner, memberships, app connections, repository
// owner), the old address (web redirect, agent alias, reserved slug) and a
// transfer back.
func TestSpaceTransferAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	var mu sync.Mutex
	var mails []string
	w.s.accounts.sendNotice = func(_ context.Context, to, subject, body string) error {
		mu.Lock()
		defer mu.Unlock()
		mails = append(mails, to+"|"+subject+"|"+body)
		return nil
	}
	mailTo := func(email, subject string) string {
		t.Helper()
		for i := 0; i < 100; i++ {
			mu.Lock()
			for _, m := range mails {
				if strings.HasPrefix(m, email+"|"+subject+"|") {
					mu.Unlock()
					return m
				}
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("no %q mail to %s", subject, email)
		return ""
	}

	alice, bob, carol := w.user("alice"), w.user("bob"), w.user("carol")
	slug := randomName(t, "moving-")
	space, err := w.s.createOwnedSpace(ctx, alice.ID, "Moving house", slug)
	if err != nil {
		t.Fatal(err)
	}
	join := func(u userAccount) {
		t.Helper()
		id, err := w.db.inviteHuman(ctx, alice.ID, space.ID, u.Email)
		if err != nil {
			t.Fatal(err)
		}
		if err := w.db.acceptHumanInvitation(ctx, u.ID, space.ID, id); err != nil {
			t.Fatal(err)
		}
	}
	join(bob)
	join(carol)
	if err := w.db.manageHumanMember(ctx, alice.ID, space.ID, carol.ID, "suspend"); err != nil {
		t.Fatal(err)
	}
	// A member without a username cannot be offered the space.
	nameless, err := w.db.FindOrCreate(ctx, randomName(t, "nameless-")+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	join(nameless)

	aliceToken := w.connect(alice, map[string]string{space.ID: "read_write"}, "")
	bobToken := w.connect(bob, map[string]string{space.ID: "read_write"}, "")
	oldName, newName := alice.Username+"/"+slug, bob.Username+"/"+slug

	aliceSession := w.session(alice)
	offer := func(member string, session *http.Cookie) int {
		t.Helper()
		res := oauthCall(w.h, "POST", "/account/transfer/offer", url.Values{"csrf": {w.h.transferCSRF("offer", session.Value)}, "space": {space.ID}, "member": {member}}, session)
		return res.Code
	}
	// The sharing page asks for a recent sign-in before offering.
	page := oauthCall(w.h, "GET", "/account/sharing/"+space.ID, nil, aliceSession).Body.String()
	if !strings.Contains(page, `id="transfer"`) || !strings.Contains(page, "Confirm it’s you in Security") || strings.Contains(page, `action="/account/transfer/offer"`) {
		t.Fatal("sharing page offers a transfer without a fresh session")
	}
	if code := offer(bob.ID, aliceSession); code != 403 {
		t.Fatalf("offer without confirmation: %d", code)
	}
	w.s.accounts.markConfirmed(aliceSession.Value, alice.ID, time.Now())
	page = oauthCall(w.h, "GET", "/account/sharing/"+space.ID, nil, aliceSession).Body.String()
	if !strings.Contains(page, `action="/account/transfer/offer"`) || !strings.Contains(page, `<option value="`+bob.ID+`">`) || strings.Contains(page, `<option value="`+carol.ID+`">`) || strings.Contains(page, `<option value="`+nameless.ID+`">`) {
		t.Fatal("offer form should list only active members with a username")
	}
	for name, member := range map[string]string{"suspended": carol.ID, "nameless": nameless.ID, "owner": alice.ID, "stranger": w.user("stranger").ID, "garbage": "x"} {
		if code := offer(member, aliceSession); code != 400 {
			t.Fatalf("offer to %s: %d", name, code)
		}
	}
	// Only the owner can offer, with their own CSRF token.
	bobSession := w.session(bob)
	w.s.accounts.markConfirmed(bobSession.Value, bob.ID, time.Now())
	if code := offer(bob.ID, bobSession); code != 404 {
		t.Fatalf("member offering: %d", code)
	}
	if res := oauthCall(w.h, "POST", "/account/transfer/offer", url.Values{"csrf": {w.h.transferCSRF("cancel", aliceSession.Value)}, "space": {space.ID}, "member": {bob.ID}}, aliceSession); res.Code != 403 {
		t.Fatalf("offer with another action's token: %d", res.Code)
	}
	if code := offer(bob.ID, aliceSession); code != 303 {
		t.Fatalf("offer: %d", code)
	}
	if m := mailTo(bob.Email, "A Metatrash space has been offered to you"); !strings.Contains(m, oldName) || !strings.Contains(m, newName) || !strings.Contains(m, "Moving house") {
		t.Fatalf("offer mail: %s", m)
	}
	page = oauthCall(w.h, "GET", "/account/sharing/"+space.ID, nil, aliceSession).Body.String()
	if !strings.Contains(page, "Offered to <strong>"+bob.Email) || !strings.Contains(page, `action="/account/transfer/cancel"`) {
		t.Fatal("pending offer not shown to the owner")
	}
	// Bob sees it under All and Shared with me, and in the navigation count.
	for path, want := range map[string]bool{"/account": true, "/account/shared": true, "/account/mine": false} {
		body := oauthCall(w.h, "GET", path, nil, bobSession).Body.String()
		if got := strings.Contains(body, `action="/account/transfer/accept"`); got != want {
			t.Fatalf("%s shows the offer: %v", path, got)
		}
		if !strings.Contains(body, `aria-label="1 waiting for you"`) {
			t.Fatalf("%s: offer not counted in navigation", path)
		}
	}
	if strings.Contains(oauthCall(w.h, "GET", "/account", nil, w.session(carol)).Body.String(), `id="offers-heading"`) {
		t.Fatal("offer shown to another member")
	}

	accept := func(u userAccount) int {
		t.Helper()
		s := w.session(u)
		return oauthCall(w.h, "POST", "/account/transfer/accept", url.Values{"csrf": {w.h.transferCSRF("accept", s.Value)}, "space": {space.ID}}, s).Code
	}
	if code := accept(carol); code != 409 {
		t.Fatalf("someone else accepting: %d", code)
	}
	// Nobody can take it while they own a space at the same slug.
	erin := w.user("erin")
	join(erin)
	if _, err := w.s.createOwnedSpace(ctx, erin.ID, "Erin's own", slug); err != nil {
		t.Fatal(err)
	}
	if code := offer(erin.ID, aliceSession); code != 303 {
		t.Fatalf("offer to erin: %d", code)
	}
	if code := accept(erin); code != 409 {
		t.Fatalf("accept onto an address in use: %d", code)
	}
	if code := accept(bob); code != 409 {
		t.Fatalf("accepting an offer made to someone else: %d", code)
	}
	// Nor over their allowance (Bob owns one space; the limit is one).
	if code := offer(bob.ID, aliceSession); code != 303 {
		t.Fatalf("offer: %d", code)
	}
	if _, err := w.s.createOwnedSpace(ctx, bob.ID, "Bob's own", randomName(t, "own-")); err != nil {
		t.Fatal(err)
	}
	if code := accept(bob); code != 409 {
		t.Fatalf("accept over the allowance: %d", code)
	}
	if _, err := w.db.db.ExecContext(ctx, "UPDATE metatrash_users SET max_private_spaces = 2 WHERE user_id = ?", bob.ID); err != nil {
		t.Fatal(err)
	}
	// Declining and cancelling are harmless to repeat; an offer can be made again.
	decline := func() int {
		return oauthCall(w.h, "POST", "/account/transfer/decline", url.Values{"csrf": {w.h.transferCSRF("decline", bobSession.Value)}, "space": {space.ID}}, bobSession).Code
	}
	if decline() != 303 || decline() != 303 || accept(bob) != 409 {
		t.Fatal("decline")
	}
	if code := offer(bob.ID, aliceSession); code != 303 {
		t.Fatalf("offer again: %d", code)
	}
	cancel := func() int {
		return oauthCall(w.h, "POST", "/account/transfer/cancel", url.Values{"csrf": {w.h.transferCSRF("cancel", aliceSession.Value)}, "space": {space.ID}}, aliceSession).Code
	}
	if cancel() != 303 || cancel() != 303 || accept(bob) != 409 {
		t.Fatal("cancel")
	}
	// Suspending the member withdraws the offer.
	if code := offer(bob.ID, aliceSession); code != 303 {
		t.Fatalf("offer: %d", code)
	}
	if err := w.db.manageHumanMember(ctx, alice.ID, space.ID, bob.ID, "suspend"); err != nil {
		t.Fatal(err)
	}
	if err := w.db.manageHumanMember(ctx, alice.ID, space.ID, bob.ID, "restore"); err != nil {
		t.Fatal(err)
	}
	if code := accept(bob); code != 409 {
		t.Fatalf("offer survived suspension: %d", code)
	}
	// An expired offer cannot be accepted.
	if code := offer(bob.ID, aliceSession); code != 303 {
		t.Fatalf("offer: %d", code)
	}
	if _, err := w.db.db.ExecContext(ctx, "DELETE FROM metatrash_space_transfers WHERE space_id = ?", space.ID); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour).Unix()
	if _, err := w.db.db.ExecContext(ctx, "INSERT INTO metatrash_space_transfers (space_id, from_user_id, to_user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)", space.ID, alice.ID, bob.ID, past-int64(transferLifetime/time.Second), past); err != nil {
		t.Fatal(err)
	}
	if code := accept(bob); code != 409 {
		t.Fatalf("expired offer accepted: %d", code)
	}
	if !strings.Contains(oauthCall(w.h, "GET", "/account/sharing/"+space.ID, nil, aliceSession).Body.String(), "expired on") {
		t.Fatal("expired offer not shown as expired")
	}

	if code := offer(bob.ID, aliceSession); code != 303 {
		t.Fatalf("offer: %d", code)
	}
	if code := accept(bob); code != 303 {
		t.Fatalf("accept: %d", code)
	}
	if code := accept(bob); code != 303 {
		t.Fatalf("repeated accept: %d", code)
	}
	if m := mailTo(alice.Email, "Your Metatrash space has a new owner"); !strings.Contains(m, newName) || !strings.Contains(m, oldName) {
		t.Fatalf("accepted mail: %s", m)
	}
	var owner, status, permission string
	if err := w.db.db.QueryRowContext(ctx, "SELECT owner_user_id FROM metatrash_spaces WHERE space_id = ?", space.ID).Scan(&owner); err != nil || owner != bob.ID {
		t.Fatalf("owner: %s %v", owner, err)
	}
	if err := w.db.db.QueryRowContext(ctx, "SELECT status, agent_permission FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", space.ID, alice.ID).Scan(&status, &permission); err != nil || status != "active" || permission != "read_write" {
		t.Fatalf("previous owner's membership: %s %s %v", status, permission, err)
	}
	var count int
	if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", space.ID, bob.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("new owner still a member")
	}
	if got := w.s.repositoryOwner(w.s.ownedRepository(space.ID)); got != bob.ID {
		t.Fatalf("repository owner: %s", got)
	}
	// Bob lists it under Mine; Alice under Shared with me.
	if body := oauthCall(w.h, "GET", "/account/mine", nil, bobSession).Body.String(); !strings.Contains(body, newName) || strings.Contains(body, `id="offers-heading"`) {
		t.Fatal("new owner's spaces")
	}
	if body := oauthCall(w.h, "GET", "/account/shared", nil, aliceSession).Body.String(); !strings.Contains(body, newName) {
		t.Fatal("previous owner's shared spaces")
	}

	// Both app connections carry on; the old name still works for agents and
	// results use the new one.
	names := func(token string) map[string]string {
		out := map[string]string{}
		for _, item := range w.tool(token, "spaces", map[string]any{}, "")["spaces"].([]any) {
			m := item.(map[string]any)
			out[m["space"].(string)] = m["access"].(string)
		}
		return out
	}
	if got := names(aliceToken); got[newName] != "read_write" || got[oldName] != "" {
		t.Fatalf("alice's app: %v", got)
	}
	if got := names(bobToken); got[newName] != "read_write" {
		t.Fatalf("bob's app: %v", got)
	}
	read := w.tool(aliceToken, "read", map[string]any{"space": oldName, "path": "README.md"}, "")
	if read["space"] != newName {
		t.Fatalf("result names the old address: %v", read["space"])
	}
	w.tool(aliceToken, "write", map[string]any{"space": oldName, "path": "notes.md", "text": "still here", "ifInState": read["state"], "createOnly": true}, "")
	w.tool(bobToken, "read", map[string]any{"space": newName, "path": "notes.md"}, "")
	// Lowering the previous owner's app permission now applies to her.
	if err := w.db.setMemberAgentPermission(ctx, bob.ID, space.ID, alice.ID, "read_only"); err != nil {
		t.Fatal(err)
	}
	if got := names(aliceToken); got[newName] != "read_only" {
		t.Fatalf("alice's app after read only: %v", got)
	}

	// The website sends the old address to the new one, for people who can
	// see the space; everyone else sees what any private address shows.
	res := oauthCall(w.h, "GET", "/spaces/"+oldName+"/notes.md", nil, aliceSession)
	if res.Code != 301 || res.Header().Get("Location") != "/spaces/"+newName+"/notes.md" {
		t.Fatalf("old address: %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := oauthCall(w.h, "GET", "/spaces/"+oldName+"/", nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("old address signed out: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/spaces/"+oldName+"/", nil, w.session(w.user("nosy"))); res.Code != 404 {
		t.Fatalf("old address for a stranger: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/spaces/"+newName+"/notes.md", nil, aliceSession); res.Code != 200 {
		t.Fatalf("new address: %d", res.Code)
	}

	// A docs space configured by its old address carries on.
	if err := w.db.setSpaceVisibility(ctx, bob.ID, space.ID, spaceWeb); err != nil {
		t.Fatal(err)
	}
	w.s.accounts.config.DocsSpace = oldName
	if res := oauthCall(w.h, "GET", "/docs/notes.md", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "still here") {
		t.Fatalf("docs by the old address: %d", res.Code)
	}
	w.s.accounts.config.DocsSpace = ""
	// Signed out, a web-readable space's old address redirects too.
	if res := oauthCall(w.h, "GET", "/spaces/"+oldName+"/", nil); res.Code != 301 || res.Header().Get("Location") != "/spaces/"+newName+"/" {
		t.Fatalf("web-readable old address: %d %s", res.Code, res.Header().Get("Location"))
	}
	if err := w.db.setSpaceVisibility(ctx, bob.ID, space.ID, spacePrivate); err != nil {
		t.Fatal(err)
	}

	// The old address stays reserved for the space.
	if _, err := w.s.createOwnedSpace(ctx, alice.ID, "Moving house again", slug); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reusing the old address: %v", err)
	}
	if res := oauthCall(w.h, "POST", "/account/spaces", url.Values{"csrf": {w.s.accounts.mac("space-create:" + aliceSession.Value)}, "name": {slug}}, aliceSession); res.Code != 409 || !strings.Contains(res.Body.String(), "is reserved") || !strings.Contains(res.Body.String(), `id="space-name-error"`) {
		t.Fatalf("create form on the old address: %d", res.Code)
	}
	// Alice cannot accept another space onto it either.
	other, err := w.s.createOwnedSpace(ctx, carol.ID, "Carol's", slug)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := w.db.inviteHuman(ctx, carol.ID, other.ID, alice.Email)
	if err := w.db.acceptHumanInvitation(ctx, alice.ID, other.ID, id); err != nil {
		t.Fatal(err)
	}
	if err := w.db.offerSpaceTransfer(ctx, carol.ID, other.ID, alice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.acceptSpaceTransfer(ctx, alice.ID, other.ID); err == nil || !strings.Contains(err.Error(), "transferred to someone else") {
		t.Fatalf("accept onto a reserved address: %v", err)
	}

	// The space can come back: it takes its old address back, and Bob's
	// address now leads to it.
	if err := w.db.offerSpaceTransfer(ctx, bob.ID, space.ID, alice.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, done, err := w.s.acceptSpaceTransfer(ctx, alice.ID, space.ID); err != nil || !done {
		t.Fatalf("transfer back: %v", err)
	}
	if got := names(bobToken); got[oldName] != "read_write" {
		t.Fatalf("bob's app after the transfer back: %v", got)
	}
	if got := names(aliceToken); got[oldName] != "read_write" {
		t.Fatalf("alice's app after the transfer back: %v", got)
	}
	w.tool(bobToken, "read", map[string]any{"space": newName, "path": "notes.md"}, "")
	if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_space_aliases WHERE space_id = ?", space.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("aliases after the transfer back: %d %v", count, err)
	}
	// Removing Bob, now a member, disconnects his app from the space.
	if err := w.db.manageHumanMember(ctx, alice.ID, space.ID, bob.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	if got := names(bobToken); got[oldName] != "" {
		t.Fatalf("removed member's app: %v", got)
	}
	w.tool(bobToken, "read", map[string]any{"space": newName, "path": "notes.md"}, "not_found")
}
