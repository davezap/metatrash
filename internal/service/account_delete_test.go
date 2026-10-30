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

// Deleting an account against MariaDB: Profile's step-up and typed email,
// the refusal while it owns a space, what the request ends at once
// (sessions, apps), signing in again cancelling it, and the purge after 30
// days, which keeps other people's records and frees the address.
func TestDeleteAccountAgainstDatabase(t *testing.T) {
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
	mailed := func(email, subject string) bool {
		t.Helper()
		for i := 0; i < 100; i++ {
			mu.Lock()
			for _, m := range mails {
				if strings.HasPrefix(m, email+"|"+subject+"|") {
					mu.Unlock()
					return true
				}
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	carol := w.user("carol")
	invitation, err := w.db.inviteHuman(ctx, w.owner.ID, w.ownedID, carol.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.db.acceptHumanInvitation(ctx, carol.ID, w.ownedID, invitation); err != nil {
		t.Fatal(err)
	}
	if err := w.db.recordAcknowledgement(ctx, w.ownedID, ackRow{UserID: carol.ID, FileID: strings.Repeat("ab", 16), Blob: strings.Repeat("c", 40), Path: "policy.md", Version: 1, At: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	slug := randomName(t, "mine-")
	own, err := w.s.createOwnedSpace(ctx, carol.ID, "Mine", slug)
	if err != nil {
		t.Fatal(err)
	}
	token := w.connect(carol, map[string]string{w.ownedID: "read_write"}, "")
	w.tool(token, "read", map[string]any{"space": w.ownedName, "path": "AGENTS.md"}, "")

	session := w.session(carol)
	remove := func(typed string, session *http.Cookie) *http.Response {
		t.Helper()
		return oauthCall(w.h, "POST", "/account/delete", url.Values{"csrf": {w.s.accounts.mac("account-delete:" + session.Value)}, "email": {typed}}, session).Result()
	}
	page := oauthCall(w.h, "GET", "/account/profile", nil, session).Body.String()
	if !strings.Contains(page, `id="delete-account"`) || !strings.Contains(page, "You still own a space") || strings.Contains(page, `action="/account/delete"`) {
		t.Fatal("Profile offers Delete account while a space is owned")
	}
	if res := remove(carol.Email, session); res.StatusCode != 403 {
		t.Fatalf("delete without confirmation: %d", res.StatusCode)
	}
	w.s.accounts.markConfirmed(session.Value, carol.ID, time.Now())
	if res := remove(carol.Email, session); res.StatusCode != 409 {
		t.Fatalf("delete while owning a space: %d", res.StatusCode)
	}
	if _, err := w.s.deleteOwnedSpace(ctx, carol, own.ID, "Mine"); err != nil {
		t.Fatal(err)
	}
	if page := oauthCall(w.h, "GET", "/account/profile", nil, session).Body.String(); !strings.Contains(page, `action="/account/delete"`) {
		t.Fatal("no Delete account form once confirmed and owning nothing")
	}
	if res := remove("someone@example.com", session); res.StatusCode != 400 {
		t.Fatalf("wrong email: %d", res.StatusCode)
	}
	if res := oauthCall(w.h, "POST", "/account/delete", url.Values{"csrf": {w.s.accounts.mac("space-delete:" + session.Value)}, "email": {carol.Email}}, session); res.Code != 403 {
		t.Fatalf("another form's token: %d", res.Code)
	}
	other := w.session(carol)
	res := remove("  "+strings.ToUpper(carol.Email)+" ", session)
	if res.StatusCode != 303 || res.Header.Get("Location") != "/login" {
		t.Fatalf("delete: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	cleared := false
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("session cookie kept")
	}

	// Signed out everywhere, apps disconnected, emailed.
	for _, s := range []*http.Cookie{session, other} {
		if res := oauthCall(w.h, "GET", "/account", nil, s); res.Code != 303 {
			t.Fatalf("a session still signed in: %d", res.Code)
		}
	}
	if res := w.mcp(token, "tools/call", map[string]any{"name": "read", "arguments": map[string]any{"space": w.ownedName, "path": "AGENTS.md"}}, ""); res.status != 401 {
		t.Fatalf("the connected app still reads: %d", res.status)
	}
	if !mailed(carol.Email, "Your Metatrash account will be deleted") {
		t.Fatal("no notice of the request")
	}
	if at, err := w.db.accountDeletion(ctx, carol.ID); err != nil || at.IsZero() {
		t.Fatalf("deletion not recorded: %v %v", at, err)
	}

	// Signing in again keeps the account.
	w.s.accounts.mu.Lock()
	if _, err := w.s.accounts.startSessionLocked(ctx, carol.ID, time.Now()); err != nil {
		w.s.accounts.mu.Unlock()
		t.Fatal(err)
	}
	w.s.accounts.mu.Unlock()
	if at, err := w.db.accountDeletion(ctx, carol.ID); err != nil || !at.IsZero() {
		t.Fatalf("sign-in did not cancel: %v %v", at, err)
	}
	if !mailed(carol.Email, "Your Metatrash account will not be deleted") {
		t.Fatal("no notice of the cancellation")
	}
	w.s.purgeDeletedAccounts(ctx, time.Now().Add(AccountDeletionGrace+time.Hour))
	if u, _, err := w.db.ByID(ctx, carol.ID); err != nil || u.Email != carol.Email {
		t.Fatalf("a cancelled deletion purged: %+v %v", u, err)
	}

	// Asked again, then purged after 30 days.
	session = w.session(carol)
	w.s.accounts.markConfirmed(session.Value, carol.ID, time.Now())
	if res := remove(carol.Email, session); res.StatusCode != 303 {
		t.Fatalf("delete again: %d", res.StatusCode)
	}
	w.s.purgeDeletedAccounts(ctx, time.Now().Add(AccountDeletionGrace-time.Hour))
	admin := &AdminDB{store: w.db}
	if u, err := admin.FindUser(ctx, carol.Email); err != nil || u.DeletionRequestedAt == nil || u.DeletedAt != nil {
		t.Fatalf("purged before 30 days: %+v %v", u, err)
	}
	w.s.purgeDeletedAccounts(ctx, time.Now().Add(AccountDeletionGrace+time.Hour))
	if !mailed(carol.Email, "Your Metatrash account has been deleted") {
		t.Fatal("no final notice")
	}
	var email string
	var username *string
	var deleted *int64
	if err := w.db.db.QueryRowContext(ctx, "SELECT email, username, deleted_at FROM metatrash_users WHERE user_id = ?", carol.ID).Scan(&email, &username, &deleted); err != nil || email != deletedEmail(carol.ID) || username != nil || deleted == nil {
		t.Fatalf("purged row: %s %v %v %v", email, username, deleted, err)
	}
	for table, column := range map[string]string{"metatrash_memberships": "user_id", "metatrash_sessions": "user_id", "metatrash_oauth_grants": "user_id"} {
		var n int
		if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE "+column+" = ?", carol.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s kept %d rows: %v", table, n, err)
		}
	}
	var acks int
	if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_acknowledgements WHERE user_id = ?", carol.ID).Scan(&acks); err != nil || acks != 1 {
		t.Fatalf("acknowledgement not kept: %d %v", acks, err)
	}
	if _, err := admin.FindUser(ctx, carol.Email); err != ErrNoAccount {
		t.Fatalf("the old address still finds the account: %v", err)
	}

	// The address signs up afresh, as a new account.
	again, err := w.db.FindOrCreate(ctx, carol.Email)
	if err != nil || again.ID == carol.ID {
		t.Fatalf("signing up again: %+v %v", again, err)
	}
}
