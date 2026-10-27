package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var testPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89")

func TestVirtualPointer(t *testing.T) {
	v := VirtualInfo{SHA256: strings.Repeat("a", 64), Size: 12, Type: "application/pdf", TextSHA256: strings.Repeat("b", 64), Source: "https://example.org/a.pdf"}
	want := "metatrash-virtual 1\nsha256 " + v.SHA256 + "\nsize 12\ntype application/pdf\ntext " + v.TextSHA256 + "\nsource https://example.org/a.pdf\n"
	if got := v.pointer(); got != want {
		t.Fatalf("pointer:\n%s", got)
	}
	for given, want := range map[string]string{"": "application/pdf", "application/octet-stream": "application/pdf", "image/PNG; x=1": "image/png"} {
		if got := virtualMedia(given, "a/b.pdf", []byte("%PDF-1.4")); got != want {
			t.Errorf("%q: %s", given, got)
		}
	}
	if got := virtualMedia("", "pic", testPNG); got != "image/png" {
		t.Errorf("sniffed: %s", got)
	}
}

// Virtual files: content in the bucket, a pointer in Git, reads resolved,
// content writes refused.
func TestVirtualFiles(t *testing.T) {
	w, _, objects, _ := webWorld(t)
	ctx := context.Background()
	put := func(path string, body []byte, media string) WriteResult {
		t.Helper()
		head, err := w.repo.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		result, err := w.s.putVirtual(ctx, w.repo, w.repo.id, path, head, body, media, "")
		if err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
		return result
	}
	read := func(path string, in Input) ReadResult {
		t.Helper()
		in.Path = path
		return w.do(ctx, "read", in, "").(ReadResult)
	}

	docx := testDocx(t)
	created := put("hr/policy.docx", docx, "")
	v := created.File.Virtual
	if !created.Created || v == nil || v.Type != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || v.Size != int64(len(docx)) || v.TextSHA256 == "" || v.TextSHA256 == v.SHA256 {
		t.Fatalf("created %+v %+v", created, v)
	}
	if _, ok := objects.objects["files/"+w.repo.id+"/"+v.SHA256]; !ok {
		t.Fatal("content not in the bucket")
	}
	got := read("hr/policy.docx", Input{})
	if !strings.Contains(got.File.Text, "| Kind | Days |") || !strings.Contains(got.Note, "as Markdown") || got.File.Virtual == nil {
		t.Fatalf("read docx: %+v", got)
	}
	if part := read("hr/policy.docx", Input{StartLine: 1, LineCount: 1}); part.Lines == nil || part.File.Text != "# Leave policy\n" {
		t.Errorf("line range: %q", part.File.Text)
	}
	// Git holds the pointer; list marks the file.
	pointer, err := w.repo.blob(ctx, created.File.Blob)
	if err != nil || string(pointer) != v.pointer() {
		t.Fatalf("pointer %q %v", pointer, err)
	}
	list := w.do(ctx, "list", Input{Prefix: "hr/"}, "").(ListResult)
	found := false
	for _, f := range list.Files {
		found = found || f.Path == "hr/policy.docx" && f.Virtual != nil && f.Virtual.SHA256 == v.SHA256
	}
	if !found {
		t.Fatalf("list %+v", list.Files)
	}

	// Content writes are refused; rewriting the same pointer changes nothing.
	text := "new text"
	w.do(ctx, "write", Input{Path: "hr/policy.docx", Text: &text}, "virtual_file")
	w.do(ctx, "edit", Input{Path: "hr/policy.docx", Edits: []Edit{{OldText: "sha256", NewText: "x"}}}, "virtual_file")
	w.do(ctx, "append", Input{Path: "hr/policy.docx", Text: &text}, "virtual_file")
	same := v.pointer()
	if res := w.do(ctx, "write", Input{Path: "hr/policy.docx", Text: &same}, "").(WriteResult); res.Changed {
		t.Error("same pointer changed the file")
	}
	// A pointer can't be made by writing its text.
	w.do(ctx, "write", Input{Path: "hr/fake.pdf", Text: &same}, "virtual_file")
	// Search leaves virtual files out.
	if res := w.do(ctx, "search", Input{Query: "sha256"}, "").(SearchResult); len(res.Matches) != 0 {
		t.Errorf("search matched a pointer: %+v", res.Matches)
	}

	// A picture has no text; a text file reads as itself.
	pic := put("hr/logo.png", testPNG, "image/png")
	if got := read("hr/logo.png", Input{}); got.File.Text != "" || !strings.Contains(got.Note, "no text") || !pic.File.Virtual.image() {
		t.Errorf("image read %+v", got)
	}
	first := put("hr/rates.csv", []byte("kind,days\nannual,20\n"), "text/csv")
	if got := read("hr/rates.csv", Input{}); got.File.Text != "kind,days\nannual,20\n" || got.Note != "" {
		t.Errorf("csv read %+v", got)
	}

	// A new version is a new pointer revision; the old one still resolves.
	put("hr/rates.csv", []byte("kind,days\nannual,25\n"), "text/csv")
	if got := read("hr/rates.csv", Input{}); !strings.Contains(got.File.Text, "25") {
		t.Errorf("new version %q", got.File.Text)
	}
	if got := read("hr/rates.csv", Input{Revision: first.NewState}); !strings.Contains(got.File.Text, "20") {
		t.Errorf("old version %q", got.File.Text)
	}
	// Moves keep it virtual.
	w.do(ctx, "move", Input{From: "hr/rates.csv", To: "hr/archive/rates.csv"}, "")
	if got := read("hr/archive/rates.csv", Input{}); got.File.Virtual == nil || !strings.Contains(got.File.Text, "25") {
		t.Errorf("moved %+v", got)
	}

	// Without the bucket there are no virtual files.
	w.runner.settings.downloads = nil
	head, _ := w.repo.head(ctx)
	if _, err := w.s.putVirtual(ctx, w.repo, w.repo.id, "hr/x.pdf", head, []byte("%PDF"), "", ""); err == nil || err.(*Error).Code != "virtual_unavailable" {
		t.Errorf("no bucket: %v", err)
	}
	w.s.virtualTexts = textCache{} // texts read already come from memory
	w.do(ctx, "read", Input{Path: "hr/policy.docx"}, "virtual_unavailable")
}

