package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

func openPublicTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "spaces.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), configPath, "", filepath.Join(dir, "data"), metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestEditSearchAndLineRanges(t *testing.T) {
	s := openPublicTestService(t)
	ctx := context.Background()
	do := func(op string, in Input, code string) any {
		t.Helper()
		v, err := s.Dispatch(ctx, "public", op, in)
		if code == "" && err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		if code != "" {
			if e, ok := err.(*Error); !ok || e.Code != code {
				t.Fatalf("%s: got %v, want %s", op, err, code)
			}
		}
		return v
	}
	text := func(path string) string {
		t.Helper()
		return do("read", Input{Path: path}, "").(ReadResult).File.Text
	}
	state := do("list", Input{}, "").(ListResult).State
	plan := "# Plan\nalpha one\nbeta two\nGamma three\nbeta four\n"
	w := do("write", Input{Path: "notes/plan.md", Text: &plan, IfInState: state}, "").(WriteResult)
	other := "beta elsewhere\n"
	w2 := do("write", Input{Path: "other/x.md", Text: &other, IfInState: w.NewState}, "").(WriteResult)
	planState := w.NewState

	// Line ranges.
	r := do("read", Input{Path: "notes/plan.md", StartLine: 2, LineCount: 2}, "").(ReadResult)
	if r.File.Text != "alpha one\nbeta two\n" || r.Lines == nil || *r.Lines != (LineRange{Start: 2, End: 3, Total: 5}) {
		t.Fatalf("range: %q %+v", r.File.Text, r.Lines)
	}
	r = do("read", Input{Path: "notes/plan.md", StartLine: 5}, "").(ReadResult)
	if r.File.Text != "beta four\n" || r.Lines.End != 5 {
		t.Fatalf("tail: %q %+v", r.File.Text, r.Lines)
	}
	if r = do("read", Input{Path: "notes/plan.md"}, "").(ReadResult); r.Lines != nil || r.File.Text != plan {
		t.Fatal("a plain read changed")
	}
	do("read", Input{Path: "notes/plan.md", StartLine: 7}, "invalid_request")
	do("read", Input{Path: "notes/plan.md", LineCount: maxReadLines + 1}, "invalid_request")

	// Search.
	found := do("search", Input{Query: "beta"}, "").(SearchResult)
	if len(found.Matches) != 3 || found.Truncated || found.Matches[0].Path != "notes/plan.md" || found.Matches[0].Line != 3 || found.Matches[2].Path != "other/x.md" {
		t.Fatalf("search: %+v", found)
	}
	if found.Matches[0].ID != w.File.ID || found.State != w2.NewState {
		t.Fatalf("search ids/state: %+v", found)
	}
	if n := len(do("search", Input{Query: "beta", Prefix: "notes/"}, "").(SearchResult).Matches); n != 2 {
		t.Fatalf("prefix: %d", n)
	}
	if m := do("search", Input{Query: "gamma", IgnoreCase: true, Context: 1}, "").(SearchResult).Matches; len(m) != 1 || m[0].Line != 4 || len(m[0].Before) != 1 || m[0].Before[0] != "beta two" || m[0].After[0] != "beta four" {
		t.Fatalf("ignoreCase/context: %+v", m)
	}
	if m := do("search", Input{Query: `^beta (two|four)$`, Regex: true}, "").(SearchResult).Matches; len(m) != 2 {
		t.Fatalf("regex: %+v", m)
	}
	if res := do("search", Input{Query: "beta", Limit: 2}, "").(SearchResult); len(res.Matches) != 2 || !res.Truncated {
		t.Fatalf("limit: %+v", res)
	}
	do("search", Input{Query: "("}, "")
	do("search", Input{Query: "(", Regex: true}, "invalid_request")
	do("search", Input{Query: ""}, "invalid_request")
	do("search", Input{Query: "x", Context: 11}, "invalid_request")

	// Edit without ifInState.
	e := do("edit", Input{Path: "notes/plan.md", Edits: []Edit{{OldText: "alpha one", NewText: "alpha ONE"}}}, "").(WriteResult)
	if !e.Changed || e.Created || e.File.ID != w.File.ID || text("notes/plan.md") != strings.Replace(plan, "alpha one", "alpha ONE", 1) {
		t.Fatalf("edit: %+v", e)
	}
	h := do("history", Input{ID: w.File.ID}, "").(HistoryResult)
	if h.Entries[0].Operation != "write" || h.Entries[0].Revision != e.NewState {
		t.Fatalf("history: %+v", h.Entries)
	}
	// Ambiguous, missing, replaceAll, and all-or-none.
	do("edit", Input{Path: "notes/plan.md", Edits: []Edit{{OldText: "beta", NewText: "BETA"}}}, "conflict")
	do("edit", Input{Path: "notes/plan.md", Edits: []Edit{{OldText: "delta", NewText: "x"}}}, "conflict")
	before := text("notes/plan.md")
	do("edit", Input{Path: "notes/plan.md", Edits: []Edit{{OldText: "# Plan", NewText: "# Plans"}, {OldText: "nope", NewText: ""}}}, "conflict")
	if text("notes/plan.md") != before {
		t.Fatal("a failed edit list saved part of its edits")
	}
	e = do("edit", Input{Path: "notes/plan.md", Edits: []Edit{{OldText: "beta", NewText: "BETA", ReplaceAll: true}, {OldText: "BETA two\n", NewText: ""}}}, "").(WriteResult)
	if got := text("notes/plan.md"); got != "# Plan\nalpha ONE\nGamma three\nBETA four\n" {
		t.Fatalf("replaceAll + delete: %q", got)
	}
	// An edit that changes nothing writes nothing.
	same := do("edit", Input{Path: "notes/plan.md", Edits: []Edit{{OldText: "Gamma", NewText: "Gamma"}}}, "").(WriteResult)
	if same.Changed || same.NewState != e.NewState {
		t.Fatalf("no-op edit: %+v", same)
	}

	// Per-file ifInState: another file changing is fine, this file changing is not.
	other2 := "changed\n"
	o := do("write", Input{Path: "other/x.md", Text: &other2, IfInState: e.NewState}, "").(WriteResult)
	e2 := do("edit", Input{Path: "notes/plan.md", IfInState: e.NewState, Edits: []Edit{{OldText: "Gamma", NewText: "Delta"}}}, "").(WriteResult)
	if e2.OldState != o.NewState {
		t.Fatalf("per-file state: %+v", e2)
	}
	err := func() error {
		_, err := s.Dispatch(ctx, "public", "edit", Input{Path: "notes/plan.md", IfInState: planState, Edits: []Edit{{OldText: "Delta", NewText: "Epsilon"}}})
		return err
	}()
	if p, ok := err.(*Error); !ok || p.Code != "state_mismatch" || p.CurrentState != e2.NewState {
		t.Fatalf("stale file: %v", err)
	}
	do("edit", Input{Path: "notes/plan.md", IfInState: strings.Repeat("a", 40), Edits: []Edit{{OldText: "Delta", NewText: "x"}}}, "state_mismatch")

	// CRLF files take LF edits.
	crlf := "one\r\ntwo\r\nthree\r\n"
	do("write", Input{Path: "win.txt", Text: &crlf, IfInState: e2.NewState}, "")
	do("edit", Input{Path: "win.txt", Edits: []Edit{{OldText: "one\ntwo\n", NewText: "one\n2\n"}}}, "")
	if got := text("win.txt"); got != "one\r\n2\r\nthree\r\n" {
		t.Fatalf("crlf: %q", got)
	}

	// Refusals.
	do("edit", Input{Path: "README.md", Edits: []Edit{{OldText: "a", NewText: "b"}}}, "protected_file")
	do("edit", Input{Path: "missing.md", Edits: []Edit{{OldText: "a", NewText: "b"}}}, "not_found")
	do("edit", Input{Path: "win.txt", Edits: []Edit{{OldText: "", NewText: "b"}}}, "invalid_request")
	do("edit", Input{Path: "win.txt"}, "invalid_request")
	do("edit", Input{Path: ".metatrash.json", Edits: []Edit{{OldText: "{", NewText: "[", ReplaceAll: true}}}, "invalid_request")
	big := strings.Repeat("x", 40000)
	do("edit", Input{Path: "win.txt", Edits: []Edit{{OldText: "three", NewText: big}, {OldText: "one", NewText: big}}}, "payload_too_large")
}

