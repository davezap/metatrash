package service

import (
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestEndOtherSessions(t *testing.T) {
	s, _, _ := testAccounts(t)
	a := s.accounts
	now := time.Now()
	a.mu.Lock()
	for token, user := range map[string]string{"here": "u1", "phone": "u1", "laptop": "u1", "theirs": "u2"} {
		a.sessions[secretDigest(token)] = accountSession{UserID: user, Expires: now.Add(time.Hour)}
	}
	a.sessions[secretDigest("expired")] = accountSession{UserID: "u1", Expires: now.Add(-time.Minute)}
	a.mu.Unlock()
	if n := a.otherSessions("u1", "here", now); n != 2 {
		t.Fatalf("other sessions: %d, want 2 (expired ones do not count)", n)
	}
	if n, err := a.endOtherSessions("u1", "here"); n != 3 || err != nil {
		t.Fatalf("ended %d sessions (%v), want 3", n, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for token, want := range map[string]bool{"here": true, "phone": false, "laptop": false, "expired": false, "theirs": true} {
		if _, ok := a.sessions[secretDigest(token)]; ok != want {
			t.Fatalf("session %s kept=%v, want %v", token, ok, want)
		}
	}
}

func TestSignOutEverywhereAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	here, phone, laptop := w.session(w.owner), w.session(w.owner), w.session(w.owner)
	member := w.session(w.member)

	body := html.UnescapeString(oauthCall(w.h, "GET", "/account/security", nil, here).Body.String())
	if !strings.Contains(body, "You’re signed in here and in 2 other browsers.") {
		t.Fatal("other sessions not counted")
	}
	// No confirmation needed: signing others out cannot lock anyone out.
	res := oauthCall(w.h, "POST", "/account/sessions/end", url.Values{"csrf": {formCSRF(t, body, "/account/sessions/end")}}, here)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "sessions-ended" {
		t.Fatalf("sign out everywhere: %d %s", res.Code, res.Body)
	}
	for _, c := range []struct {
		name  string
		token string
		want  bool
	}{{"phone", phone.Value, false}, {"laptop", laptop.Value, false}, {"here", here.Value, true}} {
		if _, ok, _ := a.currentUser(ctx, c.token); ok != c.want {
			t.Fatalf("%s signed in=%v, want %v", c.name, ok, c.want)
		}
	}
	if _, ok, _ := a.currentUser(ctx, member.Value); !ok {
		t.Fatal("another account's session signed out")
	}
	body = html.UnescapeString(oauthCall(w.h, "GET", "/account/security", nil, here).Body.String())
	if !strings.Contains(body, "You’re signed in here, and nowhere else.") || strings.Contains(body, `action="/account/sessions/end"`) {
		t.Fatal("security page after signing out everywhere else")
	}
	// A signed-out browser cannot use the form.
	res = oauthCall(w.h, "POST", "/account/sessions/end", url.Values{"csrf": {a.signInCSRF("/account/sessions/end", phone.Value)}}, phone)
	if res.Code != 303 || !strings.HasSuffix(res.Header().Get("Location"), "/login") {
		t.Fatalf("signed-out session: %d %s", res.Code, res.Header().Get("Location"))
	}
	if _, ok, _ := a.currentUser(ctx, here.Value); !ok {
		t.Fatal("signed-out browser ended this session")
	}
}

// TestSessionsSurviveRestart starts sessions, then loads them into a fresh
// accounts value from the same database, as the service does after a restart.
func TestSessionsSurviveRestart(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	start := func(a *accounts, u userAccount, now time.Time) string {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		token, err := a.startSessionLocked(u.ID, now)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	restart := func() *accounts {
		t.Helper()
		b := &accounts{config: a.config, store: w.db, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{}, secret: []byte("another-process"), mailSlots: make(chan struct{}, 2)}
		if err := b.loadStoredSessions(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		return b
	}
	stored := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := w.db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_sessions WHERE "+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	signedIn := func(b *accounts, token string) string {
		t.Helper()
		u, ok, err := b.currentUser(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return ""
		}
		return u.ID
	}

	now := time.Now()
	here, phone, member := start(a, w.owner, now), start(a, w.owner, now), start(a, w.member, now)
	if stored("session_digest = ?", here) != 0 {
		t.Fatal("the token itself was stored")
	}
	if stored("session_digest IN (?, ?, ?)", secretDigest(here), secretDigest(phone), secretDigest(member)) != 3 {
		t.Fatal("sessions not stored")
	}

	b := restart()
	for token, want := range map[string]string{here: w.owner.ID, phone: w.owner.ID, member: w.member.ID} {
		if got := signedIn(b, token); got != want {
			t.Fatalf("after restart: signed in as %q, want %q", got, want)
		}
	}
	if fresh, _ := b.sessionFresh(here, time.Now()); !fresh {
		t.Fatal("a new sign-in lost its step-up window over a restart")
	}
	if n := b.otherSessions(w.owner.ID, here, time.Now()); n != 1 {
		t.Fatalf("other sessions after restart: %d, want 1", n)
	}

	// Signing out and signing out everywhere else stay done after a restart.
	b.endSession(member)
	if n, err := b.endOtherSessions(w.owner.ID, here); n != 1 || err != nil {
		t.Fatalf("ended %d (%v), want 1", n, err)
	}
	c := restart()
	for token, want := range map[string]string{here: w.owner.ID, phone: "", member: ""} {
		if got := signedIn(c, token); got != want {
			t.Fatalf("after sign-outs and restart: signed in as %q, want %q", got, want)
		}
	}

	// The step-up time is stored: a stale one stays stale, a confirmation
	// stays fresh.
	if !c.markConfirmed(here, w.owner.ID, time.Now().Add(-time.Hour)) {
		t.Fatal("mark confirmed")
	}
	if fresh, _ := restart().sessionFresh(here, time.Now()); fresh {
		t.Fatal("stale step-up came back fresh")
	}
	c.markConfirmed(here, w.owner.ID, time.Now())
	d := restart()
	if fresh, _ := d.sessionFresh(here, time.Now()); !fresh {
		t.Fatal("confirmation lost over a restart")
	}

	// Last use is written at most every lastUsedEvery.
	key := secretDigest(here)
	d.mu.Lock()
	s := d.sessions[key]
	s.LastUsed = time.Now().Add(-lastUsedEvery - time.Minute)
	d.sessions[key] = s
	d.mu.Unlock()
	signedIn(d, here)
	if stored("session_digest = ? AND last_used_at >= ?", key, time.Now().Add(-time.Minute).Unix()) != 1 {
		t.Fatal("last use not stored")
	}

	// Expired rows are not loaded and are deleted when the service starts.
	old := time.Now().Add(-48 * time.Hour)
	expiredKey := secretDigest("expired-" + randomName(t, "s"))
	if err := w.db.insertSession(expiredKey, accountSession{UserID: w.member.ID, Created: old, Expires: old.Add(sessionLifetime), AuthAt: old, LastUsed: old}); err != nil {
		t.Fatal(err)
	}
	e := restart()
	if _, ok := e.sessions[expiredKey]; ok || stored("session_digest = ?", expiredKey) != 0 {
		t.Fatal("expired session loaded or kept")
	}

	// Signing in a ninth time ends the oldest session, in the database too.
	extra := w.user("many")
	tokens := []string{}
	for i := 0; i < maxUserSessions+1; i++ {
		tokens = append(tokens, start(e, extra, time.Now().Add(time.Duration(i)*time.Second)))
	}
	if n := stored("user_id = ?", extra.ID); n != maxUserSessions {
		t.Fatalf("stored sessions for one account: %d, want %d", n, maxUserSessions)
	}
	f := restart()
	if signedIn(f, tokens[0]) != "" || signedIn(f, tokens[maxUserSessions]) != extra.ID {
		t.Fatal("the oldest session should end, the newest stay")
	}
}

// TestSignOutSurvivesRestart signs out through the web and checks the
// session does not come back.
func TestSignOutSurvivesRestart(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	a.mu.Lock()
	token, err := a.startSessionLocked(w.owner.ID, time.Now())
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	body := oauthCall(w.h, "GET", "/account", nil, cookie).Body.String()
	m := regexp.MustCompile(`action="/logout">\s*<input type="hidden" name="csrf" value="([0-9a-f]{64})"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no sign-out form")
	}
	if res := oauthCall(w.h, "POST", "/logout", url.Values{"csrf": {m[1]}}, cookie); res.Code != 303 {
		t.Fatalf("sign out: %d", res.Code)
	}
	b := &accounts{store: w.db, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{}}
	if err := b.loadStoredSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := b.currentUser(ctx, token); ok {
		t.Fatal("signed-out session came back after a restart")
	}
}
