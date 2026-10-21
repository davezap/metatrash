package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

// folderFilesHarness opens a service with the example spaces and one owned
// space, and returns a dispatcher for the owned space.
func folderFilesHarness(t *testing.T) (*Service, *repository, func(ctx context.Context, op string, in Input, code string) any) {
	t.Helper()
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	configPath, keyPath := filepath.Join(dir, "spaces.json"), filepath.Join(dir, "keys.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), configPath, keyPath, filepath.Join(dir, "data"), metatrash.SpaceAGENTS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	owned, err := provision(context.Background(), filepath.Join(dir, "owned.git"), s.config.Defaults.Storage, metatrash.SpaceAGENTS, "Mine", true)
	if err != nil {
		t.Fatal(err)
	}
	do := func(ctx context.Context, op string, in Input, code string) any {
		t.Helper()
		head, err := owned.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in.IfInState = head
		value, err := s.dispatchRepository(ctx, owned, "test", op, in)
		if code == "" && err != nil {
			t.Fatalf("%s %s%s: %v", op, in.Path, in.From, err)
		}
		if code != "" {
			if p, ok := err.(*Error); !ok || p.Code != code {
				t.Fatalf("%s %s%s: got %v, want %s", op, in.Path, in.From, err, code)
			}
		}
		return value
	}
	return s, owned, do
}

func paths(t *testing.T, r *repository) string {
	t.Helper()
	var out []string
	for p := range mustFiles(t, r) {
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// Every folder gets AGENTS.md and .metatrash.json with its first file; they
// cannot be deleted or moved, and go with the folder's last other file.
func TestFolderFiles(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	ctx := context.Background()
	text := func(s string) *string { return &s }

	if got := paths(t, owned); got != ".metatrash.json AGENTS.md" {
		t.Fatalf("new space: %s", got)
	}
	read := do(ctx, "read", Input{Path: "AGENTS.md"}, "").(ReadResult)
	if read.File.Text != string(metatrash.SpaceAGENTS) || !read.File.Protected || read.File.Deletable {
		t.Fatalf("root AGENTS.md: %+v", read.File)
	}

	// One write into a new nested folder adds the files of both folders, in
	// the same commit.
	w := do(ctx, "write", Input{Path: "notes/deep/a.md", Text: text("a\n")}, "").(WriteResult)
	if got := paths(t, owned); got != ".metatrash.json AGENTS.md notes/.metatrash.json notes/AGENTS.md notes/deep/.metatrash.json notes/deep/AGENTS.md notes/deep/a.md" {
		t.Fatalf("after write: %s", got)
	}
	if w.File.Path != "notes/deep/a.md" || !w.Created {
		t.Fatalf("write result: %+v", w)
	}
	sub := do(ctx, "read", Input{Path: "notes/.metatrash.json"}, "").(ReadResult)
	if sub.File.Text != folderConfigInherit || !sub.File.Protected {
		t.Fatalf("folder config: %+v", sub.File)
	}
	if s := do(ctx, "read", Input{Path: "notes/AGENTS.md"}, "").(ReadResult); s.File.Text != folderAgentsStarter {
		t.Fatalf("folder AGENTS.md: %q", s.File.Text)
	}

	// Kept: no delete, no move; rewriting is fine.
	do(ctx, "delete", Input{Path: "notes/AGENTS.md"}, "protected_file")
	do(ctx, "delete", Input{Path: "notes/deep/.metatrash.json"}, "protected_file")
	do(ctx, "move", Input{From: "notes/AGENTS.md", To: "notes/b.md"}, "protected_file")
	do(ctx, "write", Input{Path: "notes/AGENTS.md", Text: text("# Notes\n\nWorking notes.\n")}, "")
	do(ctx, "edit", Input{Path: "notes/AGENTS.md", Edits: []Edit{{OldText: "Working", NewText: "Shared"}}}, "")

	// README.md is an ordinary file now.
	do(ctx, "write", Input{Path: "README.md", Text: text("# Hello\n")}, "")
	do(ctx, "move", Input{From: "README.md", To: "notes/README.md"}, "")
	do(ctx, "delete", Input{Path: "notes/README.md"}, "")

	// Moving the last file out empties notes/deep/ and with it notes/, whose
	// rewritten AGENTS.md does not keep it: folder files go with the folder.
	do(ctx, "move", Input{From: "notes/deep/a.md", To: "inbox/a.md"}, "")
	if got := paths(t, owned); got != ".metatrash.json AGENTS.md inbox/.metatrash.json inbox/AGENTS.md inbox/a.md" {
		t.Fatalf("after move: %s", got)
	}
	do(ctx, "delete", Input{Path: "inbox/a.md"}, "")
	if got := paths(t, owned); got != ".metatrash.json AGENTS.md" {
		t.Fatalf("after delete: %s", got)
	}
	// The root keeps its files.
	do(ctx, "delete", Input{Path: "AGENTS.md"}, "protected_file")

	// A folder can start with its AGENTS.md: the folder exists from then on.
	do(ctx, "write", Input{Path: "plans/AGENTS.md", Text: text("# Plans\n")}, "")
	if got := paths(t, owned); got != ".metatrash.json AGENTS.md plans/.metatrash.json plans/AGENTS.md" {
		t.Fatalf("folder started by AGENTS.md: %s", got)
	}
	// A differently cased agents.md counts as the folder's AGENTS.md.
	do(ctx, "write", Input{Path: "lower/agents.md", Text: text("# lower\n")}, "")
	if _, ok := mustFiles(t, owned)["lower/AGENTS.md"]; ok {
		t.Fatal("second AGENTS.md added beside agents.md")
	}

	// GitHub folders get nothing; AGENTS.md there is a repository file.
	do(ctx, "write", Input{Path: "site/.metatrash.json", Text: text(`{"services":[{"type":"github","repo":"dave-zap/site"}]}`)}, "")
	do(ctx, "write", Input{Path: "site/docs/guide.md", Text: text("guide\n")}, "")
	files := mustFiles(t, owned)
	for _, p := range []string{"site/AGENTS.md", "site/docs/AGENTS.md", "site/docs/.metatrash.json"} {
		if _, ok := files[p]; ok {
			t.Fatalf("%s added inside a GitHub folder", p)
		}
	}
	if !files["site/.metatrash.json"].Protected {
		t.Fatal("GitHub folder settings not protected")
	}
	do(ctx, "write", Input{Path: "site/AGENTS.md", Text: text("repo agents\n")}, "")
	do(ctx, "delete", Input{Path: "site/AGENTS.md"}, "")
}

// Spaces from before 0.45.0 get the folder files they lack when opened.
func TestMissingFolderFilesAdded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	limits := Storage{MaxFileBytes: 65536, MaxRequestBytes: 524288, MaxFiles: 1000, MaxCurrentTextBytes: 1 << 24, MaxRepositoryBytes: 1 << 27}
	path := filepath.Join(dir, "old.git")
	r, err := provision(ctx, path, limits, []byte("# Agents\n"), "Old", false)
	if err != nil {
		t.Fatal(err)
	}
	// An old space: README.md, a root config and files in folders, nothing else.
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	delete(files, "AGENTS.md")
	texts := map[string]string{"README.md": "# old\n", "a/b/c.md": "c\n", "site/.metatrash.json": `{"services":[{"type":"github","repo":"o/site"}]}`, "site/x/y.md": "y\n"}
	for p, text := range texts {
		files[p] = record{File: File{ID: strings.Repeat("1", 31) + string(rune('a'+len(p)%6)), Path: p, Bytes: int64(len(text))}}
	}
	if _, err := r.commitFiles(ctx, head, files, texts, nil, nil, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := provision(ctx, path, limits, []byte("# Agents\n"), "Old", false); err != nil {
		t.Fatal(err)
	}
	got := paths(t, r)
	if got != ".metatrash.json AGENTS.md README.md a/.metatrash.json a/AGENTS.md a/b/.metatrash.json a/b/AGENTS.md a/b/c.md site/.metatrash.json site/x/y.md" {
		t.Fatalf("after reopening: %s", got)
	}
	readme := mustFiles(t, r)["README.md"]
	if readme.Protected || !readme.Deletable {
		t.Fatalf("README.md still special: %+v", readme)
	}
}

// inherit fills a folder's settings from its parent; locked keys are out of
// agents' reach, while a person (Metatrash Sync) can change them.
func TestInheritAndLocked(t *testing.T) {
	_, _, do := folderFilesHarness(t)
	agent := context.Background()
	person := context.WithValue(agent, personWriteKey{}, true)
	text := func(s string) *string { return &s }
	settings := func(path string) map[string]any {
		t.Helper()
		r := do(agent, "read", Input{Path: path}, "").(ReadResult)
		var m map[string]any
		if err := json.Unmarshal(r.Effective, &m); err != nil {
			t.Fatalf("%s effective %s: %v", path, r.Effective, err)
		}
		return m
	}

	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"inherit":true}`)}, "invalid_request")
	do(agent, "write", Input{Path: "notes/a.md", Text: text("a\n")}, "")
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Space","children":{"notes/":"notes"}}`)}, "")
	if m := settings("notes/.metatrash.json"); m["purpose"] != "Space" || m["inherit"] != true {
		t.Fatalf("inherited: %v", m)
	}
	// Own keys override.
	do(agent, "write", Input{Path: "notes/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Notes"}`)}, "")
	if m := settings("notes/.metatrash.json"); m["purpose"] != "Notes" || m["children"] == nil {
		t.Fatalf("override: %v", m)
	}

	// locked is the person's: agents can neither add nor change it.
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Space","locked":["purpose"]}`)}, "locked_setting")
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"locked":["bogus"]}`)}, "invalid_request")
	do(person, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Space","children":{},"locked":["purpose"]}`)}, "")
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Changed","children":{},"locked":["purpose"]}`)}, "locked_setting")
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"children":{},"locked":["purpose"]}`)}, "locked_setting")
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Space","children":{}}`)}, "locked_setting")
	// Unlocked keys and key order are free.
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"locked":["purpose"],"children":{"notes/":"x"},"purpose":"Space"}`)}, "")
	do(agent, "edit", Input{Path: ".metatrash.json", Edits: []Edit{{OldText: `"Space"`, NewText: `"Other"`}}}, "locked_setting")

	// Inherited locks hold in the child: no override, no leaving the parent.
	do(agent, "write", Input{Path: "notes/.metatrash.json", Text: text(`{"inherit":true}`)}, "locked_setting") // drops own purpose override
	do(person, "write", Input{Path: "notes/.metatrash.json", Text: text(`{"inherit":true}`)}, "")
	do(agent, "write", Input{Path: "notes/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Mine"}`)}, "locked_setting")
	do(agent, "write", Input{Path: "notes/.metatrash.json", Text: text(`{}`)}, "locked_setting")
	do(agent, "write", Input{Path: "notes/.metatrash.json", Text: text(`{"inherit":true,"children":{"x.md":"x"}}`)}, "")
	// A new folder starts inheriting, so it is locked too.
	do(agent, "write", Input{Path: "notes/sub/.metatrash.json", Text: text(`{"purpose":"escape"}`)}, "locked_setting")
	do(agent, "write", Input{Path: "notes/sub/b.md", Text: text("b\n")}, "")
	if m := settings("notes/sub/.metatrash.json"); m["purpose"] != "Space" {
		t.Fatalf("new folder settings: %v", m)
	}
	// The person can unlock.
	do(person, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Space"}`)}, "")
	do(agent, "write", Input{Path: "notes/.metatrash.json", Text: text(`{"purpose":"Mine"}`)}, "")
}

