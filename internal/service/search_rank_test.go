package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

// rankBoth runs a ranked search with the index and with a full scan.
func rankBoth(t *testing.T, s *Service, in Input) (indexed, scanned RankedResult) {
	t.Helper()
	ctx := context.Background()
	in.Rank = true
	v, err := s.Dispatch(ctx, "public", "search", in)
	if err != nil {
		t.Fatalf("rank %q: %v", in.Query, err)
	}
	indexed = v.(RankedResult)
	m := s.searchIdx
	s.searchIdx = nil
	v, err = s.Dispatch(ctx, "public", "search", in)
	s.searchIdx = m
	if err != nil {
		t.Fatalf("rank scan %q: %v", in.Query, err)
	}
	return indexed, v.(RankedResult)
}

func rankPaths(r RankedResult) []string {
	out := []string{}
	for _, f := range r.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestRankedSearch(t *testing.T) {
	s := openIndexedTestService(t, t.TempDir(), true)
	defer s.Close()
	writeTestFile(t, s, "guides/refunds.md", "# Refunds\nHow to ask for a refund.\nRefunds take five days.\n")
	writeTestFile(t, s, "guides/billing.md", "# Billing\nPlans, invoices and refunds.\nPaddle sends the invoice.\n")
	writeTestFile(t, s, "notes/misc.md", "Nothing about money here.\nJust notes.\n")
	writeTestFile(t, s, "refund-policy/terms.md", "Terms of sale.\n")
	sources, _ := filepath.Glob("*.go")
	sort.Strings(sources)
	for _, src := range sources[:40] {
		b, err := os.ReadFile(src)
		if err == nil && len(b) < 60000 {
			writeTestFile(t, s, "code/"+strings.TrimSuffix(src, ".go")+".txt", string(b))
		}
	}
	waitSearchIndex(t, s)

	got, want := rankBoth(t, s, Input{Query: "refund"})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("index and scan differ:\n%+v\n%+v", got.Files, want.Files)
	}
	paths := rankPaths(got)
	if len(paths) < 3 || paths[0] != "guides/refunds.md" {
		t.Fatalf("refund ranked %v", paths)
	}
	has := map[string]bool{}
	for _, p := range paths {
		has[p] = true
	}
	if !has["refund-policy/terms.md"] || !has["guides/billing.md"] || has["notes/misc.md"] {
		t.Fatalf("refund matched %v (folder match and body match expected, misc not)", paths)
	}
	if f := got.Files[0]; len(f.Lines) == 0 || f.Lines[0].Line != 1 || f.ID == "" || f.Score <= 0 {
		t.Fatalf("best file %+v", f)
	}

	// Many queries give the same answer either way, including words that
	// start very many words and words found nowhere.
	before := s.searchIdx.narrowed.Load()
	for _, q := range []string{"invoice paddle", "err nil return", "func service", "s", "context ctx", "zzzz nothing", "Refunds BILLING", "search index", "東京"} {
		for _, limit := range []int{0, 3, 50} {
			got, want := rankBoth(t, s, Input{Query: q, Limit: limit})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%q limit %d: index and scan differ:\n%v\n%v", q, limit, rankPaths(got), rankPaths(want))
			}
			if n := len(got.Files); (limit == 0 && n > rankDefaultLimit) || (limit > 0 && n > limit) {
				t.Fatalf("%q limit %d gave %d files", q, limit, n)
			}
		}
	}
	if s.searchIdx.narrowed.Load() == before {
		t.Fatal("index never narrowed a ranked search")
	}
	got, _ = rankBoth(t, s, Input{Query: "invoice paddle"})
	if p := rankPaths(got); len(p) == 0 || p[0] != "guides/billing.md" {
		t.Fatalf("invoice paddle ranked %v", p)
	}
	// Prefix keeps to the folder.
	got, want = rankBoth(t, s, Input{Query: "refund", Prefix: "guides/"})
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(rankPaths(got), []string{"guides/refunds.md", "guides/billing.md"}) {
		t.Fatalf("prefix ranked %v", rankPaths(got))
	}
	// A file changed since it was indexed is still found.
	m := s.searchIdx
	m.testSync = false
	writeTestFile(t, s, "notes/misc.md", "A refund note.\n")
	got, want = rankBoth(t, s, Input{Query: "refund"})
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(rankPaths(got)[:1], []string{"guides/refunds.md"}) {
		t.Fatalf("after change: %v vs %v", rankPaths(got), rankPaths(want))
	}
	if !strings.Contains(strings.Join(rankPaths(got), " "), "notes/misc.md") {
		t.Fatalf("changed file missing: %v", rankPaths(got))
	}
	m.testSync = true
	waitSearchIndex(t, s)

	for _, in := range []Input{
		{Query: "refund", Regex: true},
		{Query: "refund", Context: 1},
		{Query: "refund", Revision: mustList(t, s).State},
		{Query: "refund", Limit: 51},
		{Query: "-> =="},
	} {
		in.Rank = true
		if _, err := s.Dispatch(context.Background(), "public", "search", in); err == nil {
			t.Fatalf("%+v accepted", in)
		}
	}
}

