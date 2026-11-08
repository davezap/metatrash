package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The four lock levels and how they combine (0.95.12).
func TestLockLevels(t *testing.T) {
	for _, c := range []struct {
		level  string
		writer writer
		stops  bool
	}{
		{"", writerFolderAgent, false},
		{lockFolderAgents, writerFolderAgent, true},
		{lockFolderAgents, writerConnectedAgent, false},
		{lockFolderAgents, writerPerson, false},
		{lockConnectedAgents, writerConnectedAgent, true},
		{lockConnectedAgents, writerFolderAgent, false},
		{lockAgents, writerFolderAgent, true},
		{lockAgents, writerConnectedAgent, true},
		{lockAgents, writerPerson, false},
		{lockEveryone, writerPerson, true},
	} {
		if got := blocks(c.level, c.writer); got != c.stops {
			t.Errorf("blocks(%q, %d) = %v", c.level, c.writer, got)
		}
	}
	for _, c := range [][3]string{
		{lockFolderAgents, lockConnectedAgents, lockAgents},
		{lockFolderAgents, "", lockFolderAgents},
		{lockConnectedAgents, lockAgents, lockAgents},
		{lockAgents, lockEveryone, lockEveryone},
		{"", "", ""},
	} {
		if got := stronger(c[0], c[1]); got != c[2] {
			t.Errorf("stronger(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	if _, err := parseFolderConfig(`{"locked":{"fillForms":"connectedAgents","takeQuizzes":"everyone","actions":"folderAgents"},"readOnly":{"a.md":"folderAgents"}}`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"locked":{"fillForms":"people"}}`, `{"locked":{"sendForms":"agents"}}`} {
		if _, err := parseFolderConfig(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// jobContext is a folder action's job writing for payer.
func jobContext(folder, action, payer string, contributor bool) context.Context {
	ctx := context.WithValue(context.Background(), runnerWriteKey{}, true)
	return context.WithValue(ctx, runnerJobKey{}, &runnerJobContext{folder: folder, action: action, payerID: payer, contributor: contributor})
}

// folderAgents and connectedAgents stop only their own kind of agent.
func TestLocksByAgentKind(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	person := asOwner(context.Background(), owned)
	connected := context.WithValue(context.Background(), mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: owned.owner}})
	job := jobContext("docs/", "tidy", owned.owner, false)
	text := func(s string) *string { return &s }

	do(person, "write", Input{Path: "docs/a.md", Text: text("a\n")}, "")
	do(person, "write", Input{Path: "docs/b.md", Text: text("b\n")}, "")
	do(person, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"readOnly":{"a.md":"folderAgents","b.md":"connectedAgents"}}`)}, "")
	do(job, "write", Input{Path: "docs/a.md", Text: text("job\n")}, "read_only_file")
	do(job, "write", Input{Path: "docs/b.md", Text: text("job\n")}, "")
	do(connected, "write", Input{Path: "docs/b.md", Text: text("agent\n")}, "read_only_file")
	do(connected, "write", Input{Path: "docs/a.md", Text: text("agent\n")}, "")
	do(person, "write", Input{Path: "docs/a.md", Text: text("person\n")}, "")

	// The default locks: readOnly is the folder agents' to leave alone, not
	// the connected agents'.
	do(job, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true}`)}, "locked_setting")
	do(connected, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"readOnly":{"a.md":"folderAgents"}}`)}, "")
	do(job, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Docs","readOnly":{"a.md":"folderAgents"}}`)}, "")
	// Nobody but a person changes locked, or leaves the parent's locks.
	do(connected, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"locked":{"purpose":"agents"}}`)}, "locked_setting")
	do(connected, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"purpose":"Docs","readOnly":{"a.md":"folderAgents"}}`)}, "locked_setting")
}