func TestReadOnlyFiles(t *testing.T) {
	_, _, do := folderFilesHarness(t)
	agent := context.Background()
	person := context.WithValue(agent, personWriteKey{}, true)
	text := func(s string) *string { return &s }
	locks := func() map[string]string {
		t.Helper()
		got := map[string]string{}
		for _, f := range do(agent, "list", Input{}, "").(ListResult).Files {
			if f.ReadOnly != "" {
				got[f.Path] = f.ReadOnly
			}
		}
		return got
	}

	do(agent, "write", Input{Path: "docs/spec.md", Text: text("spec\n")}, "")
	do(agent, "write", Input{Path: "docs/notes.md", Text: text("notes\n")}, "")
	do(agent, "write", Input{Path: "docs/old/a.md", Text: text("a\n")}, "")
	do(agent, "write", Input{Path: "docs/scan.pdf", Text: text("pdf\n")}, "")
	for _, bad := range []string{`{"readOnly":{"a/b.md":"agents"}}`, `{"readOnly":{"[x":"agents"}}`, `{"readOnly":{"":"agents"}}`, `{"readOnly":{"x.md":"me"}}`, `{"readOnly":"x.md"}`, `{"locked":{"locked":"everyone"}}`} {
		do(agent, "write", Input{Path: "docs/.metatrash.json", Text: text(bad)}, "invalid_request")
	}
	// Agents may set readOnly while it isn't locked; a list locks for agents.
	res := do(agent, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"readOnly":["spec.md","old/"]}`)}, "").(WriteResult)
	if res.File.ReadOnly != "" {
		t.Fatal(".metatrash.json marked read-only")
	}
	do(agent, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"readOnly":{"spec.md":"agents","*.pdf":"everyone","old/":"agents","AGENTS.md":"agents","*.json":"everyone"}}`)}, "")
	want := map[string]string{"docs/spec.md": "agents", "docs/scan.pdf": "everyone", "docs/old/a.md": "agents", "docs/old/AGENTS.md": "agents", "docs/old/.metatrash.json": "agents", "docs/AGENTS.md": "agents", "docs/.metatrash.json": "agents"}
	if got := locks(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("locked files %v, want %v", got, want)
	}

	// Agents can't change, rename, move or delete them, or create one.
	do(agent, "write", Input{Path: "docs/spec.md", Text: text("changed\n")}, "read_only_file")
	do(agent, "edit", Input{Path: "docs/spec.md", Edits: []Edit{{OldText: "spec", NewText: "x"}}}, "read_only_file")
	do(agent, "delete", Input{Path: "docs/spec.md"}, "read_only_file")
	do(agent, "move", Input{From: "docs/spec.md", To: "docs/spec2.md"}, "read_only_file")
	do(agent, "move", Input{From: "docs/notes.md", To: "docs/old/notes.md"}, "read_only_file")
	do(agent, "write", Input{Path: "docs/new.pdf", Text: text("x\n")}, "read_only_file")
	do(agent, "write", Input{Path: "docs/old/deeper/b.md", Text: text("b\n")}, "read_only_file")
	do(agent, "write", Input{Path: "docs/AGENTS.md", Text: text("# Mine\n")}, "read_only_file")
	do(agent, "write", Input{Path: "docs/.metatrash.json", Text: text(`{}`)}, "read_only_file")
	do(agent, "delete", Input{Path: "docs/missing.md"}, "not_found")
	// Other files are free, and entries are not inherited.
	do(agent, "write", Input{Path: "docs/notes.md", Text: text("more\n")}, "")
	do(agent, "move", Input{From: "docs/notes.md", To: "docs/notes2.md"}, "")
	do(agent, "write", Input{Path: "docs/sub/spec.md", Text: text("free\n")}, "")

	// A person can change what is locked for agents, not what is locked for everyone.
	do(person, "write", Input{Path: "docs/spec.md", Text: text("changed\n")}, "")
	do(person, "move", Input{From: "docs/spec.md", To: "docs/spec2.md"}, "")
	do(person, "move", Input{From: "docs/spec2.md", To: "docs/spec.md"}, "")
	do(person, "write", Input{Path: "docs/scan.pdf", Text: text("changed\n")}, "read_only_file")
	do(person, "delete", Input{Path: "docs/scan.pdf"}, "read_only_file")
	do(person, "write", Input{Path: "docs/other.pdf", Text: text("x\n")}, "read_only_file")
	// The lock on .metatrash.json stops at agents, so the person lifts it.
	do(person, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"readOnly":{"spec.md":"agents"},"locked":{"readOnly":"agents"}}`)}, "")
	do(agent, "write", Input{Path: "docs/scan.pdf", Text: text("now free\n")}, "")
	// Locked, readOnly is the person's too.
	do(agent, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"locked":{"readOnly":"agents"}}`)}, "locked_setting")

	// A key locked for everyone stops the person until the lock is lifted.
	do(person, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Docs","readOnly":{"spec.md":"agents"},"locked":{"purpose":"everyone"}}`)}, "")
	do(person, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Other","readOnly":{"spec.md":"agents"},"locked":{"purpose":"everyone"}}`)}, "locked_setting")
	do(person, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Docs","readOnly":{"spec.md":"agents"}}`)}, "")
	do(person, "write", Input{Path: "docs/.metatrash.json", Text: text(`{"inherit":true,"purpose":"Other","readOnly":{"spec.md":"agents"}}`)}, "")

	// A change never removes a folder file locked against its writer.
	do(agent, "write", Input{Path: "docs/sub/.metatrash.json", Text: text(`{"inherit":true,"readOnly":{"AGENTS.md":"agents"}}`)}, "")
	do(agent, "delete", Input{Path: "docs/sub/spec.md"}, "")
	if locks()["docs/sub/AGENTS.md"] == "" {
		t.Fatal("agent's delete removed a locked AGENTS.md")
	}
}