// web_fetch with path saves the download as a virtual file in the folder.
func TestWebFetchToPath(t *testing.T) {
	w, _, _, _ := webWorld(t)
	docx := testDocx(t)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
		rw.Write(docx)
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	session := w.runner.newWebSession(w.repo.owner, false, true)
	session.add(srv.URL + "/policy.docx")
	fetched, err := session.webFetch(ctx, srv.URL+"/policy.docx")
	if err != nil {
		t.Fatal(err)
	}
	saved, change, err := session.save(ctx, w.repo, w.repo.id, pinnedFolder("hr/"), fetched.(webFetchResult), "policy.docx")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Saved == nil || saved.Saved.Path != "hr/policy.docx" || saved.Saved.Virtual.Source != srv.URL+"/policy.docx" || !strings.Contains(change, "fetched from") {
		t.Fatalf("saved %+v %q", saved.Saved, change)
	}
	if got := w.do(ctx, "read", Input{Path: "hr/policy.docx"}, "").(ReadResult); !strings.Contains(got.File.Text, "| Kind | Days |") {
		t.Errorf("read %q", got.File.Text)
	}
	b, _ := json.Marshal(saved)
	if !strings.Contains(string(b), `"saved":{`) {
		t.Errorf("result %s", b)
	}
}

// Virtual files on the website and through the account REST resource.
func TestVirtualFilesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	settings := testAgentSettings(t)
	objects := &memoryObjects{objects: map[string][]byte{}}
	settings.downloads = objects
	runner, err := newAgentRunner(w.s, settings, &memoryJobStore{jobs: map[string]agentJob{}})
	if err != nil {
		t.Fatal(err)
	}
	w.s.agents.Store(runner)
	t.Cleanup(func() { w.s.agents.Store(nil) })
	ownerSession := w.session(w.owner)
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	restToken := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "https://metatrash.com/api/v1/account")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"].(string)
	base := "/api/v1/account/spaces/" + w.ownedName + "/content?path="
	call := func(method, target, kind string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "https://metatrash.com"+target, strings.NewReader(string(body)))
		r.RemoteAddr = "192.0.2.11:1"
		r.Header.Set("Authorization", "Bearer "+restToken)
		if kind != "" {
			r.Header.Set("Content-Type", kind)
		}
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}

	// Upload, then download.
	res := call("PUT", base+"pics/logo.png&ifInState="+state, "image/png", testPNG)
	var put WriteResult
	if res.Code != 201 || json.Unmarshal(res.Body.Bytes(), &put) != nil || put.File.Virtual == nil || put.File.Virtual.Type != "image/png" {
		t.Fatalf("upload: %d %s", res.Code, res.Body)
	}
	if res := call("GET", base+"pics/logo.png", "", nil); res.Code != 200 || res.Body.String() != string(testPNG) || res.Header().Get("Content-Type") != "image/png" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %v", res.Code, res.Header())
	}
	if res := call("PUT", base+"pics/logo.png&ifInState="+strings.Repeat("0", 40), "image/png", testPNG); res.Code != 409 {
		t.Errorf("stale upload: %d", res.Code)
	}
	if res := call("PUT", base+"pics/logo.png", "image/png", testPNG); res.Code != 400 {
		t.Errorf("upload without ifInState: %d", res.Code)
	}
	state = put.NewState
	res = call("PUT", base+"docs/notes.md&ifInState="+state, "text/markdown", []byte("# Notes\n"))
	if res.Code != 201 {
		t.Fatalf("upload md: %d %s", res.Code, res.Body)
	}
	// Agents read it like any file.
	read := w.tool(token, "read", map[string]any{"space": w.ownedName, "path": "docs/notes.md"}, "")
	if file := read["file"].(map[string]any); file["text"] != "# Notes\n" || file["virtual"] == nil {
		t.Fatalf("MCP read: %v", read)
	}
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "docs/notes.md", "text": "x", "ifInState": read["state"]}, "virtual_file")

	// The website: a page about the file, ?raw for its content, ?format=md
	// for its text.
	page := "/spaces/" + w.ownedName + "/pics/logo.png"
	if res := oauthCall(w.h, "GET", page, nil, ownerSession); res.Code != 200 || !strings.Contains(res.Body.String(), "Virtual file") || !strings.Contains(res.Body.String(), "[Download](logo.png?raw)") {
		t.Fatalf("page: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", page+"?raw", nil, ownerSession); res.Code != 200 || res.Body.String() != string(testPNG) || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "inline") || !strings.Contains(res.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("raw: %d %v", res.Code, res.Header())
	}
	if res := oauthCall(w.h, "GET", page+"?format=md", nil, ownerSession); res.Code != 404 {
		t.Errorf("image ?format=md: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/spaces/"+w.ownedName+"/docs/notes.md?format=md", nil, ownerSession); res.Code != 200 || res.Body.String() != "# Notes\n" || res.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Errorf("md ?format=md: %d %q", res.Code, res.Body)
	}
	// Signed out, private: the sign-in page, as for any file.
	if res := oauthCall(w.h, "GET", page+"?raw", nil); res.Code != 303 {
		t.Errorf("signed out raw: %d", res.Code)
	}

	// 0.60.1: an MCP read of a picture carries it as image content.
	pic := w.mcp(token, "tools/call", map[string]any{"name": "read", "arguments": map[string]any{"space": w.ownedName, "path": "pics/logo.png"}}, "")
	content, _ := pic.result["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["type"] != "image" || content[1].(map[string]any)["mimeType"] != "image/png" {
		t.Errorf("MCP picture: %v", pic.result)
	}
	// The export holds the picture itself, or with virtual=list its pointer.
	exportZip := func(query string) map[string]string {
		t.Helper()
		res := call("GET", "/api/v1/account/spaces/"+w.ownedName+"/export"+query, "", nil)
		if res.Code != 200 {
			t.Fatalf("export%s: %d %s", query, res.Code, res.Body)
		}
		zr, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(b)
		}
		return out
	}
	if files := exportZip(""); files["pics/logo.png"] != string(testPNG) || !strings.Contains(files[exportManifestName], `"virtualFiles": "content"`) {
		t.Errorf("export: %q", files["pics/logo.png"])
	}
	if files := exportZip("?virtual=list"); !strings.HasPrefix(files["pics/logo.png"], "metatrash-virtual 1\n") || !strings.Contains(files[exportManifestName], `"virtualFiles": "pointers"`) {
		t.Errorf("export virtual=list: %q", files["pics/logo.png"])
	}
	if res := call("GET", "/api/v1/account/spaces/"+w.ownedName+"/export?virtual=all", "", nil); res.Code != 400 {
		t.Errorf("export with an unknown option: %d", res.Code)
	}
}

