package service

// The search index (0.96.0): one Bleve index per space, kept outside the
// repositories, that tells search which files can hold a query so only those
// are read. It never decides a match on its own: search still runs its line
// scan over the files the index names, so results are exactly those of a full
// scan. A file the index has not caught up with (its blob or text differs
// from what was indexed) is always scanned, so a lagging index is only slower,
// never wrong.
//
// The server's operator turns it on with global.searchIndex in the spaces
// configuration; without it search scans every file, as before. Only one
// server process may use an index folder (the data directory lock already
// ensures one process per data directory).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
)

// searchIndexConfig is global.searchIndex in the spaces configuration.
type searchIndexConfig struct {
	Enabled bool `json:"enabled"`
	// Dir holds one folder per space; default <data>/search.
	Dir string `json:"dir,omitempty"`
	// MaxOpen is the most indexes open at once (default 50, 1 to 1000).
	MaxOpen int `json:"maxOpen,omitempty"`
	// IdleMinutes closes an index nobody used for that long (default 10,
	// 1 to 1440).
	IdleMinutes int `json:"idleMinutes,omitempty"`
}

const (
	searchIndexSchema     = "1"
	searchIndexAnalyzer   = "mt-words"
	searchBatchBytes      = 4 << 20
	searchBatchFiles      = 200
	searchEdgeTermCap     = 2000
	searchRetrySkipped    = 15 * time.Minute
	searchJanitorInterval = 30 * time.Second
	searchStatusFile      = "status.json"
	searchRebuildFile     = "rebuild"
	searchRebuildAllFile  = "rebuild-all"
)

var searchIndexNamePattern = regexp.MustCompile(`^[co]-[a-z0-9][a-z0-9-]{0,63}$`)

func (c *searchIndexConfig) check(dataDir string) error {
	if c == nil || !c.Enabled {
		return nil
	}
	if c.Dir == "" {
		c.Dir = filepath.Join(dataDir, "search")
	}
	if !filepath.IsAbs(c.Dir) {
		return fmt.Errorf("searchIndex.dir must be an absolute path")
	}
	if c.MaxOpen == 0 {
		c.MaxOpen = 50
	}
	if c.IdleMinutes == 0 {
		c.IdleMinutes = 10
	}
	if c.MaxOpen < 1 || c.MaxOpen > 1000 {
		return fmt.Errorf("searchIndex.maxOpen must be 1 to 1000")
	}
	if c.IdleMinutes < 1 || c.IdleMinutes > 1440 {
		return fmt.Errorf("searchIndex.idleMinutes must be 1 to 1440")
	}
	return nil
}

// SearchIndexDir is the folder an index lives in, for the console.
func SearchIndexDir(spacesConfig, dataDir string) (string, error) {
	c, _, err := loadConfig(spacesConfig, "")
	if err != nil {
		return "", err
	}
	si := c.Global.SearchIndex
	if si == nil || !si.Enabled {
		return "", fmt.Errorf("the search index is off (global.searchIndex in %s)", spacesConfig)
	}
	if err := si.check(dataDir); err != nil {
		return "", err
	}
	return si.Dir, nil
}

// searchIndexName names a repository's index folder: c-<name> for a
// configured space, o-<id> for an owned one. Both are stable: renaming an
// owned space keeps its ID.
func searchIndexName(r *repository) string {
	base := strings.TrimSuffix(filepath.Base(r.path), ".git")
	prefix := "c-"
	if r.owned {
		prefix = "o-"
	}
	name := prefix + base
	if !searchIndexNamePattern.MatchString(name) {
		return ""
	}
	return name
}

// SearchIndexStatus is what status.json in an index folder holds.
type SearchIndexStatus struct {
	Name      string    `json:"name"`
	Schema    string    `json:"schema"`
	Files     int       `json:"files"`
	Waiting   int       `json:"waiting"`
	Bytes     int64     `json:"bytes"`
	UpdatedAt time.Time `json:"updatedAt"`
	Error     string    `json:"error,omitempty"`
}

