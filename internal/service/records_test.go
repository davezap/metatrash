package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"metatrash.com/metatrash/extension"
)

// recordWorld is a runner world with a test extension that keeps one record
// of two sections: the owner sees both, alice only the first, anyone else
// nothing. A section's attachment lives as a virtual file in the space.
type recordWorld struct {
	*runnerWorld
	mu      sync.Mutex
	revoked bool
	routes  []string
	objects *memoryObjects
}

const (
	recordOpen   = "## Request\n\nTen days of leave in March.\n"
	recordHidden = "## Manager notes\n\nSalary band 7, under review.\n"
)

func newRecordWorld(t *testing.T) *recordWorld {
	w := &recordWorld{runnerWorld: newRunnerWorld(t), objects: &memoryObjects{objects: map[string][]byte{}}}
	w.runner.settings.downloads = w.objects
	hooks := extension.Hooks{Name: "records",
		RecordView: func(ctx context.Context, req extension.RecordViewRequest) (extension.RecordView, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.routes = append(w.routes, req.Route)
			if req.Record.Record != "r1" || req.SpaceID != w.repo.id {
				return extension.RecordView{Hidden: true}, nil
			}
			switch {
			case req.UserID == w.repo.owner:
				return extension.RecordView{Markdown: recordOpen + recordHidden, Files: []string{"open.png", "hidden.png"}}, nil
			case req.UserID == aliceID && !w.revoked:
				return extension.RecordView{Markdown: recordOpen, Files: []string{"open.png"}}, nil
			}
			return extension.RecordView{Hidden: true, Note: "Not yours."}, nil
		},
		RecordFile: func(ctx context.Context, req extension.RecordFileRequest) (extension.RecordFile, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			allowed := req.Record == "r1" && (req.Section == "s1" && req.Name == "open.png" && (req.UserID == w.repo.owner || req.UserID == aliceID && !w.revoked) ||
				req.Section == "s2" && req.Name == "hidden.png" && req.UserID == w.repo.owner)
			if !allowed {
				return extension.RecordFile{}, missing()
			}
			return extension.RecordFile{SpaceID: w.repo.id, Path: "records/" + req.Section + "/" + req.Name}, nil
		}}
	w.s.extensions = append(w.s.extensions, hooks)
	return w
}

func (w *recordWorld) files() (string, map[string]record) {
	w.t.Helper()
	state, files, err := w.repo.snapshot(context.Background(), "")
	if err != nil {
		w.t.Fatal(err)
	}
	return state, files
}

// apply runs changes as the extension does, through RecordWriter.
func (w *recordWorld) apply(changes []extension.FileChange) (string, error) {
	sf, err := extensionHost{w.s}.OpenSpace(context.Background(), w.repo.id, w.repo.owner)
	if err != nil {
		w.t.Fatal(err)
	}
	return sf.Apply(context.Background(), changes, "Task")
}

func (w *recordWorld) readAs(ctx context.Context, path string) ReadResult {
	w.t.Helper()
	return w.do(ctx, "read", Input{Path: path}, "").(ReadResult)
}

func reader(userID string) context.Context { return withActor(context.Background(), userID) }