// 0.60.1: search inside virtual text, pictures for models, export with the
// content, and the sweep of unused content.
func TestVirtualFilesFollowUp(t *testing.T) {
	w, _, objects, _ := webWorld(t)
	ctx := context.Background()
	put := func(path string, body []byte, media string) WriteResult {
		t.Helper()
		head, err := w.repo.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		result, err := w.s.putVirtual(ctx, w.repo, w.repo.id, path, head, body, media, "")
		if err != nil {
			t.Fatalf("put %s: %v", path, err)
		}
		return result
	}
	docx := put("hr/policy.docx", testDocx(t), "").File.Virtual
	firstPut := put("hr/rates.csv", []byte("kind,days\nannual,20\n"), "text/csv")
	first := firstPut.File.Virtual
	put("hr/rates.csv", []byte("kind,days\nannual,25\n"), "text/csv")
	pic := put("hr/logo.png", testPNG, "image/png").File.Virtual

	// Search finds lines in the Markdown of a Word file and in a text
	// file's content, never in pointers.
	res := w.do(ctx, "search", Input{Query: "Leave policy"}, "").(SearchResult)
	if len(res.Matches) != 1 || res.Matches[0].Path != "hr/policy.docx" {
		t.Errorf("search docx: %+v", res.Matches)
	}
	if res := w.do(ctx, "search", Input{Query: "annual,25"}, "").(SearchResult); len(res.Matches) != 1 || res.Matches[0].Path != "hr/rates.csv" {
		t.Errorf("search csv: %+v", res.Matches)
	}
	if res := w.do(ctx, "search", Input{Query: "metatrash-virtual"}, "").(SearchResult); len(res.Matches) != 0 {
		t.Errorf("search matched a pointer: %+v", res.Matches)
	}
	// The second search came from the cache: drop the bucket's text and it
	// still matches.
	objects.mu.Lock()
	delete(objects.objects, docx.textKey(w.repo.id))
	objects.mu.Unlock()
	if res := w.do(ctx, "search", Input{Query: "Leave policy"}, "").(SearchResult); len(res.Matches) != 1 {
		t.Errorf("cached search: %+v", res.Matches)
	}

	// A picture comes with the read for clients that show it, and not for
	// others (REST).
	got := w.do(ctx, "read", Input{Path: "hr/logo.png", Images: true}, "").(ReadResult)
	if got.image == nil || got.image.media != "image/png" || string(got.image.data) != string(testPNG) || !strings.Contains(got.Note, "shown with this result") {
		t.Errorf("picture read: %+v", got)
	}
	if got := w.do(ctx, "read", Input{Path: "hr/logo.png"}, "").(ReadResult); got.image != nil {
		t.Error("picture sent without Images")
	}
	if got := w.do(ctx, "read", Input{Path: "hr/rates.csv", Images: true}, "").(ReadResult); got.image != nil {
		t.Error("text read as a picture")
	}
	// The runner's read tool sends it as an image block.
	text, isError, _, image := w.runner.callTool(ctx, w.repo, w.repo.id, pinnedFolder("hr/"), "read", json.RawMessage(`{"path":"logo.png"}`))
	if isError || image == nil || !strings.Contains(text, `"note"`) {
		t.Errorf("runner read: %s %v", text, image)
	}

	// Export: the content at each virtual file's path, or with no content
	// function its pointer; the manifest describes them.
	exported := func(content exportContent) (map[string]string, ExportManifest) {
		t.Helper()
		var buf bytes.Buffer
		if _, err := w.repo.exportZip(ctx, w.repo.id, &buf, content); err != nil {
			t.Fatal(err)
		}
		zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			out[f.Name] = string(b)
		}
		var m ExportManifest
		if err := json.Unmarshal([]byte(out[exportManifestName]), &m); err != nil {
			t.Fatal(err)
		}
		return out, m
	}
	files, manifest := exported(func(ctx context.Context, v VirtualInfo) (io.ReadCloser, error) {
		body, _, err := w.s.openVirtual(ctx, w.repo, v)
		return body, err
	})
	if files["hr/logo.png"] != string(testPNG) || files["hr/rates.csv"] != "kind,days\nannual,25\n" || manifest.VirtualFiles != "content" {
		t.Errorf("export with content: %q %q %s", files["hr/logo.png"], files["hr/rates.csv"], manifest.VirtualFiles)
	}
	listed := false
	for _, f := range manifest.Files {
		listed = listed || f.Path == "hr/logo.png" && f.Virtual != nil && f.Virtual.SHA256 == pic.SHA256
	}
	if !listed {
		t.Errorf("manifest %+v", manifest.Files)
	}
	if files, manifest := exported(nil); files["hr/logo.png"] != pic.pointer() || manifest.VirtualFiles != "pointers" {
		t.Errorf("export of pointers: %q %s", files["hr/logo.png"], manifest.VirtualFiles)
	}

	// The sweep keeps everything a commit names (the old rates.csv too),
	// and removes old content nothing names, but not new content.
	prefix := "files/" + w.repo.id + "/"
	orphan, fresh := strings.Repeat("c", 64), strings.Repeat("d", 64)
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	objects.mu.Lock()
	objects.objects[prefix+orphan] = []byte("old")
	objects.objects[prefix+fresh] = []byte("new")
	objects.objects[prefix+"notahash"] = []byte("x")
	objects.modified = map[string]time.Time{prefix + fresh: now.Add(-time.Hour)}
	objects.mu.Unlock()
	result, err := sweepSpace(ctx, w.repo, objects, now)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 1 || len(objects.removed) != 1 || objects.removed[0] != prefix+orphan {
		t.Fatalf("sweep: %+v %v", result, objects.removed)
	}
	for _, key := range []string{first.key(w.repo.id), docx.key(w.repo.id), pic.key(w.repo.id), prefix + fresh} {
		if _, ok := objects.objects[key]; !ok {
			t.Errorf("sweep removed %s", key)
		}
	}
	if got := w.do(ctx, "read", Input{Path: "hr/rates.csv", Revision: firstPut.NewState}, "").(ReadResult); !strings.Contains(got.File.Text, "20") {
		t.Errorf("old version after the sweep: %q", got.File.Text)
	}
}
