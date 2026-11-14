package service

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"metatrash.com/metatrash"
)

// openIndexedTestService opens a service with the search index on in dir
// (its data and config), with limits high enough for many writes.
func openIndexedTestService(t *testing.T, dir string, sync bool) *Service {
	t.Helper()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if err = json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	d := c["defaults"].(map[string]any)
	d["rates"] = map[string]any{"windowSeconds": 60, "clientReads": 100000, "clientWrites": 100000, "spaceReads": 100000, "spaceWrites": 100000}
	g := c["global"].(map[string]any)
	g["rates"] = map[string]any{"windowSeconds": 60, "reads": 100000, "writes": 100000}
	g["searchIndex"] = map[string]any{"enabled": true, "maxOpen": 2, "idleMinutes": 1}
	b, _ = json.Marshal(c)
	configPath := filepath.Join(dir, "spaces.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), configPath, "", filepath.Join(dir, "data"), metatrash.SpaceAGENTS)
	if err != nil {
		t.Fatal(err)
	}
	if s.searchIdx == nil {
		t.Fatal("search index not on")
	}
	s.searchIdx.testSync = sync
	return s
}

// searchBoth runs a search with the index and with a full scan.
func searchBoth(t *testing.T, s *Service, in Input) (indexed, scanned SearchResult) {
	t.Helper()
	ctx := context.Background()
	v, err := s.Dispatch(ctx, "public", "search", in)
	if err != nil {
		t.Fatalf("search %q: %v", in.Query, err)
	}
	indexed = v.(SearchResult)
	m := s.searchIdx
	s.searchIdx = nil
	v, err = s.Dispatch(ctx, "public", "search", in)
	s.searchIdx = m
	if err != nil {
		t.Fatalf("scan %q: %v", in.Query, err)
	}
	return indexed, v.(SearchResult)
}

func writeTestFile(t *testing.T, s *Service, path, text string) string {
	t.Helper()
	ctx := context.Background()
	state := mustList(t, s).State
	v, err := s.Dispatch(ctx, "public", "write", Input{Path: path, Text: &text, IfInState: state})
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return v.(WriteResult).NewState
}

func mustList(t *testing.T, s *Service) ListResult {
	t.Helper()
	v, err := s.Dispatch(context.Background(), "public", "list", Input{})
	if err != nil {
		t.Fatal(err)
	}
	return v.(ListResult)
}

func waitSearchIndex(t *testing.T, s *Service) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !s.searchIdx.idle() {
		if time.Now().After(deadline) {
			t.Fatal("search index never caught up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Results with the index equal a full scan, for random pieces of lines that
// often start and end in the middle of words.
func TestSearchIndexMatchesFullScan(t *testing.T) {
	s := openIndexedTestService(t, t.TempDir(), true)
	defer s.Close()
	sources, _ := filepath.Glob("*.go")
	sort.Strings(sources)
	r := rand.New(rand.NewSource(7))
	var lines []string
	n := 0
	for _, src := range sources {
		if n == 60 {
			break
		}
		b, err := os.ReadFile(src)
		if err != nil || len(b) > 60000 {
			continue
		}
		writeTestFile(t, s, "code/"+strings.TrimSuffix(src, ".go")+".txt", string(b))
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); len(l) >= 6 {
				lines = append(lines, l)
			}
		}
		n++
	}
	notes := "# Notes\nThe traffic light is red.\nfix-login is done; ifInState checked.\nÜbergröße café naïve 東京 test\n"
	writeTestFile(t, s, "notes/a.md", notes)
	queries := []string{"traffic light", "ogin is", "fix-login", "ifInState", "東京", "café", "röße", "->", "e", "err != nil", "func (s *Service)", "no such words here at all"}
	for len(queries) < 400 {
		rs := []rune(lines[r.Intn(len(lines))])
		ln := 3 + r.Intn(min(28, len(rs)-3))
		st := r.Intn(len(rs) - ln + 1)
		queries = append(queries, string(rs[st:st+ln]))
	}
	before := s.searchIdx.narrowed.Load()
	for _, q := range queries {
		for _, ignore := range []bool{false, true} {
			in := Input{Query: q, Limit: maxSearchLimit, IgnoreCase: ignore}
			got, want := searchBoth(t, s, in)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("query %q ignoreCase=%v: index gave %d matches, scan %d", q, ignore, len(got.Matches), len(want.Matches))
			}
		}
	}
	if used := s.searchIdx.narrowed.Load() - before; used < 600 {
		t.Fatalf("index narrowed only %d of %d searches", used, 2*len(queries))
	}
	// With a prefix too.
	got, want := searchBoth(t, s, Input{Query: "return", Prefix: "notes/", Limit: 50})
	if !reflect.DeepEqual(got, want) {
		t.Fatal("prefix search differs")
	}
}

// A lagging index is never wrong: new, changed, moved and deleted files show
// as they are at once, before and after the index catches up.
func TestSearchIndexWhileBehind(t *testing.T) {
	s := openIndexedTestService(t, t.TempDir(), false)
	defer s.Close()
	ctx := context.Background()
	writeTestFile(t, s, "a.md", "alpha beta\n")
	check := func(q string, wantPaths ...string) {
		t.Helper()
		got, want := searchBoth(t, s, Input{Query: q, Limit: 50})
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: index and scan differ: %+v vs %+v", q, got.Matches, want.Matches)
		}
		paths := []string{}
		for _, m := range got.Matches {
			paths = append(paths, m.Path)
		}
		if !reflect.DeepEqual(paths, append([]string{}, wantPaths...)) && !(len(paths) == 0 && len(wantPaths) == 0) {
			t.Fatalf("%q: got %v, want %v", q, paths, wantPaths)
		}
	}
	check("beta", "a.md") // opens the index in the background
	waitSearchIndex(t, s)
	check("beta", "a.md")
	before := s.searchIdx.narrowed.Load()
	writeTestFile(t, s, "b.md", "gamma beta\n")
	check("gamma", "b.md") // b.md not indexed yet: scanned
	if s.searchIdx.narrowed.Load() == before {
		t.Fatal("index not used")
	}
	waitSearchIndex(t, s)
	check("gamma", "b.md")
	text := "delta only\n"
	writeTestFile(t, s, "a.md", text)
	check("alpha")
	check("delta", "a.md")
	state := mustList(t, s).State
	if _, err := s.Dispatch(ctx, "public", "move", Input{From: "b.md", To: "c/b.md", IfInState: state}); err != nil {
		t.Fatal(err)
	}
	check("gamma", "c/b.md")
	state = mustList(t, s).State
	if _, err := s.Dispatch(ctx, "public", "delete", Input{Path: "c/b.md", IfInState: state}); err != nil {
		t.Fatal(err)
	}
	check("gamma")
	waitSearchIndex(t, s)
	check("gamma")
	check("delta", "a.md")
	// Regex and older revisions scan.
	before = s.searchIdx.narrowed.Load()
	got, want := searchBoth(t, s, Input{Query: "^delta", Regex: true, Limit: 50})
	if !reflect.DeepEqual(got, want) || len(got.Matches) != 1 {
		t.Fatal("regex")
	}
	if _, err := s.Dispatch(ctx, "public", "search", Input{Query: "alpha", Revision: state, Limit: 50}); err != nil {
		t.Fatal(err)
	}
	if s.searchIdx.narrowed.Load() != before {
		t.Fatal("regex or revision used the index")
	}
}

