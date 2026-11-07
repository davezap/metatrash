package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Docling conversion (0.95.5): a better Markdown for PDF virtual files, with
// heading levels, tables and reading order, from Docling
// (https://github.com/docling-project/docling) run as an outside program,
// deploy/docling/metatrash-docling. It is optional: without agents.docling
// PDFs keep pdftotext's text as before.
//
// Conversion is slow (seconds a page on a CPU), so it never runs in a
// request or in the write queue. A bounded queue of workers converts a PDF
// after it is uploaded, or when its text is first read without a Docling
// conversion (so PDFs stored before get one too). Until then reads and
// search use the pointer's text (pdftotext), as before.
//
// The Markdown is kept beside the content, by content hash and converter:
//
//	files/<space ID>/<sha256>.docling-<converter>.md
//
// <converter> is 12 hex digits of the hash of `metatrash-docling version`,
// so a new Docling makes new Markdown rather than mixing outputs. Nothing
// is written to Git: the pointer, history, sync, GitHub folders and folder
// actions see no change, so a conversion can't start a sync loop or trigger
// an action again. The objects are swept with the content they belong to,
// removed with the space, and are not counted as the space's storage.

const (
	defaultDoclingTimeout  = 600 // seconds a PDF may take
	defaultDoclingMaxPages = 200
	defaultDoclingMaxMB    = 50
	defaultDoclingThreads  = 2
	doclingQueueSize       = 64
	// doclingMissingFor is how long a read trusts that a PDF has no Docling
	// Markdown yet before asking the bucket again; doclingRetryAfter is how
	// long a failed PDF waits before it is tried again.
	doclingMissingFor = 10 * time.Minute
	doclingRetryAfter = 24 * time.Hour
)

// doclingConfig is agents.docling in accounts.json.
type doclingConfig struct {
	// Command is metatrash-docling's absolute path (deploy/docling/install.sh).
	Command string `json:"command"`
	// TimeoutSeconds bounds one PDF (default 600, 30 to 3600).
	TimeoutSeconds int `json:"timeoutSeconds"`
	// MaxPages are converted from each PDF (default 200, 1 to 2000); the
	// Markdown says when pages are left out.
	MaxPages int `json:"maxPages"`
	// MaxMB is the largest PDF converted (default 50, 1 to 100); larger ones
	// keep pdftotext's text.
	MaxMB int `json:"maxMB"`
	// Workers is how many PDFs convert at once (default 1, 1 to 4).
	Workers int `json:"workers"`
	// Threads is each conversion's CPU threads (default 2, 1 to 16).
	Threads int `json:"threads"`
	// OCR reads scanned pages: "auto" (default) or "off".
	OCR string `json:"ocr"`
}

// doclingSettings are the checked settings, with the converter's version.
type doclingSettings struct {
	command, version, ident, ocr string
	timeout                      time.Duration
	maxPages, workers, threads   int
	maxBytes                     int64
}

