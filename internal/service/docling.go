package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
// deploy/docling/metatrash-docling: on this server (command), or as a service
// on a machine of its own (url, 0.95.6), since a conversion needs 1-2 GB of
// memory. It is optional: without agents.docling PDFs keep pdftotext's text
// as before.
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
	// URL is a metatrash-docling service on another machine
	// (deploy/docling/install-server.sh), such as http://10.0.1.5:8090, and
	// TokenFile holds its token (0.95.6). Use command or url, not both.
	URL       string `json:"url"`
	TokenFile string `json:"tokenFile"`
	// TimeoutSeconds bounds one PDF (default 600, 30 to 3600).
	TimeoutSeconds int `json:"timeoutSeconds"`
	// MaxPages are converted from each PDF (default 200, 1 to 2000); the
	// Markdown says when pages are left out.
	MaxPages int `json:"maxPages"`
	// MaxMB is the largest PDF converted (default 50, 1 to 100); larger ones
	// keep pdftotext's text.
	MaxMB int `json:"maxMB"`
	// Workers is how many PDFs convert at once (default 1, 1 to 4). A
	// converter service converts one at a time, so keep 1 with url.
	Workers int `json:"workers"`
	// Threads is each conversion's CPU threads (default 2, 1 to 16).
	Threads int `json:"threads"`
	// OCR reads scanned pages: "auto" (default) or "off".
	OCR string `json:"ocr"`
	// Pair puts each converted PDF's Markdown in its place (0.95.8,
	// docling_pair.go): report.md, with report.pdf moved to
	// assets/report.pdf. Default true; false keeps the Markdown for reads
	// and search only.
	Pair *bool `json:"pair"`
}

// doclingSettings are the checked settings, with the converter's version.
type doclingSettings struct {
	command, version, ident, ocr string
	// url and token reach a converter service; version and ident are then
	// learned from it once it answers (doclingQueue.identity).
	url, token                 string
	timeout                    time.Duration
	maxPages, workers, threads int
	maxBytes                   int64
	pair                       bool
}

func (c *doclingConfig) settings() (*doclingSettings, error) {
	d := &doclingSettings{command: c.Command, ocr: c.OCR, pair: c.Pair == nil || *c.Pair}
	switch {
	case c.Command != "" && c.URL != "":
		return nil, fmt.Errorf("agents docling takes command or url, not both")
	case c.URL != "":
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("agents docling url must be the converter's address, such as http://10.0.1.5:8090 (deploy/docling/install-server.sh)")
		}
		token, err := readLimitedFile(c.TokenFile, 4096)
		if err != nil || len(strings.TrimSpace(string(token))) < 32 {
			return nil, fmt.Errorf("agents docling tokenFile must hold the converter's token (/etc/metatrash-docling/token on the converter)")
		}
		d.url, d.token = strings.TrimSuffix(c.URL, "/"), strings.TrimSpace(string(token))
	case c.TokenFile != "":
		return nil, fmt.Errorf("agents docling tokenFile goes with url")
	case !filepath.IsAbs(c.Command):
		return nil, fmt.Errorf("agents docling command must be the absolute path of metatrash-docling (deploy/docling/install.sh), such as /opt/metatrash-docling/bin/metatrash-docling, or url the address of a converter service")
	}
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
	if d.url != "" {
		// The service may be down or not yet started: Metatrash starts
		// anyway and asks it for its version in the background.
		return d, nil
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
	d.version, d.ident = version, doclingIdent(version)
	return d, nil
}

// doclingIdent names a converter version in object keys.
func doclingIdent(version string) string {
	sum := sha256.Sum256([]byte(version))
	return hex.EncodeToString(sum[:])[:12]
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
	// version and ident name the converter (doclingSettings); a service's
	// are learned when it answers, "" until then (no conversions, no
	// Docling text read).
	idMu           sync.Mutex
	version, ident string
	http           *http.Client
	// ctx is the queue's while it runs; pairWant holds, by key, the paths
	// uploaded with that content, paired once converted (docling_pair.go);
	// paired reports each pairing, for tests.
	ctx      context.Context
	pairWant map[string][]string
	paired   func(n int, err error)
	// For activity (0.95.9, activity.go): when each key was queued and
	// started converting, why the last try failed, and pairing's word on
	// uploaded paths, by space ID then path.
	queuedAt map[string]time.Time
	running  map[string]time.Time
	failures map[string]doclingFailure
	notes    map[string]map[string]pathNote
}

