package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Pairing (0.95.8): a converted PDF becomes name.md in its place, with the
// PDF moved to assets/; PDFs that should stay are left alone.
func TestDoclingPairing(t *testing.T) {
	w, _, _, _ := webWorld(t)
	ctx := context.Background()
	q := newDoclingQueue(w.s, &doclingSettings{command: "/opt/x", version: "metatrash-docling 3 docling test", ident: "abcdef012345", ocr: "auto", timeout: time.Minute, maxPages: 10, workers: 1, threads: 1, maxBytes: 1 << 20, pair: true})
	q.run = func(ctx context.Context, name string) (string, error) {
		return "# Leave policy\n\n| Kind | Days |\n| --- | --- |\n| annual | 20 |\n", nil
	}
	converted := make(chan error, 10)
	q.done = func(_ string, err error) { converted <- err }
	paired := make(chan int, 10)
	q.paired = func(n int, err error) {
		if err != nil {
			t.Errorf("pairing: %v", err)
		}
		paired <- n
	}
	w.runner.docling = q
	qctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); q.wait() }()
	q.start(qctx)
	waitFor := func(what string, c any) int {
		t.Helper()
		switch ch := c.(type) {
		case chan error:
			select {
			case err := <-ch:
				if err != nil {
					t.Fatalf("%s: %v", what, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s did not finish", what)
			}
		case chan int:
			select {
			case n := <-ch:
				return n
			case <-time.After(10 * time.Second):
				t.Fatalf("%s did not finish", what)
			}
		}
		return 0
	}
	files := func() map[string]record {
		t.Helper()
		_, f, err := w.repo.snapshot(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	put := func(path, body, refresh string) WriteResult {
		t.Helper()
		head, _ := w.repo.head(ctx)
		source := ""
		if refresh != "" {
			source = "https://example.com/" + path
		}
		res, err := w.s.putVirtual(ctx, w.repo, w.repo.id, path, head, []byte(body), "", source, refresh)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// An upload is converted, then paired: one move and one write.
	up := put("policies/Leave Request.pdf", "%PDF-1.4 leave", "")
	waitFor("conversion", converted)
	if n := waitFor("pairing", paired); n != 1 {
		t.Fatalf("paired %d", n)
	}
	now := files()
	if _, ok := now["policies/Leave Request.pdf"]; ok {
		t.Fatal("PDF left in place")
	}
	moved, ok := now["policies/assets/Leave Request.pdf"]
	if !ok || moved.ID != up.File.ID || moved.Virtual == nil {
		t.Fatalf("PDF not moved with its ID: %+v", moved)
	}
	if _, ok := now["policies/assets/AGENTS.md"]; !ok {
		t.Error("assets/ folder has no AGENTS.md")
	}
	page := w.do(ctx, "read", Input{Path: "policies/Leave Request.md"}, "").(ReadResult)
	if !strings.HasPrefix(page.File.Text, "> [Open original PDF](assets/Leave%20Request.pdf)\n\n# Leave policy\n") || page.File.Virtual != nil {
		t.Fatalf("page: %q", page.File.Text)
	}
	// The original in assets/ still reads as Docling's Markdown and is not paired again.
	orig := w.do(ctx, "read", Input{Path: "policies/assets/Leave Request.pdf"}, "").(ReadResult)
	if !strings.Contains(orig.Note, "converted with Docling") {
		t.Fatalf("original: %+v", orig)
	}

	// Kept where they are: an upload whose page exists, a refreshing
	// download, and an upload into assets/.
	page2 := "my own notes\n"
	w.do(ctx, "write", Input{Path: "notes/a.md", Text: &page2}, "")
	for _, c := range []struct{ path, refresh string }{{"notes/a.pdf", ""}, {"notes/live.pdf", "monday 08:00"}, {"notes/assets/b.pdf", ""}} {
		put(c.path, "%PDF-1.4 "+c.path, c.refresh)
		waitFor("conversion "+c.path, converted)
		if c.refresh != "" {
			continue // refreshing downloads are not uploads to pair
		}
		if n := waitFor("pairing "+c.path, paired); n != 0 {
			t.Errorf("%s paired", c.path)
		}
	}
	now = files()
	for _, p := range []string{"notes/a.pdf", "notes/live.pdf", "notes/assets/b.pdf"} {
		if _, ok := now[p]; !ok {
			t.Errorf("%s moved", p)
		}
	}
	if got := w.do(ctx, "read", Input{Path: "notes/a.md"}, "").(ReadResult); got.File.Text != page2 {
		t.Errorf("existing page overwritten: %q", got.File.Text)
	}

	// A PDF stored before pairing stays where it is, even when read.
	q.settings.pair = false
	put("old/report.pdf", "%PDF-1.4 old", "")
	waitFor("conversion old", converted)
	q.settings.pair = true
	if got := w.do(ctx, "read", Input{Path: "old/report.pdf"}, "").(ReadResult); !strings.Contains(got.Note, "converted with Docling") {
		t.Fatalf("old read: %+v", got)
	}
	select {
	case n := <-paired:
		t.Errorf("old PDF paired on read (%d)", n)
	case <-time.After(200 * time.Millisecond):
	}
	if _, ok := files()["old/report.pdf"]; !ok {
		t.Error("old PDF moved")
	}

	// A converter that names Word among its formats converts and pairs a
	// Word upload; one that does not leaves it alone.
	docx := VirtualInfo{SHA256: strings.Repeat("a", 64), Type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", Size: 10}
	if q.wants(w.repo, docx) {
		t.Fatal("PDF-only converter wants a Word file")
	}
	q.idMu.Lock()
	q.version = "metatrash-docling 4 docling test formats pdf,docx,pptx,xlsx"
	q.idMu.Unlock()
	if !q.wants(w.repo, docx) {
		t.Fatal("converter with formats does not want a Word file")
	}
	put("minutes/Board 2026.docx", "PK word", "")
	waitFor("conversion docx", converted)
	if n := waitFor("pairing docx", paired); n != 1 {
		t.Fatalf("docx paired %d", n)
	}
	if got := w.do(ctx, "read", Input{Path: "minutes/Board 2026.md"}, "").(ReadResult); !strings.HasPrefix(got.File.Text, "> [Open original Word file](assets/Board%202026.docx)\n\n# Leave policy") {
		t.Fatalf("docx page: %q", got.File.Text)
	}

	// With pair off, nothing moves.
	q.settings.pair = false
	put("off/c.pdf", "%PDF-1.4 c", "")
	waitFor("conversion c", converted)
	w.do(ctx, "read", Input{Path: "off/c.pdf"}, "")
	select {
	case n := <-paired:
		t.Errorf("paired %d with pair off", n)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDoclingPairable(t *testing.T) {
	pdf := VirtualInfo{Type: "application/pdf"}
	github := map[string]bool{"code/": true}
	for p, want := range map[string]bool{
		"a.pdf": true, "docs/A.PDF": true, "docs/assets/a.pdf": false, "assets/a.pdf": false,
		"code/a.pdf": false, "code/sub/a.pdf": false, "codex/a.pdf": true, "docs/a.txt": false,
	} {
		if got := pairable(p, pdf, github); got != want {
			t.Errorf("pairable(%s) = %v", p, got)
		}
	}
	xlsx := VirtualInfo{Type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}
	if !pairable("data/q3.xlsx", xlsx, nil) || pairable("data/q3.pdf", xlsx, nil) {
		t.Error("pairable xlsx by its extension")
	}
	if pairable("a.pdf", VirtualInfo{Type: "application/pdf", Refresh: "daily"}, nil) || pairable("a.pdf", VirtualInfo{Type: "image/png"}, nil) {
		t.Error("pairable refreshing or non-PDF file")
	}
	for v, want := range map[string]bool{"metatrash-docling 3 docling 2": false, "metatrash-docling 4 docling 2 formats pdf,docx": true, "metatrash-docling 4 formats pdf,docx docling-core 2": true, "metatrash-docling 4 formats pdf,pptx": false} {
		if got := doclingConverts(v, "docx"); got != want {
			t.Errorf("doclingConverts(%q, docx) = %v", v, got)
		}
	}
	if !doclingConverts("metatrash-docling 3 docling 2", "pdf") {
		t.Error("old converter not taken for PDFs")
	}
}
