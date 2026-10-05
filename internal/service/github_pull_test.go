package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"os/exec"

	"metatrash.com/metatrash"
)

// fakeRepoFile is one entry in a fake repository: a blob (mode 100644,
// 100755 or 120000) or a submodule (mode 160000).
type fakeRepoFile struct {
	text string
	mode string
}

type fakeCommit struct {
	tree    string
	files   map[string]fakeRepoFile
	parents []string
	message string
	author  map[string]string
	// committerSet records a request that set a committer.
	committerSet bool
}

type fakeRepo struct {
	defaultBranch string
	branches      map[string]string // branch -> commit
	commits       map[string]fakeCommit
	// exportIgnore files are left out of the tarball; subst files are
	// changed in it (as export-subst would), so both need the blob API.
	exportIgnore, subst map[string]bool
}

// fakeRepoHost is a fake GitHub serving repositories for pull and push.
type fakeRepoHost struct {
	t           *testing.T
	server      *httptest.Server
	mu          sync.Mutex
	repos       map[string]*fakeRepo
	counter     int
	blobFetches int
	tarballs    int
	tokens      []string
	// trees holds every tree by hash (commits and createTree results).
	trees map[string]map[string]fakeRepoFile
	// gitDir is a scratch repository in which real git computes tree hashes.
	gitDir string
	// writes counts write calls; beforeRef runs before a ref update.
	writes    int
	beforeRef func()
	// treeOverride, when set, replaces the hash createTree answers.
	treeOverride string
}