// searchIndexes is the manager: every open index, the catch-up worker and
// the janitor that closes idle ones.
type searchIndexes struct {
	cfg  searchIndexConfig
	s    *Service
	mu   sync.Mutex
	open map[string]*spaceIndex
	// due is the spaces the worker should bring up to date, in order.
	due      []*repository
	dueSet   map[string]bool
	wake     chan struct{}
	stop     chan struct{}
	wg       sync.WaitGroup
	busy     atomic.Int32
	narrowed atomic.Int64 // searches that used an index (tests)
	// testSync makes every search bring the index up to date first, so
	// tests use the index on their first search.
	testSync bool
}

// spaceIndex is one open index. keys and terms are replaced together after
// each batch, never changed in place, so a search sees a consistent pair.
type spaceIndex struct {
	name    string
	dir     string
	idx     bleve.Index
	an      analysis.Analyzer
	users   int
	lastUse time.Time
	ready   bool // opened and its word list loaded
	failed  error
	// updMu lets one update run on the index at a time.
	updMu sync.Mutex
	view  atomic.Pointer[indexView]
	// skipped holds files the worker could not index yet (their text is
	// not available), with when it tried, so searches don't keep asking.
	skipMu  sync.Mutex
	skipped map[string]skippedFile
}

type skippedFile struct {
	key string
	at  time.Time
}

type indexView struct {
	keys  map[string]string // path -> what was indexed (see searchKey)
	terms []string          // every word in the index, sorted
}

func newSearchIndexes(s *Service, cfg searchIndexConfig) (*searchIndexes, error) {
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		return nil, fmt.Errorf("search index folder: %w", err)
	}
	m := &searchIndexes{cfg: cfg, s: s, open: map[string]*spaceIndex{}, dueSet: map[string]bool{}, wake: make(chan struct{}, 1), stop: make(chan struct{})}
	m.wg.Add(2)
	go m.worker()
	go m.janitor()
	return m, nil
}

func (m *searchIndexes) close() {
	if m == nil {
		return
	}
	close(m.stop)
	m.wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, si := range m.open {
		if si.idx != nil {
			si.idx.Close()
		}
		delete(m.open, name)
	}
}

