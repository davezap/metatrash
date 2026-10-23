package service

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// syncToken signs user in through Metatrash Sync with one space chosen.
func (w *oauthWorld) syncToken(user userAccount, spaceID, permission string) string {
	w.t.Helper()
	return w.syncTokens(user, map[string]string{spaceID: permission})
}

// syncTokens signs user in through Metatrash Sync with these spaces chosen.
func (w *oauthWorld) syncTokens(user userAccount, spaces map[string]string) string {
	w.t.Helper()
	const syncID, redirect = "https://metatrash.com/oauth/clients/sync", "http://127.0.0.1:49152/callback"
	verifier, challenge := pkcePair(w.t)
	res := oauthCall(w.h, "GET", authorizeURL(map[string]string{"client_id": syncID, "redirect_uri": redirect, "code_challenge": challenge, "resource": "https://metatrash.com/api/v1/account"}), nil)
	browser := responseCookie(res, oauthCookie)
	session := w.session(user)
	page := oauthCall(w.h, "GET", "/oauth/consent", nil, browser, session)
	match := csrfPattern.FindStringSubmatch(page.Body.String())
	if page.Code != 200 || match == nil {
		w.t.Fatalf("consent page: %d %s", page.Code, page.Body)
	}
	form := url.Values{"csrf": {match[1]}, "decision": {"approve"}}
	for id, permission := range spaces {
		form.Set("s-"+id, permission)
	}
	res = oauthCall(w.h, "POST", "/oauth/consent", form, browser, session)
	loc, _ := url.Parse(res.Header().Get("Location"))
	if res.Code != 303 || loc == nil {
		w.t.Fatalf("approval: %d %s", res.Code, res.Header().Get("Location"))
	}
	status, tokens := w.token(url.Values{"grant_type": {"authorization_code"}, "client_id": {syncID}, "code": {loc.Query().Get("code")}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "resource": {"https://metatrash.com/api/v1/account"}})
	if status != 200 {
		w.t.Fatalf("token: %v", tokens)
	}
	return tokens["access_token"].(string)
}

func TestAcknowledgementsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	// The member owns the shared space and publishes; the owner of the other
	// space is a member here and reads.
	publisher := w.syncToken(w.member, w.joinID, "read_write")
	reader := w.syncToken(w.owner, w.joinID, "read_write")
	agent := w.connect(w.member, map[string]string{w.joinID: "read_write"}, "https://metatrash.com/api/v1/account")
	base := "/api/v1/account/spaces/" + w.joinName + "/"
	call := func(token, method, resource, body string, want int) map[string]any {
		t.Helper()
		res := w.rest(token, method, base+resource, body)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, resource, res.Code, res.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out
	}
	head := func() string {
		return call(publisher, "GET", "files", "", 200)["state"].(string)
	}
	put := func(path, text string) {
		t.Helper()
		res := w.rest(publisher, "PUT", base+"file?path="+url.QueryEscape(path), mustJSON(map[string]any{"text": text, "ifInState": head()}))
		if res.Code != 200 && res.Code != 201 {
			t.Fatalf("write %s: %d %s", path, res.Code, res.Body)
		}
	}
	put("policies/leave.md", "Leave v1\n")
	put("policies/travel.md", "Travel v1\n")

	// Nothing published yet.
	if got := call(reader, "GET", "acknowledgements", "", 200); len(got["folders"].([]any)) != 0 || got["outstanding"].(float64) != 0 {
		t.Fatalf("before publishing: %v", got)
	}
	put("policies/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":1}`)
	got := call(reader, "GET", "acknowledgements", "", 200)
	if got["outstanding"].(float64) != 2 {
		t.Fatalf("v1: %v", got)
	}
	// Agents cannot confirm, nor confirm an old version or an untracked file.
	call(agent, "POST", "acknowledgements", `{"path":"policies/leave.md","version":1}`, 403)
	call(reader, "POST", "acknowledgements", `{"path":"policies/leave.md","version":2}`, 409)
	call(reader, "POST", "acknowledgements", `{"path":"policies/AGENTS.md","version":1}`, 404)
	call(reader, "POST", "acknowledgements", `{"path":"policies/leave.md"}`, 400)
	call(reader, "POST", "acknowledgements", `{"path":"policies/leave.md","version":1}`, 200)
	call(reader, "POST", "acknowledgements", `{"path":"policies/leave.md","version":1}`, 200)
	call(reader, "POST", "acknowledgements", `{"path":"policies/travel.md","version":1}`, 200)
	if got := call(reader, "GET", "acknowledgements", "", 200); got["outstanding"].(float64) != 0 {
		t.Fatalf("after reading: %v", got)
	}
	// A draft changes nothing; version 2 asks again for leave only.
	put("policies/leave.md", "Leave v2\n")
	if got := call(reader, "GET", "acknowledgements", "", 200); got["outstanding"].(float64) != 0 {
		t.Fatalf("draft: %v", got)
	}
	put("policies/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":2}`)
	got = call(reader, "GET", "acknowledgements", "", 200)
	files := got["folders"].([]any)[0].(map[string]any)["files"].([]any)
	outstanding := map[string]bool{}
	for _, f := range files {
		f := f.(map[string]any)
		outstanding[f["path"].(string)] = f["outstanding"].(bool)
	}
	if got["outstanding"].(float64) != 1 || !outstanding["policies/leave.md"] || outstanding["policies/travel.md"] {
		t.Fatalf("v2: %v", got)
	}

	// The report needs read-write access; reading and confirming do not.
	call(reader, "GET", "acknowledgements?report=true", "", 200)
	if _, err := w.db.db.Exec("UPDATE metatrash_memberships SET agent_permission = 'read_only' WHERE space_id = ? AND user_id = ?", w.joinID, w.owner.ID); err != nil {
		t.Fatal(err)
	}
	call(reader, "GET", "acknowledgements?report=true", "", 403)
	call(reader, "GET", "acknowledgements", "", 200)
	report := call(publisher, "GET", "acknowledgements?report=true", "", 200)
	people := report["people"].([]any)
	if len(report["files"].([]any)) != 2 || len(people) != 2 {
		t.Fatalf("report: %v", report)
	}
	byName := map[string]map[string]any{}
	for _, p := range people {
		p := p.(map[string]any)
		byName[p["name"].(string)] = p
	}
	if byName[w.member.Username]["outstanding"].(float64) != 2 || byName[w.owner.Username]["outstanding"].(float64) != 1 {
		t.Fatalf("report people: %v", people)
	}
	statuses := []string{}
	for _, s := range byName[w.owner.Username]["files"].([]any) {
		statuses = append(statuses, s.(map[string]any)["status"].(string))
	}
	if strings.Join(statuses, ",") != "older,read" {
		t.Fatalf("owner statuses: %v", statuses)
	}
	call(reader, "GET", "acknowledgements?report=yes", "", 400)
	call(reader, "PUT", "acknowledgements", "", 405)
}