func newFakeRepoHost(t *testing.T) *fakeRepoHost {
	f := &fakeRepoHost{t: t, repos: map[string]*fakeRepo{}, trees: map[string]map[string]fakeRepoFile{}, gitDir: filepath.Join(t.TempDir(), "trees.git")}
	if out, err := exec.Command("git", "init", "-q", "--bare", f.gitDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// treeHash asks real git for the tree hash of files.
func (f *fakeRepoHost) treeHash(files map[string]fakeRepoFile) string {
	var list bytes.Buffer
	for p, file := range files {
		fmt.Fprintf(&list, "%s %s\t%s\x00", file.mode, f.entrySHA(file), p)
	}
	index := filepath.Join(f.t.TempDir(), "index")
	run := func(input []byte, args ...string) string {
		cmd := exec.Command("git", append([]string{"--git-dir=" + f.gitDir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			f.t.Errorf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(list.Bytes(), "update-index", "-z", "--add", "--index-info")
	return run(nil, "write-tree", "--missing-ok")
}

func (f *fakeRepoHost) apiBase() string { return f.server.URL + "/api" }

func fakeSHA(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])
}

// commit makes files the new head of branch.
func (f *fakeRepoHost) commit(repo, branch string, files map[string]fakeRepoFile) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commitLocked(repo, branch, files)
}

// commitLocked is commit for callers holding f.mu (beforeRef hooks).
func (f *fakeRepoHost) commitLocked(repo, branch string, files map[string]fakeRepoFile) string {
	r := f.repos[repo]
	if r == nil {
		r = &fakeRepo{defaultBranch: "main", branches: map[string]string{}, commits: map[string]fakeCommit{}, exportIgnore: map[string]bool{}, subst: map[string]bool{}}
		f.repos[repo] = r
	}
	copied := map[string]fakeRepoFile{}
	for p, file := range files {
		if file.mode == "" {
			file.mode = "100644"
		}
		copied[p] = file
	}
	f.counter++
	tree := f.treeHash(copied)
	f.trees[tree] = copied
	commit := fakeSHA("commit", tree, fmt.Sprint(f.counter))
	var parents []string
	if old, ok := r.branches[branch]; ok {
		parents = []string{old}
	}
	r.commits[commit] = fakeCommit{tree: tree, files: copied, parents: parents}
	r.branches[branch] = commit
	return commit
}

func (f *fakeRepoHost) entrySHA(file fakeRepoFile) string {
	if file.mode == "160000" {
		return fakeSHA("submodule", file.text)
	}
	return blobHash([]byte(file.text))
}

func (f *fakeRepoHost) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api")
	if strings.HasPrefix(path, "/app/installations/") && strings.HasSuffix(path, "/access_tokens") {
		if r.Method != "POST" || strings.Count(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".") != 2 {
			w.WriteHeader(401)
			return
		}
		var body struct {
			Repositories []string          `json:"repositories"`
			Permissions  map[string]string `json:"permissions"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Repositories) != 1 || len(body.Permissions) != 1 {
			w.WriteHeader(400)
			return
		}
		access := body.Permissions["contents"]
		if access != "read" && access != "write" {
			w.WriteHeader(400)
			return
		}
		if body.Repositories[0] == "denied" || access == "write" && body.Repositories[0] == "readonly" {
			w.WriteHeader(422)
			return
		}
		token := "ghs_" + body.Repositories[0]
		if access == "write" {
			token = "ghw_" + body.Repositories[0]
		}
		f.tokens = append(f.tokens, token)
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"token":%q,"expires_at":"2026-10-05T03:00:00Z"}`, token)
		return
	}
	if strings.HasPrefix(path, "/codeload/") {
		// The archive host: no token may be sent here.
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(400)
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "/codeload/"), "/")
		repo := f.repos[parts[0]+"/"+parts[1]]
		c, ok := repo.commits[parts[2]]
		if !ok {
			w.WriteHeader(404)
			return
		}
		f.tarballs++
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		top := strings.ReplaceAll(parts[0]+"-"+parts[1], "/", "-") + "-" + parts[2][:7] + "/"
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": parts[2]}})
		_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: top, Mode: 0775})
		for p, file := range c.files {
			if repo.exportIgnore[p] || file.mode == "160000" {
				continue
			}
			text := file.text
			if repo.subst[p] {
				text = strings.ReplaceAll(text, "$Format:%H$", parts[2])
			}
			h := &tar.Header{Typeflag: tar.TypeReg, Name: top + p, Mode: 0664, Size: int64(len(text))}
			if file.mode == "120000" {
				h = &tar.Header{Typeflag: tar.TypeSymlink, Name: top + p, Linkname: text}
			}
			_ = tw.WriteHeader(h)
			if h.Typeflag == tar.TypeReg {
				_, _ = tw.Write([]byte(text))
			}
		}
		_ = tw.Close()
		_ = gz.Close()
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(buf.Bytes())
		return
	}
	if !strings.HasPrefix(path, "/repos/") {
		w.WriteHeader(404)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(path, "/repos/"), "/", 3)
	name := parts[0] + "/" + parts[1]
	repo := f.repos[name]
	auth := r.Header.Get("Authorization")
	if auth != "Bearer ghs_"+parts[1] && auth != "Bearer ghw_"+parts[1] || r.Header.Get("X-GitHub-Api-Version") == "" {
		w.WriteHeader(401)
		return
	}
	if r.Method != http.MethodGet {
		// Writes need a token with contents: write.
		if auth != "Bearer ghw_"+parts[1] || repo == nil {
			w.WriteHeader(403)
			return
		}
		f.serveWrite(w, r, name, repo, parts)
		return
	}
	if repo == nil {
		w.WriteHeader(404)
		return
	}
	rest := ""
	if len(parts) == 3 {
		rest = parts[2]
	}
	switch {
	case rest == "":
		fmt.Fprintf(w, `{"full_name":%q,"default_branch":%q}`, name, repo.defaultBranch)
	case strings.HasPrefix(rest, "branches/"):
		commit, ok := repo.branches[strings.TrimPrefix(rest, "branches/")]
		if !ok {
			w.WriteHeader(404)
			return
		}
		fmt.Fprintf(w, `{"name":"x","commit":{"sha":%q,"commit":{"tree":{"sha":%q}}}}`, commit, repo.commits[commit].tree)
	case strings.HasPrefix(rest, "git/trees/"):
		tree := strings.TrimPrefix(rest, "git/trees/")
		if r.URL.Query().Get("recursive") != "1" {
			w.WriteHeader(400)
			return
		}
		if files, ok := f.trees[tree]; ok {
			entries := []githubTreeEntry{}
			dirs := map[string]bool{}
			for p, file := range files {
				for i, c := range p {
					if c == '/' {
						dirs[p[:i]] = true
					}
				}
				e := githubTreeEntry{Path: p, Mode: file.mode, Type: "blob", SHA: f.entrySHA(file), Size: int64(len(file.text))}
				if file.mode == "160000" {
					e.Type, e.Size = "commit", 0
				}
				entries = append(entries, e)
			}
			for d := range dirs {
				entries = append(entries, githubTreeEntry{Path: d, Mode: "040000", Type: "tree", SHA: fakeSHA("dir", d)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sha": tree, "tree": entries, "truncated": false})
			return
		}
		w.WriteHeader(404)
	case strings.HasPrefix(rest, "tarball/"):
		w.Header().Set("Location", f.apiBase()+"/codeload/"+name+"/"+strings.TrimPrefix(rest, "tarball/"))
		w.WriteHeader(302)
	case strings.HasPrefix(rest, "git/blobs/"):
		sha := strings.TrimPrefix(rest, "git/blobs/")
		for _, c := range repo.commits {
			for _, file := range c.files {
				if file.mode != "160000" && blobHash([]byte(file.text)) == sha {
					f.blobFetches++
					fmt.Fprintf(w, `{"sha":%q,"encoding":"base64","content":%q}`, sha, base64.StdEncoding.EncodeToString([]byte(file.text)))
					return
				}
			}
		}
		w.WriteHeader(404)
	default:
		w.WriteHeader(404)
	}
}

// pullWorld is an owned space wired to a fake GitHub.
type pullWorld struct {
	t    *testing.T
	s    *Service
	repo *repository
	host *fakeRepoHost
	ctx  context.Context
}

func newPullWorld(t *testing.T, storage *Storage) *pullWorld {
	t.Helper()
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
	t.Cleanup(s.Close)
	limits := s.config.Defaults.Storage
	if storage != nil {
		limits = *storage
	}
	ctx := context.Background()
	repo, err := provision(ctx, filepath.Join(dir, "owned.git"), limits, []byte(ownedSpaceREADME), "Pulls", true)
	if err != nil {
		t.Fatal(err)
	}
	host := newFakeRepoHost(t)
	cfg := githubTestConfig(t, pkcs1PEM(githubTestKey(t)))
	settings, err := cfg.settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.webBase, settings.apiBase = host.server.URL+"/web", host.apiBase()
	s.githubSourceHook = func(ctx context.Context, r *repository, repo string) (githubSource, error) {
		client := newGitHubClient(settings)
		token, err := client.installationToken(ctx, 77, repo, "read")
		if err != nil {
			return nil, err
		}
		return &githubRepoReader{client: client, token: token, repo: repo}, nil
	}
	return &pullWorld{t: t, s: s, repo: repo, host: host, ctx: ctx}
}

var pullSpaceCounter int

// pull runs pull and checks the error code ("" for success).
func (w *pullWorld) pull(folder, code string) PullResult {
	w.t.Helper()
	pullSpaceCounter++ // a fresh rate bucket per call
	value, err := w.s.pull(w.ctx, w.repo, fmt.Sprintf("test-%d", pullSpaceCounter), folder)
	if code == "" {
		if err != nil {
			w.t.Fatalf("pull %s: %v", folder, err)
		}
		return value.(PullResult)
	}
	if p, ok := err.(*Error); !ok || p.Code != code {
		w.t.Fatalf("pull %s: got %v, want %s", folder, err, code)
	}
	return PullResult{}
}

func (w *pullWorld) pullMessage(folder string) string {
	w.t.Helper()
	pullSpaceCounter++
	_, err := w.s.pull(w.ctx, w.repo, fmt.Sprintf("test-%d", pullSpaceCounter), folder)
	if err == nil {
		w.t.Fatalf("pull %s succeeded", folder)
	}
	return err.Error()
}

func (w *pullWorld) do(op string, in Input, code string) any {
	w.t.Helper()
	head, err := w.repo.head(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	in.IfInState = head
	value, err := w.s.dispatchRepository(w.ctx, w.repo, "test", op, in)
	if code == "" && err != nil {
		w.t.Fatalf("%s %s%s: %v", op, in.Path, in.From, err)
	}
	if code != "" {
		if p, ok := err.(*Error); !ok || p.Code != code {
			w.t.Fatalf("%s %s%s: got %v, want %s", op, in.Path, in.From, err, code)
		}
	}
	return value
}

func (w *pullWorld) read(path string) string {
	w.t.Helper()
	return w.do("read", Input{Path: path}, "").(ReadResult).File.Text
}

func (w *pullWorld) write(path, text string) {
	w.t.Helper()
	w.do("write", Input{Path: path, Text: &text}, "")
}

func (w *pullWorld) files(prefix string) map[string]File {
	w.t.Helper()
	_, files, err := w.repo.snapshot(w.ctx, "")
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]File{}
	for p, f := range files {
		if strings.HasPrefix(p, prefix) {
			out[p] = f.File
		}
	}
	return out
}

func (w *pullWorld) baseline(folder string) (githubBaseline, bool) {
	w.t.Helper()
	head, err := w.repo.head(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	all, err := w.repo.githubBaselines(w.ctx, head)
	if err != nil {
		w.t.Fatal(err)
	}
	b, ok := all[folder]
	return b, ok
}

func skippedReasons(r PullResult) map[string]string {
	out := map[string]string{}
	for _, s := range r.Skipped {
		out[s.Path] = s.Reason
	}
	return out
}

func siteFiles() map[string]fakeRepoFile {
	deep := strings.Repeat("d/", 8) + "too-deep.md"
	return map[string]fakeRepoFile{
		"README.md":                {text: "# Site\n"},
		"index.html":               {text: "<h1>Hello</h1>\n"},
		"docs/guide.md":            {text: "Guide\n"},
		".gitignore":               {text: "node_modules/\n"},
		".github/workflows/ci.yml": {text: "on: push\n"},
		"run.sh":                   {text: "#!/bin/sh\necho hi\n", mode: "100755"},
		"logo.png":                 {text: "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"},
		"latin1.txt":               {text: "caf\xe9\n"},
		"utf16.md":                 {text: "\xff\xfe#\x00 \x00x\x00\r\x00\n\x00"},
		"big.txt":                  {text: strings.Repeat("x", 70000)},
		"My File.txt":              {text: "spaces\n"},
		"-dash.md":                 {text: "dash\n"},
		deep:                       {text: "deep\n"},
		"current":                  {text: "index.html", mode: "120000"},
		"vendor/lib":               {text: "libcommit", mode: "160000"},
		".metatrash.json":          {text: `{"purpose":"from the repo"}`},
		"docs/.metatrash.json":     {text: `{"purpose":"docs"}`},
		"export/ignored.txt":       {text: "left out of archives\n"},
		"version.txt":              {text: "built from $Format:%H$\n"},
	}
}

func TestGitHubPull(t *testing.T) {
	w := newPullWorld(t, nil)
	first := w.host.commit("dave-zap/site", "main", siteFiles())
	w.host.repos["dave-zap/site"].exportIgnore["export/ignored.txt"] = true
	w.host.repos["dave-zap/site"].subst["version.txt"] = true

	// Only GitHub folders pull; the folder argument is a folder path.
	w.write("notes/a.md", "a")
	w.pull("notes/", "invalid_request")
	w.pull("../x", "invalid_request")
	w.pull("site", "invalid_request")
	if b, _ := w.baseline("site/"); b.Commit != "" {
		t.Fatal("baseline without a pull")
	}

	// A first pull refuses files that do not match the repository, naming them.
	w.write("site/.metatrash.json", `{"purpose":"the website","services":[{"type":"github","repo":"dave-zap/site"}]}`)
	w.write("site/README.md", "# Site\n")       // identical: fine
	w.write("site/index.html", "<h1>Hi</h1>\n") // differs
	w.write("site/todo.md", "mine\n")           // not in the repo
	w.write("site/sub/.metatrash.json", `{"purpose":"settings stay"}`)
	msg := w.pullMessage("site/")
	for _, want := range []string{"site/index.html (differs from the repository)", "site/todo.md (not in the repository)", "first pull"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("clash message %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "README.md") || strings.Contains(msg, "sub/") {
		t.Fatalf("clash message names matching files: %q", msg)
	}
	w.do("delete", Input{Path: "site/index.html"}, "")
	w.do("delete", Input{Path: "site/todo.md"}, "")
	readmeID := w.files("site/")["site/README.md"].ID

	// The first pull.
	got := w.pull("site/", "")
	kept := []string{"README.md", "index.html", "docs/guide.md", ".gitignore", ".github/workflows/ci.yml", "run.sh", "export/ignored.txt", "version.txt"}
	// utf16.md is held converted to UTF-8 (with a UTF-8 byte order mark).
	const utf16Converted = "\xef\xbb\xbf# x\r\n"
	if got.Commit != first || got.UpToDate || got.Created != len(kept) || got.Unchanged != 1 || got.Updated != 0 || got.Deleted != 0 || got.NewState == got.OldState || got.Folder != "site/" || got.Repo != "dave-zap/site" || got.Branch != "main" {
		t.Fatalf("first pull: %+v", got)
	}
	reasons := skippedReasons(got)
	wantSkipped := map[string]string{
		"logo.png": "binary file", "latin1.txt": "Latin-1", "big.txt": "larger than", "My File.txt": "name not allowed", "-dash.md": "name not allowed",
		strings.Repeat("d/", 8) + "too-deep.md": "name not allowed", "current": "symbolic link", "vendor/lib": "submodule",
		".metatrash.json": "settings file", "docs/.metatrash.json": "settings file",
	}
	if got.SkippedCount != len(wantSkipped) || len(reasons) != len(wantSkipped) {
		t.Fatalf("skipped: %d %v", got.SkippedCount, reasons)
	}
	for p, want := range wantSkipped {
		if !strings.Contains(reasons[p], want) {
			t.Fatalf("skipped %s: %q, want %q", p, reasons[p], want)
		}
	}
	for _, p := range kept {
		want := siteFiles()[p].text
		if got := w.read("site/" + p); got != want {
			t.Fatalf("site/%s: %q, want %q", p, got, want)
		}
	}
	if text := w.read("site/utf16.md"); text != utf16Converted {
		t.Fatalf("converted: %q", text)
	}
	if got.ConvertedCount != 1 || len(got.Converted) != 1 || got.Converted[0] != (PullConverted{"site/utf16.md", "UTF-16 LE"}) {
		t.Fatalf("converted list: %+v", got.Converted)
	}
	files := w.files("site/")
	if files["site/README.md"].ID != readmeID {
		t.Fatal("identical file lost its ID")
	}
	if _, ok := files["site/sub/.metatrash.json"]; !ok {
		t.Fatal("folder settings file removed")
	}
	if len(files) != len(kept)+3 {
		t.Fatalf("folder holds %d files: %v", len(files), files)
	}
	// Archives leave out export-ignore files and change export-subst ones:
	// those two come from the blob API.
	if w.host.tarballs != 1 || w.host.blobFetches != 2 {
		t.Fatalf("tarballs %d, blob fetches %d", w.host.tarballs, w.host.blobFetches)
	}
	base, ok := w.baseline("site/")
	if !ok || base.Commit != first || base.Repo != "dave-zap/site" || base.Branch != "main" || len(base.Files) != len(kept)+1 || base.Modes["run.sh"] != "100755" || len(base.Modes) != 1 || len(base.Skipped) != len(wantSkipped) {
		t.Fatalf("baseline %+v", base)
	}
	for _, sk := range base.Skipped {
		if sk.Path == "vendor/lib" && sk.Mode != "160000" || sk.Path == "logo.png" && sk.Blob != blobHash([]byte(siteFiles()["logo.png"].text)) {
			t.Fatalf("skipped record %+v", sk)
		}
	}
	for rel, blob := range base.Files {
		if rel == "utf16.md" {
			if blob != blobHash([]byte(utf16Converted)) || base.Converted[rel] != (githubConverted{blobHash([]byte(siteFiles()[rel].text)), "UTF-16 LE"}) {
				t.Fatalf("converted baseline %s %+v", blob, base.Converted)
			}
			continue
		}
		if blob != blobHash([]byte(siteFiles()[rel].text)) {
			t.Fatalf("baseline blob for %s", rel)
		}
	}
	// The recorded baseline rebuilds GitHub's tree exactly.
	if tree, err := gitTreeHash(base.githubTree()); err != nil || tree != base.Tree {
		t.Fatalf("baseline tree %s %v, want %s", tree, err, base.Tree)
	}
	// History shows each pulled file once, as a create in the pull commit.
	guide := files["site/docs/guide.md"]
	h := w.do("history", Input{ID: guide.ID}, "").(HistoryResult)
	if len(h.Entries) != 1 || h.Entries[0].Operation != "create" || h.Entries[0].Revision != got.NewState || h.Entries[0].Path != "site/docs/guide.md" {
		t.Fatalf("history %+v", h.Entries)
	}
	h = w.do("history", Input{ID: readmeID}, "").(HistoryResult)
	if len(h.Entries) != 1 || h.Entries[0].Operation != "create" {
		t.Fatalf("unchanged file history %+v", h.Entries)
	}
	// The baseline is the service's own: agents cannot read or list it.
	w.do("read", Input{Path: ".metatrash/github.json"}, "invalid_request")
	for p := range w.files("") {
		if strings.HasPrefix(p, ".metatrash/") {
			t.Fatalf("listed %s", p)
		}
	}

	// Writes elsewhere carry the baseline forward.
	w.write("notes/b.md", "b")
	w.do("delete", Input{Path: "notes/b.md"}, "")
	if b, ok := w.baseline("site/"); !ok || b.Commit != first {
		t.Fatal("baseline lost by a later write")
	}

	// Nothing new on GitHub: up to date, nothing written.
	head, _ := w.repo.head(w.ctx)
	again := w.pull("site/", "")
	if !again.UpToDate || again.NewState != head || again.OldState != head || again.Unchanged != len(kept)+1 || again.ConvertedCount != 1 || again.SkippedCount != len(wantSkipped) || again.Created+again.Updated+again.Deleted != 0 {
		t.Fatalf("up to date: %+v", again)
	}
	if after, _ := w.repo.head(w.ctx); after != head {
		t.Fatal("up-to-date pull wrote")
	}

	// GitHub moves on: an unchanged folder follows it.
	next := siteFiles()
	next["README.md"] = fakeRepoFile{text: "# Site v2\n"}
	next["run.sh"] = fakeRepoFile{text: "#!/bin/sh\necho v2\n", mode: "100755"}
	delete(next, "docs/guide.md")
	next["docs/new.md"] = fakeRepoFile{text: "New\n"}
	delete(next, "logo.png")
	second := w.host.commit("dave-zap/site", "main", next)
	upd := w.pull("site/", "")
	if upd.Commit != second || upd.Updated != 2 || upd.Created != 1 || upd.Deleted != 1 || upd.Unchanged != len(kept)-2 || upd.SkippedCount != len(wantSkipped)-1 {
		t.Fatalf("update: %+v", upd)
	}
	files = w.files("site/")
	if files["site/README.md"].ID != readmeID || w.read("site/README.md") != "# Site v2\n" || w.read("site/docs/new.md") != "New\n" {
		t.Fatal("update content or ID")
	}
	if _, ok := files["site/docs/guide.md"]; ok {
		t.Fatal("file deleted on GitHub still here")
	}
	h = w.do("history", Input{ID: guide.ID}, "").(HistoryResult)
	if len(h.Entries) != 2 || h.Entries[0].Operation != "delete" || h.Entries[0].Revision != upd.NewState {
		t.Fatalf("deleted file history %+v", h.Entries)
	}
	h = w.do("history", Input{ID: readmeID}, "").(HistoryResult)
	if len(h.Entries) != 2 || h.Entries[0].Operation != "write" {
		t.Fatalf("updated file history %+v", h.Entries)
	}

	// Changes in the folder do not stop a pull when GitHub has nothing new:
	// they stay, and are counted.
	w.write("site/README.md", "# local edit\n")
	w.write("site/extra.md", "extra\n")
	w.do("delete", Input{Path: "site/docs/new.md"}, "")
	if r := w.pull("site/", ""); !r.UpToDate || r.LocalChanges != 3 || r.Unchanged != len(kept)-1 {
		t.Fatalf("local changes, nothing new: %+v", r)
	}
	// Folder settings files are not repository files; changing them is fine.
	w.write("site/sub/.metatrash.json", `{"purpose":"still fine"}`)
	w.write("site/README.md", "# Site v2\n")
	w.do("delete", Input{Path: "site/extra.md"}, "")
	w.write("site/docs/new.md", "New\n")
	if r := w.pull("site/", ""); !r.UpToDate || r.LocalChanges != 0 {
		t.Fatalf("restored folder: %+v", r)
	}

	// Another branch that does not exist: the message names the default branch.
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site","branch":"dev"}]}`)
	if msg := w.pullMessage("site/"); !strings.Contains(msg, `no branch "dev"`) || !strings.Contains(msg, `default branch is "main"`) {
		t.Fatalf("missing branch: %q", msg)
	}
	// A different repository or branch starts over as a first pull, so the
	// files from the old one clash.
	w.host.commit("dave-zap/site", "dev", map[string]fakeRepoFile{"README.md": {text: "# dev\n"}})
	if msg := w.pullMessage("site/"); !strings.Contains(msg, "first pull") || !strings.Contains(msg, "site/README.md (differs") {
		t.Fatalf("branch change: %q", msg)
	}
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site"}]}`)
	if r := w.pull("site/", ""); !r.UpToDate {
		t.Fatalf("back on main: %+v", r)
	}

	// Repositories the app cannot reach, and ones that do not exist.
	w.write("other/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/denied"}]}`)
	if msg := w.pullMessage("other/"); !strings.Contains(msg, "cannot reach dave-zap/denied") {
		t.Fatalf("denied: %q", msg)
	}
	w.write("other/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/nothing"}]}`)
	w.pull("other/", "forbidden")
	w.host.commit("dave-zap/empty", "seed", map[string]fakeRepoFile{"a": {text: "a"}})
	w.write("other/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/empty"}]}`)
	if msg := w.pullMessage("other/"); !strings.Contains(msg, "is empty") {
		t.Fatalf("empty: %q", msg)
	}

	// A second GitHub folder pulls beside the first; both baselines stay.
	w.host.commit("dave-zap/app", "main", map[string]fakeRepoFile{"main.go": {text: "package main\n"}})
	w.write("deps/app/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/app"}]}`)
	if r := w.pull("deps/app", ""); r.Created != 1 || w.read("deps/app/main.go") != "package main\n" {
		t.Fatalf("second folder: %+v", r)
	}
	if b, ok := w.baseline("site/"); !ok || b.Commit != second {
		t.Fatal("first baseline lost")
	}

	// The folder changing while GitHub is being read stops the pull.
	third := siteFiles()
	third["README.md"] = fakeRepoFile{text: "# Site v3\n"}
	w.host.commit("dave-zap/site", "main", third)
	real := w.s.githubSourceHook
	w.s.githubSourceHook = func(ctx context.Context, r *repository, repo string) (githubSource, error) {
		src, err := real(ctx, r, repo)
		return racingSource{src, func() { w.write("site/index.html", "<h1>raced</h1>\n") }}, err
	}
	if msg := w.pullMessage("site/"); !strings.Contains(msg, "changed during the pull") {
		t.Fatalf("race: %q", msg)
	}
	w.s.githubSourceHook = real
	// The repository stays sound after many multi-file commits.
	if out, err := git(w.ctx, w.repo.path, "", nil, "fsck", "--strict", "--no-dangling"); err != nil {
		t.Fatalf("fsck: %v %s", err, out)
	}
}

