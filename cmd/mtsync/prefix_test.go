package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitTarget(t *testing.T) {
	for _, c := range []struct {
		target        string
		account       bool
		space, prefix string
	}{
		{"dave-zap/bartco", true, "dave-zap/bartco", ""},
		{"dave-zap/bartco/site", true, "dave-zap/bartco", "site/"},
		{"dave-zap/bartco/site/docs/", true, "dave-zap/bartco", "site/docs/"},
		{"public", true, "public", ""},
		{"public/inbox/dave", true, "public", "inbox/dave/"},
		{"0123456789abcdef0123456789abcdef/notes", true, "0123456789abcdef0123456789abcdef", "notes/"},
		{"private", false, "private", ""},
		{"private/notes", false, "private", "notes/"},
	} {
		if space, prefix := splitTarget(c.target, c.account); space != c.space || prefix != c.prefix {
			t.Errorf("%s: %q %q", c.target, space, prefix)
		}
	}
}

// Syncing a folder of a space touches only that folder, with paths relative
// to it.
func TestSyncSubfolder(t *testing.T) {
	server, _ := testSpace(t)
	agent := newClient(server, "private", func(context.Context, bool) (string, error) { return "writer", nil }, false)
	ctx := context.Background()
	put := func(path, text string) {
		t.Helper()
		state, _ := agent.head(ctx)
		if _, err := agent.write(ctx, path, text, state); err != nil {
			t.Fatal(err)
		}
	}
	put("notes/a.txt", "a\n")
	put("notes/deep/b.txt", "b\n")
	put("other/c.txt", "elsewhere\n")

	dir := t.TempDir()
	c := newClient(server, "private/notes", func(context.Context, bool) (string, error) { return "writer", nil }, false)
	s, err := openSyncer(c, dir, server, "private/notes", t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	s.settle = 0
	sync := func() {
		t.Helper()
		for i := 0; ; i++ {
			err := s.cycle(ctx)
			if err == errRestart && i < 3 {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	sync()
	if got := strings.Join(localPaths(t, dir), " "); got != "a.txt deep/b.txt" {
		t.Fatalf("subfolder download: %s", got)
	}

	// A new local file lands inside the folder; a rename stays a move there.
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	sync()
	if text, err := agent.read(ctx, "notes/new.txt", ""); err != nil || text != "new\n" {
		t.Fatalf("upload into subfolder: %q %v", text, err)
	}
	_, before, _ := agent.list(ctx)
	id := ""
	for _, f := range before {
		if f.Path == "notes/a.txt" {
			id = f.ID
		}
	}
	if err := os.Rename(filepath.Join(dir, "a.txt"), filepath.Join(dir, "deep", "a.txt")); err != nil {
		t.Fatal(err)
	}
	sync()
	_, after, _ := agent.list(ctx)
	moved := false
	for _, f := range after {
		moved = moved || (f.Path == "notes/deep/a.txt" && f.ID == id)
		if f.Path == "notes/a.txt" {
			t.Fatal("old path left after move")
		}
	}
	if !moved {
		t.Fatal("rename not a move inside the subfolder")
	}

	// A README.md inside the folder is an ordinary, deletable file.
	put("notes/README.md", "folder readme\n")
	sync()
	os.Remove(filepath.Join(dir, "README.md"))
	sync()
	if _, err := agent.read(ctx, "notes/README.md", ""); err == nil {
		t.Fatal("subfolder README not deleted")
	}

	// The rest of the space is untouched.
	if text, err := agent.read(ctx, "other/c.txt", ""); err != nil || text != "elsewhere\n" {
		t.Fatal("file outside the subfolder changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); err == nil {
		t.Fatal("space README downloaded into the subfolder")
	}
}