func TestEditAndSearchOverREST(t *testing.T) {
	s := openPublicTestService(t)
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
	if code, _ := call("PUT", "/api/v1/spaces/public/file?path=a.md", map[string]any{"text": "one\ntwo\nthree\n", "ifInState": list["state"]}); code != 201 {
		t.Fatalf("write: %d", code)
	}
	code, out := call("GET", "/api/v1/spaces/public/file?path=a.md&startLine=2&lineCount=1", nil)
	if code != 200 || out["file"].(map[string]any)["text"] != "two\n" || out["lines"].(map[string]any)["total"] != 3.0 {
		t.Fatalf("read range: %d %v", code, out)
	}
	code, out = call("GET", "/api/v1/spaces/public/search?query=TWO&ignoreCase=true&context=1", nil)
	m := out["matches"].([]any)
	if code != 200 || len(m) != 1 || m[0].(map[string]any)["line"] != 2.0 || len(m[0].(map[string]any)["before"].([]any)) != 1 {
		t.Fatalf("search: %d %v", code, out)
	}
	if code, _ = call("GET", "/api/v1/spaces/public/search?query=x&bogus=1", nil); code != 400 {
		t.Fatalf("unknown search parameter: %d", code)
	}
	code, out = call("POST", "/api/v1/spaces/public/edit", map[string]any{"path": "a.md", "edits": []map[string]any{{"oldText": "two", "newText": "2"}}})
	if code != 200 || out["changed"] != true {
		t.Fatalf("edit: %d %v", code, out)
	}
	if code, out = call("POST", "/api/v1/spaces/public/edit", map[string]any{"path": "a.md", "edits": []map[string]any{{"oldText": "two", "newText": "2"}}}); code != 409 || out["error"].(map[string]any)["code"] != "conflict" {
		t.Fatalf("stale edit: %d %v", code, out)
	}
	if code, _ = call("POST", "/api/v1/spaces/public/edit", map[string]any{"path": "a.md", "edits": []map[string]any{{"oldText": "one", "newText": "1", "extra": true}}}); code != 400 {
		t.Fatalf("unknown edit field: %d", code)
	}
	if code, _ = call("PUT", "/api/v1/spaces/public/edit", nil); code != 405 {
		t.Fatalf("edit method: %d", code)
	}
	_, out = call("GET", "/api/v1/spaces/public/file?path=a.md", nil)
	if out["file"].(map[string]any)["text"] != "one\n2\nthree\n" {
		t.Fatalf("after edit: %v", out)
	}
}