// acquire returns r's index when it is open and ready, holding it until
// release. Otherwise it returns nil and starts opening it in the background
// when there is room, so this search scans and a later one uses the index.
func (m *searchIndexes) acquire(r *repository) *spaceIndex {
	if m == nil {
		return nil
	}
	name := searchIndexName(r)
	if name == "" {
		return nil
	}
	if m.testSync {
		m.mu.Lock()
		if m.open[name] == nil {
			m.open[name] = &spaceIndex{name: name, dir: filepath.Join(m.cfg.Dir, name), lastUse: time.Now(), skipped: map[string]skippedFile{}}
		}
		m.mu.Unlock()
		m.update(r)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	si := m.open[name]
	if si != nil {
		si.lastUse = time.Now()
		if !si.ready || si.failed != nil {
			return nil
		}
		si.users++
		return si
	}
	if len(m.open) >= m.cfg.MaxOpen && !m.evictLocked() {
		return nil
	}
	m.open[name] = &spaceIndex{name: name, dir: filepath.Join(m.cfg.Dir, name), lastUse: time.Now(), skipped: map[string]skippedFile{}}
	m.queueLocked(r)
	return nil
}

func (m *searchIndexes) release(si *spaceIndex) {
	if si == nil {
		return
	}
	m.mu.Lock()
	si.users--
	si.lastUse = time.Now()
	m.mu.Unlock()
}

// evictLocked closes the least recently used index nobody holds.
func (m *searchIndexes) evictLocked() bool {
	var oldest *spaceIndex
	for _, si := range m.open {
		if si.users == 0 && si.ready && !m.dueSet[si.name] && (oldest == nil || si.lastUse.Before(oldest.lastUse)) {
			oldest = si
		}
	}
	if oldest == nil {
		return false
	}
	m.closeLocked(oldest)
	return true
}

func (m *searchIndexes) closeLocked(si *spaceIndex) {
	delete(m.open, si.name)
	if si.idx != nil {
		if err := si.idx.Close(); err != nil {
			log.Printf("search index %s: close: %v", si.name, err)
		}
	}
}

// queue asks the worker to bring r's index up to date.
func (m *searchIndexes) queue(r *repository) {
	m.mu.Lock()
	m.queueLocked(r)
	m.mu.Unlock()
}

func (m *searchIndexes) queueLocked(r *repository) {
	name := searchIndexName(r)
	if name == "" || m.dueSet[name] {
		return
	}
	m.dueSet[name] = true
	m.due = append(m.due, r)
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *searchIndexes) worker() {
	defer m.wg.Done()
	for {
		m.mu.Lock()
		var r *repository
		if len(m.due) > 0 {
			r = m.due[0]
			m.due = m.due[1:]
		}
		m.mu.Unlock()
		if r == nil {
			select {
			case <-m.stop:
				return
			case <-m.wake:
				continue
			}
		}
		m.busy.Add(1)
		m.update(r)
		m.mu.Lock()
		delete(m.dueSet, searchIndexName(r))
		m.mu.Unlock()
		m.busy.Add(-1)
		select {
		case <-m.stop:
			return
		default:
		}
	}
}

// idle reports no queued or running work (tests).
func (m *searchIndexes) idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.due) == 0 && m.busy.Load() == 0
}

func (m *searchIndexes) janitor() {
	defer m.wg.Done()
	t := time.NewTicker(searchJanitorInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		m.closeIdle(time.Now())
		m.takeRebuildRequests()
	}
}

func (m *searchIndexes) closeIdle(now time.Time) {
	idle := time.Duration(m.cfg.IdleMinutes) * time.Minute
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, si := range m.open {
		if si.users == 0 && si.ready && !m.dueSet[si.name] && now.Sub(si.lastUse) >= idle {
			m.closeLocked(si)
		}
	}
}

// takeRebuildRequests acts on the files `metatrash search-index rebuild`
// leaves: an index folder holding "rebuild", or "rebuild-all" in the root.
// The folder is removed once closed, and built again on its next use.
func (m *searchIndexes) takeRebuildRequests() {
	all := false
	if _, err := os.Stat(filepath.Join(m.cfg.Dir, searchRebuildAllFile)); err == nil {
		all = true
		os.Remove(filepath.Join(m.cfg.Dir, searchRebuildAllFile))
	}
	entries, err := os.ReadDir(m.cfg.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !searchIndexNamePattern.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(m.cfg.Dir, e.Name())
		if _, err := os.Stat(filepath.Join(dir, searchRebuildFile)); err != nil && !all {
			continue
		}
		m.mu.Lock()
		si := m.open[e.Name()]
		if si != nil && (si.users > 0 || m.dueSet[si.name]) {
			m.mu.Unlock()
			continue // try again on the next round
		}
		if si != nil {
			m.closeLocked(si)
		}
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("search index %s: rebuild: %v", e.Name(), err)
		}
		m.mu.Unlock()
	}
}

// removeName deletes an index (its space was deleted).
func (m *searchIndexes) removeName(name string) {
	if m == nil || !searchIndexNamePattern.MatchString(name) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if si := m.open[name]; si != nil {
		m.closeLocked(si)
	}
	os.RemoveAll(filepath.Join(m.cfg.Dir, name))
}

