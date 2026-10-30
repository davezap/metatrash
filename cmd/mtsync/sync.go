package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// stateDir holds mtsync's own files inside the synced folder. It is never
// uploaded.
const stateDir = ".mtsync"

// baseFile is a file as it was when local and remote last agreed. Virtual
// is set for a virtual file (mtsync 0.61.0): Blob is then its pointer's, and
// the local file holds the content Virtual describes.
type baseFile struct {
	ID      string       `json:"id"`
	Blob    string       `json:"blob"`
	Virtual *virtualInfo `json:"virtual,omitempty"`
}

// The three sides of a path are compared by key: an ordinary file's Git blob,
// or "sha256:" and a virtual file's content hash, so a change to a virtual
// file's pointer alone (a new source or refresh schedule) is no change to
// the file. "" is a path that is not there.
const contentKey = "sha256:"

func (f baseFile) key() string {
	if f.Virtual != nil {
		return contentKey + f.Virtual.SHA256
	}
	return f.Blob
}

func (f remoteFile) key() string {
	if f.Virtual != nil {
		return contentKey + f.Virtual.SHA256
	}
	return f.Blob
}

// localFile is a local file's Git blob and SHA-256 ("" when it is not there).
type localFile struct {
	blob, sha string
}

// key is the local file compared with a virtual file, or an ordinary one.
func (l localFile) key(virtual bool) string {
	switch {
	case l.blob == "":
		return ""
	case virtual:
		return contentKey + l.sha
	}
	return l.blob
}

// syncState is saved in .mtsync/state.json. Files is the last agreed version
// of every path, the base of the three-way comparison. State is the space
// state at which Files matched the space exactly; it is empty while a pass is
// unfinished, so the next pass lists the space again.
type syncState struct {
	Server string              `json:"server"`
	Space  string              `json:"space"`
	State  string              `json:"state"`
	Files  map[string]baseFile `json:"files"`
	// Virtual is set once Files says which files are virtual (mtsync
	// 0.61.0); a state from before lists the space in full once, as only a
	// listing says so.
	Virtual bool `json:"virtual,omitempty"`
}

type localEntry struct {
	size  int64
	mtime time.Time
	file  localFile
}

type syncer struct {
	c         *client
	root      string
	st        *syncState
	statePath string
	logf      func(format string, args ...any)
	now       func() time.Time
	// settle is how long a file must be unchanged before it is uploaded, so
	// a save in progress is not sent half written.
	settle time.Duration
	cache  map[string]localEntry
	// warned remembers refused uploads by path and blob, so a refusal is
	// reported once and retried only after the file changes.
	warned   map[string]string
	readOnly bool
	// leftEmpty holds folders a deletion emptied but could not remove.
	leftEmpty map[string]bool
	// noChanges is set when the server can't list changes (before 0.37.0).
	noChanges bool
	// onPass, when set, hears how each pass ended (nil when it completed),
	// and hears nil after a scan that found the folder and the space as they
	// were, so the status shows when the folder was last checked.
	onPass func(err error)
	// poke, when set, asks for a pass at once (mtsync sync).
	poke <-chan struct{}
	// remoteLive is true while watchRemote's last request to the space
	// succeeded, so a quiet scan can count as a check of both sides.
	remoteLive atomic.Bool
}

// lock keeps a second mtsync (the service and a command, say) from syncing
// the same folder at once.
func (s *syncer) lock() (func(), error) {
	l, err := acquireLock(filepath.Join(s.root, stateDir, "lock"), s.root)
	if err != nil {
		return nil, err
	}
	return l.release, nil
}

var errRestart = errors.New("the space changed during the pass")