func TestRecordPointers(t *testing.T) {
	w := newRecordWorld(t)
	ctx := context.Background()
	rec := extension.Record{Record: "r1", Section: "s1", Step: "review", Org: "acme", Form: "Leave", From: "Dave"}

	// Only extension code makes one; it is marked in the index.
	if _, err := w.apply([]extension.FileChange{{Path: "tasks/leave.md", Record: &rec}}); err != nil {
		t.Fatal(err)
	}
	_, files := w.files()
	f := files["tasks/leave.md"]
	if f.Record == nil || *f.Record != rec {
		t.Fatalf("index %+v", f.Record)
	}
	pointer, _ := w.repo.blob(ctx, f.Blob)
	if string(pointer) != recordPointer(rec) || !strings.HasPrefix(string(pointer), "metatrash-virtual 1\nrecord r1\nsection s1\n") {
		t.Fatalf("pointer %q", pointer)
	}

	// Each reader sees what the extension lets them, on every route.
	if got := w.readAs(reader(w.repo.owner), "tasks/leave.md"); got.File.Text != recordOpen+recordHidden || got.File.Record == nil {
		t.Errorf("owner read %+v", got)
	}
	alice := w.readAs(reader(aliceID), "tasks/leave.md")
	if alice.File.Text != recordOpen || alice.RecordFiles["open.png"] != "/api/v1/records/r1/sections/s1/files/open.png" || len(alice.RecordFiles) != 1 {
		t.Errorf("alice read %+v", alice)
	}
	for _, c := range []context.Context{reader(strings.Repeat("9", 32)), ctx} {
		if got := w.readAs(c, "tasks/leave.md"); got.File.Text != "" || got.Note != "Not yours." || got.RecordFiles != nil {
			t.Errorf("stranger read %+v", got)
		}
	}
	w.routes = nil
	w.readAs(withRecordRoute(reader(aliceID), extension.RouteMCP), "tasks/leave.md")
	w.readAs(withRecordRoute(reader(aliceID), extension.RouteRunner), "tasks/leave.md")
	w.readAs(reader(aliceID), "tasks/leave.md")
	if strings.Join(w.routes, ",") != "mcp,runner,api" {
		t.Errorf("routes %v", w.routes)
	}
	// A line range is of the view, and ifBlob never says unchanged.
	if got := w.do(reader(aliceID), "read", Input{Path: "tasks/leave.md", StartLine: 3, LineCount: 1}, "").(ReadResult); got.File.Text != "Ten days of leave in March.\n" {
		t.Errorf("lines %q", got.File.Text)
	}
	if got := w.do(reader(aliceID), "read", Input{Path: "tasks/leave.md", IfBlob: f.Blob}, "").(ReadResult); got.Unchanged || got.File.Text != recordOpen {
		t.Errorf("ifBlob %+v", got)
	}

	// MT Sync and earlier revisions get the pointer only.
	syncCtx := context.WithValue(reader(aliceID), mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: aliceID, ClientID: "https://metatrash.com" + syncClientPath}})
	if got := w.readAs(syncCtx, "tasks/leave.md"); got.File.Text != string(pointer) {
		t.Errorf("sync read %q", got.File.Text)
	}
	state, _ := w.files()
	if got := w.do(reader(w.repo.owner), "read", Input{Path: "tasks/leave.md", Revision: state}, "").(ReadResult); got.File.Text != string(pointer) {
		t.Errorf("revision read %q", got.File.Text)
	}

	// Nothing writes over it but the extension; its text goes nowhere else.
	text := "mine\n"
	w.do(as(w.repo.owner), "write", Input{Path: "tasks/leave.md", Text: &text}, "record_pointer")
	w.do(as(w.repo.owner), "edit", Input{Path: "tasks/leave.md", Edits: []Edit{{OldText: "record", NewText: "x"}}}, "record_pointer")
	w.do(as(w.repo.owner), "append", Input{Path: "tasks/leave.md", Text: &text}, "record_pointer")
	copied := string(pointer)
	w.do(as(w.repo.owner), "write", Input{Path: "tasks/copy.md", Text: &copied}, "record_pointer")
	if _, err := w.apply([]extension.FileChange{{Path: "tasks/leave.md", Base: f.Blob, Text: "plain"}}); err == nil || err.(*Error).Code != "record_pointer" {
		t.Errorf("apply over a pointer: %v", err)
	}
	if _, err := w.apply([]extension.FileChange{{Path: "tasks/forged.md", Text: copied}}); err == nil || err.(*Error).Code != "record_pointer" {
		t.Errorf("apply of pointer text: %v", err)
	}
	if _, err := w.apply([]extension.FileChange{{Path: "tasks/bad.md", Record: &extension.Record{Record: "r1\nsha256 x"}}}); err == nil {
		t.Error("a record value with a newline was accepted")
	}
	// Writing the same pointer text back (an old sync client) changes nothing.
	same := string(pointer)
	if got := w.do(as(w.repo.owner), "write", Input{Path: "tasks/leave.md", Text: &same}, "").(WriteResult); got.NewState != got.OldState {
		t.Error("the unchanged pointer made a commit")
	}

	// A move within the space keeps it a record pointer.
	w.do(as(w.repo.owner), "move", Input{From: "tasks/leave.md", To: "done/leave.md"}, "")
	if got := w.readAs(reader(aliceID), "done/leave.md"); got.File.Text != recordOpen {
		t.Errorf("after move %+v", got)
	}

	// Search reads neither the pointer nor the view.
	for _, q := range []string{"Salary", "leave in March", "acme"} {
		got := w.do(reader(w.repo.owner), "search", Input{Query: q}, "").(SearchResult)
		for _, m := range got.Matches {
			if strings.HasPrefix(m.Path, "done/") {
				t.Errorf("search %q found %+v", q, m)
			}
		}
	}

	// A new step: the extension rewrites the pointer.
	_, files = w.files()
	next := rec
	next.Step = "approve"
	if _, err := w.apply([]extension.FileChange{{Path: "done/leave.md", Base: files["done/leave.md"].Blob, Record: &next}}); err != nil {
		t.Fatal(err)
	}
	if _, files = w.files(); files["done/leave.md"].Record.Step != "approve" {
		t.Errorf("step %+v", files["done/leave.md"].Record)
	}

	// Revoking access blanks it on the next read.
	w.mu.Lock()
	w.revoked = true
	w.mu.Unlock()
	if got := w.readAs(reader(aliceID), "done/leave.md"); got.File.Text != "" || got.Note == "" {
		t.Errorf("after revoke %+v", got)
	}

	// Plain text in an ordinary file is ordinary.
	w.write("notes/a.md", "record r1\n")
	if _, files = w.files(); files["notes/a.md"].Record != nil {
		t.Error("an ordinary file became a record")
	}
}

