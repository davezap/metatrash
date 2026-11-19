package service

import (
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testFrontend(label string) fstest.MapFS {
	return fstest.MapFS{
		".vite/manifest.json": {Data: []byte(`{"src/main.ts":{"file":"assets/main.js","isEntry":true,"css":["assets/main.css"]}}`)},
		"assets/main.js":      {Data: []byte(label)},
		"assets/main.css":     {Data: []byte("body{}")},
	}
}
func TestFrontendIsolationAndSnapshot(t *testing.T) {
	a, b := &Service{}, &Service{}
	source := testFrontend("public")
	if err := a.SetFrontend(source); err != nil {
		t.Fatal(err)
	}
	if err := b.SetFrontend(testFrontend("company")); err != nil {
		t.Fatal(err)
	}
	source["assets/main.js"].Data[0] = 'X'
	for _, pair := range []struct {
		s    *Service
		want string
	}{{a, "public"}, {b, "company"}} {
		h := &httpAdapter{service: pair.s}
		data, err := h.frontendAsset("main.js")
		if err != nil || string(data) != pair.want {
			t.Fatalf("asset %q, %v", data, err)
		}
		if h.frontendEntry("src/main.ts").Entry != "assets/main.js" {
			t.Fatal("missing entry")
		}
		if _, err := h.frontendAsset("other.js"); err == nil {
			t.Fatal("unexpected fallback to another edition")
		}
	}
	if err := a.SetFrontend(testFrontend("replacement")); err == nil {
		t.Fatal("allowed replacement")
	}
}
func TestFrontendRejectsInvalidBundles(t *testing.T) {
	for _, manifest := range []string{
		`{}`,
		`{"src/main.ts":{"file":"../private.js","isEntry":true}}`,
		`{"src/main.ts":{"file":"assets/missing.js","isEntry":true}}`,
		`{"src/main.ts":{"file":"assets/main.js","isEntry":true,"imports":["absent"]}}`,
		`{"src/main.ts":{"file":"assets/main.js","isEntry":true,"css":["assets/main.css?private"]}}`,
	} {
		source := testFrontend("test")
		source[".vite/manifest.json"] = &fstest.MapFile{Data: []byte(manifest)}
		if err := (&Service{}).SetFrontend(source); err == nil {
			t.Fatalf("accepted %s", manifest)
		}
	}
}

func TestFrontendEditorIsolation(t *testing.T) {
	source := testFrontend("company")
	source["slate/slate.js"] = &fstest.MapFile{Data: []byte("company editor")}
	source["slate/slate.css"] = &fstest.MapFile{Data: []byte("company styles")}
	s := &Service{}
	if err := s.SetFrontend(source); err != nil {
		t.Fatal(err)
	}
	source["slate/slate.js"].Data[0] = 'X'
	h := &httpAdapter{service: s}
	if got := string(h.editorAsset("slate.js", []byte("public"))); got != "company editor" {
		t.Fatalf("editor %q", got)
	}
	baseline := &httpAdapter{service: &Service{}}
	if got := string(baseline.editorAsset("slate.js", []byte("public"))); got != "public" {
		t.Fatalf("baseline %q", got)
	}
	// Charts (0.99.0) are optional: the bundle's when it has them.
	if _, ok := s.frontend.editor["charts/charts.js"]; ok {
		t.Fatal("charts without a charts file")
	}
	source["slate/charts/charts.js"] = &fstest.MapFile{Data: []byte("company charts")}
	withCharts := &Service{}
	if err := withCharts.SetFrontend(source); err != nil {
		t.Fatal(err)
	}
	if got := string(withCharts.frontend.editor["charts/charts.js"]); got != "company charts" {
		t.Fatalf("charts %q", got)
	}
	// Code colouring (0.99.4): languages/ and its chunks, only from the bundle.
	langs := &httpAdapter{service: withCharts}
	if w := serveTestAsset(langs, "/assets/languages/languages.js"); w.Code != 404 {
		t.Fatalf("languages without languages files: %d", w.Code)
	}
	source["slate/languages/languages.js"] = &fstest.MapFile{Data: []byte("company languages")}
	source["slate/languages/chunks/python-ABC.js"] = &fstest.MapFile{Data: []byte("python chunk")}
	source["slate/languages/notes.txt"] = &fstest.MapFile{Data: []byte("not served")}
	withLanguages := &Service{}
	if err := withLanguages.SetFrontend(source); err != nil {
		t.Fatal(err)
	}
	langs = &httpAdapter{service: withLanguages}
	if w := serveTestAsset(langs, "/assets/languages/languages.js"); w.Code != 200 || w.Body.String() != "company languages" || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Fatalf("languages.js: %d %q", w.Code, w.Body.String())
	}
	if w := serveTestAsset(langs, "/assets/languages/chunks/python-ABC.js"); w.Code != 200 || w.Body.String() != "python chunk" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("chunk: %d %q", w.Code, w.Body.String())
	}
	for _, path := range []string{"/assets/languages/notes.txt", "/assets/languages/../slate.js", "/assets/languages/missing.js"} {
		if w := serveTestAsset(langs, path); w.Code != 404 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := serveTestAsset(&httpAdapter{service: &Service{}}, "/assets/languages/languages.js"); w.Code != 404 {
		t.Fatalf("languages without a bundle: %d", w.Code)
	}
	delete(source, "slate/slate.css")
	if err := (&Service{}).SetFrontend(source); err == nil {
		t.Fatal("accepted incomplete editor bundle")
	}
}

func serveTestAsset(h *httpAdapter, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/", nil)
	r.URL.Path = path
	w := httptest.NewRecorder()
	if !h.serveAsset(w, r) {
		w.Code = 0
	}
	return w
}
