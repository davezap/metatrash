package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Docling conversion (0.95.5): PDFs get Docling's Markdown in the
// background; reads and search use it once made, pdftotext's text (or none)
// until then, and nothing changes in Git.
func TestDoclingConversion(t *testing.T) {
	w, _, objects, _ := webWorld(t)
	ctx := context.Background()
	q := newDoclingQueue(w.s, &doclingSettings{command: "/opt/x", version: "metatrash-docling 1 docling test", ident: "abcdef012345", ocr: "auto", timeout: time.Minute, maxPages: 10, workers: 1, threads: 1, maxBytes: 1 << 20})
	release := make(chan struct{})
	var runs atomic.Int32
	q.run = func(ctx context.Context, name string) (string, error) {
		runs.Add(1)
		b, err := os.ReadFile(name)
		if err != nil {
			return "", err
		}
		<-release
		if bytes.Contains(b, []byte("broken")) {
			return "", errors.New("broken PDF")
		}
		return "# Report\r\n\r\n## Findings\n\n| Kind | Days |\n| --- | --- |\n| annual | 20 |\n", nil
	}
	finished := make(chan error, 10)
	q.done = func(_ string, err error) { finished <- err }
	w.runner.docling = q
	qctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); q.wait() }()
	q.start(qctx)
	wait := func() error {
		t.Helper()
		select {
		case err := <-finished:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("conversion did not finish")
			return nil
		}
	}

	head, err := w.repo.head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	put, err := w.s.putVirtual(ctx, w.repo, w.repo.id, "reports/q3.pdf", head, []byte("%PDF-1.4 a report"), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	v := *put.File.Virtual
	key := q.key(w.repo, v)
	if key != "files/"+w.repo.id+"/"+v.SHA256+".docling-abcdef012345.md" {
		t.Fatalf("key %s", key)
	}
	// While it converts, the read says a better text is coming.
	got := w.do(ctx, "read", Input{Path: "reports/q3.pdf"}, "").(ReadResult)
	if !strings.Contains(got.Note, "being made") {
		t.Fatalf("read while converting: %+v", got)
	}
	close(release)
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	stored, ok := objects.objects[key]
	if !ok || !strings.HasPrefix(string(stored), "# Report\n\n## Findings") {
		t.Fatalf("stored %q", stored)
	}
	got = w.do(ctx, "read", Input{Path: "reports/q3.pdf"}, "").(ReadResult)
	if !strings.Contains(got.File.Text, "## Findings") || !strings.Contains(got.Note, "converted with Docling") {
		t.Fatalf("read after: %+v", got)
	}
	if res := w.do(ctx, "search", Input{Query: "annual"}, "").(SearchResult); len(res.Matches) != 1 || res.Matches[0].Path != "reports/q3.pdf" {
		t.Fatalf("search: %+v", res.Matches)
	}
	// Nothing was committed by the conversion.
	if now, _ := w.repo.head(ctx); now != put.NewState {
		t.Errorf("conversion changed the space: %s != %s", now, put.NewState)
	}
	// After a restart (an empty cache) the stored Markdown is read, not made again.
	w.s.virtualTexts = textCache{}
	q.missing = map[string]time.Time{}
	if got := w.do(ctx, "read", Input{Path: "reports/q3.pdf"}, "").(ReadResult); !strings.Contains(got.File.Text, "## Findings") || runs.Load() != 1 {
		t.Fatalf("read from the bucket: %d %+v", runs.Load(), got)
	}

	// A failed conversion leaves the file as it was and is not retried at once.
	head, _ = w.repo.head(ctx)
	broken, err := w.s.putVirtual(ctx, w.repo, w.repo.id, "reports/broken.pdf", head, []byte("%PDF-1.4 broken"), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := wait(); err == nil {
		t.Fatal("broken PDF converted")
	}
	got = w.do(ctx, "read", Input{Path: "reports/broken.pdf"}, "").(ReadResult)
	if got.File.Text != "" || strings.Contains(got.Note, "being made") || got.File.Virtual == nil {
		t.Fatalf("read failed conversion: %+v", got)
	}
	if _, ok := objects.objects[q.key(w.repo, *broken.File.Virtual)]; ok || runs.Load() != 2 {
		t.Fatalf("failed conversion stored, or ran again: %d", runs.Load())
	}

	// Too big, and not a PDF: never queued.
	if q.wants(w.repo, VirtualInfo{SHA256: v.SHA256, Type: "application/pdf", Size: 2 << 20}) || q.wants(w.repo, VirtualInfo{SHA256: v.SHA256, Type: "image/png", Size: 10}) {
		t.Error("wants too much")
	}
	// Without Docling, nothing changes.
	var none *doclingQueue
	if text, ok, converting := none.text(ctx, w.repo, v); ok || converting || text != "" {
		t.Error("nil queue gave text")
	}
	none.enqueue(w.repo, v)

	// The sweep removes Markdown whose content nothing names, and keeps the rest.
	prefix := "files/" + w.repo.id + "/"
	orphan := prefix + strings.Repeat("e", 64) + ".docling-abcdef012345.md"
	objects.mu.Lock()
	objects.objects[orphan] = []byte("# Gone\n")
	objects.mu.Unlock()
	if _, err := sweepSpace(ctx, w.repo, objects, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, ok := objects.objects[orphan]; ok {
		t.Error("orphan Docling Markdown kept")
	}
	if _, ok := objects.objects[key]; !ok {
		t.Error("sweep removed a current PDF's Markdown")
	}
}

func TestDoclingKey(t *testing.T) {
	sha := strings.Repeat("a", 64)
	if got, ok := isDoclingKey(sha + ".docling-0123456789ab.md"); !ok || got != sha {
		t.Errorf("key: %s %v", got, ok)
	}
	for _, name := range []string{sha, sha + ".docling-x", "abc.docling-1.md", sha + ".other-1.md"} {
		if _, ok := isDoclingKey(name); ok {
			t.Errorf("%s taken as a Docling key", name)
		}
	}
}

// The settings and the command line, with a stand-in for metatrash-docling.
func TestDoclingCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := t.TempDir()
	script := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := script("good", `if [ "$1" = version ]; then echo "metatrash-docling 1 docling 2.148.0"; exit 0; fi
echo "# Args"; echo; echo "$@" | sed "s|$PWD|DIR|"; echo "home=$HOME offline=$HF_HUB_OFFLINE threads=$OMP_NUM_THREADS"
`)
	d, err := (&doclingConfig{Command: good, MaxPages: 30, Threads: 3, OCR: "off"}).settings()
	if err != nil {
		t.Fatal(err)
	}
	if d.version != "metatrash-docling 1 docling 2.148.0" || len(d.ident) != 12 || d.timeout != 600*time.Second || d.workers != 1 || d.maxBytes != 50<<20 {
		t.Fatalf("settings %+v", d)
	}
	q := newDoclingQueue(nil, d)
	pdf := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := q.convertFile(context.Background(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "convert --input DIR/document.pdf --max-pages 30 --timeout 600 --ocr off --threads 3") || !strings.Contains(out, "offline=1 threads=3") || !strings.Contains(out, "home="+filepath.Dir(pdf)) {
		t.Errorf("command line: %s", out)
	}

	failing := script("failing", `if [ "$1" = version ]; then echo "metatrash-docling 1"; exit 0; fi
echo "loading models" >&2; echo "conversion failed: no models" >&2; exit 1
`)
	d.command = failing
	if _, err := q.convertFile(context.Background(), pdf); err == nil || !strings.Contains(err.Error(), "conversion failed: no models") {
		t.Errorf("failure: %v", err)
	}

	for config, want := range map[*doclingConfig]string{
		{Command: "metatrash-docling"}:              "absolute path",
		{Command: script("silent", "echo hello\n")}: "does not run",
		{Command: filepath.Join(dir, "missing")}:    "does not run",
		{Command: good, OCR: "tesseract"}:           `"auto" or "off"`,
		{Command: good, MaxPages: 5000}:             "maxPages must be 1 to 2000",
		{Command: good, Workers: 9}:                 "workers must be 1 to 4",
		{Command: good, MaxMB: 500}:                 "maxMB must be 1 to 100",
		{Command: good, TimeoutSeconds: 5}:          "timeoutSeconds must be 30 to 3600",
	} {
		if _, err := config.settings(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want %s", config, err, want)
		}
	}
}
