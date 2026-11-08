package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
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

// TestSessionLimitEndsIdlest: at the limit across all accounts, signing in
// ends the session idle longest instead of refusing (0.95.11).
func TestSessionLimitEndsIdlest(t *testing.T) {
	s, _, _ := testAccounts(t)
	a := s.accounts
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := 0; i < maxSessions; i++ {
		used := now.Add(-time.Duration(i%100) * time.Minute)
		a.sessions[secretDigest(fmt.Sprint("t", i))] = accountSession{UserID: fmt.Sprintf("%032x", i), Created: now.Add(-time.Hour), LastUsed: used, Expires: now.Add(time.Hour)}
	}
	idle := secretDigest("idle")
	a.sessions[idle] = accountSession{UserID: "u-idle", Created: now.Add(-3 * time.Hour), Expires: now.Add(time.Hour)}
	delete(a.sessions, secretDigest("t1"))
	token, err := a.startSessionLocked(context.Background(), "u-new", now)
	if err != nil || token == "" {
		t.Fatalf("sign-in at the limit: %v", err)
	}
	if _, ok := a.sessions[idle]; ok {
		t.Fatal("the session idle longest was kept")
	}
	if _, ok := a.sessions[secretDigest(token)]; !ok || len(a.sessions) != maxSessions {
		t.Fatalf("new session kept=%v, sessions %d, want %d", ok, len(a.sessions), maxSessions)
	}
}

func TestSignOutEverywhereAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	here, phone, laptop := w.session(w.owner), w.session(w.owner), w.session(w.owner)
	member := w.session(w.member)

	if state := securityState(t, w, here); state["otherSessions"] != 2.0 || len(state["sessions"].([]any)) != 3 {
		t.Fatalf("other sessions not listed: %v", state)
	}
	// No confirmation needed: signing others out cannot lock anyone out.
	res := securityPost(w, "/account/sessions/end", url.Values{}, here)
	if res.Code != 200 || noticeOf(res) != "sessions-ended" {
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
	if state := securityState(t, w, here); state["otherSessions"] != 0.0 || len(state["sessions"].([]any)) != 1 {
		t.Fatalf("security after signing out everywhere else: %v", state)
	}
	// A signed-out browser cannot use it.
	res = securityPost(w, "/account/sessions/end", url.Values{}, phone)
	if res.Code != 401 {
		t.Fatalf("signed-out session: %d %s", res.Code, res.Body)
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
		token, err := a.startSessionLocked(context.Background(), u.ID, now)
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
	token, err := a.startSessionLocked(context.Background(), w.owner.ID, time.Now())
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookie, Value: token}
	if res := oauthCall(w.h, "POST", "/logout", url.Values{"csrf": {a.mac("logout:" + token)}}, cookie); res.Code != 303 {
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

func TestDeviceLabel(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36":                            "Chrome on Windows",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 Edg/129.0.0.0":              "Edge on Windows",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15":                      "Safari on Mac",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1":    "Safari on iPhone",
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/129.0 Mobile/15E148 Safari/604.1":     "Chrome on iPhone",
		"Mozilla/5.0 (iPad; CPU OS 17_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/131.0 Mobile/15E148 Safari/605.1.15":           "Firefox on iPad",
		"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36":                      "Chrome on Android",
		"Mozilla/5.0 (Linux; Android 14; SM-S921B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/26.0 Chrome/122.0.0.0 Mobile Safari/537.36": "Samsung Internet on Android",
		"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0":                                                                     "Firefox on Linux",
		"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36":                             "Chrome on ChromeOS",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36 OPR/114.0.0.0":              "Opera on Windows",
		"curl/8.5.0": "",
		"":           "",
	} {
		if got := deviceLabel(ua); got != want {
			t.Errorf("%q: got %q, want %q", ua, got, want)
		}
	}
	now := time.Now()
	for d, want := range map[time.Duration]string{30 * time.Second: "just now", 10 * time.Minute: "10 minutes ago", 90 * time.Minute: "an hour ago", 5 * time.Hour: "5 hours ago", 72 * time.Hour: "3 days ago"} {
		if got := sinceText(now.Add(-d), now); got != want {
			t.Errorf("%v: got %q, want %q", d, got, want)
		}
	}
	if clip("Chrome\x00 on\nWindows", 64) != "Chrome onWindows" || len(clip(strings.Repeat("a", 100), 45)) != 45 {
		t.Error("clip")
	}
}

// TestSignedInDevices checks the devices table on Security and signing out
// one device from it.
func TestSignedInDevices(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	a := w.s.accounts
	start := func(u userAccount, device, ip string, ago time.Duration) *http.Cookie {
		t.Helper()
		a.mu.Lock()
		defer a.mu.Unlock()
		token, err := a.startSessionLocked(withSessionOrigin(ctx, sessionOrigin{Device: device, IP: ip}), u.ID, time.Now().Add(-ago))
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: token}
	}
	here := start(w.owner, "Chrome on Windows", "203.0.113.5", 0)
	phone := start(w.owner, "Safari on iPhone", "198.51.100.7", 3*time.Hour)
	old := start(w.owner, "", "", 20*time.Hour)
	member := start(w.member, "Firefox on Linux", "192.0.2.99", time.Hour)

	devices := func() []siteDevice {
		var out struct {
			Sessions []siteDevice `json:"sessions"`
		}
		_ = json.Unmarshal([]byte(jsonOf(securityState(t, w, here))), &out)
		return out.Sessions
	}
	list := devices()
	got := []string{}
	for _, d := range list {
		got = append(got, fmt.Sprintf("%s|%s|%s|%v", d.Device, d.IP, d.SignedIn, d.Current))
	}
	// This device first, then the most recently used; never another account's.
	if len(list) != 3 || !strings.HasPrefix(got[0], "Chrome on Windows|203.0.113.5|") || !strings.HasSuffix(got[0], "|true") ||
		got[1] != "Safari on iPhone|198.51.100.7|3 hours ago|false" || !strings.HasPrefix(got[2], "Unknown browser||20 hours ago|") {
		t.Fatalf("devices: %q", got)
	}
	a.mu.Lock()
	phoneID, memberID := a.sessionRowID(secretDigest(phone.Value)), a.sessionRowID(secretDigest(member.Value))
	hereID := a.sessionRowID(secretDigest(here.Value))
	a.mu.Unlock()
	if list[1].ID != phoneID || strings.Contains(jsonOf(list), secretDigest(phone.Value)) {
		t.Fatal("rows must name sessions by row ID, never by digest")
	}
	// Another account's session, this browser's own and unknown IDs: not found.
	for name, id := range map[string]string{"member": memberID, "here": hereID, "unknown": strings.Repeat("0", 32), "bad": "x"} {
		res := securityPost(w, "/account/sessions/remove", url.Values{"session": {id}}, here)
		if res.Code != 404 || !strings.Contains(html.UnescapeString(res.Body.String()), "That device is already signed out.") {
			t.Fatalf("%s: %d", name, res.Code)
		}
	}
	// A wrong token does not work.
	if res := siteCall(w, "POST", "/api/v1/site/account/security/sessions/remove", jsonOf(map[string]string{"session": phoneID}), here, "wrong"); res.Code != 403 {
		t.Fatalf("accepted a wrong CSRF token: %d", res.Code)
	}
	res := securityPost(w, "/account/sessions/remove", url.Values{"session": {phoneID}}, here)
	if res.Code != 200 || noticeOf(res) != "session-ended" {
		t.Fatalf("sign out one device: %d %s", res.Code, res.Body)
	}
	b := &accounts{store: w.db, challenges: map[string]loginChallenge{}, sessions: map[string]accountSession{}, secret: []byte("restarted")}
	if err := b.loadStoredSessions(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		cookie *http.Cookie
		want   bool
	}{"phone": {phone, false}, "here": {here, true}, "old": {old, true}, "member": {member, true}} {
		if _, ok, _ := b.currentUser(ctx, c.cookie.Value); ok != c.want {
			t.Fatalf("%s signed in=%v after restart, want %v", name, ok, c.want)
		}
	}
	// Device and address are stored and come back after a restart.
	b.mu.Lock()
	s := b.sessions[secretDigest(here.Value)]
	b.mu.Unlock()
	if s.Device != "Chrome on Windows" || s.IP != "203.0.113.5" {
		t.Fatalf("restored session: %+v", s)
	}
	if list := devices(); len(list) != 2 || strings.Contains(jsonOf(list), "Safari on iPhone") {
		t.Fatal("signed-out device still listed")
	}
}