// gitBlob is Git's blob hash of text, as the server reports it.
func gitBlob(b []byte) string {
	h := sha1.New()
	h.Write([]byte("blob " + strconv.Itoa(len(b)) + "\x00"))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func hashes(b []byte) localFile {
	sum := sha256.Sum256(b)
	return localFile{blob: gitBlob(b), sha: hex.EncodeToString(sum[:])}
}

// isText reports content a space holds as text: UTF-8 without NUL. Anything
// else is a virtual file.
func isText(b []byte) bool {
	return utf8.Valid(b) && bytes.IndexByte(b, 0) < 0
}

func openSyncer(c *client, root, server, space string, logf func(string, ...any)) (*syncer, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, stateDir), 0700); err != nil {
		return nil, err
	}
	s := &syncer{c: c, root: root, statePath: filepath.Join(root, stateDir, "state.json"), logf: logf, now: time.Now, settle: time.Second, cache: map[string]localEntry{}, warned: map[string]string{}}
	st := &syncState{Server: server, Space: space, Files: map[string]baseFile{}}
	b, err := os.ReadFile(s.statePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, st); err != nil {
			return nil, fmt.Errorf("%s is damaged: %v", s.statePath, err)
		}
		if st.Server != server || st.Space != space {
			return nil, fmt.Errorf("%s already syncs %s on %s; use another folder", root, st.Space, st.Server)
		}
		if st.Files == nil {
			st.Files = map[string]baseFile{}
		}
	}
	s.st = st
	return s, nil
}

func (s *syncer) save() error {
	b, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.statePath, b, 0600)
}

// folderFile reports whether path is a folder's AGENTS.md or .metatrash.json,
// which the space keeps while the folder holds other files (Metatrash 0.45.0).
// The space says so itself in a full listing; this covers files known from
// the last agreed state.
func folderFile(path string) bool {
	name := path[strings.LastIndex(path, "/")+1:]
	return name == ".metatrash.json" || strings.EqualFold(name, "AGENTS.md")
}

// ignored names are editor and system files that never belong in a space.
func ignored(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case ".ds_store", "thumbs.db", "desktop.ini":
		return true
	}
	return strings.HasSuffix(name, "~") || strings.HasSuffix(lower, ".swp") || strings.HasSuffix(lower, ".swx") || strings.HasSuffix(lower, ".tmp") ||
		strings.HasPrefix(name, ".~lock.") || strings.HasPrefix(name, "~$") || (strings.HasPrefix(name, "#") && strings.HasSuffix(name, "#"))
}

func (s *syncer) full(p string) string { return filepath.Join(s.root, filepath.FromSlash(p)) }

// scan hashes every local file, rereading only files whose size or time
// changed. Files modified within the settle time are returned as unstable.
func (s *syncer) scan() (map[string]localFile, map[string]bool, error) {
	files, unstable := map[string]localFile{}, map[string]bool{}
	seen := map[string]bool{}
	var empty []string
	err := filepath.WalkDir(s.root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			if full == s.root {
				return err
			}
			s.logf("cannot read %s: %v", full, err)
			return nil
		}
		rel, _ := filepath.Rel(s.root, full)
		p := filepath.ToSlash(rel)
		if d.IsDir() {
			// A space can never hold .git, so a local Git repository in the
			// folder (for your own history) is left alone.
			if p == stateDir || strings.EqualFold(d.Name(), ".git") {
				return filepath.SkipDir
			}
			if full != s.root {
				if entries, err := os.ReadDir(full); err == nil && len(entries) == 0 {
					empty = append(empty, p)
				}
			}
			return nil
		}
		if !d.Type().IsRegular() || ignored(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		seen[p] = true
		if s.now().Sub(info.ModTime()) < s.settle && s.settle > 0 {
			unstable[p] = true
			return nil
		}
		if e, ok := s.cache[p]; ok && e.size == info.Size() && e.mtime.Equal(info.ModTime()) {
			files[p] = e.file
			return nil
		}
		b, err := os.ReadFile(full)
		if err != nil {
			unstable[p] = true
			return nil
		}
		file := hashes(b)
		s.cache[p] = localEntry{size: info.Size(), mtime: info.ModTime(), file: file}
		files[p] = file
		return nil
	})
	for _, dir := range empty {
		if p, ok := s.startFolder(dir); ok {
			seen[p] = true
			files[p] = s.cache[p].file
		}
	}
	for p := range s.cache {
		if !seen[p] {
			delete(s.cache, p)
		}
	}
	return files, unstable, err
}

// folderNamePattern is a folder name the space accepts (no dot names, which
// only GitHub folders allow).
var folderNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// newFolderConfig is the .metatrash.json mtsync puts in a new empty folder,
// as the space does for a folder's first file.
const newFolderConfig = "{\"inherit\": true}\n"