func TestRecordPointerHTTP(t *testing.T) {
	w := newRecordWorld(t)
	ctx := context.Background()
	rec := extension.Record{Record: "r1", Section: "s1"}
	if _, err := w.apply([]extension.FileChange{{Path: "tasks/leave.md", Record: &rec}}); err != nil {
		t.Fatal(err)
	}
	h := &httpAdapter{service: w.s}

	// The space's page (?raw, ?format=md): the view, never cached.
	for _, who := range []string{aliceID, ""} {
		res := httptest.NewRecorder()
		h.serveRecordPage(res, httptest.NewRequest("GET", "/spaces/dave/hr/tasks/leave.md?format=md", nil), w.repo, "tasks/leave.md", rec, who)
		body := res.Body.String()
		if res.Header().Get("Cache-Control") != "no-store" || strings.Contains(body, "Salary") || strings.Contains(body, "record r1") {
			t.Errorf("page for %q: %s %v", who, body, res.Header())
		}
		if (who == aliceID) != strings.Contains(body, "Ten days") {
			t.Errorf("page for %q: %s", who, body)
		}
	}

	// REST content: the view for the reader.
	for who, want := range map[string]string{aliceID: recordOpen, w.repo.owner: recordOpen + recordHidden, "": ""} {
		res := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/v1/spaces/x/content?path=tasks/leave.md", nil)
		if who != "" {
			r = r.WithContext(context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: who}}))
		}
		h.serveContent(res, r, w.repo, w.repo.id, false)
		if res.Body.String() != want {
			t.Errorf("content for %q: %q", who, res.Body.String())
		}
	}

	// Attachments: each section's file only to its readers; a forged
	// section, or a path that isn't a virtual file, reads as missing.
	state, _ := w.files()
	for _, p := range []string{"records/s1/open.png", "records/s2/hidden.png"} {
		if _, err := w.s.putVirtual(asOwner(ctx, w.repo), w.repo, w.repo.id, p, state, []byte("png:"+p), "image/png", "", ""); err != nil {
			t.Fatal(err)
		}
		state, _ = w.files()
	}
	w.write("records/s1/plain.png", "text\n")
	fetch := func(who, record, section, name string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		h.serveRecordFile(res, httptest.NewRequest("GET", recordFilePath(record, section, name), nil), who, record, section, name)
		return res
	}
	if res := fetch(aliceID, "r1", "s1", "open.png"); res.Code != 200 || res.Body.String() != "png:records/s1/open.png" || res.Header().Get("Cache-Control") != "no-store, no-transform" {
		t.Errorf("alice open: %d %q", res.Code, res.Body.String())
	}
	for _, c := range [][4]string{{aliceID, "r1", "s2", "hidden.png"}, {aliceID, "r1", "s1", "hidden.png"}, {aliceID, "r2", "s1", "open.png"}, {"", "r1", "s1", "open.png"}} {
		if res := fetch(c[0], c[1], c[2], c[3]); res.Code != 404 {
			t.Errorf("%v: %d %s", c, res.Code, res.Body.String())
		}
	}
	if res := fetch(w.repo.owner, "r1", "s2", "hidden.png"); res.Code != 200 {
		t.Errorf("owner hidden: %d", res.Code)
	}
	w.mu.Lock()
	w.revoked = true
	w.mu.Unlock()
	if res := fetch(aliceID, "r1", "s1", "open.png"); res.Code != 404 {
		t.Errorf("after revoke: %d", res.Code)
	}
}

