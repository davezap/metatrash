package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

func TestParseFolderConfig(t *testing.T) {
	good := []string{
		`{}`,
		`{"purpose":"Notes between agents","children":{"notes/":"working notes","plan.md":"the plan"}}`,
		`{"services":[{"type":"github","repo":"dave-zap/site"}]}`,
		`{"services":[{"type":"github","repo":"dave-zap/site.io","branch":"release/1.0","push":"auto","pull":"auto"}]}`,
	}
	for _, text := range good {
		if _, err := parseFolderConfig(text); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}
	bad := map[string]string{
		``:                              "JSON object",
		`[]`:                            "JSON object",
		`{"purpose":1}`:                 "wrong value type",
		`{"service":[]}`:                "unknown",
		`{"purpose":"a","purpose":"b"}`: "duplicate",
		`{"actions":[]}`:                "not supported yet",
		`{"children":{"a/b":"x"}}`:      "one file or folder name",
		`{"children":{".git/":"x"}}`:    "one file or folder name",
		`{"services":[{"type":"s3"}]}`:  "unknown service type",
		`{"services":[{}]}`:             "needs a type",
		`{"services":[{"type":"github","repo":"site"}]}`:                               "owner/name",
		`{"services":[{"type":"github","repo":"a/.."}]}`:                               "owner/name",
		`{"services":[{"type":"github","repo":"a/b","branch":"x..y"}]}`:                "branch",
		`{"services":[{"type":"github","repo":"a/b","push":"always"}]}`:                "push",
		`{"services":[{"type":"github","repo":"a/b","pull":"manual"}]}`:                "pull",
		`{"services":[{"type":"github","repo":"a/b"},{"type":"github","repo":"a/c"}]}`: "only one github",
		`{"services":[{"type":"github","repo":"a/b","token":"x"}]}`:                    "unknown",
		`{} {}`: "",
	}
	for text, want := range bad {
		_, err := parseFolderConfig(text)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want error containing %q", text, err, want)
		}
	}
	if _, err := parseFolderConfig(`{"purpose":"` + strings.Repeat("ā", maxFolderPurpose+1) + `"}`); err == nil {
		t.Error("accepted an over-long purpose")
	}
	starter := folderConfigStarter(`Ōtautahi "notes"`)
	if c, err := parseFolderConfig(starter); err != nil || c.Purpose == nil || !strings.Contains(*c.Purpose, "Ōtautahi") {
		t.Errorf("starter does not validate: %v\n%s", err, starter)
	}
	for path, want := range map[string]bool{".metatrash.json": true, "a/b/.metatrash.json": true, "a.metatrash.json": false, ".metatrash.json/x": false, ".metatrash/files.json": false, "a/.git": false, ".gitignore": false} {
		if validPath(path) && isFolderConfig(path) != want || !validPath(path) && want {
			t.Errorf("%s: valid=%v config=%v, want config %v", path, validPath(path), isFolderConfig(path), want)
		}
	}
	// Syntax only: dot names pass here and are checked against GitHub folders on write.
	for _, path := range []string{".metatrash/files.json", ".Metatrash/x", "a/.git", "a/.GIT/config", "a/.git./x", "a/./b", "a/../b", "..", "-a", "a/-b", "a//b", "a/"} {
		if validPath(path) {
			t.Errorf("%s accepted as a path", path)
		}
	}
	for _, path := range []string{".gitignore", "a/.github/workflows/ci.yml", "a/.metatrash/x", "pkg/__init__.py", "_config.yml", "a/.metatrash.json/b", "a/.gitignore"} {
		if !validPath(path) {
			t.Errorf("%s refused as a path", path)
		}
	}
	for path, want := range map[string]bool{"a/b": false, ".metatrash.json": false, "a/.metatrash.json": false, ".gitignore": true, "a/.github/ci.yml": true, "a/.metatrash.json/b": true} {
		if hasDotName(path) != want {
			t.Errorf("hasDotName(%s) = %v", path, !want)
		}
	}
}

