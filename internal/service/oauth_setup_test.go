package service

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A brand-new account connects an app and sets up its username and first
// space from the consent page, then connects with that space preselected.
func TestOAuthFirstSpaceSetupAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	user, err := w.db.FindOrCreate(ctx, randomName(t, "new-")+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := pkcePair(t)
	res := oauthCall(w.h, "GET", authorizeURL(map[string]string{"code_challenge": challenge}), nil)
	browser := responseCookie(res, oauthCookie)
	if res.Code != 200 || browser == nil {
		t.Fatalf("authorize: %d", res.Code)
	}
	if body := oauthCall(w.h, "GET", "/oauth/consent", nil, browser).Body.String(); !strings.Contains(body, "Sign in or create an account") {
		t.Fatal("sign-in page does not offer account creation")
	}
	session := w.session(user)
	body := oauthCall(w.h, "GET", "/oauth/consent", nil, browser, session).Body.String()
	if !strings.Contains(body, "Create your first space") || !strings.Contains(body, `name="username"`) || !strings.Contains(body, "/spaces/your-username/project-notes") {
		t.Fatalf("setup step missing: %s", body)
	}
	setupCSRF := w.h.setupCSRF(session.Value, w.h.oauth.pendingFor(browser.Value))
	setup := func(fields url.Values) *httptest.ResponseRecorder {
		if !fields.Has("csrf") {
			fields.Set("csrf", setupCSRF)
		}
		return oauthCall(w.h, "POST", "/oauth/setup", fields, browser, session)
	}
	if res := setup(url.Values{"csrf": {w.h.consentCSRF(session.Value, w.h.oauth.pendingFor(browser.Value))}, "username": {"abc-def"}, "name": {"x"}}); res.Code != 403 {
		t.Fatalf("consent CSRF accepted for setup: %d", res.Code)
	}
	if res := setup(url.Values{"name": {"x"}, "extra": {"1"}}); res.Code != 400 {
		t.Fatalf("unknown field: %d", res.Code)
	}
	// Username problems are reported on the username field; nothing is created.
	for _, bad := range []string{"ab", "admin", "Bad_Name"} {
		res := setup(url.Values{"username": {bad}, "name": {"Project notes"}})
		body := res.Body.String()
		if res.Code != 400 || !strings.Contains(body, `id="setup-username-error"`) || !strings.Contains(body, `value="Project notes"`) {
			t.Fatalf("username %q: %d %s", bad, res.Code, body)
		}
	}
	if spaces, _ := w.h.accountSpaces(ctx, user); len(spaces) != 0 {
		t.Fatal("space created despite a username error")
	}
	// A good username with a bad name keeps the username and reports the name.
	username := randomName(t, "new")
	res = setup(url.Values{"username": {username}, "name": {"🙂"}})
	if body := res.Body.String(); res.Code != 400 || !strings.Contains(body, `id="setup-name-error"`) || strings.Contains(body, `name="username"`) || !strings.Contains(body, "/spaces/"+username+"/project-notes") {
		t.Fatalf("name error: %d %s", res.Code, body)
	}
	// Creating the space returns to consent with it selected.
	if res := setup(url.Values{"name": {"Project notes"}}); res.Code != 303 || res.Header().Get("Location") != "/oauth/consent" {
		t.Fatalf("create: %d %s", res.Code, res.Body)
	}
	spaces, err := w.h.accountSpaces(ctx, user)
	if err != nil || len(spaces) != 1 || spaces[0].Slug != "project-notes" {
		t.Fatalf("spaces: %v %v", spaces, err)
	}
	body = oauthCall(w.h, "GET", "/oauth/consent", nil, browser, session).Body.String()
	if !strings.Contains(body, "is ready and selected below") || !strings.Contains(body, username+"/project-notes") ||
		!strings.Contains(body, `name="s-`+spaces[0].ID+`"><option value="none">Not connected</option><option value="read_only">Read only</option><option value="read_write" selected>`) || strings.Contains(body, "Create your first space") {
		t.Fatalf("consent after setup: %s", body)
	}
	match := csrfPattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatal("consent CSRF not found")
	}
	res = oauthCall(w.h, "POST", "/oauth/consent", url.Values{"csrf": {match[1]}, "decision": {"approve"}, "s-" + spaces[0].ID: {"read_write"}}, browser, session)
	loc, _ := url.Parse(res.Header().Get("Location"))
	if res.Code != 303 || loc.Query().Get("code") == "" {
		t.Fatalf("approve: %d %s", res.Code, res.Body)
	}
	status, tokens := w.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}, "code": {loc.Query().Get("code")}, "redirect_uri": {testRedirect}, "code_verifier": {verifier}})
	if status != 200 {
		t.Fatalf("token: %v", tokens)
	}
	token := tokens["access_token"].(string)
	name := username + "/project-notes"
	if !strings.Contains(mustJSON(w.tool(token, "spaces", map[string]any{}, "")), `"access":"read_write","id":"`+spaces[0].ID+`","name":"Project notes","owner":"`+username+`","space":"`+name+`"`) {
		t.Fatal("new space not connected read and write")
	}
	state := w.tool(token, "list", map[string]any{"space": name}, "")["state"]
	w.tool(token, "write", map[string]any{"space": name, "path": "hello.md", "text": "hi", "ifInState": state, "createOnly": true}, "")

	// Accounts that already own a space are not offered the setup step.
	res = oauthCall(w.h, "GET", authorizeURL(nil), nil)
	ownerBrowser := responseCookie(res, oauthCookie)
	if body := oauthCall(w.h, "GET", "/oauth/consent", nil, ownerBrowser, w.session(w.owner)).Body.String(); strings.Contains(body, "Create your first space") {
		t.Fatal("setup offered to an account that owns a space")
	}
}
