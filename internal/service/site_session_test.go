package service

import (
	"context"
	"strings"
	"testing"
)

// The session answer for the app's space picker and account menu (0.78.0):
// which spaces are the person's own, which are readable on the web, how many
// invitations wait; and signing out from the app.
func TestSiteSessionPickerFieldsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	stranger := w.user("stranger")
	ownerSession, memberSession, strangerSession := w.session(w.owner), w.session(w.member), w.session(stranger)

	spaces := func(out map[string]any) map[string]map[string]any {
		m := map[string]map[string]any{}
		for _, s := range out["spaces"].([]any) {
			row := s.(map[string]any)
			m[row["space"].(string)] = row
		}
		return m
	}

	out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", ownerSession, ""))
	got := spaces(out)
	if got[w.ownedName]["owned"] != true || got[w.ownedName]["web"] != nil || got[w.joinName]["owned"] != nil {
		t.Fatalf("owner rows: %v", got)
	}
	if out["waiting"] != nil {
		t.Fatalf("owner session: %v", out)
	}

	// Made readable on the web: the owner and the member both see it marked.
	if err := w.db.setSpaceVisibility(ctx, w.owner.ID, w.ownedID, spaceWeb); err != nil {
		t.Fatal(err)
	}
	if got := spaces(siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", ownerSession, ""))); got[w.ownedName]["web"] != true {
		t.Fatalf("owner after web: %v", got[w.ownedName])
	}

	// An invitation waiting for the stranger is counted, and only for them.
	if _, err := w.db.inviteHuman(ctx, w.owner.ID, w.ownedID, stranger.Email); err != nil {
		t.Fatal(err)
	}
	if out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", strangerSession, "")); out["waiting"] != float64(1) {
		t.Fatalf("stranger waiting: %v", out["waiting"])
	}
	if out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", memberSession, "")); out["waiting"] != nil {
		t.Fatalf("member waiting: %v", out["waiting"])
	}

	// Signing out needs the session's token, a same-origin Origin and POST.
	memberCSRF := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", memberSession, ""))["csrf"].(string)
	if res := siteCall(w, "POST", "/api/v1/site/logout", "", memberSession, ""); res.Code != 403 {
		t.Fatalf("logout without token: %d", res.Code)
	}
	if res := siteCall(w, "POST", "/api/v1/site/logout", "", memberSession, memberCSRF, "Origin", "https://evil.example"); res.Code != 403 {
		t.Fatalf("logout from another site: %d", res.Code)
	}
	if res := siteCall(w, "GET", "/api/v1/site/logout", "", memberSession, ""); res.Code != 405 {
		t.Fatalf("GET logout: %d", res.Code)
	}
	if out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", memberSession, "")); out["signedIn"] != true {
		t.Fatalf("refused logouts signed out: %v", out)
	}
	res := siteCall(w, "POST", "/api/v1/site/logout", "", memberSession, memberCSRF)
	if res.Code != 204 || !strings.Contains(res.Header().Get("Set-Cookie"), sessionCookie+"=;") {
		t.Fatalf("logout: %d %v %s", res.Code, res.Header(), res.Body)
	}
	if out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", memberSession, "")); out["signedIn"] != false {
		t.Fatalf("still signed in after logout: %v", out)
	}
	// The owner's session is untouched.
	if out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", ownerSession, "")); out["signedIn"] != true {
		t.Fatalf("owner signed out too: %v", out)
	}
}
