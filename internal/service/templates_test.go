package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"metatrash.com/metatrash"
)

func TestTemplateSettingsChecked(t *testing.T) {
	good := []string{
		`{"templates": ["/Templates/meeting.md", "decision.md", "../Templates/x.md"], "defaultTemplate": "/Templates/meeting.md"}`,
		`{"defaultTemplate": ""}`,
		`{"templates": []}`,
	}
	for _, text := range good {
		if _, err := parseFolderConfig(text); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	bad := []string{
		`{"templates": ["meeting.txt"]}`,
		`{"templates": ["/"]}`,
		`{"templates": ["/../x.md"]}`,
		`{"templates": [".hidden/x.md"]}`,
		`{"templates": ["a\\b.md"]}`,
		`{"templates": ["x.md", "x.md"]}`,
		`{"templates": "x.md"}`,
		`{"defaultTemplate": "notes"}`,
		`{"templates": ["1.md","2.md","3.md","4.md","5.md","6.md","7.md","8.md","9.md","10.md","11.md","12.md","13.md","14.md","15.md","16.md","17.md","18.md","19.md","20.md","21.md"]}`,
	}
	for _, text := range bad {
		if _, err := parseFolderConfig(text); err == nil {
			t.Errorf("%s: accepted", text)
		}
	}
	if _, err := parseFolderConfig(`{"locked": {"templates": "agents", "defaultTemplate": "everyone"}}`); err != nil {
		t.Errorf("templates should be lockable: %v", err)
	}
}

func TestResolveTemplate(t *testing.T) {
	cases := []struct{ folder, entry, want string }{
		{"", "/Templates/a.md", "Templates/a.md"},
		{"Meetings/", "a.md", "Meetings/a.md"},
		{"Meetings/2026/", "../a.md", "Meetings/a.md"},
		{"Meetings/", "./a.md", "Meetings/a.md"},
		{"", "../a.md", ""},
		{"Meetings/", "../../a.md", ""},
	}
	for _, c := range cases {
		if got := resolveTemplate(c.folder, c.entry); got != c.want {
			t.Errorf("resolveTemplate(%q, %q) = %q, want %q", c.folder, c.entry, got, c.want)
		}
	}
}

func TestStripTemplateKeys(t *testing.T) {
	cases := []struct{ in, want string }{
		{"---\nnewName: \"{{@today}} Meeting\"\n---\n\n# Meeting\n", "# Meeting\n"},
		{"---\ntitle: Notes\nnewName: x\ntags: [a]\n---\nBody\n", "---\ntitle: Notes\ntags: [a]\n---\nBody\n"},
		{"---\nnewName: >\n  folded\ntitle: T\n---\nB", "---\ntitle: T\n---\nB"},
		{"# No front matter\n", "# No front matter\n"},
		{"---\r\nnewName: x\r\nkeep: y\r\n---\r\nBody\r\n", "---\r\nkeep: y\r\n---\r\nBody\r\n"},
		{"---\nunclosed: yes\n", "---\nunclosed: yes\n"},
	}
	for _, c := range cases {
		if got := stripTemplateKeys(c.in); got != c.want {
			t.Errorf("stripTemplateKeys(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := templateFileName(" 2026-11-28 / Board: notes. "); got != "2026-11-28 - Board- notes" {
		t.Errorf("templateFileName = %q", got)
	}
}

func TestNewFilesFromTemplates(t *testing.T) {
	s := openPublicTestService(t)
	s.clock = func() time.Time { return time.Date(2026, 11, 28, 9, 30, 0, 0, time.UTC) }
	agent := withAuthor(context.Background(), func() changeAuthor { return changeAuthor{Name: "dave", Kind: "person"} })
	ctx := context.WithValue(agent, personWriteKey{}, true)
	do := func(op string, in Input, code string) any {
		t.Helper()
		v, err := s.Dispatch(ctx, "public", op, in)
		if code == "" && err != nil {
			t.Fatalf("%s %s: %v", op, in.Path, err)
		}
		if code != "" {
			if e, ok := err.(*Error); !ok || e.Code != code {
				t.Fatalf("%s %s: got %v, want %s", op, in.Path, err, code)
			}
		}
		return v
	}
	write := func(path, text string) {
		t.Helper()
		state := do("list", Input{}, "").(ListResult).State
		do("write", Input{Path: path, Text: &text, IfInState: state}, "")
	}
	read := func(path string) string {
		t.Helper()
		return do("read", Input{Path: path}, "").(ReadResult).File.Text
	}
	repo := s.repos["public"]
	create := func(in templateRequest, code string) WriteResult {
		t.Helper()
		in.IfInState = do("list", Input{}, "").(ListResult).State
		v, err := s.createFromTemplate(ctx, repo, "public", in)
		if code == "" && err != nil {
			t.Fatalf("from %q in %q: %v", in.Template, in.Folder, err)
		}
		if code != "" {
			if e, ok := err.(*Error); !ok || e.Code != code {
				t.Fatalf("from %q in %q: got %v, want %s", in.Template, in.Folder, err, code)
			}
		}
		return v
	}

	write("Templates/meeting.md", "---\nnewName: \"{{@today}} Meeting\"\n---\n\n# {{@title}}\n\nDate: {{@today}} at {{ @now }}\nBy: {{@name}} <{{@email}}>\nKeep {{@other}} and {{text name=x}}.\n")
	write("Templates/decision.md", "# Decision\n\nDecided on {{@today}}.\n")
	write("Templates/leave.md", "---\nform: leave\nsubmitTo: done/\n---\nName: {{text name=n}}\n")
	write("Meetings/.metatrash.json", `{"templates": ["/Templates/meeting.md", "/Templates/decision.md"], "defaultTemplate": "/Templates/meeting.md"}`)
	write("Meetings/2026/.metatrash.json", `{"inherit": true}`)
	write("Other/.metatrash.json", `{"inherit": true, "templates": ["../Templates/decision.md"]}`)

	// The default template, named by newName, in an inheriting subfolder.
	w := create(templateRequest{Folder: "Meetings/2026/"}, "")
	if w.File.Path != "Meetings/2026/2026-11-28 Meeting.md" || !w.Created {
		t.Fatalf("default: %+v", w)
	}
	want := "# 2026-11-28 Meeting\n\nDate: 2026-11-28 at 09:30\nBy: dave <>\nKeep {{@other}} and {{text name=x}}.\n"
	if got := read(w.File.Path); got != want {
		t.Fatalf("filled:\n%q\nwant\n%q", got, want)
	}
	// The same name again is numbered.
	if w := create(templateRequest{Folder: "Meetings/2026/"}, ""); w.File.Path != "Meetings/2026/2026-11-28 Meeting 2.md" {
		t.Fatalf("numbered: %s", w.File.Path)
	}
	// The person's own date and time win, as for forms.
	if w := create(templateRequest{Folder: "Meetings/", Today: "2026-11-29", Now: "08:05"}, ""); w.File.Path != "Meetings/2026-11-29 Meeting.md" || !strings.Contains(read(w.File.Path), "at 08:05") {
		t.Fatalf("local time: %s %q", w.File.Path, read(w.File.Path))
	}
	// A named file from another template; a clash with a given name is refused.
	if w := create(templateRequest{Template: "Templates/decision.md", Folder: "Meetings/", Name: "Budget"}, ""); w.File.Path != "Meetings/Budget.md" || read(w.File.Path) != "# Decision\n\nDecided on 2026-11-28.\n" {
		t.Fatalf("named: %+v", w)
	}
	create(templateRequest{Template: "/Templates/decision.md", Folder: "Meetings/", Name: "Budget.md"}, "destination_exists")
	// No newName and no name.
	create(templateRequest{Template: "Templates/decision.md", Folder: "Meetings/"}, "invalid_request")
	// No default template here.
	create(templateRequest{Folder: "Other/"}, "no_default_template")
	create(templateRequest{Folder: ""}, "no_default_template")
	// Missing, form and non-Markdown templates.
	create(templateRequest{Template: "Templates/gone.md", Folder: "Meetings/", Name: "x"}, "template_not_found")
	create(templateRequest{Template: "Meetings/.metatrash.json", Folder: "Meetings/", Name: "x"}, "template_not_found")
	create(templateRequest{Template: "Templates/leave.md", Folder: "Meetings/", Name: "x"}, "form_template")
	// A template in another space can't be named: paths are this space's.
	create(templateRequest{Template: "/dave/hr/Templates/a.md", Folder: "Meetings/", Name: "x"}, "template_not_found")
	create(templateRequest{Template: "../x.md", Folder: "Meetings/", Name: "x"}, "invalid_request")
	create(templateRequest{Folder: "Meetings", Name: "x"}, "invalid_request")
	create(templateRequest{Folder: "Meetings/", Name: "a/b"}, "invalid_request")

	// templatesOf resolves each list from the folder that set it.
	_, files, _ := repo.snapshot(ctx, "")
	got, _ := repo.templatesOf(ctx, files, "Meetings/2026/")
	if strings.Join(got.Templates, ",") != "Templates/meeting.md,Templates/decision.md" || got.Default != "Templates/meeting.md" {
		t.Fatalf("templatesOf: %+v", got)
	}
	got, _ = repo.templatesOf(ctx, files, "Other/")
	if strings.Join(got.Templates, ",") != "Templates/decision.md" || got.Default != "" {
		t.Fatalf("templatesOf other: %+v", got)
	}

	// MCP write with fromTemplate: path is the whole name; text and fromTemplate don't mix.
	state := do("list", Input{}, "").(ListResult).State
	v := do("write", Input{Path: "Meetings/Agent notes.md", FromTemplate: "Templates/meeting.md", IfInState: state}, "").(WriteResult)
	if v.File.Path != "Meetings/Agent notes.md" || !strings.HasPrefix(read(v.File.Path), "# Agent notes\n") {
		t.Fatalf("mcp write: %+v", v)
	}
	text := "x"
	do("write", Input{Path: "Meetings/y.md", FromTemplate: "Templates/meeting.md", Text: &text, IfInState: v.NewState}, "invalid_request")
	do("write", Input{Path: "Meetings/Agent notes.md", FromTemplate: "Templates/meeting.md", IfInState: v.NewState}, "destination_exists")

	// A lock on templates holds for agents.
	write("Locked/.metatrash.json", `{"inherit": true, "templates": ["/Templates/meeting.md"], "locked": {"templates": "agents"}}`)
	state = do("list", Input{}, "").(ListResult).State
	changed := `{"inherit": true, "templates": ["/Templates/decision.md"], "locked": {"templates": "agents"}}`
	if _, err := s.Dispatch(agent, "public", "write", Input{Path: "Locked/.metatrash.json", Text: &changed, IfInState: state}); err == nil || !strings.Contains(err.Error(), `"templates" is locked`) {
		t.Fatalf("an agent changed locked templates: %v", err)
	}
}

func TestNewFileFromTemplateOverREST(t *testing.T) {
	s := openPublicTestService(t)
	s.clock = func() time.Time { return time.Date(2026, 11, 28, 9, 30, 0, 0, time.UTC) }
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, url string, body any) (int, map[string]any) {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, url, bytes.NewReader(payload))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	_, list := call("GET", "/api/v1/spaces/public/files?limit=1", nil)
	code, out := call("PUT", "/api/v1/spaces/public/file?path=T/note.md", map[string]any{"text": "---\nnewName: Note {{@today}}\n---\n# {{@title}}\n", "ifInState": list["state"]})
	if code != 201 {
		t.Fatalf("write: %d %v", code, out)
	}
	code, out = call("POST", "/api/v1/spaces/public/from-template", map[string]any{"template": "T/note.md", "folder": "", "ifInState": out["newState"]})
	if code != 201 || out["file"].(map[string]any)["path"] != "Note 2026-11-28.md" {
		t.Fatalf("from-template: %d %v", code, out)
	}
	if code, _ = call("POST", "/api/v1/spaces/public/from-template", map[string]any{"template": "T/note.md", "folder": "", "ifInState": out["newState"], "extra": 1}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	if code, _ = call("GET", "/api/v1/spaces/public/from-template", nil); code != 405 {
		t.Fatalf("GET: %d", code)
	}
	_, out = call("GET", "/api/v1/spaces/public/file?path=Note%202026-11-28.md", nil)
	if out["file"].(map[string]any)["text"] != "# Note 2026-11-28\n" {
		t.Fatalf("text: %v", out)
	}
}
