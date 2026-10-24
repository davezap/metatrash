package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

const wfhQuiz = `---
form: wfh-quiz
title: Working from home quiz
submitTo: %s
quiz: true
covers: wfh.md
passMark: 100
retryAfter: 1h
unmarked: name
---
Name: {{text name=name required}}

Where may you work? {{choice name=where required}}
- ( ) Home
- ( ) Cafe

Laptop locked? {{yesno name=locked required}}

Kit: {{choice name=kit multi}}
- [ ] Laptop
- [ ] Headset

Why lock it? {{text name=why required}}
`

func TestQuizKeys(t *testing.T) {
	quiz := strings.Replace(wfhQuiz, "%s", "dave/hr-review/quizzes/", 1)
	tpl, err := parseFormTemplate(quiz)
	if err != nil {
		t.Fatal(err)
	}
	if !tpl.Quiz || mustJSON(tpl.Covers) != `["wfh.md"]` || !tpl.ReadFirst || tpl.PassMark != 100 || tpl.RetryAfter != time.Hour || mustJSON(tpl.coverPatterns("policies/wfh-quiz.md")) != `["policies/wfh.md"]` {
		t.Fatalf("quiz: %+v", tpl)
	}
	for _, bad := range []string{
		"quiz: yes", "quiz: true\ncovers: ../other.md", "quiz: true\ncovers: notes.txt", "quiz: true\ncovers: a.md, sub/../b.md",
		"quiz: true\ncovers: ,", "quiz: true\ncovers: **/a.md", "quiz: true\nreadFirst: maybe",
		"quiz: true\ncovers: a.md\npassMark: 0", "quiz: true\ncovers: a.md\nretryAfter: 1w", "covers: a.md", "readFirst: false",
	} {
		text := "---\nform: q\nsubmitTo: dave/r/\n" + bad + "\n---\n{{yesno name=a}}\n"
		if _, err := parseFormTemplate(text); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if tpl, err := parseFormTemplate("---\nform: q\nsubmitTo: dave/r/\nquiz: true\ncovers: sub/a.md\npassMark: 80%\nretryAfter: 0\nreadFirst: false\n---\n{{yesno name=a}}\n"); err != nil || tpl.PassMark != 80 || tpl.RetryAfter != 0 || tpl.ReadFirst {
		t.Fatalf("passMark 80%%: %+v %v", tpl, err)
	}

	// covers: files, patterns and folders, from the quiz's folder or the top.
	many, err := parseFormTemplate("---\nform: q\nsubmitTo: dave/r/\nquiz: true\ncovers: wfh.md, TG-ISG-*.md, firmware/, /hr/leave.md\n---\n{{yesno name=a}}\n")
	if err != nil {
		t.Fatal(err)
	}
	patterns := many.coverPatterns("policies/isg/quiz.md")
	if mustJSON(patterns) != `["policies/isg/wfh.md","policies/isg/TG-ISG-*.md","policies/isg/firmware/","hr/leave.md"]` {
		t.Fatalf("patterns: %v", patterns)
	}
	for p, want := range map[string]bool{
		"policies/isg/wfh.md": true, "policies/isg/TG-ISG-010-Backup.md": true, "policies/isg/firmware/a/b.md": true, "hr/leave.md": true,
		"policies/isg/sub/TG-ISG-1.md": false, "policies/wfh.md": false, "hr/other.md": false,
	} {
		if covered(patterns, p) != want {
			t.Errorf("covered(%s) = %v", p, !want)
		}
	}
	whole, _ := parseFormTemplate("---\nform: q\nsubmitTo: dave/r/\nquiz: true\n---\n{{yesno name=a}}\n")
	if mustJSON(whole.coverPatterns("policies/q.md")) != `["policies/*.md"]` || !covered(whole.coverPatterns("q.md"), "a.md") {
		t.Fatalf("default covers: %v", whole.Covers)
	}
	if all, _ := parseFormTemplate("---\nform: q\nsubmitTo: dave/r/\nquiz: true\ncovers: /\n---\n{{yesno name=a}}\n"); !covered(all.coverPatterns("x/q.md"), "any/where.md") {
		t.Fatalf("covers /: %v", all.Covers)
	}

	// Marking: the key's blank answers are not marked; free text waits.
	key := map[string]any{"name": "Dave", "where": "Home", "locked": "Yes", "kit": []string{"Laptop", "Headset"}, "why": "Theft"}
	answers := formAnswers{"name": "Pat", "where": "Cafe", "locked": "Yes", "kit": []string{"Headset", "Laptop"}, "why": "So nobody reads it"}
	marks := tpl.mark(answers, key)
	if _, named := marks["name"]; len(marks) != 4 || named || *marks["where"] || !*marks["locked"] || !*marks["kit"] || marks["why"] != nil {
		t.Fatalf("marks: %v", mustJSON(marks))
	}
	if status, score := tpl.quizStatus(marks); status != "submitted" || score != nil {
		t.Fatalf("waiting: %s %v", status, score)
	}
	right := true
	marks["why"] = &right
	if status, score := tpl.quizStatus(marks); status != "declined" || *score != 75 {
		t.Fatalf("fail: %s %v", status, *score)
	}

	// The marked copy shows ticks and crosses, and reads back.
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	status, score := tpl.quizStatus(marks)
	text := tpl.render(answers, submission{Template: "dave/team/policies/wfh-quiz.md", Revision: strings.Repeat("a", 40), Blob: strings.Repeat("b", 40), ID: strings.Repeat("c", 32), By: "pat", At: at, Status: status, Marks: marks, Score: score})
	for _, want := range []string{"status: declined\nquiz: true\npassMark: 100\nscore: 75\nmarks:\n  where: false\n  locked: true\n  kit: true\n  why: true\nanswers:\n", "Where may you work? ✗\n", "Laptop locked? **Yes** ✓\n", "Why lock it? **So nobody reads it** ✓\n", "Name: **Pat**\n", "submission: " + strings.Repeat("c", 32) + "\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	copied, ok := parseFiledCopy(text)
	if !ok || copied.keys["templateBlob"] != strings.Repeat("b", 40) || copied.answers["where"] != "Cafe" || mustJSON(copied.answers["kit"]) != `["Headset","Laptop"]` || *copied.marks["locked"] != true || *copied.marks["where"] {
		t.Fatalf("read back: %+v", copied)
	}
}

func TestQuizzesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	w.s.clock = func() time.Time { return clock }
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 3 WHERE user_id = ?", w.member.ID); err != nil {
		t.Fatal(err)
	}
	review, err := w.s.createOwnedSpace(ctx, w.member.ID, "HR review", randomName(t, "review-"))
	if err != nil {
		t.Fatal(err)
	}
	// The member owns the shared space and the review space and reviews;
	// the other account is a read-only member who takes the quiz.
	author := w.syncTokens(w.member, map[string]string{w.joinID: "read_write", review.ID: "read_write"})
	reviewer := author
	person := w.syncToken(w.owner, w.joinID, "read_only")
	base := "/api/v1/account/spaces/" + w.joinName + "/"
	reviewBase := "/api/v1/account/spaces/" + w.member.Username + "/" + review.Slug + "/"
	call := func(token, method, path, body string, want int) map[string]any {
		t.Helper()
		res := w.rest(token, method, path, body)
		if res.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, res.Code, res.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out
	}
	put := func(token, space, path, text string) {
		t.Helper()
		head := call(token, "GET", space+"files", "", 200)["state"].(string)
		res := w.rest(token, "PUT", space+"file?path="+url.QueryEscape(path), mustJSON(map[string]any{"text": text, "ifInState": head}))
		if res.Code != 200 && res.Code != 201 {
			t.Fatalf("write %s: %d %s", path, res.Code, res.Body)
		}
	}
	code := func(out map[string]any) string { return out["error"].(map[string]any)["code"].(string) }
	quiz := strings.Replace(wfhQuiz, "%s", w.member.Username+"/"+review.Slug+"/quizzes/", 1)
	put(author, base, "policies/wfh.md", "# Working from home\n")
	put(author, base, "policies/wfh-quiz.md", quiz)
	put(author, base, "policies/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":1}`)
	send := func(token string, answers map[string]any, want int) map[string]any {
		t.Helper()
		return call(token, "POST", base+"submissions", mustJSON(map[string]any{"path": "policies/wfh-quiz.md", "answers": answers, "version": 1}), want)
	}
	right := map[string]any{"name": "Dave", "where": "Home", "locked": "Yes", "kit": []string{"Laptop"}, "why": "Theft"}
	wrong := map[string]any{"name": "Pat", "where": "Cafe", "locked": "Yes", "kit": []string{"Laptop"}, "why": "No idea"}
	// policy is the person's state: the policy, the quiz (its own state)
	// and how many things are still to do.
	policy := func() (map[string]any, map[string]any, float64) {
		t.Helper()
		got := call(person, "GET", base+"acknowledgements", "", 200)
		var file, quizFile map[string]any
		for _, f := range got["folders"].([]any)[0].(map[string]any)["files"].([]any) {
			switch f.(map[string]any)["path"] {
			case "policies/wfh.md":
				file = f.(map[string]any)
			case "policies/wfh-quiz.md":
				quizFile = f.(map[string]any)
			}
		}
		if file == nil || quizFile == nil || quizFile["tracked"] != true || mustJSON(quizFile["covers"]) != `["policies/wfh.md"]` || mustJSON(file["quizzes"]) != `["policies/wfh-quiz.md"]` {
			t.Fatalf("policy and quiz: %v", mustJSON(got))
		}
		return file, quizFile["quiz"].(map[string]any), got["outstanding"].(float64)
	}
	ack := func(token string, version int) {
		t.Helper()
		call(token, "POST", base+"acknowledgements", fmt.Sprintf(`{"path":"policies/wfh.md","version":%d}`, version), 200)
	}
	if file, q, outstanding := policy(); outstanding != 2 || file["outstanding"] != true || q["status"] != "new" || q["readFirst"] != true || mustJSON(q["toRead"]) != `["policies/wfh.md"]` {
		t.Fatalf("before: %v %v %v", file, q, outstanding)
	}
	if got := code(call(person, "POST", base+"acknowledgements", `{"path":"policies/wfh-quiz.md","version":1}`, 409)); got != "quiz_required" {
		t.Fatal(got)
	}

	// Read first (readFirst is the default), then the quiz; reading is
	// confirmed on its own.
	if got := code(send(person, right, 409)); got != "read_first" {
		t.Fatal(got)
	}
	ack(person, 1)
	if file, q, outstanding := policy(); outstanding != 1 || file["outstanding"] != false || q["toRead"] != nil {
		t.Fatalf("read: %v %v %v", file, q, outstanding)
	}

	// No key yet: only a reviewer may send it, to make one.
	if got := code(send(person, right, 409)); got != "no_answer_key" {
		t.Fatal(got)
	}
	ack(author, 1)
	mine := send(author, right, 201)
	if mine["status"] != "submitted" || mine["quiz"] != true || mine["marks"] != nil {
		t.Fatalf("reviewer's copy: %v", mine)
	}
	keyPath := "quizzes/" + strings.SplitN(w.joinName, "/", 2)[1] + "-wfh-quiz-answers.md"
	put(reviewer, reviewBase, keyPath, mine["text"].(string))

	// Marked against the key; free text waits for the reviewer.
	first := send(person, wrong, 201)
	marks := first["marks"].(map[string]any)
	if first["status"] != "submitted" || marks["where"] != false || marks["locked"] != true || marks["why"] != nil || first["score"] != nil || strings.Contains(first["text"].(string), "Theft") {
		t.Fatalf("first: %v", first)
	}
	if !strings.Contains(first["text"].(string), "Where may you work? ✗") {
		t.Fatalf("first copy:\n%s", first["text"])
	}
	if _, q, outstanding := policy(); outstanding != 1 || q["status"] != "waiting" {
		t.Fatalf("waiting: %v", q)
	}
	if got := code(send(person, right, 409)); got != "quiz_waiting" {
		t.Fatal(got)
	}

	// The reviewer ticks the free text; one wrong answer fails it.
	copyPath := strings.TrimPrefix(first["target"].(string), w.member.Username+"/"+review.Slug+"/")
	call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": copyPath}), 400)
	call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": copyPath, "status": "approved"}), 400)
	call(person, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": copyPath, "marks": map[string]any{"why": true}}), 404)
	reviewed := call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": copyPath, "marks": map[string]any{"why": true}, "note": "Read section 2 again."}), 200)
	if reviewed["status"] != "declined" || reviewed["score"] != float64(75) || !strings.Contains(reviewed["text"].(string), "reviewedBy: \""+w.member.Username+"\"") {
		t.Fatalf("reviewed: %v", reviewed)
	}
	receipt := call(person, "GET", base+"submissions?id="+first["id"].(string), "", 200)
	if receipt["status"] != "declined" || receipt["score"] != float64(75) || receipt["note"] != "Read section 2 again." || receipt["passMark"] != float64(100) || receipt["answers"].(map[string]any)["where"] != "Cafe" || receipt["text"] != reviewed["text"] {
		t.Fatalf("receipt: %v", receipt)
	}
	_, files, _ := w.s.ownedRepository(review.ID).snapshot(ctx, "")
	b, _ := w.s.ownedRepository(review.ID).blob(ctx, files[copyPath].Blob)
	if string(b) != reviewed["text"] || !strings.Contains(string(b), "Why lock it? **No idea** ✓") {
		t.Fatalf("filed copy:\n%s", b)
	}
	if _, q, _ := policy(); q["status"] != "failed" || q["retryAt"] != "2026-10-23T10:00:00Z" {
		t.Fatalf("failed: %v", q)
	}

	// Retry after an hour; passing is the last thing to do.
	if got := code(send(person, right, 409)); got != "retry_later" {
		t.Fatal(got)
	}
	clock = clock.Add(61 * time.Minute)
	second := send(person, right, 201)
	copyPath = strings.TrimPrefix(second["target"].(string), w.member.Username+"/"+review.Slug+"/")
	if passed := call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": copyPath, "marks": map[string]any{"why": true}}), 200); passed["status"] != "approved" || passed["score"] != float64(100) {
		t.Fatalf("passed: %v", passed)
	}
	if _, q, outstanding := policy(); outstanding != 0 || q["status"] != "passed" {
		t.Fatalf("passed: %v %v", q, outstanding)
	}
	if got := code(send(person, right, 409)); got != "quiz_passed" {
		t.Fatal(got)
	}
	report := call(author, "GET", base+"acknowledgements?report=true", "", 200)
	if !strings.Contains(mustJSON(report["people"]), `"name":"`+w.owner.Username+`","outstanding":0`) {
		t.Fatalf("report: %v", mustJSON(report))
	}

	// A new version of the policy asks to read it again, not for the quiz;
	// a changed quiz asks for the quiz again, and needs a new key.
	put(author, base, "policies/wfh.md", "# Working from home\n\nNew rules.\n")
	put(author, base, "policies/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":2}`)
	if file, q, outstanding := policy(); outstanding != 1 || file["outstanding"] != true || q["status"] != "passed" {
		t.Fatalf("policy changed: %v %v %v", file, q, outstanding)
	}
	put(author, base, "policies/wfh-quiz.md", quiz+"\nOne more: {{yesno name=more}}\n")
	put(author, base, "policies/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":3}`)
	if _, q, outstanding := policy(); outstanding != 2 || q["status"] != "new" || q["older"] != true {
		t.Fatalf("quiz changed: %v %v", q, outstanding)
	}
	if got := code(call(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "policies/wfh-quiz.md", "answers": right, "version": 2}), 409)); got != "template_changed" {
		t.Fatal(got)
	}
	ack(person, 3)
	if got := code(call(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "policies/wfh-quiz.md", "answers": right, "version": 3}), 409)); got != "no_answer_key" {
		t.Fatal(got)
	}

	// A quiz can cover files of another published folder; readFirst: false
	// lets it be taken before they are read.
	put(author, base, "other/rules.md", "# Rules\n")
	put(author, base, "other/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":1}`)
	rulesText := strings.Replace(strings.Replace(quiz, "form: wfh-quiz", "form: rules-quiz", 1), "covers: wfh.md", "covers: /other/\nreadFirst: false", 1)
	put(author, base, "policies/rules-quiz.md", rulesText)
	put(author, base, "policies/.metatrash.json", `{"services":[{"type":"acknowledge"}],"publishedVersion":4}`)
	got := call(person, "GET", base+"acknowledgements", "", 200)
	var rulesQuiz, rules map[string]any
	for _, folder := range got["folders"].([]any) {
		for _, f := range folder.(map[string]any)["files"].([]any) {
			switch f.(map[string]any)["path"] {
			case "policies/rules-quiz.md":
				rulesQuiz = f.(map[string]any)
			case "other/rules.md":
				rules = f.(map[string]any)
			}
		}
	}
	if rulesQuiz == nil || rules == nil || mustJSON(rulesQuiz["covers"]) != `["other/rules.md"]` || mustJSON(rules["quizzes"]) != `["policies/rules-quiz.md"]` || rulesQuiz["quiz"].(map[string]any)["readFirst"] != false {
		t.Fatalf("across folders: %v", mustJSON(got))
	}
	if got := code(call(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "policies/rules-quiz.md", "answers": right, "version": 4}), 409)); got != "no_answer_key" {
		t.Fatalf("readFirst false: %s", got)
	}

	// A plain form is approved or declined by a reviewer.
	clock = clock.Add(time.Minute)
	put(author, base, "forms/leave.md", "---\nform: leave\nsubmitTo: "+w.member.Username+"/"+review.Slug+"/leave/\n---\nName: {{text name=name required}}\n")
	leave := call(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/leave.md", "answers": map[string]any{"name": "Pat"}}), 201)
	leavePath := strings.TrimPrefix(leave["target"].(string), w.member.Username+"/"+review.Slug+"/")
	call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": leavePath, "marks": map[string]any{"name": true}}), 400)
	call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": leavePath, "status": "maybe"}), 400)
	call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": keyPath, "status": "approved"}), 400)
	if approved := call(reviewer, "POST", reviewBase+"reviews", mustJSON(map[string]any{"path": leavePath, "status": "approved", "note": "Enjoy."}), 200); approved["status"] != "approved" || !strings.Contains(approved["text"].(string), "status: approved\nreviewedBy:") {
		t.Fatalf("approved: %v", approved)
	}
	if got := call(person, "GET", base+"submissions", "", 200)["submissions"].([]any)[0].(map[string]any); got["status"] != "approved" || got["note"] != "Enjoy." {
		t.Fatalf("list: %v", got)
	}
}
