package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// fakeSpace is a space on the account routes that keeps virtual files, as a
// metatrash server from 0.60.0 does with a downloads bucket: just enough of
// it for the syncer.
type fakeSpace struct {
	mu      sync.Mutex
	state   int
	files   map[string]*fakeFile
	ids     int
	uploads map[string]int
	reads   int
}

type fakeFile struct {
	id      string
	text    string // an ordinary file's text, or a virtual file's pointer
	content []byte // a virtual file's content
	virtual *virtualInfo
}

func (f *fakeFile) remote(path string) remoteFile {
	r := remoteFile{ID: f.id, Path: path, Bytes: int64(len(f.text)), Deletable: true, Blob: gitBlob([]byte(f.text))}
	if f.virtual != nil {
		v := *f.virtual
		r.Virtual = &v
	}
	return r
}

func pointerOf(v virtualInfo) string {
	text := fmt.Sprintf("metatrash-virtual 1\nsha256 %s\nsize %d\ntype %s\n", v.SHA256, v.Size, v.Type)
	if v.Source != "" {
		text += "source " + v.Source + "\n"
	}
	if v.Refresh != "" {
		text += "refresh " + v.Refresh + "\n"
	}
	return text
}

func newFakeSpace(t *testing.T) (*fakeSpace, string) {
	t.Helper()
	fs := &fakeSpace{state: 1, files: map[string]*fakeFile{}, uploads: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(srv.Close)
	return fs, srv.URL
}

func (fs *fakeSpace) stateName() string { return fmt.Sprintf("%040d", fs.state) }

// putVirtual stores content as a virtual file, as the space does for an
// upload or a configure.
func (fs *fakeSpace) putVirtual(path string, content []byte, source, refresh string) *fakeFile {
	sum := sha256.Sum256(content)
	v := virtualInfo{SHA256: hex.EncodeToString(sum[:]), Size: int64(len(content)), Type: "application/octet-stream", Source: source, Refresh: refresh}
	f := fs.files[path]
	if f == nil {
		fs.ids++
		f = &fakeFile{id: fmt.Sprintf("id%d", fs.ids)}
		fs.files[path] = f
	}
	f.content, f.virtual, f.text = content, &v, pointerOf(v)
	fs.state++
	return f
}

// repoint changes a virtual file's pointer only, as configure or a refresh
// that finds the same content does.
func (fs *fakeSpace) repoint(path, source, refresh string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f := fs.files[path]
	f.virtual.Source, f.virtual.Refresh = source, refresh
	f.text = pointerOf(*f.virtual)
	fs.state++
}

func (fs *fakeSpace) serve(w http.ResponseWriter, r *http.Request) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	const base = "/api/v1/account/spaces/dave/work/"
	if !strings.HasPrefix(r.URL.Path, base) {
		http.NotFound(w, r)
		return
	}
	resource, q := strings.TrimPrefix(r.URL.Path, base), r.URL.Query()
	fail := func(status int, code string) {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`, code, code)
	}
	answer := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	mutated := func(path string, f *fakeFile) {
		answer(mutation{OldState: fmt.Sprintf("%040d", fs.state-1), NewState: fs.stateName(), File: f.remote(path)})
	}
	path := q.Get("path")
	switch {
	case resource == "files" && q.Has("since"):
		// As a server before 0.34.0: the syncer lists everything instead.
		fail(400, "invalid_request")
	case resource == "files":
		files := []remoteFile{}
		for p, f := range fs.files {
			files = append(files, f.remote(p))
		}
		answer(map[string]any{"state": fs.stateName(), "files": files, "nextCursor": nil})
	case resource == "file" && r.Method == "GET":
		f := fs.files[path]
		if f == nil {
			fail(404, "not_found")
			return
		}
		fs.reads++
		file := map[string]any{"text": f.text}
		if f.virtual != nil {
			file = map[string]any{"text": "", "virtual": f.virtual}
		}
		answer(map[string]any{"file": file})
	case resource == "file" && r.Method == "PUT":
		var body struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		f := fs.files[path]
		if f != nil && f.virtual != nil {
			fail(409, "virtual_file")
			return
		}
		if len(body.Text) > 65536 {
			fail(413, "payload_too_large")
			return
		}
		if f == nil {
			fs.ids++
			f = &fakeFile{id: fmt.Sprintf("id%d", fs.ids)}
			fs.files[path] = f
		}
		f.text = body.Text
		fs.state++
		mutated(path, f)
	case resource == "file" && r.Method == "DELETE":
		f := fs.files[path]
		if f == nil {
			fail(404, "not_found")
			return
		}
		delete(fs.files, path)
		fs.state++
		mutated(path, f)
	case resource == "content" && r.Method == "GET":
		f := fs.files[path]
		if f == nil || f.virtual == nil {
			fail(404, "not_found")
			return
		}
		_, _ = w.Write(f.content)
	case resource == "content" && r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		if f := fs.files[path]; f != nil && f.virtual == nil && utf8.Valid(b) {
			fail(409, "not_virtual")
			return
		}
		fs.uploads[path]++
		mutated(path, fs.putVirtual(path, b, "", ""))
	default:
		fail(404, "not_found")
	}
}

func newVirtualHarness(t *testing.T, server, dir string) *harness {
	t.Helper()
	h := &harness{t: t, ctx: context.Background(), dir: dir, server: server}
	c := newClient(server, "dave/work", func(context.Context, bool) (string, error) { return "token", nil }, true)
	s, err := openSyncer(c, dir, server, "dave/work", func(format string, args ...any) {
		h.logs = append(h.logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	})
	if err != nil {
		t.Fatal(err)
	}
	s.settle = 0
	h.s = s
	return h
}

func (h *harness) putBytes(p string, b []byte) {
	h.t.Helper()
	h.put(p, string(b))
}

func conflictCopies(t *testing.T, dir string) []string {
	var out []string
	for _, p := range localPaths(t, dir) {
		if strings.Contains(p, ".conflict-") {
			out = append(out, p)
		}
	}
	return out
}

var pdfBytes = []byte("%PDF-1.4\n\x00\x01\x02 binary\xff\xfe")

func TestSyncVirtualFiles(t *testing.T) {
	fs, server := newFakeSpace(t)
	dir := t.TempDir()
	h := newVirtualHarness(t, server, dir)

	// A virtual file in the space arrives whole, not as its text.
	fs.mu.Lock()
	fs.putVirtual("docs/report.pdf", pdfBytes, "", "")
	fs.mu.Unlock()
	h.sync()
	if got, _ := h.localText("docs/report.pdf"); got != string(pdfBytes) {
		t.Fatalf("downloaded %q", got)
	}
	if fs.reads != 0 {
		t.Fatalf("read a virtual file's text %d times", fs.reads)
	}

	// A change to its pointer alone is no change: no conflict, no download.
	h.putBytes("docs/report.pdf", pdfBytes)
	fs.repoint("docs/report.pdf", "https://example.com/report.pdf", "weekly")
	h.sync()
	if copies := conflictCopies(t, dir); len(copies) > 0 {
		t.Fatalf("pointer change made conflict copies %v; logs %v", copies, h.logs)
	}
	if v := h.s.st.Files["docs/report.pdf"].Virtual; v == nil || v.Source != "https://example.com/report.pdf" {
		t.Fatalf("base after repoint: %+v", h.s.st.Files["docs/report.pdf"])
	}

	// A file with a source can't be changed here: it is put back, and the
	// edit kept beside it.
	h.putBytes("docs/report.pdf", []byte("%PDF-1.4\n\x00edited"))
	h.sync()
	if got, _ := h.localText("docs/report.pdf"); got != string(pdfBytes) {
		t.Fatalf("sourced file not restored: %q", got)
	}
	if copies := conflictCopies(t, dir); len(copies) != 1 {
		t.Fatalf("want one copy of the edit, got %v", copies)
	}
	if fs.uploads["docs/report.pdf"] != 0 {
		t.Fatalf("uploaded a sourced file")
	}

	// A new binary file is uploaded as a virtual file, and its edits too.
	picture := []byte("\x89PNG\r\n\x1a\n\x00\x00picture")
	h.putBytes("pics/a.png", picture)
	h.sync()
	if f := fs.files["pics/a.png"]; f == nil || string(f.content) != string(picture) {
		t.Fatalf("not uploaded: %+v", f)
	}
	edited := append(append([]byte{}, picture...), 0, 1)
	h.putBytes("pics/a.png", edited)
	h.sync()
	if string(fs.files["pics/a.png"].content) != string(edited) || fs.uploads["pics/a.png"] != 2 {
		t.Fatalf("edit not uploaded (%d uploads)", fs.uploads["pics/a.png"])
	}
	h.sync()
	if fs.uploads["pics/a.png"] != 2 {
		t.Fatalf("a quiet pass uploaded again")
	}

	// Text over a virtual file (one without a source) is its new content.
	fs.mu.Lock()
	fs.putVirtual("notes/big.txt", []byte("old\n"), "", "")
	fs.mu.Unlock()
	h.sync()
	h.put("notes/big.txt", "new text\n")
	h.sync()
	if got := string(fs.files["notes/big.txt"].content); got != "new text\n" {
		t.Fatalf("text over a virtual file: %q", got)
	}

	// Text over the space's file limit goes up as a virtual file.
	long := strings.Repeat("a line of text\n", 5000)
	h.put("notes/long.md", long)
	h.sync()
	if f := fs.files["notes/long.md"]; f == nil || f.virtual == nil || string(f.content) != long {
		t.Fatalf("long text not uploaded as a virtual file: %v", h.logs)
	}
	h.sync()
	if fs.uploads["notes/long.md"] != 1 {
		t.Fatalf("long text uploaded %d times", fs.uploads["notes/long.md"])
	}

	// Deleting the local copy deletes it in the space.
	if err := os.Remove(filepath.Join(dir, "pics", "a.png")); err != nil {
		t.Fatal(err)
	}
	h.sync()
	if fs.files["pics/a.png"] != nil {
		t.Fatal("not deleted in the space")
	}
}

// mtsync before 0.61.0 agreed a virtual file by its pointer and kept its
// text: the next pass replaces that text with the content, without a
// conflict copy.
func TestSyncVirtualMigratesOldBase(t *testing.T) {
	fs, server := newFakeSpace(t)
	dir := t.TempDir()
	fs.mu.Lock()
	f := fs.putVirtual("report.pdf", pdfBytes, "", "")
	fs.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, stateDir), 0755); err != nil {
		t.Fatal(err)
	}
	old := syncState{Server: server, Space: "dave/work", State: fs.stateName(), Files: map[string]baseFile{"report.pdf": {ID: f.id, Blob: gitBlob([]byte(f.text))}}}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, stateDir, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.pdf"), []byte("# The PDF's text\n"), 0644); err != nil {
		t.Fatal(err)
	}
	h := newVirtualHarness(t, server, dir)
	h.sync()
	if got, _ := h.localText("report.pdf"); got != string(pdfBytes) {
		t.Fatalf("old text not replaced: %q; logs %v", got, h.logs)
	}
	if copies := conflictCopies(t, dir); len(copies) > 0 {
		t.Fatalf("conflict copies %v", copies)
	}
	if len(fs.uploads) != 0 {
		t.Fatal("uploaded the old text")
	}
	if h.s.st.Files["report.pdf"].Virtual == nil {
		t.Fatal("base has no virtual info")
	}
}

// A space reached with a key holds text only: a binary file stays local.
func TestSyncBinaryWithKeyStaysLocal(t *testing.T) {
	server, _ := testSpace(t)
	dir := t.TempDir()
	h := newHarness(t, server, "writer", dir)
	h.putBytes("a.bin", []byte{0, 1, 2, 0xff})
	h.sync()
	if _, files := h.remote(); len(files) > 0 {
		if _, ok := files["a.bin"]; ok {
			t.Fatal("uploaded a binary file over a key")
		}
	}
	if !strings.Contains(strings.Join(h.logs, "\n"), "stays local") {
		t.Fatalf("no warning: %v", h.logs)
	}
}