func searchMapping() *mapping.IndexMappingImpl {
	im := bleve.NewIndexMapping()
	if err := im.AddCustomAnalyzer(searchIndexAnalyzer, map[string]any{"type": custom.Name, "tokenizer": unicode.Name, "token_filters": []string{lowercase.Name}}); err != nil {
		panic(err)
	}
	im.DefaultAnalyzer = searchIndexAnalyzer
	im.StoreDynamic, im.IndexDynamic, im.DocValuesDynamic = false, false, false
	d := bleve.NewDocumentStaticMapping()
	body := bleve.NewTextFieldMapping()
	body.Analyzer = searchIndexAnalyzer
	body.Store, body.IncludeInAll, body.DocValues, body.IncludeTermVectors = false, false, false, false
	d.AddFieldMappingsAt("body", body)
	im.DefaultMapping = d
	return im
}

const searchKeysInternal = "mt-keys"
const searchSchemaInternal = "mt-schema"

// openIndex opens (or makes) the index in si.dir and loads its word list.
// An index of another schema, or one that will not open, is built afresh.
func (m *searchIndexes) openIndex(si *spaceIndex) error {
	idx, err := bleve.Open(si.dir)
	if err == nil {
		if v, _ := idx.GetInternal([]byte(searchSchemaInternal)); string(v) != searchIndexSchema {
			idx.Close()
			err = errors.New("schema changed")
		}
	}
	if err != nil {
		if !errors.Is(err, bleve.ErrorIndexPathDoesNotExist) {
			log.Printf("search index %s: building it again (%v)", si.name, err)
		}
		if err := os.RemoveAll(si.dir); err != nil {
			return err
		}
		if idx, err = bleve.New(si.dir, searchMapping()); err != nil {
			return err
		}
		if err = idx.SetInternal([]byte(searchSchemaInternal), []byte(searchIndexSchema)); err != nil {
			idx.Close()
			return err
		}
	}
	keys := map[string]string{}
	if b, _ := idx.GetInternal([]byte(searchKeysInternal)); len(b) > 0 {
		if err := json.Unmarshal(b, &keys); err != nil {
			keys = map[string]string{}
		}
	}
	terms, err := indexTerms(idx)
	if err != nil {
		idx.Close()
		return err
	}
	si.idx = idx
	si.an = idx.Mapping().AnalyzerNamed(searchIndexAnalyzer)
	si.view.Store(&indexView{keys: keys, terms: terms})
	return nil
}

func indexTerms(idx bleve.Index) ([]string, error) {
	it, err := idx.FieldDict("body")
	if err != nil {
		return nil, err
	}
	defer it.Close()
	terms := []string{}
	for {
		e, err := it.Next()
		if err != nil {
			return nil, err
		}
		if e == nil {
			break
		}
		terms = append(terms, e.Term)
	}
	return terms, nil
}

// searchKey is what a file's searchable text is, without reading it: its
// Git blob, or for a virtual file the object its text comes from (Docling's
// Markdown when Docling converts it, else the pointer's text). "" when the
// file has no text search reads.
func (s *Service) searchKey(r *repository, f record) string {
	if f.Virtual == nil {
		return "g:" + f.Blob
	}
	v := *f.Virtual
	if q := s.docling(); q.wants(r, v) {
		return "d:" + q.key(r, v)
	}
	if v.TextSHA256 != "" {
		return "v:" + v.TextSHA256
	}
	return ""
}

// update brings r's index up to the files at its head. Called by the worker.
func (m *searchIndexes) update(r *repository) {
	name := searchIndexName(r)
	m.mu.Lock()
	si := m.open[name]
	m.mu.Unlock()
	if si == nil || r.closed.Load() {
		return
	}
	si.updMu.Lock()
	defer si.updMu.Unlock()
	if si.failed != nil {
		return
	}
	if si.idx == nil {
		err := m.openIndex(si)
		m.mu.Lock()
		if err != nil {
			si.failed = err
			log.Printf("search index %s: %v", name, err)
			// Leave it in the table (failed) until idle closes it, so
			// searches don't keep retrying.
			si.ready = true
			m.mu.Unlock()
			m.writeStatus(si, 0, err)
			return
		}
		si.ready = true
		m.mu.Unlock()
	}
	waiting, err := m.catchUp(r, si)
	if err != nil {
		log.Printf("search index %s: %v", name, err)
	}
	m.writeStatus(si, waiting, err)
}