// startFolder gives a new empty local folder its .metatrash.json, so the
// folder reaches the space, which adds its AGENTS.md (0.66.0). A folder in a
// GitHub folder, or with a name the space refuses, is left alone, and so is
// every folder of a read-only space. It returns the file's path.
func (s *syncer) startFolder(dir string) (string, bool) {
	if s.readOnly || s.leftEmpty[dir] || strings.Count(dir, "/") > 6 || len(dir) > 200 {
		return "", false
	}
	for _, name := range strings.Split(dir, "/") {
		if !folderNamePattern.MatchString(name) {
			return "", false
		}
	}
	for parent := path.Dir(dir); ; parent = path.Dir(parent) {
		if parent == "." {
			parent = ""
		}
		if githubConfig(s.full(path.Join(parent, ".metatrash.json"))) {
			return "", false
		}
		if parent == "" {
			break
		}
	}
	p := dir + "/.metatrash.json"
	b := []byte(newFolderConfig)
	if err := writeFileAtomic(s.full(p), b, 0644); err != nil {
		s.logf("cannot start folder %s: %v", dir, err)
		return "", false
	}
	info, err := os.Stat(s.full(p))
	if err != nil {
		return "", false
	}
	s.cache[p] = localEntry{size: info.Size(), mtime: info.ModTime(), file: hashes(b)}
	s.logf("new folder %s: added .metatrash.json", dir)
	return p, true
}

// githubConfig reports whether the .metatrash.json at full attaches a GitHub
// repository.
func githubConfig(full string) bool {
	b, err := os.ReadFile(full)
	if err != nil {
		return false
	}
	var c struct {
		Services []struct {
			Type string `json:"type"`
		} `json:"services"`
	}
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	for _, service := range c.Services {
		if service.Type == "github" {
			return true
		}
	}
	return false
}

// currentBlob hashes a local file now; "" when it does not exist.
func (s *syncer) currentBlob(p string) (string, error) {
	b, err := os.ReadFile(s.full(p))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return gitBlob(b), nil
}

// pass is one sync round: compare local files, the space and the last agreed
// base, then apply each side's changes to the other.
type pass struct {
	*syncer
	ctx      context.Context
	remote   map[string]remoteFile
	snapshot string // state the remote listing came from
	current  string // latest state, for ifInState
	local    map[string]localFile
	unstable map[string]bool
	// deferred is set when a path whose space version differs from the base
	// is left for a later pass, so the next pass must list the space again.
	deferred bool
	changed  bool
}

// cycle runs one pass. The space's side comes from the changes since the
// last agreed state (one request, holding only what changed), or from a full
// listing on the first pass, after an unfinished one, or when the server
// can't give changes.
func (s *syncer) cycle(ctx context.Context) error {
	p := &pass{syncer: s, ctx: ctx, remote: map[string]remoteFile{}}
	known := false
	if s.st.State != "" && s.st.Virtual {
		var err error
		if known, err = p.remoteFromChanges(); err != nil {
			return err
		}
	}
	if !known {
		state, files, err := s.c.list(ctx)
		if err != nil {
			return err
		}
		p.snapshot, p.current = state, state
		for _, f := range files {
			p.remote[f.Path] = f
		}
	}
	var err error
	if p.local, p.unstable, err = s.scan(); err != nil {
		return err
	}
	if s.st.State != "" {
		s.st.State = ""
		if err := s.save(); err != nil {
			return err
		}
	}
	if err := p.run(); err != nil {
		if p.changed {
			_ = s.save()
		}
		return err
	}
	s.st.Virtual = true
	if !p.deferred {
		// The state the pass started from, not the one after its own writes:
		// a write can also add or remove a folder's AGENTS.md and
		// .metatrash.json, which the next pass then hears of as changes.
		s.st.State = p.snapshot
	}
	return s.save()
}

