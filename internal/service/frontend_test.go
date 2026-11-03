package service

import (
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
	delete(source, "slate/slate.css")
	if err := (&Service{}).SetFrontend(source); err == nil {
		t.Fatal("accepted incomplete editor bundle")
	}
}
