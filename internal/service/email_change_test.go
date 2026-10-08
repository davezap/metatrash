package service

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChangeEmailAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	notices := make(chan string, 16)
	a.sendNotice = func(_ context.Context, email, subject, body string) error {
		notices <- email + "|" + subject + "|" + body
		return nil
	}
	expectNotice := func(to, want string) {
		t.Helper()
		select {
		case n := <-notices:
			if !strings.HasPrefix(n, to+"|") || !strings.Contains(n, want) {
				t.Fatalf("notice %q, want %q to %s", n, want, to)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no notice email (%s)", want)
		}
	}
	codes := map[string]string{}
	a.sendEmailChange = func(_ context.Context, email, code string) error {
		codes[email] = code
		return nil
	}
	post := func(path string, form url.Values, session *http.Cookie) *http.Response {
		t.Helper()
		form.Set("csrf", a.signInCSRF(path, session.Value))
		return oauthCall(w.h, "POST", path, form, session).Result()
	}
	page := func(session *http.Cookie) string {
		return html.UnescapeString(oauthCall(w.h, "GET", "/account/security", nil, session).Body.String())
	}
	old := w.owner.Email
	fresh := randomName(t, "moved-") + "@example.com"

	// Not confirmed: no form, and the change is refused.
	stale := w.session(w.owner)
	if body := page(stale); !strings.Contains(body, `id="email-address"`) || strings.Contains(body, `action="/account/email/change"`) || !strings.Contains(body, "To change it, confirm it’s you.") {
		t.Fatal("email card before confirming")
	}
	if r := post("/account/email/change", url.Values{"email": {fresh}}, stale); r.StatusCode != 403 || len(codes) != 0 {
		t.Fatalf("change without confirming: %d", r.StatusCode)
	}

	session := w.session(w.owner)
	a.markConfirmed(session.Value, w.owner.ID, time.Now())
	if !strings.Contains(page(session), `action="/account/email/change"`) {
		t.Fatal("change form not offered")
	}
	for _, bad := range []string{"not-an-address", strings.ToUpper(old)} {
		if r := post("/account/email/change", url.Values{"email": {bad}}, session); r.StatusCode != 400 {
			t.Fatalf("change to %q: %d", bad, r.StatusCode)
		}
	}

	// An address that has an account gets a notice, not a code, and the
	// page looks the same.
	r := post("/account/email/change", url.Values{"email": {w.member.Email}}, session)
	if r.StatusCode != 303 || noticeOf(r) != "email-change-sent" || len(codes) != 0 {
		t.Fatalf("change to a taken address: %d, %d codes", r.StatusCode, len(codes))
	}
	expectNotice(w.member.Email, "already has its own Metatrash account")
	if !strings.Contains(page(session), "We’ve sent a code to "+`<strong class="account-email">`+w.member.Email) {
		t.Fatal("pending change not shown")
	}
	if r := post("/account/email/cancel", url.Values{}, session); r.StatusCode != 303 || noticeOf(r) != "email-change-cancelled" {
		t.Fatalf("cancel: %d", r.StatusCode)
	}
	if strings.Contains(page(session), "We’ve sent a code to") {
		t.Fatal("cancelled change still shown")
	}

	// A free address gets a code. The pending change outlives the
	// confirmation window: the code is what proves the new address.
	r = post("/account/email/change", url.Values{"email": {"  " + strings.ToUpper(fresh) + " "}}, session)
	if r.StatusCode != 303 || codes[fresh] == "" {
		t.Fatalf("change: %d, codes %v", r.StatusCode, codes)
	}
	a.mu.Lock()
	s := a.sessions[secretDigest(session.Value)]
	s.AuthAt = time.Now().Add(-time.Hour)
	a.sessions[secretDigest(session.Value)] = s
	a.mu.Unlock()
	// Another session of the same account cannot use the code.
	other := w.session(w.owner)
	if r := post("/account/email/verify", url.Values{"code": {codes[fresh]}}, other); r.StatusCode != 400 {
		t.Fatalf("code from another session: %d", r.StatusCode)
	}
	wrong := "000000"
	if codes[fresh] == wrong {
		wrong = "000001"
	}
	if r := post("/account/email/verify", url.Values{"code": {wrong}}, session); r.StatusCode != 400 {
		t.Fatalf("wrong code: %d", r.StatusCode)
	}
	// A login code waiting for the old address must not survive the change,
	// or it would create a new account there.
	a.mu.Lock()
	a.challenges[secretDigest("some-browser")] = loginChallenge{Email: old, Digest: "x", Expires: time.Now().Add(time.Minute), Ready: true}
	a.mu.Unlock()
	r = post("/account/email/verify", url.Values{"code": {codes[fresh]}}, session)
	if r.StatusCode != 303 || noticeOf(r) != "email-changed" {
		t.Fatalf("verify: %d", r.StatusCode)
	}
	got := map[string]string{}
	for range 2 {
		select {
		case n := <-notices:
			to, rest, _ := strings.Cut(n, "|")
			got[to] = rest
		case <-time.After(2 * time.Second):
			t.Fatal("missing notice")
		}
	}
	if !strings.Contains(got[old], "was changed from "+old+" to "+fresh) || !strings.Contains(got[fresh], "This is now the email address") {
		t.Fatalf("notices %v", got)
	}
	user, _, err := w.db.ByID(ctx, w.owner.ID)
	if err != nil || user.Email != fresh {
		t.Fatalf("stored address %q %v", user.Email, err)
	}
	if id, ok, _ := w.db.userIDByEmail(ctx, fresh); !ok || id != w.owner.ID {
		t.Fatal("new address does not find the account")
	}
	if ok, _ := w.db.Exists(ctx, old); ok {
		t.Fatal("old address still has an account")
	}
	a.mu.Lock()
	_, waiting := a.challenges[secretDigest("some-browser")]
	a.mu.Unlock()
	if waiting {
		t.Fatal("login code for the old address survived")
	}
	// The change signs out the account's other sessions, not this one.
	if _, ok, _ := a.currentUser(ctx, other.Value); ok {
		t.Fatal("other session still signed in after the change")
	}
	if _, ok, _ := a.currentUser(ctx, stale.Value); ok {
		t.Fatal("stale session still signed in after the change")
	}
	if body := page(session); !strings.Contains(body, fresh) || strings.Contains(body, "We’ve sent a code to") {
		t.Fatal("security page after the change")
	}
	// The code is spent.
	if r := post("/account/email/verify", url.Values{"code": {codes[fresh]}}, session); r.StatusCode != 400 {
		t.Fatalf("code used twice: %d", r.StatusCode)
	}

	// The database refuses a stale old address and a taken new one.
	if err := w.db.changeEmail(ctx, w.owner.ID, old, randomName(t, "x-")+"@example.com"); err == nil {
		t.Fatal("changed from a stale address")
	}
	if err := w.db.changeEmail(ctx, w.owner.ID, fresh, w.member.Email); err == nil || !strings.Contains(err.Error(), "own Metatrash account") {
		t.Fatalf("changed to a taken address: %v", err)
	}
	if err := w.db.changeEmail(ctx, w.owner.ID, fresh, old); err != nil {
		t.Fatalf("change back: %v", err)
	}
}
