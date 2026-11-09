package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const guidedTemplate = `---
form: damage-report
title: Damage report
submitTo: dave/reports/
---

# Report

Reported by: {{text name=reporter default=@name}} ({{email name=contact default=@email}})

Date: {{date name=day default=@today}} at {{time name=at default=@now}}

Workshop: {{select name=workshop default="Workshop A"}}
- Workshop A
- Workshop B

Damaged? {{yesno name=damaged required default=No}}

What is damaged?
{{textarea name=details required show-if="damaged=Yes"}}
Cost: {{number name=cost min=0}}

Parts: {{choice name=parts multi default="Board|Case" show-if=damaged=Yes}}
- [ ] Board
- [ ] Case
- [ ] Screen

Manager: {{text name=manager required show-if="cost>=500"}}

- Screen serial: {{text name=serial show-if="parts=Screen"}}
- Always asked: {{text name=note}}

Phone instead: {{phone name=phone show-if=!contact}}
`

func TestGuidedFormParse(t *testing.T) {
	tpl, err := parseFormTemplate(guidedTemplate)
	if err != nil {
		t.Fatal(err)
	}
	conds := map[string]string{}
	for _, f := range tpl.Fields {
		conds[f.Name] = f.ShowIf
	}
	// cost shares the details paragraph, so its condition too.
	want := map[string]string{"details": "damaged=Yes", "cost": "damaged=Yes", "parts": "damaged=Yes", "manager": "cost>=500", "serial": "parts=Screen", "note": "", "phone": "!contact", "reporter": ""}
	for name, c := range want {
		if conds[name] != c {
			t.Errorf("%s show-if %q, want %q", name, conds[name], c)
		}
	}
	for _, bad := range []struct{ field, message string }{
		{`{{text name=a default=@who}}`, "not one of @name"},
		{`{{date name=a default=@name}}`, "can't take default @name"},
		{`{{sign name=a default=true}}`, "only the person signs"},
		{"{{choice name=a default=Maybe}}\n- ( ) Yes\n- ( ) No", "not one of its options"},
		{`{{number name=a default=lots}}`, "must be a number"},
		{`{{text name=a show-if=b}}`, "not a field of this form"},
		{"{{text name=a}}\n\n{{text name=b show-if=\"a>3\"}}", "only numbers, dates and times"},
		{"{{yesno name=a}}\n\n{{text name=b show-if=a=Maybe}}", "Yes or No"},
		{"{{text name=b show-if=a}}\n\n{{text name=a}}", "must be asked before"},
		{"{{yesno name=a}} {{text name=b show-if=a}}", "must be asked before"},
		{"{{yesno name=a}}\n\n{{text name=b show-if=a}}\n{{text name=c show-if=!a}}", "same paragraph with different show-if"},
		{"{{number name=a}}\n\n{{text name=b show-if=\"a>1|2\"}}", "only after = or !="},
		{"{{sign name=a}}\n\n{{text name=b show-if=a}}", "signature"},
	} {
		_, err := parseFormTemplate("---\nform: f\nsubmitTo: dave/r/\n---\n" + bad.field + "\n")
		if err == nil || !strings.Contains(err.Error(), bad.message) {
			t.Errorf("%q: got %v, want %q", bad.field, err, bad.message)
		}
	}
}