// remoteFromChanges builds the space's side from the last agreed files and
// what changed since. It reports false when a full listing is needed: the
// agreed state is gone (the space was reset), or the server is older than
// 0.37.0, which mtsync then stops asking (head and list instead).
func (p *pass) remoteFromChanges() (bool, error) {
	base := p.st.Files
	fromBase := func() {
		for path, f := range base {
			p.remote[path] = remoteFile{ID: f.ID, Path: path, Blob: f.Blob, Deletable: !folderFile(path), Virtual: f.Virtual}
		}
	}
	if p.noChanges {
		head, err := p.c.head(p.ctx)
		if err != nil || head != p.st.State {
			return false, err
		}
		p.snapshot, p.current = head, head
		fromBase()
		return true, nil
	}
	state, changed, removed, err := p.c.changes(p.ctx, p.st.State)
	switch errorCode(err) {
	case "":
	case "invalid_request":
		p.noChanges = true
		return p.remoteFromChanges()
	case "not_found":
		// Gone from this space's history, or the space itself: a full
		// listing tells which.
		return false, nil
	default:
		return false, err
	}
	p.snapshot, p.current = state, state
	fromBase()
	for _, path := range removed {
		delete(p.remote, path)
	}
	for _, f := range changed {
		p.remote[f.Path] = f
	}
	return true, nil
}