// racingSource changes the space while contents are being read.
type racingSource struct {
	githubSource
	during func()
}

func (r racingSource) contents(ctx context.Context, commit string, want map[string]string) (map[string][]byte, error) {
	r.during()
	return r.githubSource.contents(ctx, commit, want)
}

func TestGitHubPullStorageLimits(t *testing.T) {
	w := newPullWorld(t, &Storage{MaxFileBytes: 1000, MaxRequestBytes: 524288, MaxFiles: 8, MaxCurrentTextBytes: 4000, MaxRepositoryBytes: 1 << 26})
	files := map[string]fakeRepoFile{}
	for i := 0; i < 6; i++ {
		files[fmt.Sprintf("f%d.md", i)] = fakeRepoFile{text: fmt.Sprintf("file %d\n", i)}
	}
	w.host.commit("dave-zap/small", "main", files)
	w.write("repo/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/small"}]}`)
	// README.md, root .metatrash.json, repo/.metatrash.json + 6 = 9 > 8 files.
	if msg := w.pullMessage("repo/"); !strings.Contains(msg, "does not fit") || !strings.Contains(msg, "9 files") {
		t.Fatalf("file count: %q", msg)
	}
	delete(files, "f5.md")
	files["big.md"] = fakeRepoFile{text: strings.Repeat("b", 999)}
	files["huge.md"] = fakeRepoFile{text: strings.Repeat("h", 1001)}
	delete(files, "f4.md")
	w.host.commit("dave-zap/small", "main", files)
	r := w.pull("repo/", "")
	if r.Created != 5 || skippedReasons(r)["huge.md"] == "" {
		t.Fatalf("fits: %+v", r)
	}
	// Text bytes: 4 more files of 999 bytes would pass the count but not the bytes.
	more := map[string]fakeRepoFile{}
	for i := 0; i < 4; i++ {
		more[fmt.Sprintf("g%d.md", i)] = fakeRepoFile{text: strings.Repeat("g", 999)}
	}
	w.host.commit("dave-zap/bytes", "main", more)
	w.write("repo/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/bytes"}]}`)
	w.do("delete", Input{Path: "repo/big.md"}, "")
	for i := 0; i < 4; i++ {
		w.do("delete", Input{Path: fmt.Sprintf("repo/f%d.md", i)}, "")
	}
	if msg := w.pullMessage("repo/"); !strings.Contains(msg, "does not fit") || !strings.Contains(msg, "3996 bytes") {
		t.Fatalf("bytes: %q", msg)
	}
}

