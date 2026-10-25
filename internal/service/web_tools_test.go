package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoryObjects is the downloads bucket for tests.
type memoryObjects struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func (m *memoryObjects) put(_ context.Context, key, _ string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = append([]byte(nil), body...)
	m.puts++
	return nil
}

func (m *memoryObjects) get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, fmt.Errorf("no such object")
	}
	return b, nil
}

// memoryWebStore keeps references and objects for tests.
type memoryWebStore struct {
	mu      sync.Mutex
	refs    []webRef
	objects map[string]webObject
}

func (m *memoryWebStore) latestRef(_ context.Context, userID, url string, now time.Time) (webRef, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.refs) - 1; i >= 0; i-- {
		if r := m.refs[i]; r.UserID == userID && r.URL == url && r.Expires.After(now) {
			return r, true, nil
		}
	}
	return webRef{}, false, nil
}

func (m *memoryWebStore) ref(_ context.Context, userID, id string, now time.Time) (webRef, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.refs {
		if r.ID == id && r.UserID == userID && r.Expires.After(now) {
			return r, true, nil
		}
	}
	return webRef{}, false, nil
}

func (m *memoryWebStore) saveRef(_ context.Context, r webRef) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refs = append(m.refs, r)
	return nil
}

func (m *memoryWebStore) object(_ context.Context, hash string) (webObject, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[hash]
	return o, ok, nil
}

func (m *memoryWebStore) saveObject(_ context.Context, o webObject) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[o.Hash] = o
	return nil
}

func (m *memoryWebStore) expire(context.Context, time.Time) error { return nil }

type fakeSearch struct {
	mu      sync.Mutex
	queries []string
	results []webResult
}

func (f *fakeSearch) search(_ context.Context, query string, count int) ([]webResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, query)
	if len(f.results) > count {
		return f.results[:count], nil
	}
	return f.results, nil
}

// pngBytes is a tiny PNG (its header is enough for sniffing and the tests).
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)

const leavePage = `<!doctype html><html><head><title>Leave guide</title><script>steal()</script><style>p{}</style></head>
<body><nav><a href="/">Home</a></nav><main>
<h1>Leave</h1>
<p>Workers get <strong>four weeks</strong> of annual leave. See the <a href="/doc.txt">full text</a>.</p>
<ul><li>Annual leave<ul><li>Four weeks</li></ul></li><li>Sick leave</li></ul>
<p hidden>secret hidden text</p>
<img src="/pic.png" alt="chart">
<table><tr><th>Kind</th><th>Days</th></tr><tr><td>Sick</td><td>10</td></tr></table>
</main></body></html>`

// webServer serves the pages the web test fetches; requests counts them.
func webServer(t *testing.T) (*httptest.Server, *int) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		switch r.URL.Path {
		case "/page":
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, leavePage)
		case "/old":
			http.Redirect(w, r, "/page", http.StatusFound)
		case "/doc.txt":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "Holidays Act\nSection 16: annual holidays\nSection 63: sick leave\n")
		case "/pic.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(pngBytes)
		case "/secret":
			fmt.Fprint(w, "never fetched")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func TestHTMLToMarkdown(t *testing.T) {
	base, _ := url.Parse("https://example.org/guide/leave")
	title, md := htmlToMarkdown(leavePage+`<pre>  code
  kept</pre><blockquote><p>Quoted</p><ul><li>one</li></ul></blockquote>`, base)
	if title != "Leave guide" {
		t.Errorf("title %q", title)
	}
	for _, want := range []string{
		"# Leave\n\nWorkers get **four weeks** of annual leave. See the [full text](https://example.org/doc.txt).",
		"- Annual leave\n  - Four weeks\n- Sick leave",
		"![chart](https://example.org/pic.png)",
		"| Kind | Days |\n| --- | --- |\n| Sick | 10 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	for _, unwanted := range []string{"steal", "secret hidden", "Home", "p{}"} {
		if strings.Contains(md, unwanted) {
			t.Errorf("markdown has %q:\n%s", unwanted, md)
		}
	}
	_, md = htmlToMarkdown(`<body><p>a</p><pre>  code
  kept</pre><blockquote><p>Quoted</p><ul><li>one</li></ul></blockquote></body>`, base)
	if !strings.Contains(md, "```\n  code\n  kept\n```") || !strings.Contains(md, "> Quoted\n>\n> - one") {
		t.Errorf("pre and quote:\n%s", md)
	}
	if got := decodeWebText([]byte("caf\xe9 \x93quoted\x94")); got != "café “quoted”" {
		t.Errorf("Windows-1252: %q", got)
	}
}