// The index survives a restart, is rebuilt on request or when its schema
// changes, and says what it holds in status.json.
func TestSearchIndexRestartRebuildAndStatus(t *testing.T) {
	dir := t.TempDir()
	s := openIndexedTestService(t, dir, true)
	writeTestFile(t, s, "a.md", "persistent words\n")
	got, _ := searchBoth(t, s, Input{Query: "persistent", Limit: 10})
	if len(got.Matches) != 1 {
		t.Fatal("first search")
	}
	idxDir := filepath.Join(s.searchIdx.cfg.Dir, "c-public")
	s.Close()

	var st SearchIndexStatus
	b, err := os.ReadFile(filepath.Join(idxDir, searchStatusFile))
	if err != nil || json.Unmarshal(b, &st) != nil || st.Files < 2 || st.Schema != searchIndexSchema || st.Bytes == 0 {
		t.Fatalf("status: %s %v", b, err)
	}

	s = openIndexedTestService(t, dir, false)
	mustSearch := func() {
		t.Helper()
		got, want := searchBoth(t, s, Input{Query: "persistent", Limit: 10})
		if !reflect.DeepEqual(got, want) || len(got.Matches) != 1 {
			t.Fatal("search after restart")
		}
	}
	mustSearch() // opens it
	waitSearchIndex(t, s)
	before := s.searchIdx.narrowed.Load()
	mustSearch()
	if s.searchIdx.narrowed.Load() == before {
		t.Fatal("index not used after restart")
	}
	// A rebuild request removes the folder; the next search builds it again.
	if err := os.WriteFile(filepath.Join(idxDir, searchRebuildFile), nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.searchIdx.takeRebuildRequests()
	if _, err := os.Stat(idxDir); !os.IsNotExist(err) {
		t.Fatal("rebuild did not remove the index")
	}
	mustSearch()
	waitSearchIndex(t, s)
	mustSearch()
	// Another schema: built afresh on open.
	s.searchIdx.mu.Lock()
	si := s.searchIdx.open["c-public"]
	s.searchIdx.mu.Unlock()
	if si == nil || si.idx == nil {
		t.Fatal("index not open")
	}
	if err := si.idx.SetInternal([]byte(searchSchemaInternal), []byte("0")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openIndexedTestService(t, dir, true)
	defer s.Close()
	mustSearch()
}

// Idle indexes close, and no more than maxOpen stay open.
func TestSearchIndexIdleClose(t *testing.T) {
	s := openIndexedTestService(t, t.TempDir(), true)
	defer s.Close()
	writeTestFile(t, s, "a.md", "idle words\n")
	searchBoth(t, s, Input{Query: "idle", Limit: 10})
	m := s.searchIdx
	m.mu.Lock()
	open := len(m.open)
	m.mu.Unlock()
	if open != 1 {
		t.Fatalf("open %d", open)
	}
	m.closeIdle(time.Now().Add(2 * time.Minute))
	m.mu.Lock()
	open = len(m.open)
	m.mu.Unlock()
	if open != 0 {
		t.Fatal("idle index stayed open")
	}
	got, want := searchBoth(t, s, Input{Query: "idle", Limit: 10})
	if !reflect.DeepEqual(got, want) || len(got.Matches) != 1 {
		t.Fatal("search after close")
	}
}

func TestSearchIndexConfig(t *testing.T) {
	for _, c := range []searchIndexConfig{{Enabled: true, MaxOpen: 1001}, {Enabled: true, IdleMinutes: -1}, {Enabled: true, Dir: "relative"}} {
		if err := c.check("/data"); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	c := searchIndexConfig{Enabled: true}
	if err := c.check("/data"); err != nil || c.Dir != "/data/search" || c.MaxOpen != 50 || c.IdleMinutes != 10 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	if got := mergeTerms([]string{"a", "c"}, map[string]bool{"b": true, "c": true, "d": true}); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatal(got)
	}
}