// catchUp indexes what changed, in batches, and returns how many files are
// still waiting (their text was not available).
func (m *searchIndexes) catchUp(r *repository, si *spaceIndex) (int, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-m.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return 0, err
	}
	view := si.view.Load()
	want := map[string]string{}
	for p, f := range files {
		if k := m.s.searchKey(r, f); k != "" {
			want[p] = k
		}
	}
	var changed, gone []string
	for p, k := range want {
		if view.keys[p] != k {
			changed = append(changed, p)
		}
	}
	for p := range view.keys {
		if _, ok := want[p]; !ok {
			gone = append(gone, p)
		}
	}
	sort.Strings(changed)
	sort.Strings(gone)
	if len(changed) == 0 && len(gone) == 0 {
		return 0, nil
	}
	keys := make(map[string]string, len(view.keys))
	for p, k := range view.keys {
		keys[p] = k
	}
	newTerms := map[string]bool{}
	waiting := 0
	b := si.idx.NewBatch()
	batchBytes, batchFiles := 0, 0
	flush := func() error {
		if batchFiles == 0 {
			return nil
		}
		kb, err := json.Marshal(keys)
		if err != nil {
			return err
		}
		b.SetInternal([]byte(searchKeysInternal), kb)
		if err := si.idx.Batch(b); err != nil {
			return err
		}
		b.Reset()
		batchBytes, batchFiles = 0, 0
		si.view.Store(&indexView{keys: copyKeys(keys), terms: mergeTerms(si.view.Load().terms, newTerms)})
		clear(newTerms)
		return nil
	}
	for _, p := range gone {
		b.Delete(p)
		delete(keys, p)
		batchFiles++
	}
	// Git texts in groups, so one cat-file serves many files.
	for start := 0; start < len(changed); {
		if ctx.Err() != nil {
			return waiting, ctx.Err()
		}
		end := start
		hashes := []string{}
		size := 0
		for end < len(changed) && size < searchBatchBytes && end-start < searchBatchFiles {
			f := files[changed[end]]
			if f.Virtual == nil {
				hashes = append(hashes, f.Blob)
				size += int(f.Bytes)
			}
			end++
		}
		texts, err := r.blobs(ctx, hashes)
		if err != nil {
			return waiting, err
		}
		for _, p := range changed[start:end] {
			f := files[p]
			var text string
			if f.Virtual == nil {
				text = string(texts[f.Blob])
			} else {
				t, ok := m.virtualText(ctx, r, p, f, want[p])
				if !ok {
					// Not indexed: search keeps scanning it.
					b.Delete(p)
					delete(keys, p)
					si.skip(p, want[p])
					waiting++
					batchFiles++
					continue
				}
				text = t
			}
			for _, tok := range si.an.Analyze([]byte(text)) {
				newTerms[string(tok.Term)] = true
			}
			if err := b.Index(p, map[string]any{"body": text}); err != nil {
				return waiting, err
			}
			keys[p] = want[p]
			batchBytes += len(text)
			batchFiles++
			if batchBytes >= searchBatchBytes || batchFiles >= searchBatchFiles {
				if err := flush(); err != nil {
					return waiting, err
				}
			}
		}
		start = end
	}
	return waiting, flush()
}

// virtualText reads a virtual file's text only when it is the text key
// names (Docling's Markdown made, or the pointer's text), so the index never
// holds a text search would not read.
func (m *searchIndexes) virtualText(ctx context.Context, r *repository, path string, f record, key string) (string, bool) {
	v := *f.Virtual
	if strings.HasPrefix(key, "d:") {
		text, ok, _ := m.s.docling().text(ctx, r, v)
		return text, ok
	}
	text, err := m.s.pointerTextOf(ctx, r, path, v)
	return text, err == nil
}

