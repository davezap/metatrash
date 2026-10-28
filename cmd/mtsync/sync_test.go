package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/internal/service"
)

// requestLog counts the requests a test server answers and keeps their
// lines, so a count check can say which requests it saw.
type requestLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, r.Method+" "+r.URL.RequestURI())
}

// Load returns how many requests have arrived.
func (l *requestLog) Load() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(len(l.lines))
}

// since lists the requests after the first n.
func (l *requestLog) since(n int64) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines[n:], "\n")
}

// testSpace runs a service with a key-protected space "private" and returns
// its URL and a request log.
func testSpace(t *testing.T) (string, *requestLog) {
	t.Helper()
	return testSpaceServer(t, false)
}

// testSpaceServer can act as a server from before 0.34.0, which refuses
// after= on list.
func testSpaceServer(t *testing.T, oldServer bool) (string, *requestLog) {
	t.Helper()
	dir := t.TempDir()
	high := `{"windowSeconds":60,"clientReads":100000,"clientWrites":100000,"spaceReads":100000,"spaceWrites":100000}`
	config := `{"configVersion":"0.1.0",
	  "defaults":{"rates":` + high + `,"storage":{"maxFileBytes":65536,"maxRequestBytes":524288,"maxFiles":1000,"maxCurrentTextBytes":16777216,"maxRepositoryBytes":134217728}},
	  "global":{"rates":{"windowSeconds":60,"reads":100000,"writes":100000},"maxQueuedWrites":32},
	  "spaces":{"public":{"visibility":"public","overrides":{}},"private":{"visibility":"private","overrides":{}}}}`
	configPath, keyPath := filepath.Join(dir, "spaces.json"), filepath.Join(dir, "keys.json")
	digest := func(key string) string { sum := sha256.Sum256([]byte(key)); return hex.EncodeToString(sum[:]) }
	keys, _ := json.Marshal(map[string]service.Keys{"private": {ReadHashes: []string{digest("reader")}, WriteHashes: []string{digest("writer")}}})
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keys, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := service.Open(context.Background(), configPath, keyPath, filepath.Join(dir, "data"), metatrash.SpaceAGENTS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	requests := &requestLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.add(r)
		if oldServer && (r.URL.Query().Has("after") || r.URL.Query().Has("since")) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_request","message":"Unknown or duplicate query parameter."}}`))
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, requests
}

type harness struct {
	t      *testing.T
	ctx    context.Context
	agent  *client // another writer in the space, like an agent
	s      *syncer
	dir    string
	logs   []string
	clock  time.Time
	server string
}

func newHarness(t *testing.T, server, key, dir string) *harness {
	h := &harness{t: t, ctx: context.Background(), dir: dir, server: server, clock: time.Now()}
	h.agent = newClient(server, "private", func(context.Context, bool) (string, error) { return "writer", nil }, false)
	c := newClient(server, "private", func(context.Context, bool) (string, error) { return key, nil }, false)
	s, err := openSyncer(c, dir, server, "private", func(format string, args ...any) {
		h.logs = append(h.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	if err != nil {
		t.Fatal(err)
	}
	s.settle = 0
	h.s = s
	return h
}

func (h *harness) sync() {
	h.t.Helper()
	for i := 0; ; i++ {
		err := h.s.cycle(h.ctx)
		if err == errRestart && i < 3 {
			continue
		}
		if err != nil {
			h.t.Fatal(err)
		}
		return
	}
}

// put writes a local file and moves its time forward, as an editor save does.
func (h *harness) put(p, text string) {
	h.t.Helper()
	full := filepath.Join(h.dir, filepath.FromSlash(p))
	_ = os.MkdirAll(filepath.Dir(full), 0755)
	if err := os.WriteFile(full, []byte(text), 0644); err != nil {
		h.t.Fatal(err)
	}
	h.clock = h.clock.Add(time.Second)
	_ = os.Chtimes(full, h.clock, h.clock)
}

func (h *harness) localText(p string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(h.dir, filepath.FromSlash(p)))
	return string(b), err == nil
}

func (h *harness) remote() (string, map[string]remoteFile) {
	h.t.Helper()
	state, files, err := h.agent.list(h.ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	m := map[string]remoteFile{}
	for _, f := range files {
		m[f.Path] = f
	}
	return state, m
}

func (h *harness) remoteText(p string) string {
	h.t.Helper()
	text, err := h.agent.read(h.ctx, p, "")
	if err != nil {
		h.t.Fatalf("read %s: %v", p, err)
	}
	return text
}

func (h *harness) agentWrite(p, text string) {
	h.t.Helper()
	state, _ := h.remote()
	if _, err := h.agent.write(h.ctx, p, text, state); err != nil {
		h.t.Fatal(err)
	}
}

func localPaths(t *testing.T, dir string) []string {
	paths := []string{}
	_ = filepath.WalkDir(dir, func(full string, d os.DirEntry, err error) error {
		if d.IsDir() && d.Name() == stateDir {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, full)
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(paths)
	return paths
}

func TestSyncBothWays(t *testing.T) {
	server, requests := testSpace(t)
	h := newHarness(t, server, "writer", t.TempDir())

	// First pass downloads the space.
	h.sync()
	if got := strings.Join(localPaths(t, h.dir), " "); got != ".metatrash.json AGENTS.md" {
		t.Fatalf("initial files: %s", got)
	}
	if text, _ := h.localText("AGENTS.md"); text != h.remoteText("AGENTS.md") {
		t.Fatal("AGENTS.md differs")
	}

	// A quiet pass costs one request.
	before := requests.Load()
	h.sync()
	if n := requests.Load() - before; n != 1 {
		t.Fatalf("quiet pass made %d requests:\n%s", n, requests.since(before))
	}

	// Local new file and edit upload.
	h.put("notes/plan.txt", "step one\n")
	h.sync()
	if h.remoteText("notes/plan.txt") != "step one\n" {
		t.Fatal("new file not uploaded")
	}
	h.put("notes/plan.txt", "step one\nstep two\n")
	h.sync()
	if h.remoteText("notes/plan.txt") != "step one\nstep two\n" {
		t.Fatal("edit not uploaded")
	}

	// An agent's write downloads.
	h.agentWrite("inbox/dave/hello.txt", "hi from an agent\n")
	h.sync()
	if text, _ := h.localText("inbox/dave/hello.txt"); text != "hi from an agent\n" {
		t.Fatalf("agent file not downloaded: %q", text)
	}

	// A local rename becomes a move that keeps the file's ID.
	_, files := h.remote()
	id := files["notes/plan.txt"].ID
	if err := os.Rename(filepath.Join(h.dir, "notes", "plan.txt"), filepath.Join(h.dir, "notes", "plan-v2.txt")); err != nil {
		t.Fatal(err)
	}
	h.sync()
	_, files = h.remote()
	if _, old := files["notes/plan.txt"]; old || files["notes/plan-v2.txt"].ID != id {
		t.Fatalf("rename not a move: %+v", files)
	}

	// A local delete deletes in the space; an agent's delete deletes here.
	os.Remove(filepath.Join(h.dir, "notes", "plan-v2.txt"))
	h.sync()
	if _, files = h.remote(); files["notes/plan-v2.txt"].ID != "" {
		t.Fatalf("local delete not sent: %v", h.logs)
	}
	state, _ := h.remote()
	if _, err := h.agent.remove(h.ctx, "inbox/dave/hello.txt", state); err != nil {
		t.Fatal(err)
	}
	h.sync()
	if _, ok := h.localText("inbox/dave/hello.txt"); ok {
		t.Fatal("agent delete not applied")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "inbox")); !os.IsNotExist(err) {
		t.Fatal("empty folder left behind")
	}

	// AGENTS.md cannot be deleted in the space, so a local delete restores it.
	os.Remove(filepath.Join(h.dir, "AGENTS.md"))
	h.sync()
	if _, ok := h.localText("AGENTS.md"); !ok {
		t.Fatal("AGENTS.md not restored")
	}

	// Both sides edit: the space version wins the path, ours becomes a
	// conflict copy that is uploaded too.
	h.put("doc.txt", "base\n")
	h.sync()
	h.agentWrite("doc.txt", "agent edit\n")
	h.put("doc.txt", "my edit\n")
	h.s.now = func() time.Time { return time.Date(2026, 10, 12, 15, 30, 0, 0, time.UTC) }
	h.sync()
	h.sync()
	copyPath := "doc.conflict-20261012-153000.txt"
	if text, _ := h.localText("doc.txt"); text != "agent edit\n" {
		t.Fatalf("conflict path holds %q", text)
	}
	if text, _ := h.localText(copyPath); text != "my edit\n" || h.remoteText(copyPath) != "my edit\n" {
		t.Fatal("conflict copy missing")
	}

	// A local Git repository in the folder is ignored entirely.
	h.put(".git/HEAD", "ref: refs/heads/main\n")
	h.sync()
	for _, line := range h.logs {
		if strings.Contains(line, ".git") {
			t.Fatalf("Git folder touched: %s", line)
		}
	}
	os.RemoveAll(filepath.Join(h.dir, ".git"))

	// Files the space refuses stay local and are reported once.
	h.put(".vscode/settings.json", "{}\n")
	h.sync()
	h.sync()
	refusals := 0
	for _, line := range h.logs {
		if strings.Contains(line, ".vscode/settings.json was not uploaded") {
			refusals++
		}
	}
	if refusals != 1 {
		t.Fatalf("refusal logged %d times: %v", refusals, h.logs)
	}

	// Everything else matches the space.
	_, files = h.remote()
	for _, p := range localPaths(t, h.dir) {
		if p == ".vscode/settings.json" {
			continue
		}
		text, _ := h.localText(p)
		if files[p].Blob != gitBlob([]byte(text)) {
			t.Errorf("%s differs from the space", p)
		}
	}
	if len(files) != len(localPaths(t, h.dir))-1 {
		t.Errorf("space has %d files, folder %d", len(files), len(localPaths(t, h.dir)))
	}
}

func TestSyncReadOnly(t *testing.T) {
	server, _ := testSpace(t)
	h := newHarness(t, server, "reader", t.TempDir())
	h.agentWrite("shared.txt", "from the space\n")
	h.sync()
	if text, _ := h.localText("shared.txt"); text != "from the space\n" {
		t.Fatal("read-only sync did not download")
	}
	h.put("shared.txt", "local only\n")
	h.sync()
	h.sync()
	if h.remoteText("shared.txt") != "from the space\n" {
		t.Fatal("read-only sync uploaded")
	}
	if text, _ := h.localText("shared.txt"); text != "local only\n" {
		t.Fatal("local edit lost")
	}
	if !h.s.readOnly {
		t.Fatalf("read-only access not noticed: %v", h.logs)
	}
}

func TestSyncResumesAndRefusesOtherSpace(t *testing.T) {
	server, _ := testSpace(t)
	dir := t.TempDir()
	h := newHarness(t, server, "writer", dir)
	h.sync()
	h.put("a.txt", "a\n")
	h.sync()
	// A new process on the same folder picks up where the last one stopped.
	h2 := newHarness(t, server, "writer", dir)
	h2.agentWrite("b.txt", "b\n")
	h2.sync()
	if text, _ := h2.localText("b.txt"); text != "b\n" || h2.remoteText("a.txt") != "a\n" {
		t.Fatal("resume failed")
	}
	c := newClient(server, "public", nil, false)
	if _, err := openSyncer(c, dir, server, "public", t.Logf); err == nil {
		t.Fatal("folder synced with a second space")
	}
}

func TestConflictName(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for in, want := range map[string]string{"a/b.txt": "a/b.conflict-20260102-030405.txt", "Makefile": "Makefile.conflict-20260102-030405", "x/.env": "x/.env.conflict-20260102-030405"} {
		if got := conflictName(in, now); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// After the first pass, an agent's change to one file of many costs one
// request for the changes and one to download that file.
func TestSyncFetchesOnlyChanges(t *testing.T) {
	server, requests := testSpace(t)
	h := newHarness(t, server, "writer", t.TempDir())
	for i := 0; i < 120; i++ {
		h.agentWrite(fmt.Sprintf("notes/n%03d.txt", i), fmt.Sprintf("note %d\n", i))
	}
	h.sync()
	if len(localPaths(t, h.dir)) != 124 {
		t.Fatalf("first pass: %d files", len(localPaths(t, h.dir)))
	}
	h.agentWrite("notes/n042.txt", "changed\n")
	before, logs := requests.Load(), len(h.logs)
	h.sync()
	if n := requests.Load() - before; n != 2 {
		t.Fatalf("one changed file took %d requests:\n%s\nlog: %q", n, requests.since(before), h.logs[logs:])
	}
	if text, _ := h.localText("notes/n042.txt"); text != "changed\n" {
		t.Fatal("change not downloaded")
	}
	// A removal elsewhere arrives the same way.
	state, _ := h.remote()
	if _, err := h.agent.remove(h.ctx, "notes/n007.txt", state); err != nil {
		t.Fatal(err)
	}
	before, logs = requests.Load(), len(h.logs)
	h.sync()
	if n := requests.Load() - before; n != 1 {
		t.Fatalf("one removal took %d requests:\n%s\nlog: %q", n, requests.since(before), h.logs[logs:])
	}
	if _, ok := h.localText("notes/n007.txt"); ok {
		t.Fatal("removal not applied")
	}
}

// Against a server without since=, passes list in full and still sync.
func TestSyncWithoutChangeLists(t *testing.T) {
	server, _ := testSpaceServer(t, true)
	h := newHarness(t, server, "writer", t.TempDir())
	h.sync()
	h.agentWrite("a.txt", "a\n")
	h.sync()
	h.put("b.txt", "b\n")
	h.sync()
	if text, _ := h.localText("a.txt"); text != "a\n" || h.remoteText("b.txt") != "b\n" || !h.s.noChanges {
		t.Fatal("sync against an older server")
	}
}

func TestRenameRetry(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.WriteFile(from, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := renameRetry(from, to); err != nil {
		t.Fatal(err)
	}
	// A missing source fails at once instead of retrying.
	start := time.Now()
	if err := renameRetry(from, to); !errors.Is(err, os.ErrNotExist) || time.Since(start) > time.Second {
		t.Fatalf("missing source: %v after %s", err, time.Since(start))
	}
}

func TestSyncRestoresLockedFiles(t *testing.T) {
	server, _ := testSpace(t)
	h := newHarness(t, server, "writer", t.TempDir())
	h.agentWrite("docs/a.md", "kept\n")
	h.agentWrite("docs/.metatrash.json", `{"inherit":true,"readOnly":{"a.md":"everyone"}}`)
	h.sync()

	// A change is refused: it becomes a conflict copy and the space's version returns.
	h.put("docs/a.md", "changed\n")
	h.sync()
	if text, _ := h.localText("docs/a.md"); text != "kept\n" {
		t.Fatalf("locked file not restored: %q %v", text, h.logs)
	}
	copies := 0
	for _, p := range localPaths(t, h.dir) {
		if strings.HasPrefix(p, "docs/a.conflict-") {
			if text, _ := h.localText(p); text != "changed\n" {
				t.Fatalf("conflict copy %s: %q", p, text)
			}
			copies++
		}
	}
	if copies != 1 {
		t.Fatalf("conflict copies: %d %v", copies, localPaths(t, h.dir))
	}

	// A deletion is refused: the file comes back.
	if err := os.Remove(filepath.Join(h.dir, "docs", "a.md")); err != nil {
		t.Fatal(err)
	}
	h.sync()
	h.sync()
	if text, _ := h.localText("docs/a.md"); text != "kept\n" {
		t.Fatalf("deleted locked file not restored: %v", h.logs)
	}
	if h.remoteText("docs/a.md") != "kept\n" {
		t.Fatal("locked file changed in the space")
	}
}