func TestRankedSearchREST(t *testing.T) {
	s := openIndexedTestService(t, t.TempDir(), true)
	defer s.Close()
	writeTestFile(t, s, "a.md", "apples and pears\n")
	writeTestFile(t, s, "b.md", "pears only\n")
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		code  int
		files int
	}{
		{"query=pear&rank=true", 200, 2},
		{"query=pear&rank=true&limit=1", 200, 1},
		{"query=pear&rank=maybe", 400, 0},
		{"query=pear&rank=true&regex=true", 400, 0},
	} {
		req := httptest.NewRequest("GET", "/api/v1/spaces/public/search?"+tc.query, nil)
		req.RemoteAddr = "127.0.0.1:1"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.query, w.Code, w.Body.String())
		}
		if tc.code != http.StatusOK {
			continue
		}
		var r RankedResult
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil || len(r.Files) != tc.files {
			t.Fatalf("%s: %v %s", tc.query, err, w.Body.String())
		}
	}
}

func TestBestLines(t *testing.T) {
	text := "one apple\ntwo\napple pear\npear\nan apple a pear a plum\nlast apple\n"
	got := bestLines(text, []string{"apple", "pear", "plum"})
	want := []RankedLine{{3, "apple pear"}, {5, "an apple a pear a plum"}, {1, "one apple"}}
	sort.Slice(want, func(i, j int) bool { return want[i].Line < want[j].Line })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

// space "*" searches every connected space, read-only ones too, and only
// those.
func TestRankedSearchAllSpacesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write", w.joinID: "read_only"}, "")
	write := func(space, path, text string) {
		t.Helper()
		st := w.tool(token, "list", map[string]any{"space": space, "limit": 1}, "")["state"]
		w.tool(token, "write", map[string]any{"space": space, "path": path, "text": text, "ifInState": st}, "")
	}
	write(w.ownedName, "trip.md", "Kayak trip to the lake.\nBring a kayak paddle.\n")
	// The joined space is read-only for this connection: write through the
	// member's own repository instead.
	repo := w.s.ownedRepository(w.joinID)
	st, _, err := repo.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	text := "Kayak hire costs.\n"
	if _, err := w.s.dispatchRepository(ctx, repo, w.joinName, "write", Input{Path: "costs.md", Text: &text, IfInState: st}); err != nil {
		t.Fatal(err)
	}
	other := w.user("other")
	otherSpace, err := w.s.createOwnedSpace(ctx, other.ID, "Other", randomName(t, "other-"))
	if err != nil {
		t.Fatal(err)
	}
	orepo := w.s.ownedRepository(otherSpace.ID)
	st, _, _ = orepo.snapshot(ctx, "")
	secret := "kayak secret\n"
	if _, err := w.s.dispatchRepository(ctx, orepo, otherSpace.ID, "write", Input{Path: "x.md", Text: &secret, IfInState: st}); err != nil {
		t.Fatal(err)
	}

	res := w.tool(token, "search", map[string]any{"space": "*", "query": "kayak", "rank": true}, "")
	files, _ := res["files"].([]any)
	seen := map[string]bool{}
	for _, f := range files {
		m := f.(map[string]any)
		seen[m["space"].(string)+":"+m["path"].(string)] = true
	}
	if !seen[w.ownedName+":trip.md"] || !seen[w.joinName+":costs.md"] || len(seen) != 2 {
		t.Fatalf("all spaces: %v", res)
	}
	if states := res["states"].(map[string]any); states[w.ownedName] == nil || states["public"] == nil {
		t.Fatalf("states: %v", res["states"])
	}
	first := files[0].(map[string]any)
	if first["path"] != "trip.md" {
		t.Fatalf("trip.md (two kayaks) should rank first: %v", files)
	}
	w.tool(token, "search", map[string]any{"space": "*", "query": "kayak"}, "invalid_request")
	w.tool(token, "search", map[string]any{"space": "*", "query": "kayak", "rank": true, "prefix": "a/"}, "invalid_request")
	one := w.tool(token, "search", map[string]any{"space": w.ownedName, "query": "kayak", "rank": true}, "")
	if f := one["files"].([]any); len(f) != 1 || one["state"] == nil {
		t.Fatalf("one space: %v", one)
	}
	// The anonymous endpoint refuses "*".
	anon := w.mcp("", "tools/call", map[string]any{"name": "search", "arguments": map[string]any{"space": "*", "query": "kayak", "rank": true}}, "/mcp")
	if e, _ := anon.value["error"].(map[string]any); !anon.isError || e == nil || !strings.Contains(e["message"].(string), "account connection") {
		t.Fatalf("anonymous * not refused: %v", anon.value)
	}

	// The website: a signed-out visitor can rank the public space, not a
	// private one.
	write("public", "kayak-club.md", "Kayak club notes.\n")
	res2 := siteCall(w, "GET", "/api/v1/site/spaces/public/search?query=kayak&rank=true", "", nil, "")
	if res2.Code != 200 || !strings.Contains(res2.Body.String(), "kayak-club.md") {
		t.Fatalf("visitor public: %d %s", res2.Code, res2.Body.String())
	}
	if res2 := siteCall(w, "GET", "/api/v1/site/spaces/"+w.ownedName+"/search?query=kayak&rank=true", "", nil, ""); res2.Code == 200 {
		t.Fatalf("visitor private: %d %s", res2.Code, res2.Body.String())
	}
}