// A job changes actions only where the locks let folder agents, for the
// account that pays for it, and never the action it runs for.
func TestJobChangesActions(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	owner := asOwner(context.Background(), owned)
	job := jobContext("hr/", "tidy", owned.owner, false)
	tidy := `{"name":"tidy","on":"in/*.md","do":"Tidy."}`
	second := `{"name":"second","on":"out/*.md","do":"File it."}`
	config := func(purpose string, actions ...string) *string {
		s := `{"inherit":true,"purpose":"` + purpose + `","actions":[` + strings.Join(actions, ",") + `]}`
		return &s
	}
	do(owner, "write", Input{Path: "hr/.metatrash.json", Text: config("HR", tidy)}, "")
	// Other settings are open; actions are locked for folder agents by default.
	do(job, "write", Input{Path: "hr/.metatrash.json", Text: config("Human resources", tidy)}, "")
	do(job, "write", Input{Path: "hr/.metatrash.json", Text: config("Human resources", tidy, second)}, "locked_setting")

	// The owner lifts the lock.
	open := strings.Replace(folderConfigEmpty, `    "actions": "folderAgents",
`, "", 1)
	do(owner, "write", Input{Path: ".metatrash.json", Text: &open}, "")
	do(job, "write", Input{Path: "hr/.metatrash.json", Text: config("Human resources", tidy, second)}, "")
	do(job, "write", Input{Path: "hr/.metatrash.json", Text: config("Human resources", tidy)}, "")
	// Never its own action.
	do(job, "write", Input{Path: "hr/.metatrash.json", Text: config("Human resources", `{"name":"tidy","on":"*.md","do":"Tidy."}`)}, "forbidden")
	do(job, "write", Input{Path: "hr/.metatrash.json", Text: config("Human resources")}, "forbidden")
	// Its own action is safe in other folders' settings too: they are not its folder.
	do(job, "write", Input{Path: "hr/sub/.metatrash.json", Text: config("Sub", second)}, "")
	// A contributor's job changes no actions; a member's only the member's.
	contributor := jobContext("hr/", "tidy", strings.Repeat("c", 32), true)
	do(contributor, "write", Input{Path: "hr/sub/.metatrash.json", Text: config("Sub")}, "forbidden")
	do(contributor, "write", Input{Path: "hr/sub/.metatrash.json", Text: config("Sub2", second)}, "")
}

// Fill-in and quiz locks, inherited from the folder's settings.
func TestFormLocks(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	owner := asOwner(context.Background(), owned)
	text := func(s string) *string { return &s }
	do(owner, "write", Input{Path: "hr/leave.md", Text: text("x\n")}, "")
	ctx := context.Background()
	_, files, err := owned.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	check := func(quiz bool, w writer, want string) {
		t.Helper()
		err := owned.checkFormLock(ctx, files, "hr/leave.md", quiz, w)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("quiz %v writer %d: %v, want %q", quiz, w, err, want)
		}
	}
	// By default anyone may fill in forms; no agent may take a quiz.
	check(false, writerFolderAgent, "")
	check(false, writerConnectedAgent, "")
	check(true, writerConnectedAgent, "taking the quizzes")
	check(true, writerFolderAgent, "taking the quizzes")
	check(true, writerPerson, "")

	locked := strings.Replace(folderConfigEmpty, `"takeQuizzes": "agents"`, `"takeQuizzes": "agents", "fillForms": "folderAgents"`, 1)
	do(owner, "write", Input{Path: ".metatrash.json", Text: &locked}, "")
	if _, files, err = owned.snapshot(ctx, ""); err != nil {
		t.Fatal(err)
	}
	check(false, writerFolderAgent, "filling in the forms")
	check(false, writerConnectedAgent, "")
	check(false, writerPerson, "")
}

