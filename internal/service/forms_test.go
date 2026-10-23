package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const leaveTemplate = `---
form: leave-request
title: "Leave request"
submitTo: dave/hr-review/leave/
---

<!-- Fill this in at least two weeks ahead.
Delete nothing. -->

# Leave request

Full name: {{text name=full_name required max=80}}

Staff number: {{text name=staff pattern="[A-Z]{2}[0-9]{4}" label="Staff number"}}

First day: {{date name=start required min=today}}  Last day: {{date name=end required}}

Type of leave: {{choice name=type required}}
- ( ) Annual
- ( ) Sick
- ( ) Parental

Equipment: {{choice name=kit multi max=2}}

- [ ] Laptop
- [ ] Phone
- [ ] Headset

Office: {{select name=office}}
- Auckland
- Wellington

Days: {{number name=days min=0.5 max=30 step=0.5}} Back at {{time name=back}}

Email {{email name=email}} phone {{phone name=phone}} OK? {{yesno name=ok required}}

Notes:
{{textarea name=notes rows=4 max=2000}}

` + "`{{text name=not_a_field}}` and" + `

` + "```" + `
{{text name=also_not}}
` + "```" + `

{{sign name=signature required}} <!-- inline note -->
`

func TestParseFormTemplate(t *testing.T) {
	tpl, err := parseFormTemplate(leaveTemplate)
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Form != "leave-request" || tpl.Title != "Leave request" || tpl.SubmitTo != "dave/hr-review/leave/" {
		t.Fatalf("front matter: %+v", tpl)
	}
	var names []string
	for _, f := range tpl.Fields {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, ","); got != "full_name,staff,start,end,type,kit,office,days,back,email,phone,ok,notes,signature" {
		t.Fatalf("fields: %s", got)
	}
	kit := tpl.Fields[5]
	if !kit.Multi || strings.Join(kit.Options, "|") != "Laptop|Phone|Headset" {
		t.Fatalf("kit: %+v", kit)
	}
	if tpl.Fields[1].Label != "Staff number" || tpl.Fields[1].pattern == nil {
		t.Fatalf("staff: %+v", tpl.Fields[1])
	}

	bad := map[string]string{
		"no form":      "# Just a note {{text name=a}}\n",
		"bad name":     "---\nform: Leave Request\nsubmitTo: a/b/\n---\n{{text name=a}}\n",
		"no submitTo":  "---\nform: leave\n---\n{{text name=a}}\n",
		"no fields":    "---\nform: leave\nsubmitTo: a/b/\n---\nNothing here.\n",
		"unknown type": "---\nform: leave\nsubmitTo: a/b/\n---\n{{secret name=a}}\n",
		"no field nm":  "---\nform: leave\nsubmitTo: a/b/\n---\n{{text required}}\n",
		"dup name":     "---\nform: leave\nsubmitTo: a/b/\n---\n{{text name=a}} {{text name=a}}\n",
		"bad attr":     "---\nform: leave\nsubmitTo: a/b/\n---\n{{text name=a rows=3}}\n",
		"bad pattern":  "---\nform: leave\nsubmitTo: a/b/\n---\n{{text name=a pattern=\"(\"}}\n",
		"no options":   "---\nform: leave\nsubmitTo: a/b/\n---\n{{choice name=a}}\n\nText.\n",
		"min no multi": "---\nform: leave\nsubmitTo: a/b/\n---\n{{choice name=a min=1}}\n- x\n",
		"bad date":     "---\nform: leave\nsubmitTo: a/b/\n---\n{{date name=a min=tomorrow}}\n",
		"two choices":  "---\nform: leave\nsubmitTo: a/b/\n---\n{{choice name=a}} {{select name=b}}\n- x\n",
		"dup option":   "---\nform: leave\nsubmitTo: a/b/\n---\n{{select name=a}}\n- x\n- x\n",
	}
	for name, text := range bad {
		if _, err := parseFormTemplate(text); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if isFormTemplate("# Notes\n") || !isFormTemplate(leaveTemplate) {
		t.Fatal("isFormTemplate")
	}
}

func answersJSON(t *testing.T, v map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := map[string]json.RawMessage{}
	for k, value := range v {
		b, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		out[k] = b
	}
	return out
}

func TestCheckAnswers(t *testing.T) {
	tpl, err := parseFormTemplate(leaveTemplate)
	if err != nil {
		t.Fatal(err)
	}
	good := map[string]any{
		"full_name": "  Dave *Zap*  ", "staff": "AB1234", "start": "2026-11-03", "end": "2026-11-07",
		"type": "Annual", "kit": []string{"Headset", "Laptop"}, "office": "Wellington", "days": "4.5", "back": "09:30",
		"email": "dave@example.com", "phone": "+64 21 123 4567", "ok": "Yes", "notes": "Line one\n\n- not a list\n1. nor this", "signature": "true",
	}
	answers, problems := tpl.checkAnswers(answersJSON(t, good), "2026-10-23")
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	if answers["full_name"] != "Dave *Zap*" || strings.Join(answers["kit"].([]string), ",") != "Laptop,Headset" {
		t.Fatalf("answers: %v", answers)
	}

	cases := []struct {
		field string
		value any
	}{
		{"full_name", ""},
		{"full_name", strings.Repeat("x", 81)},
		{"full_name", "two\nlines"},
		{"staff", "ab1234"},
		{"start", "2026-10-22"},
		{"start", "2026-02-30"},
		{"type", "Holiday"},
		{"type", []string{"Annual"}},
		{"kit", []string{"Laptop", "Phone", "Headset"}},
		{"kit", []string{"Laptop", "Laptop"}},
		{"kit", "Laptop"},
		{"days", "31"},
		{"days", "1.25"},
		{"days", "lots"},
		{"back", "25:00"},
		{"email", "dave"},
		{"phone", "12"},
		{"ok", "Maybe"},
		{"ok", ""},
		{"signature", "false"},
		{"signature", ""},
		{"nonsense", "x"},
	}
	for _, c := range cases {
		v := map[string]any{}
		for k, value := range good {
			v[k] = value
		}
		v[c.field] = c.value
		_, problems := tpl.checkAnswers(answersJSON(t, v), "2026-10-23")
		if len(problems) != 1 || problems[0].Name != c.field {
			t.Errorf("%s=%v: %v", c.field, c.value, problems)
		}
	}
	// Optional answers may be left out.
	_, problems = tpl.checkAnswers(answersJSON(t, map[string]any{"full_name": "Dave", "start": "2026-11-03", "end": "2026-11-04", "type": "Sick", "ok": "No", "signature": "true"}), "2026-10-23")
	if len(problems) != 0 {
		t.Fatalf("optional: %v", problems)
	}
}

func TestRenderSubmission(t *testing.T) {
	tpl, err := parseFormTemplate(leaveTemplate)
	if err != nil {
		t.Fatal(err)
	}
	answers, problems := tpl.checkAnswers(answersJSON(t, map[string]any{
		"full_name": "Dave <b>{{x}}</b>", "start": "2026-11-03", "end": "2026-11-07", "type": "Annual", "kit": []string{"Phone"},
		"ok": "Yes", "notes": "Line one\n\n- not a list", "signature": "true",
	}), "2026-10-23")
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	at := time.Date(2026, 10, 23, 9, 14, 0, 0, time.UTC)
	text := tpl.render(answers, submission{Template: "zap/team/hr/leave.md", Revision: strings.Repeat("a", 40), By: "dave", At: at})
	for _, want := range []string{
		"---\nform: leave-request\ntitle: \"Leave request\"\ntemplate: \"zap/team/hr/leave.md\"\n",
		"submittedBy: \"dave\"\nsubmittedAt: 2026-10-23T09:14:00Z\nstatus: submitted\nanswers:\n  full_name: \"Dave <b>{{x}}</b>\"\n",
		"  kit: [\"Phone\"]\n",
		"  signature: \"dave, 2026-10-23T09:14:00Z\"\n---\n# Leave request\n",
		"Full name: **Dave \\<b\\>\\{\\{x\\}\\}\\</b\\>**\n",
		"Staff number: _(blank)_\n",
		"First day: **2026-11-03**  Last day: **2026-11-07**\n",
		"Type of leave:\n- [x] Annual\n- [ ] Sick\n",
		"Equipment:\n\n- [ ] Laptop\n- [x] Phone\n",
		"Notes:\n> Line one\n>\n> \\- not a list\n",
		"`{{text name=not_a_field}}` and",
		"```\n{{text name=also_not}}\n```",
		"**Signed by dave, 2026-10-23 09:14 UTC**\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<!--") || strings.Contains(text, "two weeks") || strings.Contains(text, "inline note") {
		t.Errorf("comments kept:\n%s", text)
	}
	// The filed copy is no longer a form.
	if _, err := parseFormTemplate(text); err == nil || err.(*Error).Code != "not_a_form" || isFormTemplate(text) {
		t.Errorf("copy is a form: %v", err)
	}
}
