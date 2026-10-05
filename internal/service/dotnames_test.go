package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

// Names starting with a dot live only inside GitHub folders (step 2b).
func TestDotNamesInGitHubFolders(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	configPath, keyPath := filepath.Join(dir, "spaces.json"), filepath.Join(dir, "keys.json")
	_ = os.WriteFile(configPath, b, 0600)
	_ = os.WriteFile(keyPath, []byte(`{}`), 0600)
	s, err := Open(context.Background(), configPath, keyPath, filepath.Join(dir, "data"), metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	owned, err := provision(ctx, filepath.Join(dir, "owned.git"), s.config.Defaults.Storage, []byte(ownedSpaceREADME), "Dots", true)
	if err != nil {
		t.Fatal(err)
	}
	do := func(r *repository, op string, in Input, code string) any {
		t.Helper()
		head, err := r.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in.IfInState = head
		value, err := s.dispatchRepository(ctx, r, "test", op, in)
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
	text := func(s string) *string { return &s }
	github := text(`{"services":[{"type":"github","repo":"dave-zap/site"}]}`)

	// Outside GitHub folders, dot names are refused with a reason.
	for _, path := range []string{".gitignore", "notes/.env", "notes/.hidden/a.md", ".github/workflows/ci.yml"} {
		do(owned, "write", Input{Path: path, Text: text("x")}, "invalid_request")
	}
	public := s.repos["public"]
	do(public, "write", Input{Path: "a/.gitignore", Text: text("x")}, "invalid_request")
	// Never .git, anywhere, in any case; never the service's index at the root.
	for _, path := range []string{"site/.git", "site/.git/config", "site/.GIT/HEAD", "site/sub/.git", ".metatrash/files.json"} {
		do(owned, "write", Input{Path: path, Text: text("x")}, "invalid_request")
	}

	// Inside a GitHub folder they work, at any depth.
	do(owned, "write", Input{Path: "site/.metatrash.json", Text: github}, "")
	for _, path := range []string{"site/.gitignore", "site/.github/workflows/ci.yml", "site/src/.env.example", "site/pkg/__init__.py"} {
		do(owned, "write", Input{Path: path, Text: text("x")}, "")
	}
	read := do(owned, "read", Input{Path: "site/.github/workflows/ci.yml"}, "").(ReadResult)
	if read.File.Text != "x" || !read.File.Deletable {
		t.Fatalf("read dotfile: %+v", read.File)
	}
	listed := do(owned, "list", Input{Prefix: "site/.github/"}, "").(ListResult)
	if len(listed.Files) != 1 {
		t.Fatalf("list under a dot folder: %+v", listed.Files)
	}
	// A config inside a dot folder of the repo is still a Metatrash config.
	do(owned, "write", Input{Path: "site/.github/.metatrash.json", Text: text(`{"purpose":"CI"}`)}, "")
	// Underscore names are fine everywhere.
	do(owned, "write", Input{Path: "notes/_drafts/a.md", Text: text("x")}, "")

	// Moves: out of the GitHub folder refused for dot names, within it fine.
	do(owned, "move", Input{From: "site/.gitignore", To: "notes/.gitignore"}, "invalid_request")
	do(owned, "move", Input{From: "site/src/.env.example", To: "site/.env.example"}, "")
	do(owned, "write", Input{Path: "notes/plain.txt", Text: text("x")}, "")
	do(owned, "move", Input{From: "notes/plain.txt", To: "site/.plain"}, "")
	do(owned, "move", Input{From: "site/.plain", To: "notes/plain.txt"}, "")

	// The github service cannot be removed, nor its config deleted, while dot names remain.
	do(owned, "write", Input{Path: "site/.metatrash.json", Text: text(`{"purpose":"no longer a repo"}`)}, "invalid_request")
	do(owned, "delete", Input{Path: "site/.metatrash.json"}, "invalid_request")
	// Changing other settings keeps the service and is fine.
	do(owned, "write", Input{Path: "site/.metatrash.json", Text: text(`{"purpose":"site","services":[{"type":"github","repo":"dave-zap/site","branch":"dev"}]}`)}, "")
	for _, path := range []string{"site/.gitignore", "site/.github/workflows/ci.yml", "site/.github/.metatrash.json", "site/.env.example"} {
		do(owned, "delete", Input{Path: path}, "")
	}
	do(owned, "delete", Input{Path: "site/.metatrash.json"}, "")
	// Plain files stay; the folder is ordinary again and refuses dot names.
	do(owned, "read", Input{Path: "site/pkg/__init__.py"}, "")
	do(owned, "write", Input{Path: "site/.gitignore", Text: text("x")}, "invalid_request")

	// A config written together with the dot name's folder decides for itself:
	// a GitHub folder's own config can sit beside dot names in a fresh folder.
	do(owned, "write", Input{Path: "app/.metatrash.json", Text: github}, "")
	do(owned, "write", Input{Path: "app/.gitignore", Text: text("node_modules/\n")}, "")
	got := do(owned, "write", Input{Path: "app/.metatrash.json", Text: text(`{"services":[{"type":"github","repo":"dave-zap/app"}]}`)}, "").(WriteResult)
	if !got.Changed || strings.Contains(got.File.Path, "..") {
		t.Fatalf("rewrite: %+v", got)
	}
}
