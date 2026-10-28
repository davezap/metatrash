package service

import (
	"strings"
	"testing"
)

// withSpaceFiles gives a pull world the space-files bucket, so it keeps
// virtual files.
func (w *pullWorld) withSpaceFiles() *memoryObjects {
	w.t.Helper()
	settings := testAgentSettings(w.t)
	objects := &memoryObjects{objects: map[string][]byte{}}
	settings.downloads = objects
	runner, err := newAgentRunner(w.s, settings, &memoryJobStore{jobs: map[string]agentJob{}})
	if err != nil {
		w.t.Fatal(err)
	}
	w.s.agents.Store(runner)
	w.t.Cleanup(func() { w.s.agents.Store(nil) })
	w.repo.id = strings.Repeat("ab", 16)
	return objects
}

// 0.61.0: GitHub folders hold binary files, and text over the file limit,
// as virtual files, and push sends them back as blobs.
func TestGitHubVirtualFiles(t *testing.T) {
	w := newPullWorld(t, &Storage{MaxFileBytes: 1000, MaxRequestBytes: 524288, MaxFiles: 100, MaxCurrentTextBytes: 100000, MaxRepositoryBytes: 1 << 26})
	objects := w.withSpaceFiles()
	w.enablePush()
	logo := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	big := strings.Repeat("A long line of text.\n", 100) // over the 1000-byte limit
	files := map[string]fakeRepoFile{
		"README.md":    {text: "# Repo\n"},
		"logo.png":     {text: logo},
		"data/big.txt": {text: big},
		"latin1.txt":   {text: "caf\xe9\n"},
	}
	w.host.commit("dave-zap/bin", "main", files)
	w.write("repo/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/bin"}]}`)
	pulled := w.pull("repo/", "")
	if pulled.Created != 4 || pulled.SkippedCount != 0 {
		t.Fatalf("pull: %+v", pulled)
	}
	held := w.files("repo/")
	for path, content := range map[string]string{"repo/logo.png": logo, "repo/data/big.txt": big, "repo/latin1.txt": "caf\xe9\n"} {
		f := held[path]
		if f.Virtual == nil || f.Virtual.GitBlob != blobHash([]byte(content)) || string(objects.objects[f.Virtual.key(w.repo.id)]) != content {
			t.Fatalf("%s: %+v", path, f.Virtual)
		}
	}
	if held["repo/README.md"].Virtual != nil {
		t.Fatal("small text became virtual")
	}
	if got := w.read("repo/data/big.txt"); got != big {
		t.Fatalf("big text reads %d bytes", len(got))
	}
	if w.host.rawFetches != 1 {
		t.Fatalf("raw fetches %d (only the file over the limit)", w.host.rawFetches)
	}
	b, _ := w.baseline("repo/")
	if b.Files["logo.png"] != blobHash([]byte(logo)) {
		t.Fatalf("baseline holds %s", b.Files["logo.png"])
	}

	// Nothing pending: pointers are compared by content.
	if p := w.pending("repo/"); !p.UpToDate {
		t.Fatalf("pending after pull: %+v", p.Changes)
	}
	// A pointer-only change (a new source line) is no change either.
	f := held["repo/logo.png"]
	info := *f.Virtual
	info.Source = "https://example.com/logo.png"
	pointer := info.pointer()
	w.do("write", Input{Path: "repo/logo.png", Text: &pointer, Virtual: &info}, "")
	if p := w.pending("repo/"); !p.UpToDate {
		t.Fatalf("pending after a pointer change: %+v", p.Changes)
	}

	// A new picture uploaded here is pushed as a blob.
	head, _ := w.repo.head(w.ctx)
	icon := "\x89PNG\r\n\x1a\n\x00icon"
	if _, err := w.s.putVirtual(w.ctx, w.repo, "test", "repo/icon.png", head, []byte(icon), "image/png", "", ""); err != nil {
		t.Fatal(err)
	}
	p := w.pending("repo/")
	if c := changeMap(p.Changes)["repo/icon.png"]; c.Change != "added" || !strings.Contains(c.Note, "virtual file") || len(p.NotPushed) != 0 {
		t.Fatalf("pending: %+v", p)
	}
	pushed := w.push("repo/", "Add icon", "", "")
	if pushed.Added != 1 {
		t.Fatalf("push: %+v", pushed)
	}
	_, commit := w.host.head("dave-zap/bin", "main")
	if commit.files["icon.png"].text != icon || commit.files["logo.png"].text != logo {
		t.Fatal("GitHub does not hold the pictures")
	}
	if p := w.pending("repo/"); !p.UpToDate {
		t.Fatalf("pending after push: %+v", p.Changes)
	}

	// GitHub replacing the binary with text makes it an ordinary file.
	next := map[string]fakeRepoFile{}
	for p, f := range commit.files {
		next[p] = f
	}
	next["logo.png"] = fakeRepoFile{text: "now text\n"}
	w.host.commit("dave-zap/bin", "main", next)
	w.pull("repo/", "")
	if f := w.files("repo/")["repo/logo.png"]; f.Virtual != nil || w.read("repo/logo.png") != "now text\n" {
		t.Fatalf("text over a virtual file: %+v", f)
	}
}

// Without the bucket, binary files are skipped as before.
func TestGitHubPullWithoutSpaceFiles(t *testing.T) {
	w := newPullWorld(t, nil)
	w.host.commit("dave-zap/bin", "main", map[string]fakeRepoFile{"README.md": {text: "# Repo\n"}, "logo.png": {text: "\x89PNG\r\n\x1a\n\x00"}})
	w.write("repo/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/bin"}]}`)
	if pulled := w.pull("repo/", ""); pulled.SkippedCount != 1 || pulled.Skipped[0].Reason != "binary file" {
		t.Fatalf("pull: %+v", pulled)
	}
}