func (c *doclingConfig) settings() (*doclingSettings, error) {
	if !filepath.IsAbs(c.Command) {
		return nil, fmt.Errorf("agents docling command must be the absolute path of metatrash-docling (deploy/docling/install.sh), such as /opt/metatrash-docling/bin/metatrash-docling")
	}
	d := &doclingSettings{command: c.Command, ocr: c.OCR}
	timeout, err := intSetting("agents docling timeoutSeconds", c.TimeoutSeconds, defaultDoclingTimeout, 30, 3600)
	if err != nil {
		return nil, err
	}
	d.timeout = time.Duration(timeout) * time.Second
	if d.maxPages, err = intSetting("agents docling maxPages", c.MaxPages, defaultDoclingMaxPages, 1, 2000); err != nil {
		return nil, err
	}
	maxMB, err := intSetting("agents docling maxMB", c.MaxMB, defaultDoclingMaxMB, 1, maxVirtualBytes>>20)
	if err != nil {
		return nil, err
	}
	d.maxBytes = int64(maxMB) << 20
	if d.workers, err = intSetting("agents docling workers", c.Workers, 1, 1, 4); err != nil {
		return nil, err
	}
	if d.threads, err = intSetting("agents docling threads", c.Threads, defaultDoclingThreads, 1, 16); err != nil {
		return nil, err
	}
	switch d.ocr {
	case "":
		d.ocr = "auto"
	case "auto", "off":
	default:
		return nil, fmt.Errorf(`agents docling ocr must be "auto" or "off"`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.command, "version")
	cmd.Env = doclingEnv(os.TempDir(), d.threads)
	out, err := cmd.Output()
	version := strings.TrimSpace(string(out))
	if err != nil || !strings.HasPrefix(version, "metatrash-docling ") || strings.ContainsAny(version, "\n\r") {
		return nil, fmt.Errorf("agents docling command %s does not run (`%s version` should print \"metatrash-docling ...\"): %v", d.command, d.command, err)
	}
	sum := sha256.Sum256([]byte(version))
	d.version, d.ident = version, hex.EncodeToString(sum[:])[:12]
	return d, nil
}

// intSetting checks a number setting, 0 meaning its default.
func intSetting(name string, value, def, low, high int) (int, error) {
	if value == 0 {
		return def, nil
	}
	if value < low || value > high {
		return 0, fmt.Errorf("%s must be %d to %d", name, low, high)
	}
	return value, nil
}

// doclingEnv is the converter's whole environment: no secrets of the
// service, offline models, a home of its own.
func doclingEnv(home string, threads int) []string {
	return []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=" + home,
		"TMPDIR=" + home,
		"LANG=C.UTF-8",
		"HF_HUB_OFFLINE=1",
		"TRANSFORMERS_OFFLINE=1",
		"OMP_NUM_THREADS=" + strconv.Itoa(threads),
	}
}

// doclingRun converts the PDF in file name to Markdown; tests replace it.
type doclingRun func(ctx context.Context, name string) (string, error)

// doclingJob is one PDF to convert.
type doclingJob struct {
	r    *repository
	info VirtualInfo
	key  string
}

// doclingQueue converts PDFs in the background.
type doclingQueue struct {
	s        *Service
	settings *doclingSettings
	run      doclingRun
	jobs     chan doclingJob
	wg       sync.WaitGroup
	mu       sync.Mutex
	pending  map[string]bool      // queued or converting, by key
	missing  map[string]time.Time // no Markdown in the bucket when checked
	failed   map[string]time.Time // conversion failed (or the PDF is too big)
	// done counts finished conversions, for tests.
	done func(key string, err error)
}

func newDoclingQueue(s *Service, d *doclingSettings) *doclingQueue {
	q := &doclingQueue{s: s, settings: d, jobs: make(chan doclingJob, doclingQueueSize), pending: map[string]bool{}, missing: map[string]time.Time{}, failed: map[string]time.Time{}}
	q.run = q.convertFile
	return q
}

func (q *doclingQueue) start(ctx context.Context) {
	for range q.settings.workers {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-q.jobs:
					err := q.convert(ctx, j)
					q.mu.Lock()
					delete(q.pending, j.key)
					if err != nil {
						q.failed[j.key] = time.Now()
					} else {
						delete(q.missing, j.key)
					}
					q.mu.Unlock()
					if err != nil && ctx.Err() == nil {
						log.Printf("docling: space %s, PDF %s: %v", j.r.id, j.info.SHA256[:12], err)
					}
					if q.done != nil {
						q.done(j.key, err)
					}
				}
			}
		}()
	}
}

func (q *doclingQueue) wait() { q.wg.Wait() }

// key names the Markdown of v in space r.
func (q *doclingQueue) key(r *repository, v VirtualInfo) string {
	return "files/" + r.id + "/" + v.SHA256 + ".docling-" + q.settings.ident + ".md"
}

// wants reports a virtual file Docling converts.
func (q *doclingQueue) wants(r *repository, v VirtualInfo) bool {
	return q != nil && r.owned && idPattern.MatchString(r.id) && v.Type == "application/pdf" && objectHashPattern.MatchString(v.SHA256) && v.Size <= q.settings.maxBytes
}

// enqueue asks for v's conversion, unless it is queued, failed recently or
// the queue is full (a later read asks again).
func (q *doclingQueue) enqueue(r *repository, v VirtualInfo) {
	if !q.wants(r, v) {
		return
	}
	key := q.key(r, v)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending[key] || time.Since(q.failed[key]) < doclingRetryAfter {
		return
	}
	select {
	case q.jobs <- doclingJob{r: r, info: v, key: key}:
		q.pending[key] = true
	default:
	}
}