// doclingRetrySoon marks a failure of the converter rather than of the PDF
// (the service is down or busy): the PDF is tried again in minutes, not a day.
type doclingRetrySoon struct{ err error }

func (e doclingRetrySoon) Error() string { return e.err.Error() }

const doclingRetrySoonAfter = 15 * time.Minute

func newDoclingQueue(s *Service, d *doclingSettings) *doclingQueue {
	q := &doclingQueue{s: s, settings: d, jobs: make(chan doclingJob, doclingQueueSize), pending: map[string]bool{}, missing: map[string]time.Time{}, failed: map[string]time.Time{}, pairWant: map[string][]string{},
		queuedAt: map[string]time.Time{}, running: map[string]time.Time{}, failures: map[string]doclingFailure{}, notes: map[string]map[string]pathNote{}}
	q.version, q.ident = d.version, d.ident
	q.run = q.convertFile
	if d.url != "" {
		q.run = q.convertRemote
		q.http = &http.Client{Timeout: d.timeout + time.Minute}
	}
	return q
}

func (q *doclingQueue) start(ctx context.Context) {
	q.mu.Lock()
	q.ctx = ctx
	q.mu.Unlock()
	if q.settings.url != "" {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			q.watchVersion(ctx)
		}()
	}
	for range q.settings.workers {
		q.wg.Add(1)
		go func() {
			defer q.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-q.jobs:
					q.mu.Lock()
					q.running[j.key] = time.Now()
					q.mu.Unlock()
					err := q.convert(ctx, j)
					q.mu.Lock()
					delete(q.pending, j.key)
					delete(q.running, j.key)
					delete(q.queuedAt, j.key)
					now := time.Now()
					var soon doclingRetrySoon
					if errors.As(err, &soon) {
						q.failed[j.key] = now.Add(doclingRetrySoonAfter - doclingRetryAfter)
						q.failures[j.key] = doclingFailure{why: soon.Error(), soon: true, at: now, due: now.Add(doclingRetrySoonAfter)}
					} else if err != nil {
						q.failed[j.key] = now
						q.failures[j.key] = doclingFailure{why: err.Error(), at: now, due: now.Add(doclingRetryAfter)}
					} else {
						delete(q.missing, j.key)
						delete(q.failures, j.key)
					}
					for key, f := range q.failures {
						if now.After(f.due) {
							delete(q.failures, key)
						}
					}
					uploaded := q.pairWant[j.key]
					delete(q.pairWant, j.key)
					if err == nil && len(uploaded) > 0 && ctx.Err() == nil {
						// Listed as pairing from now, so activity has no gap.
						for _, p := range uploaded {
							q.note(j.r.id, p, pathNote{state: "pairing", message: "Converted. Moving the original into assets/ and writing the Markdown page in its place."})
						}
					}
					q.mu.Unlock()
					if err != nil && ctx.Err() == nil {
						log.Printf("docling: space %s, file %s: %v", j.r.id, j.info.SHA256[:12], err)
					}
					if err == nil && len(uploaded) > 0 {
						q.pairLater(j.r, j.info, uploaded)
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

// identity is the converter's version line and its key name.
func (q *doclingQueue) identity() (version, ident string) {
	q.idMu.Lock()
	defer q.idMu.Unlock()
	return q.version, q.ident
}

// watchVersion asks the converter service for its version at start and every
// five minutes, so a new Docling there makes new Markdown here.
func (q *doclingQueue) watchVersion(ctx context.Context) {
	for {
		version, err := q.remoteVersion(ctx)
		if err == nil {
			q.idMu.Lock()
			if version != q.version {
				log.Printf("docling: converter %s is %s", q.settings.url, version)
				q.version, q.ident = version, doclingIdent(version)
			}
			q.idMu.Unlock()
		} else if ctx.Err() == nil {
			log.Printf("docling: converter %s: %v", q.settings.url, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// remoteVersion is GET /version of the converter service.
func (q *doclingQueue) remoteVersion(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.settings.url+"/version", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+q.settings.token)
	res, err := q.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("not reachable")
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	version := strings.TrimSpace(string(b))
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(version, "metatrash-docling ") || strings.ContainsAny(version, "\n\r") {
		return "", fmt.Errorf("answered %s to /version (check tokenFile)", res.Status)
	}
	return version, nil
}

// doclingFormat is a file type Docling converts: its extension, as the
// converter's version line names it (formats …), and what a page calls it.
type doclingFormat struct{ ext, label string }

// doclingFormats are the types Metatrash sends to Docling (0.95.8). Pictures
// are left out on purpose: a photo should stay a picture.
var doclingFormats = map[string]doclingFormat{
	"application/pdf": {"pdf", "PDF"},
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document":   {"docx", "Word file"},
	"application/vnd.openxmlformats-officedocument.presentationml.presentation": {"pptx", "PowerPoint file"},
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":         {"xlsx", "Excel file"},
}

// doclingConverts reports a format the converter's version line accepts:
// "… formats pdf,docx,…" from metatrash-docling 4, PDF only before.
func doclingConverts(version, ext string) bool {
	_, list, found := strings.Cut(version, " formats ")
	if !found {
		return ext == "pdf"
	}
	list, _, _ = strings.Cut(list, " ")
	for _, f := range strings.Split(list, ",") {
		if f == ext {
			return true
		}
	}
	return false
}

// key names the Markdown of v in space r.
func (q *doclingQueue) key(r *repository, v VirtualInfo) string {
	_, ident := q.identity()
	return "files/" + r.id + "/" + v.SHA256 + ".docling-" + ident + ".md"
}

// wants reports a virtual file Docling converts (none until the converter
// is known).
func (q *doclingQueue) wants(r *repository, v VirtualInfo) bool {
	if q == nil {
		return false
	}
	version, ident := q.identity()
	if ident == "" {
		return false
	}
	format, ok := doclingFormats[v.Type]
	return ok && doclingConverts(version, format.ext) && r.owned && idPattern.MatchString(r.id) && objectHashPattern.MatchString(v.SHA256) && v.Size <= q.settings.maxBytes
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
		q.queuedAt[key] = time.Now()
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
	name := filepath.Join(dir, "document."+doclingFormats[j.info.Type].ext)
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

// convertRemote sends the PDF in file name to the converter service.
func (q *doclingQueue) convertRemote(ctx context.Context, name string) (string, error) {
	d := q.settings
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	query := url.Values{"maxPages": {strconv.Itoa(d.maxPages)}, "timeout": {strconv.Itoa(int(d.timeout / time.Second))}, "ocr": {d.ocr}}
	if ext := strings.TrimPrefix(filepath.Ext(name), "."); ext != "pdf" {
		query.Set("format", ext)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url+"/convert?"+query.Encode(), f)
	if err != nil {
		return "", err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+d.token)
	res, err := q.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", doclingRetrySoon{fmt.Errorf("converter not reachable")}
	}
	defer res.Body.Close()
	var out limitedBuffer
	out.limit = maxVirtualText + 4096
	_, err = io.Copy(&out, res.Body)
	if err != nil && !out.full {
		return "", doclingRetrySoon{fmt.Errorf("converter answer cut off")}
	}
	switch {
	case res.StatusCode == http.StatusOK:
		return out.String(), nil
	case res.StatusCode == http.StatusUnprocessableEntity || res.StatusCode == http.StatusRequestEntityTooLarge:
		return "", fmt.Errorf("%s", lastLine(out.String()))
	}
	return "", doclingRetrySoon{fmt.Errorf("converter answered %s: %s", res.Status, lastLine(out.String()))}
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