func TestGuidedFormAnswers(t *testing.T) {
	tpl, err := parseFormTemplate(guidedTemplate)
	if err != nil {
		t.Fatal(err)
	}
	c := formContext{Today: "2026-11-09", Now: "14:05", Name: "pat", Email: "pat@example.com"}

	// Nothing sent: defaults fill in, damaged=No hides the rest.
	answers, problems := tpl.checkAnswersFor(answersJSON(t, map[string]any{}), c)
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	for name, want := range map[string]string{"reporter": "pat", "contact": "pat@example.com", "day": "2026-11-09", "at": "14:05", "workshop": "Workshop A", "damaged": "No", "note": ""} {
		if answers[name] != want {
			t.Errorf("%s = %v, want %q", name, answers[name], want)
		}
	}
	for _, hidden := range []string{"details", "cost", "parts", "manager", "serial", "phone"} {
		if _, ok := answers[hidden]; ok {
			t.Errorf("%s answered though not asked", hidden)
		}
	}

	// A blank sent stays blank (the person cleared it); a hidden answer is dropped.
	answers, problems = tpl.checkAnswersFor(answersJSON(t, map[string]any{"reporter": "", "contact": "", "phone": "+64 21 123 4567", "manager": "ignored"}), c)
	if len(problems) != 0 || answers["reporter"] != "" || answers["phone"] != "+64 21 123 4567" {
		t.Fatalf("%v %v", answers, problems)
	}
	if _, ok := answers["manager"]; ok {
		t.Error("hidden manager kept")
	}

	// damaged=Yes asks details (required), and cost over 500 asks for a manager.
	_, problems = tpl.checkAnswersFor(answersJSON(t, map[string]any{"damaged": "Yes", "cost": "650"}), c)
	got := map[string]bool{}
	for _, p := range problems {
		got[p.Name] = true
	}
	if !got["details"] || !got["manager"] || len(problems) != 2 {
		t.Errorf("problems %v", problems)
	}
	answers, problems = tpl.checkAnswersFor(answersJSON(t, map[string]any{"damaged": "Yes", "details": "Cracked", "cost": "650", "manager": "Sam", "parts": []string{"Screen"}, "serial": "S1"}), c)
	if len(problems) != 0 || answers["manager"] != "Sam" || answers["serial"] != "S1" {
		t.Fatalf("%v %v", answers, problems)
	}

	// The default list of a multi choice, when parts is left out.
	answers, _ = tpl.checkAnswersFor(answersJSON(t, map[string]any{"damaged": "Yes", "details": "x"}), c)
	if p, _ := answers["parts"].([]string); strings.Join(p, ",") != "Board,Case" {
		t.Errorf("parts %v", answers["parts"])
	}
	if _, ok := answers["serial"]; ok {
		t.Error("serial asked without Screen")
	}

	// Rendering leaves the questions not asked out of the copy.
	answers, _ = tpl.checkAnswersFor(answersJSON(t, map[string]any{}), c)
	text := tpl.render(answers, submission{Template: "dave/team/r.md", Revision: strings.Repeat("a", 40), By: "pat", At: time.Date(2026, 11, 9, 1, 5, 0, 0, time.UTC)})
	for _, gone := range []string{"What is damaged", "Cost:", "Parts:", "Board", "Manager:", "Screen serial", "Phone instead", "  details:", "  manager:"} {
		if strings.Contains(text, gone) {
			t.Errorf("%q in copy:\n%s", gone, text)
		}
	}
	for _, kept := range []string{"Reported by: **pat** (**pat@example.com**)", "Damaged? **No**", "- Always asked: _(blank)_", "  reporter: \"pat\"\n"} {
		if !strings.Contains(text, kept) {
			t.Errorf("missing %q in copy:\n%s", kept, text)
		}
	}
	if strings.Contains(text, "\n\n\n") {
		t.Errorf("gaps where questions were:\n%s", text)
	}
}

func TestShowIfHolds(t *testing.T) {
	num := formField{Name: "n", Type: "number"}
	date := formField{Name: "d", Type: "date"}
	multi := formField{Name: "m", Type: "choice", Multi: true, Options: []string{"A", "B"}}
	for _, c := range []struct {
		cond   string
		on     formField
		answer any
		want   bool
	}{
		{"n", num, "", false},
		{"n", num, "0", true},
		{"!n", num, "", true},
		{"n=5", num, "5.0", true},
		{"n!=5", num, "4", true},
		{"n>5", num, "5", false},
		{"n>=5", num, "5", true},
		{"n<5", num, "", false},
		{"d<today", date, "2026-11-08", true},
		{"d>=2026-12-01", date, "2026-11-08", false},
		{"m=B", multi, []string{"A", "B"}, true},
		{"m!=B", multi, []string{"A"}, true},
		{"m=A|B", multi, []string{}, false},
		{"m", multi, []string{}, false},
	} {
		s, err := parseShowIf(c.cond)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.holds(c.on, c.answer, "2026-11-09"); got != c.want {
			t.Errorf("%s with %v: %v", c.cond, c.answer, got)
		}
	}
}

