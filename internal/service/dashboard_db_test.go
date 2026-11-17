package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Notices, forms done on a dashboard and forms filed without review
// (0.99.0).
func TestDashboardNoticesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	w.s.clock = func() time.Time { return time.Date(2026, 11, 18, 17, 5, 0, 0, time.UTC) }
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
	put("team/lunch.md", "**Nearly time to eat.**\n")
	put("team/lock-up.md", "---\nform: lock-up\ntitle: Locking up\nsubmitTo: "+w.joinName+"/team/done/\nreview: none\n---\nLocked? {{yesno name=locked required}}\n")
	put("team/quiz-none.md", "---\nform: q\nquiz: true\nreview: none\nsubmitTo: "+w.joinName+"/team/done/\n---\nA? {{text name=a}}\n")
	notices := func(token string) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, n := range call(token, "GET", "notices?paths=team/lunch.md,team/lock-up.md,team/gone.md", "", 200)["notices"].([]any) {
			m := n.(map[string]any)
			out[m["path"].(string)] = m
		}
		return out
	}
	got := notices(person)
	if got["team/lunch.md"]["kind"] != "notice" || got["team/lunch.md"]["done"] != false || got["team/lock-up.md"]["kind"] != "form" || got["team/gone.md"]["kind"] != "missing" {
		t.Fatalf("before: %v", got)
	}
	blob := got["team/lunch.md"]["blob"].(string)

	// A person acknowledges the notice's text; an agent can't; a form is sent.
	call(agent, "POST", "notices", mustJSON(map[string]any{"path": "team/lunch.md", "blob": blob}), 403)
	call(person, "POST", "notices", mustJSON(map[string]any{"path": "team/lunch.md", "blob": strings.Repeat("0", 40)}), 409)
	call(person, "POST", "notices", mustJSON(map[string]any{"path": "team/lock-up.md", "blob": got["team/lock-up.md"]["blob"]}), 409)
	if done := call(person, "POST", "notices", mustJSON(map[string]any{"path": "team/lunch.md", "blob": blob}), 200); done["done"] != true {
		t.Fatalf("ack: %v", done)
	}
	if got = notices(person); got["team/lunch.md"]["done"] != true || got["team/lock-up.md"]["done"] != false {
		t.Fatalf("after ack: %v", got)
	}
	if got = notices(author); got["team/lunch.md"]["done"] != false {
		t.Fatalf("someone else: %v", got)
	}

	// review: none files the copy in the same space as complete.
	receipt := call(person, "POST", "submissions", mustJSON(map[string]any{"path": "team/lock-up.md", "answers": map[string]any{"locked": "Yes"}}), 201)
	if receipt["status"] != "complete" || !strings.HasPrefix(receipt["target"].(string), w.joinName+"/team/done/") {
		t.Fatalf("receipt: %v", receipt)
	}
	if !strings.Contains(receipt["text"].(string), "status: complete") {
		t.Fatalf("copy: %s", receipt["text"])
	}
	if got = notices(person); got["team/lock-up.md"]["done"] != true {
		t.Fatalf("after send: %v", got)
	}
	copyPath := strings.TrimPrefix(receipt["target"].(string), w.joinName+"/")
	refused := call(author, "POST", "reviews", mustJSON(map[string]any{"path": copyPath, "status": "approved"}), 409)
	if refused["error"].(map[string]any)["code"] != "no_review" {
		t.Fatalf("review: %v", refused)
	}
	// Nothing waits for review.
	todo := call(author, "GET", "acknowledgements", "", 200)
	if waiting, _ := todo["toReview"].([]any); len(waiting) != 0 {
		t.Fatalf("to review: %v", todo)
	}
	// A quiz is always marked.
	if res := w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "team/quiz-none.md", "answers": map[string]any{"a": "x"}})); res.Code != 400 || !strings.Contains(res.Body.String(), "review: none") {
		t.Fatalf("quiz: %d %s", res.Code, res.Body)
	}

	// A changed notice asks again.
	put("team/lunch.md", "**Nearly time to eat!**\n")
	if got = notices(person); got["team/lunch.md"]["done"] != false {
		t.Fatalf("changed: %v", got)
	}

	// The query uses the signed-in person for @me.
	put("team/jobs/a.md", "---\nassignee: "+w.owner.Username+"\n---\n")
	put("team/jobs/b.md", "---\nassignee: someone\n---\n")
	if q := call(person, "POST", "query", `{"source": "/team/jobs/", "where": "assignee=@me"}`, 200); q["count"] != float64(1) {
		t.Fatalf("query: %v", q)
	}
	_ = ctx
}
