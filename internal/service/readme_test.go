package service

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRefreshREADME(t *testing.T) {
	ctx := context.Background()
	gitDir := filepath.Join(t.TempDir(), "public.git")
	limits := Storage{MaxFileBytes: 65536, MaxRequestBytes: 65536, MaxFiles: 100, MaxCurrentTextBytes: 1 << 20, MaxRepositoryBytes: 1 << 24}
	r, err := provision(ctx, gitDir, limits, []byte("# Old\n\nMCP endpoint `/mcp` is pending.\n"), "public", false)
	if err != nil {
		t.Fatal(err)
	}
	before, files, err := r.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	id := files["README.md"].ID
	others := len(files)

	readme := []byte("# Agent shared files\n\nMCP endpoint: `/mcp` (Streamable HTTP).\n")
	state, changed, err := RefreshREADME(ctx, gitDir, readme)
	if err != nil || !changed || state == before {
		t.Fatalf("refresh = %s %v %v", state, changed, err)
	}
	head, files, err := r.snapshot(ctx, "")
	if err != nil || head != state {
		t.Fatalf("head %s, want %s: %v", head, state, err)
	}
	f := files["README.md"]
	if f.ID != id || !f.Protected || f.Bytes != int64(len(readme)) || len(files) != others {
		t.Fatalf("README record %+v, %d files (want ID %s, %d files)", f, len(files), id, others)
	}
	text, err := r.blob(ctx, f.Blob)
	if err != nil || string(text) != string(readme) {
		t.Fatalf("README text %q: %v", text, err)
	}
	out, err := git(ctx, gitDir, "", nil, "log", "-1", "--format=%B", state)
	if err != nil || string(out) != id+" write README.md\n\n" {
		t.Fatalf("commit message %q: %v", out, err)
	}

	again, changed, err := RefreshREADME(ctx, gitDir, readme)
	if err != nil || changed || again != state {
		t.Fatalf("second refresh = %s %v %v, want no change at %s", again, changed, err, state)
	}
	if _, _, err := RefreshREADME(ctx, filepath.Join(t.TempDir(), "missing.git"), readme); err == nil {
		t.Fatal("missing repository accepted")
	}
}
