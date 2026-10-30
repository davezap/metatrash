package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// siteCall sends one site API request as the website's own page would:
// same-origin, with the session cookie and CSRF token when given.
func siteCall(w *oauthWorld, method, path, body string, session *http.Cookie, csrf string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://metatrash.com"+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.12:1"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if method != "GET" {
		r.Header.Set("Origin", "https://metatrash.com")
	}
	if session != nil {
		r.AddCookie(session)
	}
	if csrf != "" {
		r.Header.Set(siteCSRFHeader, csrf)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i+1] == "" {
			r.Header.Del(headers[i])
		} else {
			r.Header.Set(headers[i], headers[i+1])
		}
	}
	res := httptest.NewRecorder()
	w.h.ServeHTTP(res, r)
	return res
}

func siteJSON(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	return out
}

// The site API (0.72.0): the REST resource for the signed-in browser, with
// the person's own access, a same-origin Origin and a CSRF token on writes.
func TestSiteAPIAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	stranger := w.user("stranger")
	ownerSession, strangerSession := w.session(w.owner), w.session(stranger)
	writePrivateReadme(w)
	owned := "/api/v1/site/spaces/" + w.ownedName
	joined := "/api/v1/site/spaces/" + w.joinName

	// Session: signed out it lists only the public space, read-only.
	out := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", nil, ""))
	if out["signedIn"] != false || out["csrf"] != nil || len(out["spaces"].([]any)) != 1 {
		t.Fatalf("signed-out session: %v", out)
	}
	out = siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", ownerSession, ""))
	csrf, _ := out["csrf"].(string)
	if out["signedIn"] != true || out["account"] != w.owner.Username || csrf != w.s.accounts.mac("site-api:"+ownerSession.Value) {
		t.Fatalf("owner session: %v", out)
	}
	access := map[string]string{}
	for _, s := range out["spaces"].([]any) {
		row := s.(map[string]any)
		access[row["space"].(string)] = row["access"].(string)
	}
	if access["public"] != "read_write" || access[w.ownedName] != "read_write" || access[w.joinName] == "" {
		t.Fatalf("owner spaces: %v", access)
	}

	// Reading: the owner can, a stranger finds nothing, signed out is asked to sign in.
	if res := siteCall(w, "GET", owned+"/file?path=README.md", "", ownerSession, ""); res.Code != 200 || !strings.Contains(res.Body.String(), "# Private space") {
		t.Fatalf("owner read: %d %s", res.Code, res.Body)
	}
	if res := siteCall(w, "GET", owned+"/file?path=README.md", "", strangerSession, ""); res.Code != 404 {
		t.Fatalf("stranger read: %d", res.Code)
	}
	if res := siteCall(w, "GET", owned+"/file?path=README.md", "", nil, ""); res.Code != 401 {
		t.Fatalf("signed-out read: %d", res.Code)
	}
	if res := siteCall(w, "GET", "/api/v1/site/spaces/"+w.owner.Username+"/no-such-space/files", "", nil, ""); res.Code != 401 {
		t.Fatalf("signed-out missing: %d", res.Code)
	}

	// Writing needs the token, a same-origin Origin and no cross-site fetch.
	state := siteJSON(t, siteCall(w, "GET", owned+"/files", "", ownerSession, ""))["state"].(string)
	write := `{"text": "# From the site\n", "ifInState": "` + state + `"}`
	if res := siteCall(w, "PUT", owned+"/file?path=web.md", write, ownerSession, ""); res.Code != 403 {
		t.Fatalf("no token: %d", res.Code)
	}
	if res := siteCall(w, "PUT", owned+"/file?path=web.md", write, ownerSession, w.s.accounts.mac("site-api:"+strangerSession.Value)); res.Code != 403 {
		t.Fatalf("another session's token: %d", res.Code)
	}
	if res := siteCall(w, "PUT", owned+"/file?path=web.md", write, ownerSession, csrf, "Origin", ""); res.Code != 403 {
		t.Fatalf("no origin: %d", res.Code)
	}
	if res := siteCall(w, "PUT", owned+"/file?path=web.md", write, ownerSession, csrf, "Origin", "https://evil.example"); res.Code != 403 {
		t.Fatalf("other origin: %d", res.Code)
	}
	if res := siteCall(w, "GET", owned+"/files", "", ownerSession, "", "Sec-Fetch-Site", "cross-site"); res.Code != 403 {
		t.Fatalf("cross-site read: %d", res.Code)
	}
	// Opened from the address bar (0.72.1): reading is fine, writing is not.
	if res := siteCall(w, "GET", "/api/v1/site/session", "", ownerSession, "", "Sec-Fetch-Site", "none"); res.Code != 200 {
		t.Fatalf("address bar read: %d", res.Code)
	}
	if res := siteCall(w, "PUT", owned+"/file?path=web.md", write, ownerSession, csrf, "Sec-Fetch-Site", "none"); res.Code != 403 {
		t.Fatalf("address bar write: %d", res.Code)
	}
	if res := siteCall(w, "PUT", owned+"/file?path=web.md", write, nil, csrf); res.Code != 401 {
		t.Fatalf("signed-out write: %d", res.Code)
	}
	res := siteCall(w, "PUT", owned+"/file?path=web.md", write, ownerSession, csrf)
	if res.Code != 201 {
		t.Fatalf("owner write: %d %s", res.Code, res.Body)
	}
	id, _ := siteJSON(t, res)["file"].(map[string]any)["id"].(string)
	// The change is the person's, through the website.
	history := siteJSON(t, siteCall(w, "GET", owned+"/history?id="+id, "", ownerSession, ""))
	entries, _ := history["entries"].([]any)
	if len(entries) == 0 {
		t.Fatalf("history: %v", history)
	}
	by, _ := entries[0].(map[string]any)["by"].(map[string]any)
	if by["name"] != w.owner.Username || by["app"] != siteAppName || by["kind"] != "person" {
		t.Fatalf("author: %v", by)
	}

	// A member writes as their permission allows; a suspended one is refused.
	setPermission := func(permission, status string) {
		if _, err := w.db.db.ExecContext(ctx, "UPDATE metatrash_memberships SET agent_permission = ?, status = ? WHERE space_id = ? AND user_id = ?", permission, status, w.joinID, w.owner.ID); err != nil {
			t.Fatal(err)
		}
	}
	joinedWrite := func() int {
		state := siteJSON(t, siteCall(w, "GET", joined+"/files", "", ownerSession, ""))["state"].(string)
		return siteCall(w, "PUT", joined+"/file?path=member.md", `{"text": "x\n", "ifInState": "`+state+`"}`, ownerSession, csrf).Code
	}
	setPermission("read_only", "active")
	if code := joinedWrite(); code != 403 {
		t.Fatalf("read-only member write: %d", code)
	}
	setPermission("read_write", "active")
	if code := joinedWrite(); code != 201 && code != 200 {
		t.Fatalf("read-write member write: %d", code)
	}
	setPermission("read_write", "suspended")
	if res := siteCall(w, "GET", joined+"/files", "", ownerSession, ""); res.Code != 403 {
		t.Fatalf("suspended member read: %d", res.Code)
	}

	// Readable on the web: anyone reads, nobody but members writes.
	if _, err := w.db.db.ExecContext(ctx, "UPDATE metatrash_spaces SET visibility = 'web' WHERE space_id = ?", w.ownedID); err != nil {
		t.Fatal(err)
	}
	if res := siteCall(w, "GET", owned+"/file?path=README.md", "", nil, ""); res.Code != 200 {
		t.Fatalf("signed-out read, web: %d", res.Code)
	}
	strangerCSRF := siteJSON(t, siteCall(w, "GET", "/api/v1/site/session", "", strangerSession, ""))["csrf"].(string)
	state = siteJSON(t, siteCall(w, "GET", owned+"/files", "", strangerSession, ""))["state"].(string)
	if res := siteCall(w, "PUT", owned+"/file?path=x.md", `{"text": "x\n", "ifInState": "`+state+`"}`, strangerSession, strangerCSRF); res.Code != 403 {
		t.Fatalf("stranger write, web: %d", res.Code)
	}

	// The per-person resources work for the signed-in person.
	if res := siteCall(w, "GET", owned+"/acknowledgements", "", ownerSession, ""); res.Code != 200 {
		t.Fatalf("acknowledgements: %d %s", res.Code, res.Body)
	}
	if res := siteCall(w, "GET", owned+"/submissions", "", ownerSession, ""); res.Code != 200 {
		t.Fatalf("submissions: %d %s", res.Code, res.Body)
	}

	// Routes the site does not offer, and wrong methods.
	for _, path := range []string{owned + "/pull", owned + "/pending", owned + "/configure", "/api/v1/site/nothing", "/api/v1/site/spaces/" + w.ownedName} {
		if res := siteCall(w, "GET", path, "", ownerSession, ""); res.Code != 404 {
			t.Fatalf("%s: %d", path, res.Code)
		}
	}
	if res := siteCall(w, "PUT", owned+"/content?path=x.png", "x", ownerSession, csrf); res.Code != 405 {
		t.Fatalf("upload: %d", res.Code)
	}
	if res := siteCall(w, "POST", "/api/v1/site/session", "", ownerSession, csrf); res.Code != 405 {
		t.Fatalf("POST session: %d", res.Code)
	}
}
