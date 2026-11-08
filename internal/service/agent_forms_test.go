package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// Agents send forms through the submit tool and the REST API, as the person
// they are connected as, where the folder's locks let them (0.95.12).
func TestAgentSubmitsForms(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 3 WHERE user_id = ?", w.member.ID); err != nil {
		t.Fatal(err)
	}
	review, err := w.s.createOwnedSpace(ctx, w.member.ID, "HR review", randomName(t, "review-"))
	if err != nil {
		t.Fatal(err)
	}
	author := w.syncTokens(w.member, map[string]string{w.joinID: "read_write", review.ID: "read_write"})
	agent := w.connect(w.owner, map[string]string{w.joinID: "read_only"}, "")
	base := "/api/v1/account/spaces/" + w.joinName + "/"
	put := func(path, text string) {
		t.Helper()
		res := w.rest(author, "GET", base+"files", "")
		var list map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &list)
		head, _ := list["state"].(string)
		res = w.rest(author, "PUT", base+"file?path="+url.QueryEscape(path), mustJSON(map[string]any{"text": text, "ifInState": head}))
		if res.Code != 200 && res.Code != 201 {
			t.Fatalf("write %s: %d %s", path, res.Code, res.Body)
		}
	}
	leave := "---\nform: leave\ntitle: Leave\nsubmitTo: " + w.member.Username + "/" + review.Slug + "/leave/\n---\nName: {{text name=name required}}\n"
	put("forms/leave.md", leave)

	// The submit tool: the copy names the agent.
	receipt := w.tool(agent, "submit", map[string]any{"space": w.joinName, "path": "forms/leave.md", "answers": map[string]any{"name": "Pat"}}, "")
	text, _ := receipt["text"].(string)
	if receipt["status"] != "submitted" || !strings.Contains(text, "submittedVia: \"") || !strings.Contains(text, "(agent)") || !strings.HasPrefix(receipt["target"].(string), w.member.Username+"/"+review.Slug+"/leave/") {
		t.Fatalf("receipt: %v", receipt)
	}
	w.tool(agent, "submit", map[string]any{"space": w.joinName, "path": "forms/leave.md", "answers": map[string]any{}}, "invalid_answers")
	w.tool(agent, "submit", map[string]any{"space": w.joinName, "path": "forms/missing.md", "answers": map[string]any{"name": "Pat"}}, "not_found")
	// The REST API takes agents too.
	restAgent := w.connect(w.owner, map[string]string{w.joinID: "read_only"}, "https://metatrash.com/api/v1/account")
	if res := w.rest(restAgent, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/leave.md", "answers": map[string]any{"name": "Pat"}})); res.Code != 201 || !strings.Contains(res.Body.String(), "(agent)") {
		t.Fatalf("REST: %d %s", res.Code, res.Body)
	}
	// A person's copy names nobody else.
	person := w.syncToken(w.owner, w.joinID, "read_only")
	if res := w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/leave.md", "answers": map[string]any{"name": "Pat"}})); res.Code != 201 || strings.Contains(res.Body.String(), "submittedVia") {
		t.Fatalf("person: %d %s", res.Code, res.Body)
	}

	// The owner locks filling in forms for connected agents.
	put(".metatrash.json", strings.Replace(folderConfigEmpty, `"takeQuizzes": "agents"`, `"takeQuizzes": "agents", "fillForms": "connectedAgents"`, 1))
	w.tool(agent, "submit", map[string]any{"space": w.joinName, "path": "forms/leave.md", "answers": map[string]any{"name": "Pat"}}, "locked_form")
	if res := w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/leave.md", "answers": map[string]any{"name": "Pat"}})); res.Code != 201 {
		t.Fatalf("person after the lock: %d %s", res.Code, res.Body)
	}

	// Quizzes are locked for agents by default.
	quiz := strings.Replace(wfhQuiz, "%s", w.member.Username+"/"+review.Slug+"/quizzes/", 1)
	put("policies/wfh.md", "# Working from home\n")
	put("policies/wfh-quiz.md", quiz)
	put("policies/.metatrash.json", `{"inherit":true,"services":[{"type":"acknowledge"}],"publishedVersion":1}`)
	w.tool(agent, "submit", map[string]any{"space": w.joinName, "path": "policies/wfh-quiz.md", "answers": map[string]any{"name": "Pat"}}, "locked_form")
}
