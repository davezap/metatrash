package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type mcpResult struct {
	status  int
	header  map[string][]string
	result  map[string]any
	isError bool
	value   map[string]any
}

// mcpAccountCall sends one JSON-RPC request to /mcp/account.
func (w *oauthWorld) mcp(token, method string, params any, path string) mcpResult {
	w.t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params, "id": 1}
	b, _ := json.Marshal(body)
	if path == "" {
		path = "/mcp/account"
	}
	r := httptest.NewRequest("POST", "https://metatrash.com"+path, bytes.NewReader(b))
	r.RemoteAddr = "192.0.2.10:12345"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	w.h.ServeHTTP(res, r)
	out := mcpResult{status: res.Code, header: res.Header()}
	var envelope map[string]any
	if json.Unmarshal(res.Body.Bytes(), &envelope) == nil {
		if result, ok := envelope["result"].(map[string]any); ok {
			out.result = result
			out.isError = result["isError"] == true
			if content, ok := result["content"].([]any); ok && len(content) > 0 {
				_ = json.Unmarshal([]byte(content[0].(map[string]any)["text"].(string)), &out.value)
			}
		}
	}
	return out
}

func (w *oauthWorld) tool(token, name string, args map[string]any, code string) map[string]any {
	w.t.Helper()
	res := w.mcp(token, "tools/call", map[string]any{"name": name, "arguments": args}, "")
	if res.status != 200 {
		w.t.Fatalf("%s: HTTP %d", name, res.status)
	}
	if code == "" && res.isError {
		w.t.Fatalf("%s %v: unexpected error %v", name, args, res.value)
	}
	if code != "" {
		e, _ := res.value["error"].(map[string]any)
		if !res.isError || e == nil || e["code"] != code {
			w.t.Fatalf("%s %v: wanted %s, got %v", name, args, code, res.value)
		}
	}
	return res.value
}

// connect runs the full OAuth flow for user with the given consent.
func (w *oauthWorld) connect(user userAccount, choices map[string]string, resource string) string {
	w.t.Helper()
	verifier, challenge := pkcePair(w.t)
	code, res := w.authorize(user, challenge, choices, resource)
	if code == "" {
		w.t.Fatalf("authorize: %d %s", res.Code, res.Body)
	}
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {testClientID}, "code": {code}, "redirect_uri": {testRedirect}, "code_verifier": {verifier}}
	status, tokens := w.token(form)
	if status != 200 {
		w.t.Fatalf("token: %v", tokens)
	}
	return tokens["access_token"].(string)
}

func (w *oauthWorld) rest(token, method, path string, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://metatrash.com"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.11:1"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	w.h.ServeHTTP(res, r)
	return res
}

func TestOAuthProtectedMCPAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()

	// Without a token: 401 with a challenge naming the protected resource metadata.
	res := w.mcp("", "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "t", "version": "1"}}, "")
	challenge := strings.Join(res.header["Www-Authenticate"], " ")
	if res.status != 401 || !strings.Contains(challenge, `resource_metadata="https://metatrash.com/.well-known/oauth-protected-resource/mcp/account"`) || strings.Contains(challenge, "error=") {
		t.Fatalf("missing-token challenge: %d %q", res.status, challenge)
	}
	if res := w.mcp("mt_at_"+strings.Repeat("x", 43), "tools/list", map[string]any{}, ""); res.status != 401 || !strings.Contains(strings.Join(res.header["Www-Authenticate"], " "), `error="invalid_token"`) {
		t.Fatal("invalid token accepted")
	}
	// Space keys are not OAuth tokens.
	if res := w.mcp("writer", "tools/list", map[string]any{}, ""); res.status != 401 {
		t.Fatal("space key accepted on OAuth endpoint")
	}
	// Credentials in URLs are refused everywhere.
	if res := w.mcp("", "tools/list", map[string]any{}, "/mcp?key=writer"); res.status != 400 {
		t.Fatal("key query accepted on /mcp")
	}
	if res := w.mcp("", "tools/list", map[string]any{}, "/mcp/account?access_token=x"); res.status != 400 {
		t.Fatal("access_token query accepted")
	}

	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write", w.joinID: "read_write"}, "")
	init := w.mcp(token, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "t", "version": "1"}}, "")
	if init.status != 200 || !strings.Contains(init.result["instructions"].(string), "Call spaces first") {
		t.Fatalf("initialize: %d %v", init.status, init.result)
	}
	if list := w.mcp(token, "tools/list", map[string]any{}, ""); len(list.result["tools"].([]any)) != 7 {
		t.Fatalf("tools: %v", list.result)
	}
	spaces := w.tool(token, "spaces", map[string]any{}, "")["spaces"].([]any)
	access, ids := map[string]string{}, map[string]any{}
	for _, item := range spaces {
		m := item.(map[string]any)
		access[m["space"].(string)] = m["access"].(string)
		ids[m["space"].(string)] = m["id"]
	}
	if len(spaces) != 3 || access["public"] != "read_write" || access[w.ownedName] != "read_write" || access[w.joinName] != "read_write" ||
		ids["public"] != nil || ids[w.ownedName] != w.ownedID || ids[w.joinName] != w.joinID {
		t.Fatalf("spaces: %v", spaces)
	}
	w.tool(token, "spaces", map[string]any{"x": 1}, "invalid_request")

	// Owned space: read, list, write, move, history.
	readme := w.tool(token, "read", map[string]any{"space": w.ownedID, "path": "README.md"}, "")
	state := readme["state"]
	written := w.tool(token, "write", map[string]any{"space": w.ownedID, "path": "notes/a.md", "text": "hello", "ifInState": state, "createOnly": true}, "")
	w.tool(token, "move", map[string]any{"space": w.ownedID, "from": "notes/a.md", "to": "notes/b.md", "ifInState": written["newState"]}, "")
	if list := w.tool(token, "list", map[string]any{"space": w.ownedID, "prefix": "notes/"}, ""); len(list["files"].([]any)) != 1 {
		t.Fatal("owned list")
	}
	w.tool(token, "history", map[string]any{"space": w.ownedID, "id": written["file"].(map[string]any)["id"]}, "")
	// The root .metatrash.json exists; a GitHub folder can be designated and a file deleted.
	root := w.tool(token, "read", map[string]any{"space": w.ownedName, "path": ".metatrash.json"}, "")
	if !strings.Contains(root["file"].(map[string]any)["text"].(string), "purpose") {
		t.Fatalf("root config: %v", root)
	}
	designated := w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "site/.metatrash.json", "text": `{"purpose":"site","services":[{"type":"github","repo":"dave-zap/site"}]}`, "ifInState": root["state"]}, "")
	removed := w.tool(token, "delete", map[string]any{"space": w.ownedName, "path": "notes/b.md", "ifInState": designated["newState"]}, "")
	w.tool(token, "delete", map[string]any{"space": w.ownedName, "path": ".metatrash.json", "ifInState": removed["newState"]}, "protected_file")
	// Public space follows its anonymous rules; key-protected and unconnected spaces are invisible.
	w.tool(token, "list", map[string]any{"space": "public"}, "")
	w.tool(token, "list", map[string]any{"space": "private"}, "not_found")
	other := w.user("other")
	otherSpace, err := w.s.createOwnedSpace(ctx, other.ID, "Other", randomName(t, "other-"))
	if err != nil {
		t.Fatal(err)
	}
	w.tool(token, "list", map[string]any{"space": otherSpace.ID}, "not_found")
	w.tool(token, "list", map[string]any{"space": other.Username + "/" + otherSpace.Slug}, "not_found")
	if e := w.tool(token, "list", map[string]any{"space": "nobody/none"}, "not_found")["error"].(map[string]any); !strings.Contains(e["message"].(string), "Call spaces") {
		t.Fatalf("not_found does not point to spaces: %v", e)
	}

	// Space names: owner/slug and the ID reach the same space; results and
	// cursors use the owner/slug name. A bare slug is refused with a hint.
	byName := w.tool(token, "read", map[string]any{"space": w.joinName, "path": "README.md"}, "")
	if byName["space"] != w.joinName {
		t.Fatalf("read by name: %v", byName["space"])
	}
	if byID := w.tool(token, "read", map[string]any{"space": w.joinID, "path": "README.md"}, ""); byID["space"] != w.joinName || byID["state"] != byName["state"] {
		t.Fatalf("read by ID: %v", byID)
	}
	if e := w.tool(token, "read", map[string]any{"space": w.joinSlug, "path": "README.md"}, "not_found")["error"].(map[string]any); !strings.Contains(e["message"].(string), "owner/"+w.joinSlug) {
		t.Fatalf("bare slug hint: %v", e)
	}
	page := w.tool(token, "list", map[string]any{"space": w.ownedID, "limit": 1}, "")
	if page["space"] != w.ownedName || page["nextCursor"] == nil {
		t.Fatalf("list by ID: %v", page)
	}
	w.tool(token, "list", map[string]any{"space": w.ownedName, "limit": 1, "cursor": page["nextCursor"]}, "")
	w.tool(token, "list", map[string]any{"space": w.joinName + "/x"}, "not_found")

	// The anonymous endpoint still cannot reach owned spaces.
	anon := w.mcp("", "tools/call", map[string]any{"name": "list", "arguments": map[string]any{"space": w.ownedID}}, "/mcp")
	if e, _ := anon.value["error"].(map[string]any); !anon.isError || e["code"] != "forbidden" {
		t.Fatalf("anonymous owned access: %v", anon.value)
	}

	// Joined space: owner of that space sets the member's agent permission.
	joinedState := w.tool(token, "list", map[string]any{"space": w.joinID}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.joinID, "path": "from-member.md", "text": "x", "ifInState": joinedState}, "")
	if _, err := w.db.db.ExecContext(ctx, "UPDATE metatrash_memberships SET agent_permission = 'read_only' WHERE space_id = ? AND user_id = ?", w.joinID, w.owner.ID); err != nil {
		t.Fatal(err)
	}
	joinedState = w.tool(token, "list", map[string]any{"space": w.joinID}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.joinID, "path": "denied.md", "text": "x", "ifInState": joinedState}, "insufficient_scope")
	if spaces := w.tool(token, "spaces", map[string]any{}, "")["spaces"].([]any); !strings.Contains(mustJSON(spaces), `"access":"read_only","id":"`+w.joinID+`","name":"Shared plans"`) {
		t.Fatalf("reduced permission not listed: %v", spaces)
	}
	// Suspension blocks the next operation; restoration brings consent back.
	if err := w.db.manageHumanMember(ctx, w.member.ID, w.joinID, w.owner.ID, "suspend"); err != nil {
		t.Fatal(err)
	}
	w.tool(token, "list", map[string]any{"space": w.joinID}, "forbidden")
	if err := w.db.manageHumanMember(ctx, w.member.ID, w.joinID, w.owner.ID, "restore"); err != nil {
		t.Fatal(err)
	}
	w.tool(token, "list", map[string]any{"space": w.joinID}, "")

	// Read-only consent on an owned space blocks writes before quota or Git.
	readOnly := w.connect(w.member, map[string]string{w.joinID: "read_only"}, "")
	memberState := w.tool(readOnly, "list", map[string]any{"space": w.joinID}, "")["state"]
	w.tool(readOnly, "write", map[string]any{"space": w.joinID, "path": "x.md", "text": "x", "ifInState": memberState}, "insufficient_scope")
	w.tool(readOnly, "delete", map[string]any{"space": w.joinID, "path": "README.md", "ifInState": memberState}, "insufficient_scope")
	w.tool(readOnly, "list", map[string]any{"space": w.ownedID}, "not_found")

	// REST resource: separate audience, same checks.
	if res := w.rest(token, "GET", "/api/v1/account/spaces", ""); res.Code != 401 || !strings.Contains(res.Header().Get("WWW-Authenticate"), "oauth-protected-resource/api/v1/account") {
		t.Fatalf("MCP token accepted by REST resource: %d", res.Code)
	}
	restToken := w.connect(w.member, map[string]string{w.joinID: "read_only"}, "https://metatrash.com/api/v1/account")
	if res := w.rest(restToken, "GET", "/api/v1/account/spaces", ""); res.Code != 200 || !strings.Contains(res.Body.String(), w.joinID) {
		t.Fatalf("REST spaces: %d %s", res.Code, res.Body)
	}
	if res := w.rest(restToken, "GET", "/api/v1/account/spaces/"+w.joinID+"/file?path=README.md", ""); res.Code != 200 {
		t.Fatalf("REST read: %d %s", res.Code, res.Body)
	}
	if res := w.rest(restToken, "GET", "/api/v1/account/spaces/"+w.joinName+"/file?path=README.md", ""); res.Code != 200 || !strings.Contains(res.Body.String(), `"space":"`+w.joinName+`"`) {
		t.Fatalf("REST read by owner/slug: %d %s", res.Code, res.Body)
	}
	if res := w.rest(restToken, "GET", "/api/v1/account/spaces/"+w.joinSlug+"/files", ""); res.Code != 404 {
		t.Fatalf("REST bare slug: %d %s", res.Code, res.Body)
	}
	if res := w.rest(restToken, "GET", "/api/v1/account/spaces/"+w.joinID, ""); res.Code != 404 {
		t.Fatalf("REST two-segment path: %d", res.Code)
	}
	body := `{"text":"x","ifInState":"` + memberState.(string) + `"}`
	if res := w.rest(restToken, "PUT", "/api/v1/account/spaces/"+w.joinID+"/file?path=y.md", body); res.Code != 403 || !strings.Contains(res.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) {
		t.Fatalf("REST read-only write: %d %s", res.Code, res.Header())
	}
	if res := w.rest(restToken, "GET", "/api/v1/account/spaces/private/files", ""); res.Code != 404 {
		t.Fatal("REST reached a key-protected space")
	}
	if res := w.rest("", "GET", "/api/v1/account/spaces", ""); res.Code != 401 {
		t.Fatal("REST without token")
	}
	if res := w.rest(restToken, "GET", "/api/v1/spaces/public/files?key=abc", ""); res.Code != 400 {
		t.Fatal("REST key query accepted")
	}

	// Removal deletes consent and the space disappears from the connection.
	if err := w.db.manageHumanMember(ctx, w.member.ID, w.joinID, w.owner.ID, "remove"); err != nil {
		t.Fatal(err)
	}
	w.tool(token, "list", map[string]any{"space": w.joinID}, "not_found")
	if spaces := w.tool(token, "spaces", map[string]any{}, "")["spaces"].([]any); len(spaces) != 2 {
		t.Fatalf("removed space still listed: %v", spaces)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestOAuthAccountManagementAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write", w.joinID: "read_write"}, "")
	session := w.session(w.owner)
	page := oauthCall(w.h, "GET", "/account", nil, session)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, `id="connected-apps"`) || !strings.Contains(body, "https://metatrash.com/mcp/account") || !strings.Contains(body, "Owner notes — read and write") || !strings.Contains(body, "Shared plans — read and write") || !strings.Contains(body, "claude.ai · MCP") {
		t.Fatalf("connected apps: %d %s", page.Code, body)
	}
	// The space owner lowers the member's app permission from the sharing page.
	memberSession := w.session(w.member)
	sharing := oauthCall(w.h, "GET", "/account/sharing/"+w.joinID, nil, memberSession)
	if sharing.Code != 200 || !strings.Contains(sharing.Body.String(), "Their connected apps may") {
		t.Fatalf("sharing page: %d", sharing.Code)
	}
	agentCSRF := w.s.accounts.mac("membership:agent:" + memberSession.Value)
	form := url.Values{"csrf": {agentCSRF}, "space": {w.joinID}, "member": {w.owner.ID}, "permission": {"read_only"}}
	if res := oauthCall(w.h, "POST", "/account/membership/agent", form, session); res.Code != 403 {
		t.Fatalf("app permission change with another session's CSRF: %d", res.Code)
	}
	ownForm := url.Values{"csrf": {w.s.accounts.mac("membership:agent:" + session.Value)}, "space": {w.joinID}, "member": {w.owner.ID}, "permission": {"read_only"}}
	if res := oauthCall(w.h, "POST", "/account/membership/agent", ownForm, session); res.Code != 404 {
		t.Fatalf("non-owner changed app permission: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/membership/agent", url.Values{"csrf": {agentCSRF}, "space": {w.joinID}, "member": {w.owner.ID}, "permission": {"admin"}}, memberSession); res.Code != 400 {
		t.Fatalf("invalid permission accepted: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/membership/agent", form, memberSession); res.Code != 303 {
		t.Fatalf("owner app permission change: %d %s", res.Code, res.Body)
	}
	state := w.tool(token, "list", map[string]any{"space": w.joinID}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.joinID, "path": "a.md", "text": "x", "ifInState": state}, "insufficient_scope")
	if body := oauthCall(w.h, "GET", "/account", nil, session).Body.String(); !strings.Contains(body, "Shared plans — read only") {
		t.Fatal("lowered permission not shown on connected apps")
	}
	// Revoke from Your account ends the connection at once.
	apps, err := w.h.connectedApps(context.Background(), w.owner)
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps: %v %v", apps, err)
	}
	revokeCSRF := w.s.accounts.mac("apps-revoke:" + session.Value)
	if res := oauthCall(w.h, "POST", "/account/apps/revoke", url.Values{"csrf": {revokeCSRF}, "grant": {apps[0].ID}}, memberSession); res.Code != 403 {
		t.Fatalf("revoke with another session's CSRF: %d", res.Code)
	}
	otherCSRF := w.s.accounts.mac("apps-revoke:" + memberSession.Value)
	if res := oauthCall(w.h, "POST", "/account/apps/revoke", url.Values{"csrf": {otherCSRF}, "grant": {apps[0].ID}}, memberSession); res.Code != 303 || !w.valid(token) {
		t.Fatal("another account revoked the connection")
	}
	if res := oauthCall(w.h, "POST", "/account/apps/revoke", url.Values{"csrf": {revokeCSRF}, "grant": {apps[0].ID}}, session); res.Code != 303 || w.valid(token) {
		t.Fatalf("revoke: %d", res.Code)
	}
	if res := w.mcp(token, "tools/list", map[string]any{}, ""); res.status != 401 {
		t.Fatal("revoked token still works")
	}
	if body := oauthCall(w.h, "GET", "/account", nil, session).Body.String(); !strings.Contains(body, "No apps are connected.") {
		t.Fatal("revoked app still listed")
	}
}

func TestOAuthChangeSpacesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	w.tool(token, "list", map[string]any{"space": w.joinName}, "not_found")
	session, memberSession := w.session(w.owner), w.session(w.member)
	apps, err := w.h.connectedApps(ctx, w.owner)
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps: %v %v", apps, err)
	}
	grant := apps[0].ID
	if body := oauthCall(w.h, "GET", "/account", nil, session).Body.String(); !strings.Contains(body, `href="/account/apps/`+grant+`">Change spaces</a>`) {
		t.Fatal("Change spaces link missing")
	}

	// The page is the consent chooser, pre-filled with the current choices.
	page := oauthCall(w.h, "GET", "/account/apps/"+grant, nil, session)
	body := page.Body.String()
	if page.Code != 200 || !strings.Contains(body, `name="s-`+w.ownedID+`"><option value="none">Not connected</option><option value="read_only">Read only</option><option value="read_write" selected>`) ||
		!strings.Contains(body, `name="s-`+w.joinID+`"><option value="none" selected>`) || !strings.Contains(body, w.joinName) {
		t.Fatalf("change spaces page: %d %s", page.Code, body)
	}
	if res := oauthCall(w.h, "GET", "/account/apps/"+grant, nil, memberSession); res.Code != 404 {
		t.Fatalf("another account's connection page: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/account/apps/"+strings.Repeat("0", 32), nil, session); res.Code != 404 {
		t.Fatalf("unknown connection page: %d", res.Code)
	}

	csrf := w.s.accounts.mac("apps-spaces:" + session.Value)
	change := func(cookie *http.Cookie, csrf, grant string, fields map[string]string) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {csrf}, "grant": {grant}}
		for space, choice := range fields {
			form.Set("s-"+space, choice)
		}
		return oauthCall(w.h, "POST", "/account/apps/spaces", form, cookie)
	}
	// Adding a space applies to the same token at once; unlisted spaces keep their choice.
	if res := change(session, csrf, grant, map[string]string{w.joinID: "read_only"}); res.Code != 303 {
		t.Fatalf("add space: %d %s", res.Code, res.Body)
	}
	state := w.tool(token, "list", map[string]any{"space": w.joinName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.joinName, "path": "a.md", "text": "x", "ifInState": state}, "insufficient_scope")
	ownedState := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "a.md", "text": "x", "ifInState": ownedState}, "")
	if res := change(session, csrf, grant, map[string]string{w.joinID: "read_write"}); res.Code != 303 {
		t.Fatalf("raise to read and write: %d %s", res.Code, res.Body)
	}
	w.tool(token, "write", map[string]any{"space": w.joinName, "path": "a.md", "text": "x", "ifInState": state}, "")

	// Form checks: CSRF bound to the session, own connections only, known fields and values.
	if res := change(memberSession, csrf, grant, map[string]string{w.ownedID: "none"}); res.Code != 403 {
		t.Fatalf("another session's CSRF: %d", res.Code)
	}
	if res := change(memberSession, w.s.accounts.mac("apps-spaces:"+memberSession.Value), grant, map[string]string{w.joinID: "none"}); res.Code != 404 {
		t.Fatalf("another account changed the connection: %d", res.Code)
	}
	w.tool(token, "list", map[string]any{"space": w.joinName}, "")
	if res := change(session, csrf, grant, map[string]string{w.joinID: "admin"}); res.Code != 400 {
		t.Fatalf("invalid choice: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/apps/spaces", url.Values{"csrf": {csrf}, "grant": {grant}, "x": {"1"}}, session); res.Code != 400 {
		t.Fatalf("unknown field: %d", res.Code)
	}
	// A space the user cannot use is refused with the consent page's checks.
	other := w.user("other")
	otherSpace, err := w.s.createOwnedSpace(ctx, other.ID, "Other", randomName(t, "other-"))
	if err != nil {
		t.Fatal(err)
	}
	if res := change(session, csrf, grant, map[string]string{otherSpace.ID: "read_only"}); res.Code != 409 || !strings.Contains(res.Body.String(), "no longer available") {
		t.Fatalf("unowned space: %d", res.Code)
	}
	w.tool(token, "list", map[string]any{"space": other.Username + "/" + otherSpace.Slug}, "not_found")

	// Removing a space takes effect on the next operation.
	if res := change(session, csrf, grant, map[string]string{w.ownedID: "none"}); res.Code != 303 {
		t.Fatalf("remove space: %d", res.Code)
	}
	w.tool(token, "list", map[string]any{"space": w.ownedName}, "not_found")

	// A space hidden while its owner suspends the user keeps its consent.
	if err := w.db.manageHumanMember(ctx, w.member.ID, w.joinID, w.owner.ID, "suspend"); err != nil {
		t.Fatal(err)
	}
	if body := oauthCall(w.h, "GET", "/account/apps/"+grant, nil, session).Body.String(); !strings.Contains(body, "1 other connected space is suspended") || strings.Contains(body, `name="s-`+w.joinID+`"`) {
		t.Fatal("suspended space not reported as kept")
	}
	if res := change(session, csrf, grant, map[string]string{w.ownedID: "read_write"}); res.Code != 303 {
		t.Fatalf("change while suspended: %d", res.Code)
	}
	if err := w.db.manageHumanMember(ctx, w.member.ID, w.joinID, w.owner.ID, "restore"); err != nil {
		t.Fatal(err)
	}
	w.tool(token, "list", map[string]any{"space": w.joinName}, "")
	w.tool(token, "list", map[string]any{"space": w.ownedName}, "")

	// A read-only connection cannot be raised to write here: its tokens never widen.
	memberToken := w.connect(w.member, map[string]string{w.joinID: "read_only"}, "")
	memberApps, err := w.h.connectedApps(ctx, w.member)
	if err != nil || len(memberApps) != 1 {
		t.Fatalf("member apps: %v %v", memberApps, err)
	}
	memberCSRF := w.s.accounts.mac("apps-spaces:" + memberSession.Value)
	if body := oauthCall(w.h, "GET", "/account/apps/"+memberApps[0].ID, nil, memberSession).Body.String(); !strings.Contains(body, "This connection is read-only") || strings.Contains(body, `value="read_write"`) {
		t.Fatal("read-only connection offers write")
	}
	if res := change(memberSession, memberCSRF, memberApps[0].ID, map[string]string{w.joinID: "read_write"}); res.Code != 409 || !strings.Contains(res.Body.String(), "connected read-only") {
		t.Fatalf("read-only connection raised: %d", res.Code)
	}
	if res := change(memberSession, memberCSRF, memberApps[0].ID, map[string]string{w.joinID: "none"}); res.Code != 303 {
		t.Fatalf("member remove: %d", res.Code)
	}
	w.tool(memberToken, "list", map[string]any{"space": w.joinName}, "not_found")
	w.tool(memberToken, "list", map[string]any{"space": "public"}, "")
}