// Through the REST API: a left-out answer takes the sender's name, email and
// time, a question not asked is left out of the copy, and Metatrash Sync's
// listing gives the person's address (0.95.19).
func TestGuidedFormsAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	if _, err := w.db.db.Exec("UPDATE metatrash_users SET max_private_spaces = 3 WHERE user_id = ?", w.member.ID); err != nil {
		t.Fatal(err)
	}
	review, err := w.s.createOwnedSpace(ctx, w.member.ID, "Reports", randomName(t, "reports-"))
	if err != nil {
		t.Fatal(err)
	}
	author := w.syncTokens(w.member, map[string]string{w.joinID: "read_write", review.ID: "read_write"})
	base := "/api/v1/account/spaces/" + w.joinName + "/"
	res := w.rest(author, "GET", base+"files", "")
	var list map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &list)
	head, _ := list["state"].(string)
	form := strings.Replace(guidedTemplate, "dave/reports/", w.member.Username+"/"+review.Slug+"/", 1)
	if res := w.rest(author, "PUT", base+"file?path=forms/damage.md", mustJSON(map[string]any{"text": form, "ifInState": head})); res.Code != 200 && res.Code != 201 {
		t.Fatalf("write: %d %s", res.Code, res.Body)
	}
	person := w.syncToken(w.owner, w.joinID, "read_only")
	res = w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/damage.md", "answers": map[string]any{"manager": "not asked"}, "today": time.Now().UTC().Format("2006-01-02"), "now": "07:45"}))
	if res.Code != 201 {
		t.Fatalf("submit: %d %s", res.Code, res.Body)
	}
	var receipt map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &receipt)
	text, _ := receipt["text"].(string)
	for _, want := range []string{"  reporter: \"" + w.owner.Username + "\"\n", "  contact: \"" + w.owner.Email + "\"\n", "  at: \"07:45\"\n", "  damaged: \"No\"\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "manager") || strings.Contains(text, "What is damaged") {
		t.Errorf("questions not asked in the copy:\n%s", text)
	}
	// Asked once damaged is Yes, and then required.
	res = w.rest(person, "POST", base+"submissions", mustJSON(map[string]any{"path": "forms/damage.md", "answers": map[string]any{"damaged": "Yes"}}))
	if res.Code != 400 || !strings.Contains(res.Body.String(), `"details"`) {
		t.Fatalf("details: %d %s", res.Code, res.Body)
	}
	// The listing gives Metatrash Sync the person's address, and other apps none.
	if res := w.rest(person, "GET", "/api/v1/account/spaces", ""); !strings.Contains(res.Body.String(), `"email":"`+w.owner.Email+`"`) {
		t.Errorf("sync listing: %s", res.Body)
	}
	app := w.connect(w.owner, map[string]string{w.joinID: "read_only"}, "https://metatrash.com/api/v1/account")
	if res := w.rest(app, "GET", "/api/v1/account/spaces", ""); res.Code != 200 || strings.Contains(res.Body.String(), "email") {
		t.Errorf("app listing: %d %s", res.Code, res.Body)
	}
}

// A quiz question the person wasn't asked isn't marked.
func TestQuizSkipsQuestionsNotAsked(t *testing.T) {
	tpl, err := parseFormTemplate("---\nform: q\nsubmitTo: dave/r/\nquiz: true\n---\nDriver? {{yesno name=driver required}}\n\nLicence class: {{choice name=class required show-if=driver=Yes}}\n- ( ) Car\n- ( ) Truck\n")
	if err != nil {
		t.Fatal(err)
	}
	answers, problems := tpl.checkAnswers(answersJSON(t, map[string]any{"driver": "No", "class": "Truck"}), "2026-11-09")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	marks := tpl.mark(answers, map[string]any{"driver": "No", "class": "Car"})
	if _, marked := marks["class"]; marked || len(marks) != 1 || !*marks["driver"] {
		t.Fatalf("marks: %v", mustJSON(marks))
	}
}

func TestAutoFlowKey(t *testing.T) {
	if _, err := parseFormTemplate("---\nform: f\nsubmitTo: dave/r/\nautoFlow: true\n---\n# One\n{{text name=a}}\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := parseFormTemplate("---\nform: f\nsubmitTo: dave/r/\nautoFlow: yes\n---\n{{text name=a}}\n"); err == nil || !strings.Contains(err.Error(), "autoFlow is true or false") {
		t.Fatal(err)
	}
}