func (si *spaceIndex) skip(path, key string) {
	si.skipMu.Lock()
	si.skipped[path] = skippedFile{key: key, at: time.Now()}
	si.skipMu.Unlock()
}

// recentlySkipped reports a file the worker could not index lately, so a
// search does not queue it again at once.
func (si *spaceIndex) recentlySkipped(path, key string) bool {
	si.skipMu.Lock()
	defer si.skipMu.Unlock()
	s, ok := si.skipped[path]
	return ok && s.key == key && time.Since(s.at) < searchRetrySkipped
}

func copyKeys(keys map[string]string) map[string]string {
	out := make(map[string]string, len(keys))
	for p, k := range keys {
		out[p] = k
	}
	return out
}

// mergeTerms adds new words to a sorted word list. Words of removed files
// stay: the list may hold more than the index, never less.
func mergeTerms(old []string, add map[string]bool) []string {
	if len(add) == 0 {
		return old
	}
	extra := make([]string, 0, len(add))
	for t := range add {
		if i := sort.SearchStrings(old, t); i == len(old) || old[i] != t {
			extra = append(extra, t)
		}
	}
	if len(extra) == 0 {
		return old
	}
	sort.Strings(extra)
	out := make([]string, 0, len(old)+len(extra))
	i, j := 0, 0
	for i < len(old) || j < len(extra) {
		if j == len(extra) || (i < len(old) && old[i] < extra[j]) {
			out = append(out, old[i])
			i++
		} else {
			out = append(out, extra[j])
			j++
		}
	}
	return out
}

func (m *searchIndexes) writeStatus(si *spaceIndex, waiting int, err error) {
	st := SearchIndexStatus{Name: si.name, Schema: searchIndexSchema, Waiting: waiting, UpdatedAt: time.Now().UTC()}
	if v := si.view.Load(); v != nil {
		st.Files = len(v.keys)
	}
	if err != nil {
		st.Error = err.Error()
	}
	filepath.WalkDir(si.dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if i, err := d.Info(); err == nil {
				st.Bytes += i.Size()
			}
		}
		return nil
	})
	b, _ := json.MarshalIndent(st, "", "  ")
	tmp := filepath.Join(si.dir, searchStatusFile+".tmp")
	if os.WriteFile(tmp, b, 0600) == nil {
		os.Rename(tmp, filepath.Join(si.dir, searchStatusFile))
	}
}