// A folder action's job reads a record pointer as its payer.
func TestRecordPointerInJobs(t *testing.T) {
	w := newRecordWorld(t)
	w.write("hr/.metatrash.json", `{"actions":[{"name":"look","on":"inbound/*.md","do":"Read the task.","tools":["read"]}]}`)
	rec := extension.Record{Record: "r1", Section: "s1"}
	if _, err := w.apply([]extension.FileChange{{Path: "hr/tasks/leave.md", Record: &rec}}); err != nil {
		t.Fatal(err)
	}
	var seen string
	w.model.turns = []func(agentRequest) (agentResponse, error){
		useTools(toolUse("t1", "read", map[string]any{"path": "tasks/leave.md"})),
		func(req agentRequest) (agentResponse, error) {
			seen = lastResults(t, req)[0].Content
			return agentResponse{Content: []json.RawMessage{textBlock("Read.")}, StopReason: "end_turn"}, nil
		},
	}
	w.write("hr/inbound/go.md", "go\n")
	if jobs := w.wait(1); jobs[0].Status != jobDone {
		t.Fatalf("jobs %+v", jobs)
	}
	// The payer is the owner here, who may see both sections.
	if !strings.Contains(seen, "Salary band 7") || strings.Contains(seen, "record r1") {
		t.Errorf("job read %s", seen)
	}
	if w.routes[len(w.routes)-1] != extension.RouteRunner {
		t.Errorf("routes %v", w.routes)
	}
}

func TestRecordHooksChecked(t *testing.T) {
	view := func(context.Context, extension.RecordViewRequest) (extension.RecordView, error) {
		return extension.RecordView{}, nil
	}
	s := &Service{}
	s.extensions = []extension.Hooks{{Name: "a", RecordView: view}}
	if s.recordHooks() == nil {
		t.Fatal("no record hooks")
	}
	// Without an extension a record pointer reads blank.
	w := newRunnerWorld(t)
	rec := extension.Record{Record: "r1"}
	sf, _ := extensionHost{w.s}.OpenSpace(context.Background(), w.repo.id, "")
	if _, err := sf.Apply(context.Background(), []extension.FileChange{{Path: "t.md", Record: &rec}}, "x"); err != nil {
		t.Fatal(err)
	}
	got := w.do(context.Background(), "read", Input{Path: "t.md"}, "").(ReadResult)
	if got.File.Text != "" || got.Note != recordNoExtensionNote {
		t.Errorf("read %+v", got)
	}
}