// Writes check only the files they touch against ifInState, unless strict.
func TestPerFileWriteChecks(t *testing.T) {
	s := openPublicTestService(t)
	ctx := context.Background()
	do := func(op string, in Input, code string) any {
		t.Helper()
		v, err := s.Dispatch(ctx, "public", op, in)
		if code == "" && err != nil {
			t.Fatalf("%s %s: %v", op, in.Path+in.From, err)
		}
		if code != "" {
			if e, ok := err.(*Error); !ok || e.Code != code {
				t.Fatalf("%s %s: got %v, want %s", op, in.Path+in.From, err, code)
			}
		}
		return v
	}
	text := func(s string) *string { return &s }
	base := do("list", Input{}, "").(ListResult).State
	a := do("write", Input{Path: "agent-a/notes.md", Text: text("a"), IfInState: base}, "").(WriteResult)
	if a.Rebased {
		t.Fatal("a current write reported rebased")
	}
	// A second agent, still holding base, writes elsewhere: accepted on top of a's write.
	b := do("write", Input{Path: "agent-b/notes.md", Text: text("b"), IfInState: base}, "").(WriteResult)
	if !b.Rebased || b.OldState != a.NewState {
		t.Fatalf("other file from an old state: %+v", b)
	}
	// The same file changed since: refused.
	do("write", Input{Path: "agent-a/notes.md", Text: text("a2"), IfInState: base}, "state_mismatch")
	// strict keeps the whole-space check.
	do("write", Input{Path: "agent-c/notes.md", Text: text("c"), IfInState: base, Strict: true}, "state_mismatch")
	// Move into a path created since ifInState, and move of a file changed since: refused.
	do("move", Input{From: "agent-b/notes.md", To: "agent-a/notes.md", IfInState: b.NewState}, "destination_exists")
	c := do("write", Input{Path: "agent-c/notes.md", Text: text("c"), IfInState: b.NewState}, "").(WriteResult)
	do("write", Input{Path: "shared/new.md", Text: text("n"), IfInState: c.NewState}, "")
	do("move", Input{From: "agent-c/notes.md", To: "shared/new.md", IfInState: c.NewState}, "state_mismatch")
	do("write", Input{Path: "agent-c/notes.md", Text: text("c2"), IfInState: c.NewState}, "")
	do("move", Input{From: "agent-c/notes.md", To: "agent-c/moved.md", IfInState: c.NewState}, "state_mismatch")
	// Deletes too: another file changed is fine, this file changed is not.
	d := do("delete", Input{Path: "agent-a/notes.md", IfInState: c.NewState}, "").(Mutation)
	if !d.Rebased {
		t.Fatalf("delete: %+v", d)
	}
	do("delete", Input{Path: "agent-c/notes.md", IfInState: c.NewState}, "state_mismatch")
	// Unknown or unreachable states are refused.
	do("write", Input{Path: "agent-b/notes.md", Text: text("x"), IfInState: strings.Repeat("b", 40)}, "state_mismatch")
	// Edits accept an old ifInState when the file is unchanged, unless strict.
	do("edit", Input{Path: "agent-b/notes.md", IfInState: base, Strict: true, Edits: []Edit{{OldText: "b", NewText: "B"}}}, "state_mismatch")
	e := do("edit", Input{Path: "agent-b/notes.md", IfInState: b.NewState, Edits: []Edit{{OldText: "b", NewText: "B"}}}, "").(WriteResult)
	if !e.Rebased {
		t.Fatalf("edit: %+v", e)
	}

	// REST write keeps the whole-space check unless the body says strict: false.
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	put := func(path string, body map[string]any) (int, map[string]any) {
		payload, _ := json.Marshal(body)
		r := httptest.NewRequest("PUT", "/api/v1/spaces/public/file?path="+path, bytes.NewReader(payload))
		r.RemoteAddr = "127.0.0.1:1"
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := put("rest/one.md", map[string]any{"text": "1", "ifInState": base}); code != 409 {
		t.Fatalf("REST default: %d", code)
	}
	if code, out := put("rest/one.md", map[string]any{"text": "1", "ifInState": base, "strict": false}); code != 201 || out["rebased"] != true {
		t.Fatalf("REST strict false: %d %v", code, out)
	}
}