// Folder configuration rules on an owned and a configured space, through the
// shared dispatcher: root starter, services, nesting, moves and deletes.
func TestFolderConfigRules(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "spaces.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "keys.json")
	if err = os.WriteFile(keyPath, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), configPath, keyPath, filepath.Join(dir, "data"), metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	owned, err := provision(ctx, filepath.Join(dir, "owned.git"), s.config.Defaults.Storage, []byte(ownedSpaceREADME), "Bart & Co", true)
	if err != nil {
		t.Fatal(err)
	}
	state := func(r *repository) string {
		head, err := r.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return head
	}
	do := func(r *repository, op string, in Input, code string) any {
		t.Helper()
		in.IfInState = state(r)
		value, err := s.dispatchRepository(ctx, r, "test", op, in)
		if code == "" && err != nil {
			t.Fatalf("%s %s%s: %v", op, in.Path, in.From, err)
		}
		if code != "" {
			p, ok := err.(*Error)
			if !ok || p.Code != code {
				t.Fatalf("%s %s%s: got %v, want %s", op, in.Path, in.From, err, code)
			}
		}
		return value
	}
	text := func(s string) *string { return &s }
	github := func(repo string) *string { return text(`{"services":[{"type":"github","repo":"` + repo + `"}]}`) }

	// New spaces start with a valid root config naming the space.
	read := do(owned, "read", Input{Path: ".metatrash.json"}, "").(ReadResult)
	if !strings.Contains(read.File.Text, "Bart & Co") || read.File.Protected || read.File.Deletable {
		t.Fatalf("root starter: %+v", read.File)
	}
	if _, err := parseFolderConfig(read.File.Text); err != nil {
		t.Fatal(err)
	}
	// Reopening does not add a second one.
	before := state(owned)
	if _, err := provision(ctx, filepath.Join(dir, "owned.git"), s.config.Defaults.Storage, nil, "Bart & Co", true); err != nil || state(owned) != before {
		t.Fatalf("reopen changed the space: %v", err)
	}

	do(owned, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"Bartco work","children":{"site/":"the website"}}`)}, "")
	do(owned, "write", Input{Path: ".metatrash.json", Text: github("dave-zap/site")}, "invalid_request")
	do(owned, "write", Input{Path: ".metatrash.json", Text: text(`{"purpose":"x","extra":1}`)}, "invalid_request")
	do(owned, "write", Input{Path: "site/.metatrash.json", Text: github("dave-zap/site")}, "")
	do(owned, "write", Input{Path: "site/docs/.metatrash.json", Text: github("dave-zap/docs")}, "invalid_request")
	do(owned, "write", Input{Path: "site/docs/.metatrash.json", Text: text(`{"purpose":"docs inside the repo"}`)}, "")
	do(owned, "write", Input{Path: "other/.metatrash.json", Text: github("dave-zap/other")}, "")
	// Rewriting the same folder's config is not nesting.
	do(owned, "write", Input{Path: "site/.metatrash.json", Text: text(`{"purpose":"site","services":[{"type":"github","repo":"dave-zap/site","push":"auto"}]}`)}, "")

	// A folder around an existing GitHub folder is refused too.
	do(owned, "write", Input{Path: "work/x/.metatrash.json", Text: github("dave-zap/x")}, "")
	do(owned, "write", Input{Path: "work/.metatrash.json", Text: github("dave-zap/work")}, "invalid_request")

	// Config files never move; ordinary files cross the boundary freely.
	do(owned, "move", Input{From: "site/.metatrash.json", To: "elsewhere/.metatrash.json"}, "protected_file")
	do(owned, "write", Input{Path: "notes/a.md", Text: text("hello")}, "")
	do(owned, "move", Input{From: "notes/a.md", To: "notes/.metatrash.json"}, "protected_file")
	do(owned, "move", Input{From: "notes/a.md", To: "site/a.md"}, "")

	// Delete: ordinary files, folder configs, not README or the root config.
	listed := do(owned, "list", Input{}, "").(ListResult)
	for _, f := range listed.Files {
		if f.Deletable != (f.Path != "README.md" && f.Path != ".metatrash.json") {
			t.Fatalf("deletable wrong for %s", f.Path)
		}
	}
	deleted := do(owned, "delete", Input{Path: "site/a.md"}, "").(Mutation)
	if !deleted.File.Deletable {
		t.Fatal("deleted file reported undeletable")
	}
	hist := do(owned, "history", Input{ID: deleted.File.ID}, "").(HistoryResult)
	if len(hist.Entries) != 3 || hist.Entries[0].Operation != "delete" || hist.Entries[1].Operation != "move" || hist.Entries[0].Path != "site/a.md" {
		t.Fatalf("history of deleted file: %+v", hist.Entries)
	}
	do(owned, "read", Input{Path: hist.Entries[1].Path, Revision: hist.Entries[1].Revision}, "")
	do(owned, "history", Input{ID: strings.Repeat("0", 32)}, "not_found")
	do(owned, "read", Input{Path: "site/a.md"}, "not_found")
	do(owned, "read", Input{Path: "site/a.md", Revision: deleted.OldState}, "")
	do(owned, "delete", Input{Path: "site/a.md"}, "not_found")
	do(owned, "delete", Input{Path: "README.md"}, "protected_file")
	do(owned, "delete", Input{Path: ".metatrash.json"}, "protected_file")
	do(owned, "delete", Input{Path: "work/x/.metatrash.json"}, "")
	do(owned, "write", Input{Path: "work/.metatrash.json", Text: github("dave-zap/work")}, "")
	if _, err := s.dispatchRepository(ctx, owned, "test", "delete", Input{Path: "work/.metatrash.json", IfInState: deleted.OldState}); err == nil || err.(*Error).Code != "state_mismatch" {
		t.Fatalf("stale delete: %v", err)
	}

	configs, err := owned.folderConfigs(ctx, mustFiles(t, owned))
	if err != nil {
		t.Fatal(err)
	}
	tree := fileTree(mustFiles(t, owned), "")
	markServiceFolders(tree, "", configs)
	marked := map[string]string{}
	var walk func([]*browserNode)
	walk = func(nodes []*browserNode) {
		for _, n := range nodes {
			if n.Service != "" {
				marked[n.Name] = n.Service
			}
			walk(n.Children)
		}
	}
	walk(tree)
	if marked["site"] != "GitHub dave-zap/site" || marked["work"] != "GitHub dave-zap/work" || marked["docs"] != "" || len(marked) != 3 {
		t.Fatalf("explorer marks: %v", marked)
	}

	// Configured spaces (here public) get a root config too, but no services.
	public := s.repos["public"]
	do(public, "read", Input{Path: ".metatrash.json"}, "")
	do(public, "write", Input{Path: "code/.metatrash.json", Text: github("dave-zap/site")}, "invalid_request")
	do(public, "write", Input{Path: "code/.metatrash.json", Text: text(`{"purpose":"shared snippets"}`)}, "")
	do(public, "delete", Input{Path: "code/.metatrash.json"}, "")

	// The history of a deleted file stays in the log as a delete entry.
	logText, err := gitLog(ctx, owned)
	if err != nil || !strings.Contains(logText, " delete site/a.md") {
		t.Fatalf("history: %v\n%s", err, logText)
	}
}

func mustFiles(t *testing.T, r *repository) map[string]record {
	t.Helper()
	_, files, err := r.snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func gitLog(ctx context.Context, r *repository) (string, error) {
	b, err := git(ctx, r.path, "", nil, "log", "--format=%s", branch, "--")
	return string(b), err
}
