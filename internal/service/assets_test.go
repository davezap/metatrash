package service

import (
	"context"
	"io/fs"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

// Slate and the Mermaid files it loads for diagrams come from the embedded web/ folder.
func TestSlateAndMermaidAssets(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), "../../config/spaces.example.json", "", filepath.Join(dir, "data"), metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.Path = path
		r.RemoteAddr = "127.0.0.1:12345"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := get("/assets/slate.js"); w.Code != 200 || !strings.Contains(w.Body.String(), "SlateMarkdownEditor") {
		t.Fatalf("slate.js: %d", w.Code)
	}
	entry := get("/assets/mermaid/mermaid.js")
	if entry.Code != 200 || entry.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(entry.Body.String(), "./chunks/") {
		t.Fatalf("mermaid.js: %d %s", entry.Code, entry.Header().Get("Content-Type"))
	}
	chunks, err := fs.ReadDir(mermaidFiles, "web/mermaid/chunks")
	if err != nil || len(chunks) == 0 {
		t.Fatalf("embedded chunks: %v", err)
	}
	for _, chunk := range chunks {
		if !strings.Contains(entry.Body.String(), chunk.Name()) {
			continue // imported by another chunk
		}
		w := get("/assets/mermaid/chunks/" + chunk.Name())
		if w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
			t.Fatalf("%s: %d %q", chunk.Name(), w.Code, w.Header().Get("Cache-Control"))
		}
	}
	for _, path := range []string{"/assets/mermaid/", "/assets/mermaid/chunks", "/assets/mermaid/nope.js", "/assets/mermaid/../slate.js", "/assets/mermaid/chunks/../mermaid.js"} {
		if w := get(path); w.Code != 404 {
			t.Fatalf("%s: %d, want 404", path, w.Code)
		}
	}
}
