package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFormSubmissionsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	w.s.clock = func() time.Time { return time.Date(2026, 10, 23, 9, 14, 30, 0, time.UTC) }
	// The member owns the shared space and a review space the owner of the
	// other space cannot see; that owner is a member of the shared space and
	// fills in the form.
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 3 WHERE user_id = ?", w.member.ID); err != nil {
		t.Fatal(err)
	}
	review, err := w.s.createOwnedSpace(ctx, w.member.ID, "HR review", randomName(t, "review-"))
	if err != nil {
		t.Fatal(err)
	}
	author := w.syncToken(w.member, w.joinID, "read_write")
	person := w.syncToken(w.owner, w.joinID, "read_only")
	agent := w.connect(w.owner, map[string]string{w.joinID: "read_write"}, "https://metatrash.com/api/v1/account")
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
	head := func() string { return call(author, "GET", "files", "", 200)["state"].(string) }
	put := func(path, text string) {
		t.Helper()
		res := w.rest(author, "PUT", base+"file?path="+url.QueryEscape(path), mustJSON(map[string]any{"text": text, "ifInState": head()}))
		if res.Code != 200 && res.Code != 201 {
			t.Fatalf("write %s: %d %s", path, res.Code, res.Body)
		}
	}
	template := func(submitTo string) string {
		return "---\nform: leave\ntitle: Leave\nsubmitTo: " + submitTo + "\n---\n<!-- Two weeks ahead. -->\nName: {{text name=name required}}\nType: {{choice name=type required}}\n- ( ) Annual\n- ( ) Sick\n{{sign name=sig required}}\n"
	}
	put("hr/leave.md", template(w.member.Username+"/"+review.Slug+"/leave/"))
	started := head()
	send := func(token, path string, answers map[string]any, revision string, want int) map[string]any {
		t.Helper()
		body := map[string]any{"path": path, "answers": answers}
		if revision != "" {
			body["revision"] = revision
		}
		return call(token, "POST", "submissions", mustJSON(body), want)
	}
	good := map[string]any{"name": "Pat", "type": "Sick", "sig": "true"}

	// The REST listing names the account a connection signs in as.
	if res := w.rest(person, "GET", "/api/v1/account/spaces", ""); res.Code != 200 || !strings.Contains(res.Body.String(), `"account":"`+w.owner.Username+`"`) {
		t.Fatalf("account: %d %s", res.Code, res.Body)
	}

	// Agents cannot send forms; wrong answers name each field.
	send(agent, "hr/leave.md", good, "", 403)
	refused := send(person, "hr/leave.md", map[string]any{"type": "Holiday"}, "", 400)["error"].(map[string]any)
	if refused["code"] != "invalid_answers" || len(refused["fields"].([]any)) != 3 {
		t.Fatalf("refused: %v", refused)
	}
	send(person, "hr/missing.md", good, "", 404)
	put("hr/notes.md", "# Notes {{text name=a}}\n")
	send(person, "hr/notes.md", good, "", 400)

	// A read-only member sends it; the copy lands in the review space.
	first := send(person, "hr/leave.md", good, started, 201)
	slug := strings.SplitN(w.joinName, "/", 2)[1]
	wantPath := "leave/" + slug + "-" + w.owner.Username + "-leave-2026-10-23T0914Z.md"
	if first["target"] != w.member.Username+"/"+review.Slug+"/"+wantPath || first["form"] != "leave" || first["id"] == "" {
		t.Fatalf("first: %v", first)
	}
	second := send(person, "hr/leave.md", good, "", 201)
	if !strings.HasSuffix(second["target"].(string), "-2026-10-23T0914Z-2.md") {
		t.Fatalf("second: %v", second)
	}
	repo := w.s.ownedRepository(review.ID)
	state, files, err := repo.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	f, ok := files[wantPath]
	if !ok || files["leave/AGENTS.md"].ID == "" || files["leave/.metatrash.json"].ID == "" {
		t.Fatalf("review files: %v", files)
	}
	b, _ := repo.blob(ctx, f.Blob)
	if text := string(b); text != first["text"] || !strings.Contains(text, "submittedBy: \""+w.owner.Username+"\"") || !strings.Contains(text, "- [x] Sick") || strings.Contains(text, "Two weeks") {
		t.Fatalf("copy:\n%s", text)
	}
	log, err := git(ctx, repo.path, "", nil, "log", "-1", "--format=%B", state)
	if err != nil || !strings.Contains(string(log), "Form submission: leave") || !strings.Contains(string(log), "Metatrash-By: "+w.owner.Username) || !strings.Contains(string(log), "Metatrash-Kind: person") {
		t.Fatalf("commit: %s %v", log, err)
	}
	// The person still cannot reach the review space.
	if res := w.rest(person, "GET", "/api/v1/account/spaces/"+w.member.Username+"/"+review.Slug+"/files", ""); res.Code != 404 {
		t.Fatalf("review space: %d", res.Code)
	}

	// Receipts: the list, then one with its text; other people see none.
	list := call(person, "GET", "submissions", "", 200)["submissions"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["text"] != nil {
		t.Fatalf("receipts: %v", list)
	}
	one := call(person, "GET", "submissions?id="+first["id"].(string), "", 200)
	if one["text"] != first["text"] {
		t.Fatalf("receipt: %v", one)
	}
	if got := call(author, "GET", "submissions", "", 200)["submissions"].([]any); len(got) != 0 {
		t.Fatalf("author receipts: %v", got)
	}
	call(author, "GET", "submissions?id="+first["id"].(string), "", 404)
	call(person, "GET", "submissions?id=nope", "", 400)

	// A changed template refuses answers given to the old one.
	put("hr/leave.md", template(w.member.Username+"/"+review.Slug+"/leave/")+"Extra: {{text name=extra}}\n")
	if code := send(person, "hr/leave.md", good, started, 409)["error"].(map[string]any)["code"]; code != "template_changed" {
		t.Fatalf("changed: %v", code)
	}
	stale := strings.Repeat("0", 40)
	call(person, "POST", "submissions", mustJSON(map[string]any{"path": "hr/leave.md", "answers": good, "blob": stale}), 409)
	// submitTo must name a space of the same owner, outside GitHub folders.
	put("hr/leave.md", template(w.ownedName+"/inbox/"))
	send(person, "hr/leave.md", good, "", 400)
	put("hr/leave.md", template(w.member.Username+"/no-such-space/"))
	send(person, "hr/leave.md", good, "", 400)
	put("hr/leave.md", template(w.member.Username+"/"+review.Slug))
	if target := send(person, "hr/leave.md", good, "", 201)["target"].(string); target != w.member.Username+"/"+review.Slug+"/"+slug+"-"+w.owner.Username+"-leave-2026-10-23T0914Z.md" {
		t.Fatalf("root target: %s", target)
	}
}