// text is v's Docling Markdown, if it has been made; otherwise it asks for
// it and reports converting: whether a conversion is under way or due.
func (q *doclingQueue) text(ctx context.Context, r *repository, v VirtualInfo) (text string, ok, converting bool) {
	if !q.wants(r, v) {
		return "", false, false
	}
	key := q.key(r, v)
	if text, ok := q.s.virtualTexts.get(key); ok {
		return text, true, false
	}
	q.mu.Lock()
	failed := time.Since(q.failed[key]) < doclingRetryAfter
	known := q.pending[key] || time.Since(q.missing[key]) < doclingMissingFor
	q.mu.Unlock()
	if failed {
		return "", false, false
	}
	if !known {
		store, _ := q.s.spaceFiles()
		if store == nil {
			return "", false, false
		}
		if b, err := store.get(ctx, key); err == nil {
			text := string(b)
			q.s.virtualTexts.add(key, text)
			return text, true, false
		}
		q.mu.Lock()
		q.missing[key] = time.Now()
		q.mu.Unlock()
	}
	q.enqueue(r, v)
	q.mu.Lock()
	converting = q.pending[key]
	q.mu.Unlock()
	return "", false, converting
}

// convert makes and stores one PDF's Markdown.
func (q *doclingQueue) convert(ctx context.Context, j doclingJob) error {
	store, _ := q.s.spaceFiles()
	if store == nil {
		return fmt.Errorf("no space-files bucket")
	}
	// Another server, or an earlier run, may have made it already.
	if b, err := store.get(ctx, j.key); err == nil {
		q.s.virtualTexts.add(j.key, string(b))
		return nil
	}
	dir, err := os.MkdirTemp("", "metatrash-docling-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	name := filepath.Join(dir, "document.pdf")
	if err := q.fetch(ctx, j, name); err != nil {
		return err
	}
	text, err := q.run(ctx, name)
	if err != nil {
		return err
	}
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\r\n", "\n"), "")
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("no text")
	}
	if len(text) > maxVirtualText {
		cut := maxVirtualText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n\n…(the rest of the text is left out)\n"
	}
	if err := store.put(ctx, j.key, "text/markdown; charset=utf-8", []byte(text)); err != nil {
		return err
	}
	q.s.virtualTexts.add(j.key, text)
	return nil
}

// fetch copies the PDF from the bucket into file name.
func (q *doclingQueue) fetch(ctx context.Context, j doclingJob, name string) error {
	body, _, err := q.s.openVirtual(ctx, j.r, j.info)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, io.LimitReader(body, q.settings.maxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// convertFile runs metatrash-docling on the PDF in file name.
func (q *doclingQueue) convertFile(ctx context.Context, name string) (string, error) {
	d := q.settings
	// The program has its own limit; this one is the backstop.
	ctx, cancel := context.WithTimeout(ctx, d.timeout+30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.command, "convert", "--input", name,
		"--max-pages", strconv.Itoa(d.maxPages), "--timeout", strconv.Itoa(int(d.timeout/time.Second)),
		"--ocr", d.ocr, "--threads", strconv.Itoa(d.threads))
	cmd.Dir = filepath.Dir(name)
	cmd.Env = doclingEnv(filepath.Dir(name), d.threads)
	cmd.WaitDelay = 10 * time.Second
	var out, errOut limitedBuffer
	out.limit, errOut.limit = maxVirtualText+4096, 8192
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("took longer than %s", d.timeout)
	}
	if err != nil && !out.full {
		return "", fmt.Errorf("%v: %s", err, lastLine(errOut.String()))
	}
	return out.String(), nil
}

// lastLine is the last non-empty line of s, for a log.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// docling is the server's Docling queue, nil without agents.docling.
func (s *Service) docling() *doclingQueue {
	if a := s.agents.Load(); a != nil {
		return a.docling
	}
	return nil
}

// isDoclingKey reports a Docling Markdown object of the space at prefix,
// giving the content hash it belongs to.
func isDoclingKey(name string) (sha string, ok bool) {
	sha, rest, found := strings.Cut(name, ".docling-")
	if !found || !objectHashPattern.MatchString(sha) || !strings.HasSuffix(rest, ".md") {
		return "", false
	}
	return sha, true
}
