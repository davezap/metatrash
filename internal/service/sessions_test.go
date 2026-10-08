package service

import (
	"context"
	"html"
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
	if n := a.endOtherSessions("u1", "here"); n != 3 {
		t.Fatalf("ended %d sessions, want 3", n)
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
