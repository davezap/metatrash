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
	if strings.Count(body, `action="/account/sessions/remove"`) != 2 || !strings.Contains(body, `action="/account/sessions/end"`) {
		t.Fatal("other sessions not listed")
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
	if !strings.Contains(body, "Only this device is signed in.") || strings.Contains(body, `action="/account/sessions/end"`) || strings.Contains(body, `action="/account/sessions/remove"`) {
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

	page := func() string {
		res := oauthCall(w.h, "GET", "/account/security", nil, here)
		if res.Code != 200 {
			t.Fatalf("security page: %d", res.Code)
		}
		return html.UnescapeString(res.Body.String())
	}
	body := page()
	table := body[strings.Index(body, `id="sessions"`):]
	for _, want := range []string{"Signed-in devices", "Chrome on Windows</strong> <span class=\"pill\">this device</span>", "Safari on iPhone", "198.51.100.7", "3 hours ago", "Unknown browser", "20 hours ago", "Sign out everywhere else"} {
		if !strings.Contains(table, want) {
			t.Fatalf("devices table lacks %q", want)
		}
	}
	if strings.Contains(table, "Firefox on Linux") || strings.Contains(table, "192.0.2.99") {
		t.Fatal("another account's session listed")
	}
	if strings.Index(table, "Chrome on Windows") > strings.Index(table, "Safari on iPhone") || strings.Index(table, "Safari on iPhone") > strings.Index(table, "Unknown browser") {
		t.Fatal("this device first, then the most recently used")
	}
	if strings.Count(table, `action="/account/sessions/remove"`) != 2 {
		t.Fatal("the current device must not have a sign-out button; the others must")
	}
	if !strings.Contains(table, `title="Sign out" aria-label="Sign out Safari on iPhone, signed in 3 hours ago"`) {
		t.Fatal("sign-out button label")
	}

	csrf := formCSRF(t, body, "/account/sessions/remove")
	a.mu.Lock()
	phoneID, memberID := a.sessionRowID(secretDigest(phone.Value)), a.sessionRowID(secretDigest(member.Value))
	hereID := a.sessionRowID(secretDigest(here.Value))
	a.mu.Unlock()
	if !strings.Contains(table, `name="session" value="`+phoneID+`"`) || strings.Contains(body, secretDigest(phone.Value)) {
		t.Fatal("rows must name sessions by row ID, never by digest")
	}
	// Another account's session, this browser's own and unknown IDs: not found.
	for name, id := range map[string]string{"member": memberID, "here": hereID, "unknown": strings.Repeat("0", 32), "bad": "x"} {
		res := oauthCall(w.h, "POST", "/account/sessions/remove", url.Values{"csrf": {csrf}, "session": {id}}, here)
		if res.Code != 404 || !strings.Contains(html.UnescapeString(res.Body.String()), "That device is already signed out.") {
			t.Fatalf("%s: %d", name, res.Code)
		}
	}
	// Another action's token does not work.
	if res := oauthCall(w.h, "POST", "/account/sessions/remove", url.Values{"csrf": {formCSRF(t, body, "/account/sessions/end")}, "session": {phoneID}}, here); res.Code == 303 {
		t.Fatal("accepted another form's CSRF token")
	}
	res := oauthCall(w.h, "POST", "/account/sessions/remove", url.Values{"csrf": {csrf}, "session": {phoneID}}, here)
	if res.Code != 303 || responseCookie(res, noticeCookie).Value != "session-ended" {
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
	body = page()
	if strings.Contains(body, "Safari on iPhone") || strings.Count(body, `action="/account/sessions/remove"`) != 1 {
		t.Fatal("signed-out device still listed")
	}
}