// candidates returns the files under search that can hold query, or ok
// false when the index cannot narrow it (search then reads every file).
// paths are the files search would read, sorted; keys their searchKey.
// Files the index has not caught up with are always candidates, and when
// there are any, the worker is asked to catch up.
func (m *searchIndexes) candidates(r *repository, si *spaceIndex, q string, paths []string, keys map[string]string) (out []string, ok bool) {
	view := si.view.Load()
	words := []string{}
	for _, t := range si.an.Analyze([]byte(q)) {
		words = append(words, string(t.Term))
	}
	if len(words) == 0 {
		return nil, false
	}
	// A query may start or end in the middle of a word ("ogin" in
	// "login"), so the first word matches any word ending with it, the last
	// any word starting with it, and a single word any word containing it.
	// Words in between are whole.
	slots := make([][]string, len(words))
	if len(words) == 1 {
		w := words[0]
		slots[0] = expandTerms(view.terms, "", func(t string) bool { return strings.Contains(t, w) })
	} else {
		first, last := words[0], words[len(words)-1]
		slots[0] = expandTerms(view.terms, "", func(t string) bool { return strings.HasSuffix(t, first) })
		slots[len(words)-1] = expandTerms(view.terms, last, func(string) bool { return true })
		for i := 1; i < len(words)-1; i++ {
			slots[i] = []string{words[i]}
		}
	}
	// An edge matching too many words is left out; that only widens the
	// answer.
	var conj []query.Query
	for i, slot := range slots {
		if slot == nil && (i == 0 || i == len(slots)-1) {
			continue
		}
		var ds []query.Query
		for _, t := range slot {
			tq := bleve.NewTermQuery(t)
			tq.SetField("body")
			ds = append(ds, tq)
		}
		if len(ds) == 0 {
			ds = append(ds, bleve.NewMatchNoneQuery())
		}
		conj = append(conj, bleve.NewDisjunctionQuery(ds...))
	}
	if len(conj) == 0 {
		return nil, false
	}
	hit := map[string]bool{}
	if count, _ := si.idx.DocCount(); count > 0 {
		req := bleve.NewSearchRequestOptions(bleve.NewConjunctionQuery(conj...), int(count), 0, false)
		req.Score = "none"
		res, err := si.idx.Search(req)
		if err != nil {
			log.Printf("search index %s: %v", si.name, err)
			return nil, false
		}
		for _, h := range res.Hits {
			hit[h.ID] = true
		}
	}
	behind := false
	out = make([]string, 0, len(hit))
	for _, p := range paths {
		if view.keys[p] == keys[p] {
			if hit[p] {
				out = append(out, p)
			}
			continue
		}
		out = append(out, p)
		if !si.recentlySkipped(p, keys[p]) {
			behind = true
		}
	}
	if behind {
		m.queue(r)
	}
	m.narrowed.Add(1)
	return out, true
}

func expandTerms(terms []string, prefix string, match func(string) bool) []string {
	out := []string{}
	start := 0
	if prefix != "" {
		start = sort.SearchStrings(terms, prefix)
	}
	for _, t := range terms[start:] {
		if prefix != "" && !strings.HasPrefix(t, prefix) {
			break
		}
		if match(t) {
			if len(out) == searchEdgeTermCap {
				return nil
			}
			out = append(out, t)
		}
	}
	return out
}

// SearchIndexStatuses reads status.json from every index folder in dir, by
// folder name.
func SearchIndexStatuses(dir string) ([]SearchIndexStatus, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := []SearchIndexStatus{}
	for _, e := range entries {
		if !e.IsDir() || !searchIndexNamePattern.MatchString(e.Name()) {
			continue
		}
		st := SearchIndexStatus{Name: e.Name()}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), searchStatusFile))
		if err == nil {
			err = json.Unmarshal(b, &st)
		}
		if err != nil {
			st.Error = "no status yet"
		}
		st.Name = e.Name()
		if _, err := os.Stat(filepath.Join(dir, e.Name(), searchRebuildFile)); err == nil {
			st.Error = strings.TrimSpace(st.Error + " rebuild requested")
		}
		out = append(out, st)
	}
	return out, nil
}

// SearchIndexFolder turns what a person types (c-public, o-<id>, a
// configured space's name or an owned space's ID) into an index folder name.
func SearchIndexFolder(arg string) (string, error) {
	switch {
	case searchIndexNamePattern.MatchString(arg):
		return arg, nil
	case idPattern.MatchString(arg):
		return "o-" + arg, nil
	case spacePattern.MatchString(arg):
		return "c-" + arg, nil
	}
	return "", fmt.Errorf("%q is not a space name, space ID or index folder", arg)
}

// RequestSearchIndexRebuild leaves the file the running server acts on
// within a minute: the index is removed and built again on its next use.
// name "" asks for every index.
func RequestSearchIndexRebuild(dir, name string) error {
	if name == "" {
		return os.WriteFile(filepath.Join(dir, searchRebuildAllFile), nil, 0600)
	}
	folder := filepath.Join(dir, name)
	if _, err := os.Stat(folder); err != nil {
		return fmt.Errorf("no index %s in %s (it is built on the space's first search)", name, dir)
	}
	return os.WriteFile(filepath.Join(folder, searchRebuildFile), nil, 0600)
}