func TestWebFetcherRefusesPrivateAddresses(t *testing.T) {
	srv, requests := webServer(t)
	f := newWebFetcher(false)
	for _, raw := range []string{srv.URL + "/page", "http://localhost/page", "http://10.0.0.1/", "https://example.org:8443/"} {
		u, err := parseWebURL(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.fetch(context.Background(), u, "", ""); err == nil || !strings.Contains(err.Error(), "public internet") && !strings.Contains(err.Error(), "standard web ports") {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if *requests != 0 {
		t.Errorf("%d requests reached the test server", *requests)
	}
	for address, ok := range map[string]bool{"127.0.0.1:80": false, "10.1.2.3:443": false, "[::1]:443": false, "169.254.169.254:80": false, "8.8.8.8:443": true} {
		if err := webDialControl("tcp", address, nil); (err == nil) != ok {
			t.Errorf("%s: %v", address, err)
		}
	}
	for _, bad := range []string{"ftp://example.org/", "https://user:pw@example.org/", "example.org", "https://"} {
		if _, err := parseWebURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

// webWorld is a runner world with the web tools set up.
func webWorld(t *testing.T) (*runnerWorld, *fakeSearch, *memoryObjects, *memoryWebStore) {
	w := newRunnerWorld(t)
	search := &fakeSearch{}
	objects := &memoryObjects{objects: map[string][]byte{}}
	refs := &memoryWebStore{objects: map[string]webObject{}}
	w.runner.settings.search, w.runner.settings.downloads = search, objects
	w.runner.settings.searchLimit, w.runner.settings.fetchLimit = 50, 50
	w.runner.webRefs, w.runner.fetcher = refs, newWebFetcher(true)
	if err := w.runner.setWebTools(true, true); err != nil {
		t.Fatal(err)
	}
	return w, search, objects, refs
}

// resultText is a tool result's text, whether its content is a string or
// blocks (an image).
func resultText(t *testing.T, raw json.RawMessage) (text string, image bool, isError bool) {
	t.Helper()
	var b struct {
		Content json.RawMessage `json:"content"`
		IsError bool            `json:"is_error"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(b.Content, &text) == nil {
		return text, false, b.IsError
	}
	var blocks []struct {
		Type, Text string
		Source     struct {
			MediaType string `json:"media_type"`
			Data      string `json:"data"`
		} `json:"source"`
	}
	if err := json.Unmarshal(b.Content, &blocks); err != nil {
		t.Fatal(err)
	}
	for _, block := range blocks {
		if block.Type == "text" {
			text = block.Text
		}
		if block.Type == "image" && block.Source.MediaType == "image/png" && block.Source.Data != "" {
			image = true
		}
	}
	return text, image, b.IsError
}

func lastRaw(req agentRequest) []json.RawMessage { return req.Messages[len(req.Messages)-1].Content }

func TestRunnerWebTools(t *testing.T) {
	w, search, objects, refs := webWorld(t)
	srv, requests := webServer(t)
	search.results = []webResult{{Title: "Leave guide", URL: srv.URL + "/old", Snippet: "Four weeks"}}
	w.write("hr/.metatrash.json", `{"actions":[{"name":"research","on":"inbound/*.md","do":"Research the question and write notes."}]}`)
	var page webFetchResult
	w.model.turns = []func(agentRequest) (agentResponse, error){
		func(req agentRequest) (agentResponse, error) {
			names := map[string]bool{}
			for _, tool := range req.Tools {
				var def struct{ Name string }
				json.Unmarshal(tool, &def)
				names[def.Name] = true
			}
			var system contentBlock
			json.Unmarshal(req.System[0], &system)
			if !names["web_search"] || !names["web_fetch"] || !strings.Contains(system.Text, "You can use the web.") {
				t.Errorf("tools %v", names)
			}
			return useTools(toolUse("t1", "web_search", map[string]any{"query": "nz annual leave"}), toolUse("t2", "web_fetch", map[string]any{"url": srv.URL + "/secret"}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastRaw(req)
			found, _, _ := resultText(t, results[0])
			refused, _, isError := resultText(t, results[1])
			if !strings.Contains(found, srv.URL+"/old") || !isError || !strings.Contains(refused, "url_not_seen") {
				t.Errorf("search %s / fetch %s", found, refused)
			}
			return useTools(toolUse("t3", "web_fetch", map[string]any{"url": srv.URL + "/old"}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			text, _, isError := resultText(t, lastRaw(req)[0])
			if isError || json.Unmarshal([]byte(text), &page) != nil {
				t.Fatalf("fetch: %s", text)
			}
			if !webRefPattern.MatchString(page.Path) || !strings.HasSuffix(page.Path, ".md") || !strings.HasSuffix(page.Original, ".html") || page.Title != "Leave guide" ||
				!strings.Contains(page.Preview, "[full text]("+srv.URL+"/doc.txt)") || strings.Contains(page.Preview, "steal") || page.URL != srv.URL+"/old" {
				t.Errorf("fetch result %+v", page)
			}
			return useTools(
				toolUse("t4", "read", map[string]any{"path": page.Path, "startLine": 1, "lineCount": 1}),
				toolUse("t5", "search", map[string]any{"path": page.Path, "query": "annual", "ignoreCase": true}),
				toolUse("t6", "web_fetch", map[string]any{"url": srv.URL + "/doc.txt"}),
				toolUse("t7", "web_fetch", map[string]any{"url": srv.URL + "/pic.png"}),
				toolUse("t8", "move", map[string]any{"from": page.Path, "to": "notes/leave.md", "ifInState": strings.Repeat("0", 40)}),
				toolUse("t9", "list", map[string]any{"prefix": ""}),
			)(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastRaw(req)
			var texts []string
			for i, raw := range results {
				text, _, isError := resultText(t, raw)
				if isError {
					t.Errorf("result %d: %s", i, text)
				}
				texts = append(texts, text)
			}
			if !strings.Contains(texts[0], `"text":"# Leave\n"`) || !strings.Contains(texts[0], `"total":`) {
				t.Errorf("read: %s", texts[0])
			}
			if !strings.Contains(texts[1], "four weeks") || !strings.Contains(texts[1], "Annual leave") {
				t.Errorf("search: %s", texts[1])
			}
			var doc, pic webFetchResult
			json.Unmarshal([]byte(texts[2]), &doc)
			json.Unmarshal([]byte(texts[3]), &pic)
			if !strings.HasSuffix(doc.Path, ".txt") || doc.Original != "" || doc.Lines != 3 || !strings.Contains(doc.Preview, "Section 16") {
				t.Errorf("doc %+v", doc)
			}
			if !strings.HasSuffix(pic.Path, ".png") || pic.Note == "" || pic.Bytes != int64(len(pngBytes)) {
				t.Errorf("pic %+v", pic)
			}
			if !strings.Contains(texts[4], `"path":"notes/leave.md"`) || strings.Contains(texts[4], "hr/") {
				t.Errorf("keep: %s", texts[4])
			}
			if strings.Contains(texts[5], "web/") {
				t.Errorf("list shows references: %s", texts[5])
			}
			return useTools(
				toolUse("t10", "read", map[string]any{"path": pic.Path}),
				toolUse("t11", "web_fetch", map[string]any{"url": srv.URL + "/old"}),
				toolUse("t12", "read", map[string]any{"path": "web/0123456789abcdef.md"}),
				toolUse("t13", "move", map[string]any{"from": pic.Path, "to": "notes/pic.png", "ifInState": strings.Repeat("0", 40)}),
			)(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastRaw(req)
			if _, image, isError := resultText(t, results[0]); !image || isError {
				t.Error("the image was not shown")
			}
			again, _, _ := resultText(t, results[1])
			var second webFetchResult
			json.Unmarshal([]byte(again), &second)
			if !second.Unchanged || second.Path != page.Path {
				t.Errorf("refetch %s", again)
			}
			if text, _, isError := resultText(t, results[2]); !isError || !strings.Contains(text, "not_found") {
				t.Errorf("unknown reference: %s", text)
			}
			if text, _, isError := resultText(t, results[3]); !isError || !strings.Contains(text, "text files only") {
				t.Errorf("keeping an image: %s", text)
			}
			return agentResponse{Content: []json.RawMessage{textBlock("Researched leave.")}, StopReason: "end_turn"}, nil
		},
	}
	w.write("hr/inbound/q.md", "How much annual leave?\n")
	jobs := w.wait(1)
	if jobs[0].Status != jobDone {
		t.Fatalf("job %+v", jobs[0])
	}
	if len(w.model.turns) != 0 {
		t.Fatalf("%d turns left", len(w.model.turns))
	}
	if text := w.read("hr/notes/leave.md"); !strings.HasPrefix(text, "# Leave\n\nWorkers get **four weeks**") {
		t.Errorf("kept file: %q", text)
	}
	log := w.read("hr/runner.md")
	if !strings.Contains(log, "notes/leave.md (kept from "+srv.URL+"/old)") {
		t.Errorf("runner.md:\n%s", log)
	}
	// /old (redirect + page), /doc.txt, /pic.png, then /old again (redirect,
	// then 304 for the page).
	if *requests != 6 {
		t.Errorf("%d requests", *requests)
	}
	refs.mu.Lock()
	saved := append([]webRef(nil), refs.refs...)
	refs.mu.Unlock()
	if len(saved) != 3 {
		t.Errorf("%d references", len(saved))
	}
	for _, r := range saved {
		if r.UserID != w.repo.owner || r.Expires.Sub(r.Created) < 28*24*time.Hour {
			t.Errorf("reference %+v", r)
		}
	}
	// The page, its Markdown, the text file and the image.
	if objects.puts != 4 {
		t.Errorf("%d uploads", objects.puts)
	}
	// Another user's job cannot read the owner's references.
	other := w.runner.newWebSession(aliceID, true, true)
	if _, _, err := other.lookup(context.Background(), page.Path); err == nil {
		t.Error("another user read the reference")
	}
}

func TestRunnerWebOff(t *testing.T) {
	w, _, _, _ := webWorld(t)
	w.write("hr/.metatrash.json", `{"actions":[{"name":"tidy","on":"inbound/*.md","do":"Tidy.","web":false}]}`)
	w.model.turns = []func(agentRequest) (agentResponse, error){
		func(req agentRequest) (agentResponse, error) {
			for _, tool := range req.Tools {
				if strings.Contains(string(tool), "web_") || strings.Contains(string(tool), "web/") {
					t.Errorf("web tool offered: %s", tool)
				}
			}
			return useTools(toolUse("t1", "web_fetch", map[string]any{"url": "https://example.org/"}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			if text, _, isError := resultText(t, lastRaw(req)[0]); !isError || !strings.Contains(text, "Unknown tool") {
				t.Errorf("web_fetch with web off: %s", text)
			}
			return agentResponse{Content: []json.RawMessage{textBlock("Done.")}, StopReason: "end_turn"}, nil
		},
	}
	w.write("hr/inbound/a.md", "a\n")
	if jobs := w.wait(1); jobs[0].Status != jobDone || len(w.model.turns) != 0 {
		t.Fatalf("job %+v", jobs[0])
	}
}

func TestS3Signature(t *testing.T) {
	// The GET Object example of the AWS Signature Version 4 documentation.
	s := &s3Store{region: "us-east-1", keyID: "AKIAIOSFODNN7EXAMPLE", secret: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		now: func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }}
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	s.sign(req, nil)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization:\n%s\nwant\n%s", got, want)
	}
	// Put and get against a fake bucket.
	var stored []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/downloads/web/abc" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=key/") {
			http.Error(w, "bad", 403)
			return
		}
		if r.Method == http.MethodPut {
			stored = make([]byte, r.ContentLength)
			r.Body.Read(stored)
			return
		}
		w.Write(stored)
	}))
	defer srv.Close()
	store := &s3Store{endpoint: srv.URL, region: "us-west-004", bucket: "downloads", keyID: "key", secret: "secret", http: s3HTTPClient()}
	if err := store.put(context.Background(), "web/abc", "text/plain", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if b, err := store.get(context.Background(), "web/abc"); err != nil || string(b) != "hello" {
		t.Fatalf("get %q %v", b, err)
	}
	if _, err := store.get(context.Background(), "web/missing"); err == nil {
		t.Error("missing object read")
	}
}

func TestWebSettings(t *testing.T) {
	dir := t.TempDir()
	key := dir + "/key"
	if err := os.WriteFile(key, []byte(strings.Repeat("ab", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := dir + "/secret"
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := agentsConfig{Enabled: true, KeyFile: key, Search: &webSearchConfig{Provider: "brave", KeyFile: secret},
		Downloads: &webDownloadsConfig{Endpoint: "https://s3.us-west-004.backblazeb2.com", Bucket: "metatrash-web", KeyID: "0041234", KeyFile: secret}}
	settings, err := c.settings()
	if err != nil {
		t.Fatal(err)
	}
	store, ok := settings.downloads.(*s3Store)
	if !ok || store.region != "us-west-004" || store.secret != "s3cret" || settings.search == nil || settings.searchLimit != defaultSearchDailyLimit || settings.fetchLimit != defaultFetchDailyLimit {
		t.Errorf("settings %+v %+v", settings, store)
	}
	for _, broken := range []func(*agentsConfig){
		func(c *agentsConfig) { c.Search.Provider = "tavily" },
		func(c *agentsConfig) { c.Search.KeyFile = dir + "/none" },
		func(c *agentsConfig) { c.Downloads.Endpoint = "http://s3.example.com" },
		func(c *agentsConfig) { c.Downloads.Endpoint = "https://s3.example.com" },
		func(c *agentsConfig) { c.Downloads.Bucket = "Bad_Bucket" },
		func(c *agentsConfig) { c.Downloads.KeyID = "" },
	} {
		c := agentsConfig{Enabled: true, KeyFile: key, Search: &webSearchConfig{Provider: "brave", KeyFile: secret},
			Downloads: &webDownloadsConfig{Endpoint: "https://s3.us-west-004.backblazeb2.com", Bucket: "metatrash-web", KeyID: "0041234", KeyFile: secret}}
		broken(&c)
		if _, err := c.settings(); err == nil {
			t.Errorf("accepted %+v %+v", c.Search, c.Downloads)
		}
	}
}
