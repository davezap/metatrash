package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freshSession is a session that signed in just now, so the steps that need
// a recent confirmation are allowed.
func (w *oauthWorld) freshSession(user userAccount) *http.Cookie {
	c := w.session(user)
	w.s.accounts.mu.Lock()
	key := secretDigest(c.Value)
	s := w.s.accounts.sessions[key]
	s.AuthAt = time.Now()
	w.s.accounts.sessions[key] = s
	w.s.accounts.mu.Unlock()
	return c
}

func siteToken(t *testing.T, w *oauthWorld, session *http.Cookie) string {
	t.Helper()
	return siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", session, ""))["csrf"].(string)
}

func wantCode(t *testing.T, res interface {
	Result() *http.Response
}, status int, code string) {
	t.Helper()
	r := res.Result()
	if r.StatusCode != status {
		t.Fatalf("status %d, want %d", r.StatusCode, status)
	}
	if code != "" {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"code":"`+code+`"`) {
			t.Fatalf("want %s: %s", code, b)
		}
	}
}

// The Settings dialog's account routes (0.79.0): the account overview,
// Profile, Spaces and sharing, and the steps that need Confirm it's you.
func TestSiteAccountAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	stranger := w.user("stranger")
	owner, ownerToken := w.session(w.owner), ""
	ownerToken = siteToken(t, w, owner)
	strangerSession := w.session(stranger)
	strangerToken := siteToken(t, w, strangerSession)
	call := func(session *http.Cookie, token, method, path, body string) map[string]any {
		t.Helper()
		res := siteCall(w, method, "/api/v1/site/account"+path, body, session, token)
		if res.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body)
		}
		return siteJSON(t, res)
	}

	// Signed out: nothing. Signed in but no token, or another origin: no writes.
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account", "", nil, ""), 401, "unauthorized")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/username", `{"username":"x"}`, owner, ""), 403, "forbidden")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/username", `{"username":"x"}`, owner, ownerToken, "Origin", "https://evil.example"), 403, "forbidden")
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account", "", owner, "", "Sec-Fetch-Site", "cross-site"), 403, "forbidden")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/username", `{"username":"x","extra":1}`, owner, ownerToken), 400, "invalid_request")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/nothing", `{}`, owner, ownerToken), 404, "")
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account/username", "", owner, ""), 405, "")

	// The overview: the owner's own space, the space they joined.
	out := call(owner, "", "GET", "", "")
	if out["email"] != w.owner.Email || out["username"] != w.owner.Username || out["fresh"] != nil {
		t.Fatalf("overview: %v", out)
	}
	if spaces := out["spaces"].([]any); len(spaces) != 1 || spaces[0].(map[string]any)["space"] != w.ownedName {
		t.Fatalf("spaces: %v", out["spaces"])
	}
	if joined := out["joined"].([]any); len(joined) != 1 || joined[0].(map[string]any)["space"] != w.joinName || joined[0].(map[string]any)["open"] != true {
		t.Fatalf("joined: %v", out["joined"])
	}

	// Profile: a new account chooses its username once.
	fresh, err := w.db.FindOrCreate(ctx, randomName(t, "newbie")+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	newbie := w.session(fresh)
	newbieToken := siteToken(t, w, newbie)
	if out := call(newbie, "", "GET", "", ""); out["canCreate"] != false || out["username"] != nil {
		t.Fatalf("newbie: %v", out)
	}
	name := randomName(t, "newbie")
	call(newbie, newbieToken, "POST", "/username", `{"username":"`+name+`"}`)
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/username", `{"username":"`+name+`x"}`, newbie, newbieToken), 409, "")
	if out := call(newbie, "", "GET", "", ""); out["username"] != name || out["canCreate"] != true {
		t.Fatalf("newbie after username: %v", out)
	}

	// Spaces: create one, with the browser's time zone; the same name again clashes.
	out = call(newbie, newbieToken, "POST", "/spaces", `{"name":"Garden Plans","timezone":"Australia/Sydney"}`)
	if out["space"] != name+"/garden-plans" {
		t.Fatalf("created: %v", out)
	}
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces", `{"name":"Garden plans"}`, newbie, newbieToken), 409, "conflict")
	created := call(newbie, "", "GET", "", "")["spaces"].([]any)[0].(map[string]any)
	if created["timezone"] != "Australia/Sydney" || created["ready"] != true {
		t.Fatalf("created space: %v", created)
	}
	gardenID := created["id"].(string)

	// Time zone, and a wrong one.
	call(newbie, newbieToken, "POST", "/spaces/"+gardenID+"/timezone", `{"timezone":"Pacific/Auckland"}`)
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+gardenID+"/timezone", `{"timezone":"Mars/Base"}`, newbie, newbieToken), 400, "invalid_request")

	// Readable on the web needs a fresh confirmation; private again doesn't.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+gardenID+"/visibility", `{"visibility":"web"}`, newbie, newbieToken), 403, "step_up_needed")
	freshNewbie := w.freshSession(fresh)
	freshToken := siteToken(t, w, freshNewbie)
	if out := call(freshNewbie, freshToken, "POST", "/spaces/"+gardenID+"/visibility", `{"visibility":"web"}`); !strings.Contains(out["notice"].(string), "readable by anyone") {
		t.Fatalf("web: %v", out)
	}
	call(newbie, newbieToken, "POST", "/spaces/"+gardenID+"/visibility", `{"visibility":"private"}`)
	// Another person's space is not theirs to change.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+w.ownedID+"/visibility", `{"visibility":"private"}`, newbie, newbieToken), 404, "")
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account/spaces/"+w.ownedID+"/sharing", "", newbie, ""), 404, "")

	// Sharing: invite the stranger, who accepts from the overview.
	out = call(owner, ownerToken, "POST", "/spaces/"+w.ownedID+"/invite", `{"email":"`+stranger.Email+`"}`)
	if !strings.Contains(out["notice"].(string), "Invitation") {
		t.Fatalf("invite: %v", out)
	}
	invitations := call(strangerSession, "", "GET", "", "")["invitations"].([]any)
	if len(invitations) != 1 {
		t.Fatalf("stranger invitations: %v", invitations)
	}
	inv := invitations[0].(map[string]any)
	sharing := call(owner, "", "GET", "/spaces/"+w.ownedID+"/sharing", "")
	if len(sharing["invitations"].([]any)) != 1 || sharing["space"] != w.ownedName {
		t.Fatalf("sharing: %v", sharing)
	}
	call(strangerSession, strangerToken, "POST", "/invitations/accept", `{"space":"`+inv["spaceId"].(string)+`","invitation":"`+inv["id"].(string)+`"}`)
	sharing = call(owner, "", "GET", "/spaces/"+w.ownedID+"/sharing", "")
	members := sharing["members"].([]any)
	if len(members) != 1 || members[0].(map[string]any)["email"] != stranger.Email || len(sharing["candidates"].([]any)) != 1 {
		t.Fatalf("members: %v", sharing)
	}
	memberID := members[0].(map[string]any)["id"].(string)
	call(owner, ownerToken, "POST", "/spaces/"+w.ownedID+"/agent", `{"member":"`+memberID+`","permission":"read_only"}`)
	call(owner, ownerToken, "POST", "/spaces/"+w.ownedID+"/suspend", `{"member":"`+memberID+`"}`)
	call(owner, ownerToken, "POST", "/spaces/"+w.ownedID+"/restore", `{"member":"`+memberID+`"}`)
	m := call(owner, "", "GET", "/spaces/"+w.ownedID+"/sharing", "")["members"].([]any)[0].(map[string]any)
	if m["status"] != "active" || m["agentPermission"] != "read_only" {
		t.Fatalf("member after changes: %v", m)
	}
	// A member can't manage the owner's sharing.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+w.ownedID+"/invite", `{"email":"a@example.com"}`, strangerSession, strangerToken), 404, "")

	// Transfer: offering needs a fresh confirmation; the member accepts it.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+w.ownedID+"/transfer-offer", `{"member":"`+memberID+`"}`, owner, ownerToken), 403, "step_up_needed")
	freshOwner := w.freshSession(w.owner)
	freshOwnerToken := siteToken(t, w, freshOwner)
	call(freshOwner, freshOwnerToken, "POST", "/spaces/"+w.ownedID+"/transfer-offer", `{"member":"`+memberID+`"}`)
	if tr := call(owner, "", "GET", "/spaces/"+w.ownedID+"/sharing", "")["transfer"].(map[string]any); tr["toEmail"] != stranger.Email {
		t.Fatalf("transfer: %v", tr)
	}
	call(owner, ownerToken, "POST", "/spaces/"+w.ownedID+"/transfer-cancel", `{}`)
	call(freshOwner, freshOwnerToken, "POST", "/spaces/"+w.ownedID+"/transfer-offer", `{"member":"`+memberID+`"}`)
	offers := call(strangerSession, "", "GET", "", "")["offers"].([]any)
	if len(offers) != 1 {
		t.Fatalf("offers: %v", offers)
	}
	out = call(strangerSession, strangerToken, "POST", "/offers/accept", `{"space":"`+w.ownedID+`"}`)
	if !strings.HasPrefix(out["space"].(string), stranger.Username+"/") {
		t.Fatalf("accepted: %v", out)
	}

	// Deleting a space: fresh confirmation, and its name typed.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+gardenID+"/delete", `{"name":"Garden Plans"}`, newbie, newbieToken), 403, "step_up_needed")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/spaces/"+gardenID+"/delete", `{"name":"wrong"}`, freshNewbie, freshToken), 400, "")
	call(freshNewbie, freshToken, "POST", "/spaces/"+gardenID+"/delete", `{"name":"Garden Plans"}`)
	if spaces := call(newbie, "", "GET", "", "")["spaces"].([]any); len(spaces) != 0 {
		t.Fatalf("after delete: %v", spaces)
	}

	// Confirm it's you: how, and a wrong code.
	confirm := call(newbie, "", "GET", "/confirm", "")
	if confirm["email"] != true || confirm["address"] != fresh.Email || confirm["fresh"] != nil || confirm["passkey"] != nil {
		t.Fatalf("confirm: %v", confirm)
	}
	if c := call(freshNewbie, "", "GET", "/confirm", ""); c["fresh"] != true || c["minutes"] == nil {
		t.Fatalf("fresh confirm: %v", c)
	}
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/confirm/verify", `{"code":"123456"}`, newbie, newbieToken), 400, "")

	// Deleting the account: fresh confirmation, the address typed; then signed out.
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/delete", `{"email":"`+fresh.Email+`"}`, newbie, newbieToken), 403, "step_up_needed")
	wantCode(t, siteCall(w, "POST", "/api/v1/site/account/delete", `{"email":"someone@example.com"}`, freshNewbie, freshToken), 400, "")
	if out := call(freshNewbie, freshToken, "POST", "/delete", `{"email":"`+strings.ToUpper(fresh.Email)+`"}`); out["signedOut"] != true {
		t.Fatalf("deleted: %v", out)
	}
	wantCode(t, siteCall(w, "GET", "/api/v1/site/account", "", newbie, ""), 401, "")
}

// The account pages that moved into the Settings dialog redirect there.
func TestAccountPagesRedirectToSettings(t *testing.T) {
	w := newOAuthWorld(t)
	session := w.session(w.owner)
	for path, want := range map[string]string{
		"/account":                      "/app/#settings/spaces",
		"/account/mine":                 "/app/#settings/spaces",
		"/account/shared":               "/app/#settings/spaces",
		"/account/profile":              "/app/#settings/profile",
		"/account/sharing/" + w.ownedID: "/app/#settings/spaces/" + w.ownedID,
		"/account/services":             "/app/#settings/services",
		"/account/apps/" + w.ownedID:    "/app/#settings/services/apps/" + w.ownedID,
		"/account/actions/" + w.ownedID: "/app/#settings/services/actions/" + w.ownedID,
		"/account/security":             "",
	} {
		res := siteCall(w, "GET", path, "", session, "")
		if got := res.Header().Get("Location"); got != want || want == "" && res.Code != 200 {
			t.Fatalf("%s: %d %q, want %q", path, res.Code, got, want)
		}
	}
}