func TestGitHubPullRateLimit(t *testing.T) {
	w := newPullWorld(t, nil)
	w.host.commit("dave-zap/site", "main", map[string]fakeRepoFile{"a.md": {text: "a"}})
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site"}]}`)
	for i := 0; i < githubPullsPerSpace; i++ {
		if _, err := w.s.pull(w.ctx, w.repo, "same", "site/"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.pull(w.ctx, w.repo, "same", "site/"); err == nil || err.(*Error).Code != "rate_limited" {
		t.Fatalf("rate limit: %v", err)
	}
}

func TestClassifyRepoEntries(t *testing.T) {
	sha := strings.Repeat("a", 40)
	keep, skipped := classifyRepoEntries("x/", []githubTreeEntry{
		{Path: "ok.md", Mode: "100644", Type: "blob", SHA: sha, Size: 1},
		{Path: "dir", Mode: "040000", Type: "tree", SHA: sha},
		{Path: ".metatrash.json/inside.md", Mode: "100644", Type: "blob", SHA: sha, Size: 1},
		{Path: "bad sha", Mode: "100644", Type: "blob", SHA: "nope"},
		{Path: "odd", Mode: "100664", Type: "blob", SHA: sha},
		{Path: ".git/config", Mode: "100644", Type: "blob", SHA: sha},
	}, 100)
	if len(keep) != 1 || keep["ok.md"].SHA != sha || len(skipped) != 4 {
		t.Fatalf("keep %v skipped %v", keep, skipped)
	}
	for content, want := range map[string]string{
		"\xff\xfe\x00\xd8i\x00":            "UTF-16 text that cannot be converted",
		"\xfe\xff\x00h\x00":                "UTF-16 text",
		"\xff\xfe\x00\x00\x00\x00\x11\x00": "UTF-32 text",
		"\x89PNG\r\n\x1a\n\x00":            "binary file",
		"caf\xe9":                          "Latin-1",
	} {
		if got := notTextReason([]byte(content)); !strings.Contains(got, want) {
			t.Fatalf("reason for %q: %q, want %q", content, got, want)
		}
	}
	if blobHash([]byte("hello\n")) != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Fatal("blob hash differs from git hash-object")
	}
}

// Pull through /mcp/account and the REST resource, with the GitHub
// connection looked up in the database for the space owner.
func TestGitHubPullAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	host := newFakeRepoHost(t)
	cfg := githubTestConfig(t, pkcs1PEM(githubTestKey(t)))
	settings, err := cfg.settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.webBase, settings.apiBase = host.server.URL+"/web", host.apiBase()
	w.s.accounts.github = settings
	commit := host.commit("dave-zap/site", "main", map[string]fakeRepoFile{"README.md": {text: "# Site\n"}, ".gitignore": {text: "dist/\n"}, "logo.png": {text: "\x89PNG\x00"}})
	host.commit("zaptronics/tools", "main", map[string]fakeRepoFile{"a.md": {text: "a"}})

	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write", w.joinID: "read_write"}, "")
	write := func(path, text string) {
		t.Helper()
		state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
		w.tool(token, "write", map[string]any{"space": w.ownedName, "path": path, "text": text, "ifInState": state}, "")
	}
	write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site"}]}`)
	write("tools/.metatrash.json", `{"services":[{"type":"github","repo":"zaptronics/tools"}]}`)

	// No GitHub connection on the owner's account yet.
	res := w.mcp(token, "tools/call", map[string]any{"name": "pull", "arguments": map[string]any{"space": w.ownedName, "folder": "site/"}}, "")
	if e, _ := res.value["error"].(map[string]any); !res.isError || e["code"] != "forbidden" || !strings.Contains(e["message"].(string), "no GitHub connection for dave-zap") {
		t.Fatalf("unconnected: %v", res.value)
	}
	now := time.Now()
	for _, inst := range []githubInstallation{
		{InstallationID: randomInstallationID(t), UserID: w.owner.ID, GitHubUserID: 42, GitHubLogin: "dave-zap", AccountLogin: "Dave-Zap", AccountType: "User", Status: "active"},
		{InstallationID: randomInstallationID(t), UserID: w.owner.ID, GitHubUserID: 42, GitHubLogin: "dave-zap", AccountLogin: "zaptronics", AccountType: "Organization", Status: "suspended"},
	} {
		if err := w.db.linkGitHubInstallation(ctx, inst, now); err != nil {
			t.Fatal(err)
		}
	}
	got := w.tool(token, "pull", map[string]any{"space": w.ownedName, "folder": "site"}, "")
	if got["commit"] != commit || got["created"] != float64(2) || got["skippedCount"] != float64(1) || got["space"] != w.ownedName || got["folder"] != "site/" {
		t.Fatalf("pull: %v", got)
	}
	read := w.tool(token, "read", map[string]any{"space": w.ownedName, "path": "site/.gitignore"}, "")
	if read["file"].(map[string]any)["text"] != "dist/\n" {
		t.Fatalf("read: %v", read)
	}
	if again := w.tool(token, "pull", map[string]any{"space": w.ownedName, "folder": "site/"}, ""); again["upToDate"] != true {
		t.Fatalf("again: %v", again)
	}
	if len(host.tokens) != 2 || host.tokens[0] != "ghs_site" {
		t.Fatalf("installation tokens %v", host.tokens)
	}
	// A suspended installation is named.
	res = w.mcp(token, "tools/call", map[string]any{"name": "pull", "arguments": map[string]any{"space": w.ownedName, "folder": "tools/"}}, "")
	if e, _ := res.value["error"].(map[string]any); !res.isError || e["code"] != "forbidden" || !strings.Contains(e["message"].(string), "suspended on zaptronics") {
		t.Fatalf("suspended: %v", res.value)
	}
	// Arguments are checked.
	w.tool(token, "pull", map[string]any{"space": w.ownedName}, "invalid_request")
	w.tool(token, "pull", map[string]any{"space": w.ownedName, "folder": "site/", "extra": true}, "invalid_request")
	// The public space has no GitHub folders.
	w.tool(token, "pull", map[string]any{"space": "public", "folder": "site/"}, "invalid_request")
	// Read-only connections cannot pull.
	readOnly := w.connect(w.owner, map[string]string{w.ownedID: "read_only"}, "")
	w.tool(readOnly, "pull", map[string]any{"space": w.ownedName, "folder": "site/"}, "insufficient_scope")

	// REST: POST /api/v1/account/spaces/{owner}/{slug}/pull.
	restToken := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "https://metatrash.com/api/v1/account")
	path := "/api/v1/account/spaces/" + w.ownedName + "/pull"
	if r := w.rest(restToken, "POST", path, `{"folder":"site/"}`); r.Code != 200 || !strings.Contains(r.Body.String(), `"upToDate":true`) {
		t.Fatalf("REST pull: %d %s", r.Code, r.Body.String())
	}
	if r := w.rest(restToken, "GET", path, ""); r.Code != 405 {
		t.Fatalf("REST GET pull: %d", r.Code)
	}
	if r := w.rest(restToken, "POST", path, `{"folder":"site/","x":1}`); r.Code != 400 {
		t.Fatalf("REST extra field: %d", r.Code)
	}
	if r := w.rest(restToken, "POST", path+"?folder=site", `{"folder":"site/"}`); r.Code != 400 {
		t.Fatalf("REST query: %d", r.Code)
	}
	// The anonymous REST routes have no pull.
	if r := w.rest("", "POST", "/api/v1/spaces/public/pull", `{"folder":"site/"}`); r.Code != 404 {
		t.Fatalf("anonymous pull: %d", r.Code)
	}
}