func (p *pass) run() error {
	all := map[string]bool{}
	for path := range p.st.Files {
		all[path] = true
	}
	for path := range p.remote {
		all[path] = true
	}
	for path := range p.local {
		all[path] = true
	}
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	handled, err := p.moves(paths)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if handled[path] {
			continue
		}
		base, rf, lf := p.st.Files[path], p.remote[path], p.local[path]
		b, r := base.key(), rf.key()
		if base.Virtual == nil && rf.Virtual != nil && base.Blob != "" && base.Blob == rf.Blob {
			// Agreed by mtsync before 0.61.0, which downloaded a virtual
			// file's text instead of its content and could never upload it:
			// the local copy is that text, so the content replaces it.
			b = r
			if lf.blob != "" && lf.key(true) != r && !p.unstable[path] {
				if err := p.pullChange(path, localFile{blob: "old text", sha: "old text"}, rf); err != nil {
					return err
				}
				continue
			}
		}
		// The local file as the space side knows it, and as the base does.
		l, lb := lf.key(rf.Virtual != nil), lf.key(base.Virtual != nil)
		if p.unstable[path] {
			if r != b {
				p.deferred = true
			}
			continue
		}
		var err error
		switch {
		case l == r:
			p.agree(path)
		case lb == b:
			err = p.pullChange(path, lf, rf)
		case r == b:
			err = p.push(path, lf)
		case l == "":
			// Deleted here, edited in the space: keep the edit.
			err = p.pullChange(path, lf, rf)
		case r == "":
			// Deleted in the space, edited here: keep the edit.
			err = p.push(path, lf)
		default:
			err = p.conflict(path, lf, rf)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// agree records the space's version of path as the agreed one (none when
// the space has no file there).
func (p *pass) agree(path string) {
	old, had := p.st.Files[path]
	f, ok := p.remote[path]
	if !ok || f.key() == "" {
		if had {
			delete(p.st.Files, path)
			p.changed = true
		}
		return
	}
	next := baseFile{ID: f.ID, Blob: f.Blob, Virtual: f.Virtual}
	if !had || !sameBase(old, next) {
		p.st.Files[path] = next
		p.changed = true
	}
}

func sameBase(a, b baseFile) bool {
	if a.ID != b.ID || a.Blob != b.Blob || (a.Virtual == nil) != (b.Virtual == nil) {
		return false
	}
	return a.Virtual == nil || *a.Virtual == *b.Virtual
}

// moves turns a local rename (a path gone and a new path with the same
// content) into a move in the space, which keeps the file's ID and history.
func (p *pass) moves(paths []string) (map[string]bool, error) {
	handled := map[string]bool{}
	if p.readOnly {
		return handled, nil
	}
	gone := map[string][]string{}
	for _, path := range paths {
		base := p.st.Files[path]
		if b := base.key(); b != "" && p.local[path].blob == "" && !p.unstable[path] && p.remote[path].key() == b && p.remote[path].Deletable {
			gone[b] = append(gone[b], path)
		}
	}
	for _, to := range paths {
		lf := p.local[to]
		if lf.blob == "" || p.st.Files[to].key() != "" || p.remote[to].key() != "" || p.unstable[to] || p.wasWarned(to, lf.blob) {
			continue
		}
		key := lf.key(false)
		if len(gone[key]) == 0 {
			key = lf.key(true)
		}
		if len(gone[key]) == 0 {
			continue
		}
		from := gone[key][0]
		m, err := p.c.move(p.ctx, from, to, p.current)
		if err != nil {
			if err := p.refused(to, lf.blob, err); err != nil {
				return nil, err
			}
			continue
		}
		gone[key] = gone[key][1:]
		p.current = m.NewState
		f := p.remote[from]
		delete(p.remote, from)
		f.Path, f.ID = to, m.File.ID
		p.remote[to] = f
		delete(p.st.Files, from)
		p.st.Files[to] = baseFile{ID: m.File.ID, Blob: f.Blob, Virtual: f.Virtual}
		p.changed = true
		handled[from], handled[to] = true, true
		p.logf("moved %s -> %s", from, to)
	}
	return handled, nil
}

// pullChange makes the local file match the space's version rf (none
// deletes it), provided the local file is still lf.
func (p *pass) pullChange(path string, lf localFile, rf remoteFile) error {
	if now, err := p.currentBlob(path); err != nil || now != lf.blob && lf.blob != "old text" || lf.blob == "old text" && now == "" {
		p.deferred = true
		return nil
	}
	if rf.key() == "" {
		if err := os.Remove(p.full(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		p.pruneDirs(path)
		delete(p.cache, path)
		p.agree(path)
		p.logf("deleted %s (removed in the space)", path)
		return nil
	}
	if rf.Virtual != nil {
		if err := p.downloadVirtual(path, rf); err != nil {
			return err
		}
	} else {
		text, err := p.content(path, rf.Blob)
		if err != nil {
			return err
		}
		if err := p.writeLocal(path, text); err != nil {
			return err
		}
	}
	p.agree(path)
	switch {
	case lf.blob == "old text":
		p.logf("downloaded %s whole (it is a virtual file: mtsync before 0.61.0 kept its text only)", path)
	case lf.blob == "":
		p.logf("downloaded %s", path)
	default:
		p.logf("updated %s", path)
	}
	return nil
}

// content returns the space's text for path with blob r, copying it from a
// local file with the same content when there is one (a move or copy in the
// space) instead of downloading it.
func (p *pass) content(path, r string) ([]byte, error) {
	if b, ok := p.localCopy(r, false); ok {
		return b, nil
	}
	text, err := p.c.read(p.ctx, path, p.snapshot)
	if err != nil {
		return nil, err
	}
	return []byte(text), nil
}

// localCopy reads a stable local file whose key (as a virtual file, or an
// ordinary one) is key.
func (p *pass) localCopy(key string, virtual bool) ([]byte, bool) {
	for other, lf := range p.local {
		if lf.key(virtual) == key && !p.unstable[other] {
			if b, err := os.ReadFile(p.full(other)); err == nil && hashes(b).key(virtual) == key {
				return b, true
			}
		}
	}
	return nil, false
}

// downloadVirtual puts a virtual file's content at path: copied from a local
// file with the same content when there is one, else downloaded at the
// pass's snapshot into a temporary file beside it, checked against its
// hash, then renamed into place.
func (p *pass) downloadVirtual(path string, rf remoteFile) error {
	if b, ok := p.localCopy(rf.key(), true); ok {
		return p.writeLocal(path, b)
	}
	full := p.full(path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(full), ".mtsync-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	h := sha256.New()
	n, err := p.c.download(p.ctx, path, p.snapshot, io.MultiWriter(f, h))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != rf.Virtual.SHA256 || n != rf.Virtual.Size {
		return fmt.Errorf("%s: the download does not match the space's file (%d bytes, sha256 %s); trying again later", path, n, got)
	}
	if err := os.Chmod(tmp, 0644); err != nil {
		return err
	}
	if err := renameRetry(tmp, full); err != nil {
		return err
	}
	// The next scan hashes it, once.
	delete(p.cache, path)
	return nil
}

func (p *pass) writeLocal(path string, b []byte) error {
	full := p.full(path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		return err
	}
	if err := writeFileAtomic(full, b, 0644); err != nil {
		return err
	}
	if info, err := os.Stat(full); err == nil {
		p.cache[path] = localEntry{size: info.Size(), mtime: info.ModTime(), file: hashes(b)}
	}
	return nil
}

// pruneDirs removes folders left empty by a deletion, up to the root. One
// it cannot remove (another program holds it open) is remembered, so scan
// does not take it for a new folder and bring it back to the space.
func (p *pass) pruneDirs(file string) {
	for dir := path.Dir(file); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if os.Remove(p.full(dir)) != nil {
			if entries, err := os.ReadDir(p.full(dir)); err == nil && len(entries) == 0 {
				if p.leftEmpty == nil {
					p.leftEmpty = map[string]bool{}
				}
				p.leftEmpty[dir] = true
			}
			return
		}
	}
}

// push sends the local version of path (lf; none deletes it) to the space:
// UTF-8 text as a file's text, anything else, or new content for a virtual
// file, as a virtual file's content.
func (p *pass) push(path string, lf localFile) error {
	if p.readOnly || p.wasWarned(path, lf.blob) {
		return nil
	}
	rf, existed := p.remote[path]
	if lf.blob == "" {
		if existed && !rf.Deletable {
			// A folder's AGENTS.md and .metatrash.json cannot be deleted:
			// restore them. When the folder's other files went too, the
			// space removes them and the next pass deletes them here.
			return p.pullChange(path, localFile{}, rf)
		}
		m, err := p.c.remove(p.ctx, path, p.current)
		if err != nil {
			return p.refused(path, lf.blob, err)
		}
		p.current = m.NewState
		delete(p.remote, path)
		p.agree(path)
		p.logf("deleted %s in the space", path)
		return nil
	}
	b, err := os.ReadFile(p.full(path))
	if errors.Is(err, os.ErrNotExist) {
		p.deferred = true
		return nil
	}
	if err != nil {
		return err
	}
	now := hashes(b)
	if now != lf {
		// Changed since the scan: the next pass looks again.
		p.deferred = true
		return nil
	}
	var m mutation
	virtual := rf.Virtual != nil || !isText(b)
	switch {
	case virtual && rf.Virtual != nil && rf.Virtual.Source != "":
		// Fetched from its source (and maybe refreshed from it): the space
		// keeps the source's version, so a local change goes to a copy.
		return p.restoreLocked(path, lf, rf, fmt.Sprintf("%s comes from %s, so it can't be changed here", path, rf.Virtual.Source))
	case virtual && !p.c.account:
		p.warn(path, lf.blob, "%s is not UTF-8 text; a space reached with a key holds text only, so it stays local", path)
		return nil
	case virtual && len(b) > maxVirtualBytes:
		p.warn(path, lf.blob, "%s is larger than %d MB, the most a space holds in one file, so it stays local", path, maxVirtualBytes>>20)
		return nil
	case virtual:
		if m, err = p.upload(path, lf, b); err != nil || m.NewState == "" {
			return err
		}
	default:
		m, err = p.c.write(p.ctx, path, string(b), p.current)
		if errorCode(err) == "payload_too_large" && p.c.account && len(b) <= maxVirtualBytes {
			// Text over the space's file limit is kept as a virtual file.
			if m, err = p.upload(path, lf, b); err != nil || m.NewState == "" {
				return err
			}
			break
		}
		if err != nil {
			return p.refused(path, lf.blob, err)
		}
		m.File.Blob = lf.blob
	}
	p.current = m.NewState
	p.remote[path] = m.File
	p.agree(path)
	if existed {
		p.logf("uploaded %s", path)
	} else {
		p.logf("uploaded new %s", path)
	}
	return nil
}

// upload sends b as the virtual file at path. An empty mutation with no
// error means the file stays local (warned once).
func (p *pass) upload(path string, lf localFile, b []byte) (mutation, error) {
	m, err := p.c.upload(p.ctx, path, b, p.current)
	if errorCode(err) == "not_found" {
		// The route is missing: a server before 0.60.0.
		p.warn(path, lf.blob, "%s is not UTF-8 text or is too large for a text file, and this server keeps text only (binary files need metatrash 0.60.0), so it stays local", path)
		return mutation{}, nil
	}
	if err != nil {
		return mutation{}, p.refused(path, lf.blob, err)
	}
	if m.File.Virtual == nil || m.File.Virtual.SHA256 != lf.sha {
		return mutation{}, fmt.Errorf("%s: the space did not store the content uploaded", path)
	}
	return m, nil
}

// refused handles a failed upload: a stale state restarts the pass, a lost
// connection stops it, and a refusal of this file is reported once.
func (p *pass) refused(path, blob string, err error) error {
	var e *apiError
	if !errors.As(err, &e) {
		return err
	}
	switch e.Code {
	case "state_mismatch":
		return errRestart
	case "insufficient_scope", "forbidden":
		if e.Code == "forbidden" && p.c.account {
			// On a sign-in connection this refuses one file (such as a
			// GitHub workflow); with a space key it means a read-only key.
			break
		}
		p.readOnly = true
		p.logf("this computer can only read %s, so local changes stay here. Signed-in access is changed on Your account -> Services.", p.st.Space)
		return nil
	case "rate_limited", "queue_full", "internal_error", "http_error", "unauthorized":
		return err
	case "read_only_file":
		// Locked for everyone in the space's settings: put the space's
		// version back, keeping a local change as a conflict copy.
		if r, ok := p.remote[path]; ok {
			return p.restoreLocked(path, p.local[path], r, path+" is read-only in the space")
		}
	}
	p.warn(path, blob, "%s was not uploaded: %s", path, e.Message)
	return nil
}

// restoreLocked puts back the space's version rf of a file that can't be
// changed here (why says so) and was changed (local version lf) or deleted
// (lf empty) here; a change is kept as a conflict copy.
func (p *pass) restoreLocked(path string, lf localFile, rf remoteFile, why string) error {
	if lf.blob == "" {
		p.logf("%s, so it was put back", why)
		return p.pullChange(path, localFile{}, rf)
	}
	if now, err := p.currentBlob(path); err != nil || now != lf.blob {
		p.deferred = true
		return nil
	}
	copyPath := conflictName(path, p.now())
	if err := renameRetry(p.full(path), p.full(copyPath)); err != nil {
		return err
	}
	delete(p.cache, path)
	p.logf("%s; your version is now %s", why, copyPath)
	return p.pullChange(path, localFile{}, rf)
}

func (s *syncer) wasWarned(path, blob string) bool {
	warned, ok := s.warned[path]
	return ok && warned == blob
}

func (p *pass) warn(path, blob, format string, args ...any) {
	if !p.wasWarned(path, blob) {
		p.warned[path] = blob
		p.logf(format, args...)
	}
}

// conflict keeps both versions: the local file is renamed to a conflict copy
// (uploaded on the next pass) and the space version takes its place.
func (p *pass) conflict(path string, lf localFile, rf remoteFile) error {
	if now, err := p.currentBlob(path); err != nil || now != lf.blob {
		p.deferred = true
		return nil
	}
	copyPath := conflictName(path, p.now())
	if err := renameRetry(p.full(path), p.full(copyPath)); err != nil {
		return err
	}
	delete(p.cache, path)
	p.logf("conflict: %s changed here and in the space; your version is now %s", path, copyPath)
	return p.pullChange(path, localFile{}, rf)
}

// conflictName inserts ".conflict-<time>" before the extension, keeping to the
// characters space paths allow.
func conflictName(p string, now time.Time) string {
	dir, name := path.Split(p)
	ext := path.Ext(name)
	if ext == name {
		ext = ""
	}
	return dir + strings.TrimSuffix(name, ext) + ".conflict-" + now.UTC().Format("20060102-150405") + ext
}

// writeFileAtomic replaces path through a temporary file in the same folder,
// so readers never see a half-written file.
func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".mtsync-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(tmp, mode)
	}
	if err == nil {
		err = renameRetry(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// renameRetry renames, trying again for up to two seconds. On Windows a
// rename onto a file fails with "Access is denied" while another program
// (OneDrive, an antivirus scanner, an editor) briefly has it open.
func renameRetry(from, to string) error {
	err := os.Rename(from, to)
	for wait := 20 * time.Millisecond; err != nil && !errors.Is(err, os.ErrNotExist) && wait < 2*time.Second; wait *= 2 {
		time.Sleep(wait)
		err = os.Rename(from, to)
	}
	return err
}