// Spaces made before 0.95.12 get the default locks once.
func TestDefaultLocksAdded(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"{}\n", folderConfigEmpty},
		{`{"purpose":"Mine","locked":["purpose"]}`, "{\n  \"purpose\": \"Mine\",\n  \"locked\": {\n    \"actions\": \"folderAgents\",\n    \"publishedVersion\": \"folderAgents\",\n    \"purpose\": \"agents\",\n    \"readOnly\": \"folderAgents\",\n    \"services\": \"folderAgents\",\n    \"takeQuizzes\": \"agents\"\n  }\n}\n"},
		{`{"locked":{"actions":"everyone"},"footer":"x"}`, "{\n  \"locked\": {\n    \"actions\": \"everyone\",\n    \"publishedVersion\": \"folderAgents\",\n    \"readOnly\": \"folderAgents\",\n    \"services\": \"folderAgents\",\n    \"takeQuizzes\": \"agents\"\n  },\n  \"footer\": \"x\"\n}\n"},
		{`{"inherit":true}`, `{"inherit":true}`},
		{`not json`, `not json`},
	} {
		got, changed := withDefaultLocks(c.in)
		if got != c.want || changed != (c.in != c.want) {
			t.Errorf("%s: got %q (changed %v), want %q", c.in, got, changed, c.want)
		}
	}

	ctx := context.Background()
	dir := t.TempDir()
	limits := Storage{MaxFileBytes: 65536, MaxRequestBytes: 524288, MaxFiles: 1000, MaxCurrentTextBytes: 1 << 24, MaxRepositoryBytes: 1 << 27}
	path := filepath.Join(dir, "old.git")
	if _, err := git(ctx, "", "", nil, "init", "--bare", "--object-format=sha1", "--initial-branch=main", path); err != nil {
		t.Fatal(err)
	}
	old := &repository{path: path, limits: limits, owned: true}
	files := map[string]record{}
	texts := map[string]string{".metatrash.json": `{"purpose":"old"}`, "AGENTS.md": "# Agents\n", "own/.metatrash.json": `{"purpose":"own"}`, "own/AGENTS.md": "x\n", "own/a.md": "a\n", "in/.metatrash.json": `{"inherit":true}`, "in/AGENTS.md": "x\n", "in/b.md": "b\n"}
	var changes []commitChange
	i := 0
	for p, text := range texts {
		i++
		id := strings.Repeat("a", 30) + string(rune('a'+i/10)) + string(rune('a'+i%10))
		files[p] = record{File: File{ID: id, Path: p, Bytes: int64(len(text))}}
		changes = append(changes, commitChange{id, "create", p})
	}
	if _, err := old.commitFiles(ctx, "", files, texts, nil, changes, "old"); err != nil {
		t.Fatal(err)
	}
	r, err := provision(ctx, path, limits, []byte("# Agents\n"), "Old", true)
	if err != nil {
		t.Fatal(err)
	}
	read := func(p string) string {
		t.Helper()
		_, files, err := r.snapshot(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		b, err := r.blob(ctx, files[p].Blob)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, p := range []string{".metatrash.json", "own/.metatrash.json"} {
		if !strings.Contains(read(p), `"takeQuizzes": "agents"`) {
			t.Errorf("%s: %s", p, read(p))
		}
	}
	if read("in/.metatrash.json") != `{"inherit":true}` {
		t.Errorf("inheriting folder changed: %s", read("in/.metatrash.json"))
	}
	// A lock the owner lifts stays lifted when the space is opened again.
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	f := files[".metatrash.json"]
	lifted := "{}\n"
	f.Bytes = int64(len(lifted))
	files[".metatrash.json"] = f
	if _, err := r.commitFiles(ctx, head, files, map[string]string{".metatrash.json": lifted}, nil, []commitChange{{f.ID, "write", ".metatrash.json"}}, "lift"); err != nil {
		t.Fatal(err)
	}
	if _, err := provision(ctx, path, limits, []byte("# Agents\n"), "Old", true); err != nil {
		t.Fatal(err)
	}
	if got := read(".metatrash.json"); got != lifted {
		t.Errorf("default locks added again: %s", got)
	}
	// New spaces record them from the start.
	fresh := filepath.Join(dir, "new.git")
	n, err := provision(ctx, fresh, limits, []byte("# Agents\n"), "New", true)
	if err != nil {
		t.Fatal(err)
	}
	head, err = n.head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if marker, err := n.serviceFile(ctx, head, defaultLocksPath); err != nil || marker == nil {
		t.Errorf("new space without the marker: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal(err)
	}
}

// A job gets submit only when its action lists it; without a database the
// call is refused like any form outside an account space.
func TestRunnerSubmitTool(t *testing.T) {
	w := newRunnerWorld(t)
	args := []byte(`{"path":"leave.md","answers":{"name":"Pat"}}`)
	names := func(tools []string) string {
		var out []string
		for _, raw := range toolsFor(w.runner.tools, tools) {
			var d struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(raw, &d)
			out = append(out, d.Name)
		}
		return strings.Join(out, " ")
	}
	if got := names(nil); strings.Contains(got, "submit") {
		t.Errorf("default tools: %s", got)
	}
	if got := names([]string{"read", "submit"}); got != "read submit" {
		t.Errorf("listed tools: %s", got)
	}
	without := context.WithValue(context.Background(), runnerJobKey{}, &runnerJobContext{folder: "hr/", action: "a", payerID: w.repo.owner})
	if text, isError, _, _ := w.runner.callTool(without, w.repo, w.repo.id, pinnedFolder("hr/"), "submit", args); !isError || !strings.Contains(text, "Unknown tool") {
		t.Errorf("unlisted submit: %s", text)
	}
	with := context.WithValue(context.Background(), runnerJobKey{}, &runnerJobContext{folder: "hr/", action: "a", payerID: w.repo.owner, submit: true})
	if text, isError, _, _ := w.runner.callTool(with, w.repo, w.repo.id, pinnedFolder("hr/"), "submit", args); !isError || !strings.Contains(text, "not_found") {
		t.Errorf("listed submit: %s", text)
	}
}
