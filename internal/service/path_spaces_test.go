package service

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUnicodeFileLifecycle(t *testing.T) {
	s := openPublicTestService(t)
	ctx := context.Background()
	do := func(op string, in Input) any {
		t.Helper()
		state, err := s.repos["public"].head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in.IfInState = state
		value, err := s.Dispatch(ctx, "public", op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return value
	}
	original := "Données [été] #50% & '📄'/Report:2026?.md"
	text := "Unicode and punctuation survive storage."
	written := do("write", Input{Path: original, Text: &text}).(WriteResult)
	read := do("read", Input{Path: original}).(ReadResult)
	if read.File.Path != original || read.File.Text != text {
		t.Fatalf("read: %+v", read)
	}
	renamed := "日本語/Report:2026?.md"
	do("move", Input{From: original, To: renamed})
	history := do("history", Input{ID: written.File.ID}).(HistoryResult)
	if len(history.Entries) != 2 || history.Entries[0].Path != renamed || history.Entries[1].Path != original {
		t.Fatalf("history lost spelling: %+v", history)
	}
	do("delete", Input{Path: renamed})
}

func TestPathsWithSpaces(t *testing.T) {
	for _, path := range []string{"test/Downloading Controller Logs with PyVMS-r1.pdf", "Controller Logs/notes.md", "a  b/c.txt", "a", "a/.metatrash.json", "café.pdf", "日本語/🗂️.pdf", "-a.pdf", " a.pdf", "a.pdf ", "notes:2026.txt", "what?.md", "a [1] #50% & \"quoted\".pdf", ".gitignore"} {
		if !validPath(path) {
			t.Errorf("refused %q", path)
		}
	}
	for _, path := range []string{"a\tb.pdf", "a\nb.pdf", "a\\b.pdf", "a/../b.pdf", "a/.git/config", "a/.GIT./config", "a/.git /config", "a/.git ./config", ".metatrash ./files.json", "a/. ./b.pdf", ".metatrash/files.json", ".mtsync/state.json", ".MTSYNC ./state.json", "a//b.pdf", "a/", "bad\x00.pdf", "bad\xff.pdf", strings.Repeat("a", 241), strings.Repeat("é", 121), strings.Repeat("a/", 8) + "b.pdf"} {
		if validPath(path) {
			t.Errorf("accepted %q", path)
		}
	}
}

// Invalid queries and paths must fail before touching the repository/body.
func TestContentPathErrors(t *testing.T) {
	h := &httpAdapter{}
	for _, tc := range []struct{ query, message string }{
		{"path=" + url.QueryEscape("test/bad\\.pdf"), "Invalid path:"},
		{"path=" + url.QueryEscape("test/../bad.pdf"), "Invalid path:"},
		{"path=a.pdf&path=b.pdf", "each once"},
		{"path=a.pdf&unknown=1", "each once"},
		{"path=%zz", "each once"},
	} {
		for _, method := range []string{"GET", "PUT"} {
			r := httptest.NewRequest(method, "/content?"+tc.query, nil)
			w := httptest.NewRecorder()
			h.serveContent(w, r, nil, "", method == "PUT")
			if w.Code != 400 || !strings.Contains(w.Body.String(), tc.message) {
				t.Errorf("%s %s: %d %s", method, tc.query, w.Code, w.Body)
			}
		}
	}
}
