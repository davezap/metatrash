package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// stateDir holds mtsync's own files inside the synced folder. It is never
// uploaded.
const stateDir = ".mtsync"

// baseFile is a file as it was when local and remote last agreed.
type baseFile struct {
	ID   string `json:"id"`
	Blob string `json:"blob"`
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
}

type localEntry struct {
	size  int64
	mtime time.Time
	blob  string
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
	// noChanges is set when the server can't list changes (before 0.37.0).
	noChanges bool
	// onPass, when set, hears how each pass ended (nil when it completed).
	onPass func(err error)
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
func (s *syncer) scan() (map[string]string, map[string]bool, error) {
	files, unstable := map[string]string{}, map[string]bool{}
	seen := map[string]bool{}
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
			files[p] = e.blob
			return nil
		}
		b, err := os.ReadFile(full)
		if err != nil {
			unstable[p] = true
			return nil
		}
		blob := gitBlob(b)
		s.cache[p] = localEntry{size: info.Size(), mtime: info.ModTime(), blob: blob}
		files[p] = blob
		return nil
	})
	for p := range s.cache {
		if !seen[p] {
			delete(s.cache, p)
		}
	}
	return files, unstable, err
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
	local    map[string]string
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
	if s.st.State != "" {
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
	if !p.deferred {
		s.st.State = p.current
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
			p.remote[path] = remoteFile{ID: f.ID, Path: path, Blob: f.Blob, Deletable: p.c.prefix != "" || !strings.EqualFold(path, "README.md") && path != ".metatrash.json"}
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
		b, l, r := p.st.Files[path].Blob, p.local[path], p.remote[path].Blob
		if p.unstable[path] {
			if r != b {
				p.deferred = true
			}
			continue
		}
		var err error
		switch {
		case l == r:
			p.agree(path, r)
		case l == b:
			err = p.pullChange(path, l, r)
		case r == b:
			err = p.push(path, l)
		case l == "":
			// Deleted here, edited in the space: keep the edit.
			err = p.pullChange(path, l, r)
		case r == "":
			// Deleted in the space, edited here: keep the edit.
			err = p.push(path, l)
		default:
			err = p.conflict(path, l, r)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *pass) agree(path, blob string) {
	old, had := p.st.Files[path]
	if blob == "" {
		if had {
			delete(p.st.Files, path)
			p.changed = true
		}
		return
	}
	next := baseFile{ID: p.remote[path].ID, Blob: blob}
	if !had || old != next {
		p.st.Files[path] = next
		p.changed = true
	}
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
		b := p.st.Files[path].Blob
		if b != "" && p.local[path] == "" && !p.unstable[path] && p.remote[path].Blob == b && p.remote[path].Deletable {
			gone[b] = append(gone[b], path)
		}
	}
	for _, to := range paths {
		l := p.local[to]
		if l == "" || p.st.Files[to].Blob != "" || p.remote[to].Blob != "" || p.unstable[to] || len(gone[l]) == 0 || p.wasWarned(to, l) {
			continue
		}
		from := gone[l][0]
		m, err := p.c.move(p.ctx, from, to, p.current)
		if err != nil {
			if err := p.refused(to, l, err); err != nil {
				return nil, err
			}
			continue
		}
		gone[l] = gone[l][1:]
		p.current = m.NewState
		f := p.remote[from]
		delete(p.remote, from)
		f.Path, f.ID = to, m.File.ID
		p.remote[to] = f
		delete(p.st.Files, from)
		p.st.Files[to] = baseFile{ID: m.File.ID, Blob: l}
		p.changed = true
		handled[from], handled[to] = true, true
		p.logf("moved %s -> %s", from, to)
	}
	return handled, nil
}

// pullChange makes the local file match the space version r ("" deletes it),
// provided the local file is still l.
func (p *pass) pullChange(path, l, r string) error {
	if now, err := p.currentBlob(path); err != nil || now != l {
		p.deferred = true
		return nil
	}
	if r == "" {
		if err := os.Remove(p.full(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		p.pruneDirs(path)
		delete(p.cache, path)
		p.agree(path, "")
		p.logf("deleted %s (removed in the space)", path)
		return nil
	}
	text, err := p.content(path, r)
	if err != nil {
		return err
	}
	if err := p.writeLocal(path, text); err != nil {
		return err
	}
	p.agree(path, r)
	if l == "" {
		p.logf("downloaded %s", path)
	} else {
		p.logf("updated %s", path)
	}
	return nil
}

// content returns the space's text for path with blob r, copying it from a
// local file with the same content when there is one (a move or copy in the
// space) instead of downloading it.
func (p *pass) content(path, r string) ([]byte, error) {
	for other, blob := range p.local {
		if blob == r && !p.unstable[other] {
			if b, err := os.ReadFile(p.full(other)); err == nil && gitBlob(b) == r {
				return b, nil
			}
		}
	}
	text, err := p.c.read(p.ctx, path, p.snapshot)
	if err != nil {
		return nil, err
	}
	return []byte(text), nil
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
		p.cache[path] = localEntry{size: info.Size(), mtime: info.ModTime(), blob: gitBlob(b)}
	}
	return nil
}

// pruneDirs removes folders left empty by a deletion, up to the root.
func (p *pass) pruneDirs(file string) {
	for dir := path.Dir(file); dir != "." && dir != "/"; dir = path.Dir(dir) {
		if os.Remove(p.full(dir)) != nil {
			return
		}
	}
}

// push sends the local version of path ("" deletes it) to the space.
func (p *pass) push(path, l string) error {
	if p.readOnly || p.wasWarned(path, l) {
		return nil
	}
	if l == "" {
		if f, ok := p.remote[path]; ok && !f.Deletable {
			// README.md and the root .metatrash.json cannot be deleted: restore them.
			return p.pullChange(path, "", f.Blob)
		}
		m, err := p.c.remove(p.ctx, path, p.current)
		if err != nil {
			return p.refused(path, l, err)
		}
		p.current = m.NewState
		delete(p.remote, path)
		p.agree(path, "")
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
	blob := gitBlob(b)
	if !utf8.Valid(b) {
		p.warn(path, blob, "%s is not UTF-8 text; spaces hold text only, so it stays local", path)
		return nil
	}
	m, err := p.c.write(p.ctx, path, string(b), p.current)
	if err != nil {
		return p.refused(path, blob, err)
	}
	p.current = m.NewState
	f := m.File
	f.Blob = blob
	_, existed := p.remote[path]
	p.remote[path] = f
	p.agree(path, blob)
	if existed {
		p.logf("uploaded %s", path)
	} else {
		p.logf("uploaded new %s", path)
	}
	return nil
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
	}
	p.warn(path, blob, "%s was not uploaded: %s", path, e.Message)
	return nil
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
func (p *pass) conflict(path, l, r string) error {
	if now, err := p.currentBlob(path); err != nil || now != l {
		p.deferred = true
		return nil
	}
	copyPath := conflictName(path, p.now())
	if err := os.Rename(p.full(path), p.full(copyPath)); err != nil {
		return err
	}
	delete(p.cache, path)
	p.logf("conflict: %s changed here and in the space; your version is now %s", path, copyPath)
	return p.pullChange(path, "", r)
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
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
