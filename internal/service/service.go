package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type job struct {
	ctx    context.Context
	run    func() (any, error)
	result chan outcome
}
type outcome struct {
	value any
	err   error
}

type Service struct {
	ownedMu    sync.RWMutex
	ownedRepos map[string]*repository
	ownedRoot  string
	ownedDB    *accountDatabase
	accounts   *accounts
	config     Config
	keys       map[string]Keys
	repos      map[string]*repository
	rates      limiter
	cursorKey  []byte
	queue      chan job
	stop       chan struct{}
	done       chan struct{}
	lockPath   string
}

// Open provisions missing configured spaces and locks the data directory to one process.
func Open(ctx context.Context, configPath, keysPath, dataDir string, readme []byte) (*Service, error) {
	c, keys, err := loadConfig(configPath, keysPath)
	if err != nil {
		return nil, err
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	lock := filepath.Join(dataDir, ".service-lock")
	if err = os.Mkdir(lock, 0700); err != nil {
		return nil, fmt.Errorf("data directory locked; if no service is running, remove %s: %w", lock, err)
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(lock)
		}
	}()
	if err = os.WriteFile(filepath.Join(lock, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		return nil, err
	}
	reposPath := filepath.Join(dataDir, "repos")
	if err = os.MkdirAll(reposPath, 0700); err != nil {
		return nil, err
	}
	s := &Service{ownedRepos: map[string]*repository{}, ownedRoot: filepath.Join(dataDir, "owned-repos"), config: c, keys: keys, repos: map[string]*repository{}, rates: limiter{buckets: map[string]bucket{}}, cursorKey: make([]byte, 32), queue: make(chan job, c.Global.MaxQueuedWrites), stop: make(chan struct{}), done: make(chan struct{}), lockPath: lock}
	if _, err = rand.Read(s.cursorKey); err != nil {
		return nil, err
	}
	for name, sc := range c.Spaces {
		r, err := provision(ctx, filepath.Join(reposPath, name+".git"), sc.Limits.Storage, readme)
		if err != nil {
			return nil, fmt.Errorf("provision %s: %w", name, err)
		}
		s.repos[name] = r
	}
	go s.worker()
	success = true
	return s, nil
}

func (s *Service) worker() {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		case j := <-s.queue:
			if err := j.ctx.Err(); err != nil {
				j.result <- outcome{err: err}
				continue
			}
			value, err := j.run()
			j.result <- outcome{value: value, err: err}
		}
	}
}

// Close is called once, after the HTTP server has stopped accepting requests.
func (s *Service) Close() {
	close(s.stop)
	<-s.done
	if s.accounts != nil {
		_ = s.accounts.store.Close()
	}
	os.RemoveAll(s.lockPath)
}

func (s *Service) queued(ctx context.Context, fn func() (any, error)) (any, error) {
	j := job{ctx: ctx, run: fn, result: make(chan outcome, 1)}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stop:
		return nil, fmt.Errorf("service stopped")
	case s.queue <- j:
	default:
		return nil, &Error{Status: 503, Code: "queue_full", Message: "Write queue is full.", RetryAfterSeconds: 1}
	}
	select {
	case o := <-j.result:
		return o.value, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stop:
		return nil, fmt.Errorf("service stopped")
	}
}

// Access authorizes before atomically reserving operation counters. Every adapter
// must call it once per operation, behind the separate HTTP ingress limiter.
func (s *Service) Access(space, key, client string, write bool) error {
	// Owned repositories are never reachable through this configured-space entry
	// point, with or without a key. OAuth access uses ownedAgentAccess instead.
	if s.ownedRepository(space) != nil {
		return problem(403, "forbidden", "Account-owned spaces are available to agents only through OAuth at /mcp/account.")
	}
	sc, exists := s.config.Spaces[space]
	if !exists {
		return problem(401, "unauthorized", "A valid space key is required.")
	}
	if sc.Visibility == "private" {
		canWrite := matchesKey(key, s.keys[space].WriteHashes)
		canRead := matchesKey(key, s.keys[space].ReadHashes)
		if !canRead && !canWrite {
			return problem(401, "unauthorized", "A valid space key is required.")
		}
		if write && !canWrite {
			return problem(403, "forbidden", "A write key is required.")
		}
	}
	g, r := s.config.Global.Rates, sc.Limits.Rates
	kind, globalLimit := "read", g.Reads
	clientLimit, spaceLimit := r.ClientReads, r.SpaceReads
	if write {
		kind, globalLimit = "write", g.Writes
		clientLimit, spaceLimit = r.ClientWrites, r.SpaceWrites
	}
	return s.rates.reserve(
		allowance{"global:" + kind, globalLimit, g.WindowSeconds},
		allowance{"space:" + space + ":" + kind, spaceLimit, r.WindowSeconds},
		allowance{"client:" + space + ":" + kind + ":" + client, clientLimit, r.WindowSeconds},
	)
}

// Dispatch is transport-independent. Call Access first with trusted transport credentials/client IP.
// It reaches configured spaces only; owned spaces use dispatchRepository after agentAccess.
func (s *Service) Dispatch(ctx context.Context, space, op string, in Input) (any, error) {
	return s.dispatchRepository(ctx, s.repos[space], space, op, in)
}

// dispatchRepository runs one operation on the repository that the caller's
// access check admitted. The space name is used only for results and cursors.
func (s *Service) dispatchRepository(ctx context.Context, r *repository, space, op string, in Input) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if r == nil {
		return nil, missing()
	}
	switch op {
	case "read":
		if !validPath(in.Path) {
			return nil, invalid("Invalid path.")
		}
		state, files, err := r.snapshot(ctx, in.Revision)
		if err != nil {
			return nil, err
		}
		f, ok := files[in.Path]
		if !ok {
			return nil, missing()
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return nil, err
		}
		result := ReadResult{Space: space, State: state}
		result.File.File, result.File.Text = f.File, string(b)
		return result, nil
	case "list", "history":
		return s.page(ctx, r, space, op, in)
	case "write", "move":
		if !hashPattern.MatchString(in.IfInState) {
			return nil, invalid("ifInState must be a commit hash from read/list.")
		}
		if op == "write" {
			if !validPath(in.Path) || in.Text == nil {
				return nil, invalid("Valid path and text are required.")
			}
			if !utf8.ValidString(*in.Text) || strings.ContainsRune(*in.Text, 0) {
				return nil, invalid("Text must be UTF-8 without NUL.")
			}
			if int64(len(*in.Text)) > r.limits.MaxFileBytes {
				return nil, problem(413, "payload_too_large", "Text exceeds the file limit.")
			}
			if strings.EqualFold(in.Path, "README.md") {
				return nil, problem(403, "protected_file", "README.md is immutable.")
			}
		} else {
			if !validPath(in.From) || !validPath(in.To) || in.From == in.To {
				return nil, invalid("Distinct valid source and destination paths are required.")
			}
			if strings.EqualFold(in.From, "README.md") || strings.EqualFold(in.To, "README.md") {
				return nil, problem(403, "protected_file", "README.md is immutable.")
			}
		}
		return s.queued(ctx, func() (any, error) { return s.mutate(ctx, r, space, op, in) })
	default:
		return nil, invalid("Unknown operation.")
	}
}

func (s *Service) mutate(ctx context.Context, r *repository, space, op string, in Input) (any, error) {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	if head != in.IfInState {
		return nil, &Error{Status: 409, Code: "state_mismatch", Message: "Space changed; re-read before retrying.", CurrentState: head}
	}
	dest := in.Path
	var f record
	created := false
	if op == "move" {
		dest = in.To
		var ok bool
		f, ok = files[in.From]
		if !ok {
			return nil, missing()
		}
		if _, exists := files[dest]; exists {
			return nil, problem(409, "destination_exists", "Destination exists.")
		}
		delete(files, in.From)
		f.Path = dest
	} else {
		var exists bool
		f, exists = files[dest]
		if exists && in.CreateOnly {
			return nil, problem(409, "destination_exists", "Destination exists.")
		}
		if exists {
			b, err := r.blob(ctx, f.Blob)
			if err != nil {
				return nil, err
			}
			if string(b) == *in.Text {
				return WriteResult{Mutation: Mutation{Space: space, OldState: head, NewState: head, File: f.File}}, nil
			}
		} else {
			id, err := randomHex(16)
			if err != nil {
				return nil, err
			}
			f = record{File: File{ID: id, Path: dest}}
			created = true
		}
		f.Bytes = int64(len(*in.Text))
	}
	for p := range files {
		if strings.HasPrefix(dest, p+"/") || strings.HasPrefix(p, dest+"/") {
			return nil, invalid("File/directory path collision.")
		}
	}
	files[dest] = f
	operation := op
	if created {
		operation = "create"
	}
	var text *string
	if op == "write" {
		text = in.Text
	}
	state, err := r.commit(ctx, head, files, dest, text, operation)
	if err != nil {
		return nil, err
	}
	m := Mutation{Space: space, OldState: head, NewState: state, File: f.File}
	if op == "move" {
		return m, nil
	}
	return WriteResult{Mutation: m, Created: created, Changed: true}, nil
}

type cursor struct {
	Space     string `json:"s"`
	Operation string `json:"o"`
	Filter    string `json:"f"`
	Limit     int    `json:"l"`
	State     string `json:"r"`
	Offset    int    `json:"p"`
}

func (s *Service) encodeCursor(c cursor) *string {
	b, _ := json.Marshal(c)
	h := hmac.New(sha256.New, s.cursorKey)
	h.Write(b)
	token := base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return &token
}

func (s *Service) page(ctx context.Context, r *repository, space, op string, in Input) (any, error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > 100 {
		return nil, invalid("limit must be 1..100.")
	}
	filter := in.Prefix
	if op == "history" {
		filter = in.ID
		if !idPattern.MatchString(in.ID) {
			return nil, invalid("Invalid file ID.")
		}
	} else if in.Prefix != "" {
		if !strings.HasSuffix(in.Prefix, "/") || !validPath(strings.TrimSuffix(in.Prefix, "/")) || strings.Count(in.Prefix, "/") > 7 {
			return nil, invalid("Invalid folder prefix.")
		}
	}
	c := cursor{Space: space, Operation: op, Filter: filter, Limit: in.Limit}
	if in.Cursor != "" {
		parts := strings.Split(in.Cursor, ".")
		if len(parts) != 2 || len(in.Cursor) > 2048 {
			return nil, invalid("Invalid cursor.")
		}
		b, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, invalid("Invalid cursor.")
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[1])
		h := hmac.New(sha256.New, s.cursorKey)
		h.Write(b)
		if err != nil || !hmac.Equal(sig, h.Sum(nil)) {
			return nil, invalid("Invalid cursor; restart listing after a service restart.")
		}
		var saved cursor
		if err := strictJSON(b, &saved); err != nil || saved.Space != space || saved.Operation != op || saved.Filter != filter || saved.Limit != in.Limit || saved.Offset < 0 || !hashPattern.MatchString(saved.State) {
			return nil, invalid("Cursor arguments changed.")
		}
		c = saved
	}
	state, files, err := r.snapshot(ctx, c.State)
	if err != nil {
		return nil, err
	}
	c.State = state
	if op == "list" {
		paths := []string{}
		for p := range files {
			if strings.HasPrefix(p, in.Prefix) {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		if c.Offset > len(paths) {
			return nil, invalid("Invalid cursor offset.")
		}
		end := c.Offset + c.Limit
		if end > len(paths) {
			end = len(paths)
		}
		result := ListResult{Space: space, State: state, Files: []File{}}
		for _, p := range paths[c.Offset:end] {
			result.Files = append(result.Files, files[p].File)
		}
		if end < len(paths) {
			c.Offset = end
			result.NextCursor = s.encodeCursor(c)
		}
		return result, nil
	}
	found := false
	for _, f := range files {
		if f.ID == in.ID {
			found = true
			break
		}
	}
	if !found {
		return nil, missing()
	}
	b, err := git(ctx, r.path, "", nil, "log", "--first-parent", "--fixed-strings", "--grep="+in.ID+" ", "--format=%H%x09%cI%x09%s", "--max-count="+strconv.Itoa(c.Limit+1), "--skip="+strconv.Itoa(c.Offset), state, "--")
	if err != nil {
		return nil, err
	}
	entries := []HistoryEntry{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("invalid history entry")
		}
		message := strings.SplitN(parts[2], " ", 3)
		if len(message) != 3 || message[0] != in.ID {
			return nil, fmt.Errorf("invalid history identity")
		}
		stamp, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			return nil, err
		}
		entries = append(entries, HistoryEntry{Revision: parts[0], Path: message[2], Operation: message[1], Timestamp: stamp.UTC().Format(time.RFC3339)})
	}
	result := HistoryResult{Space: space, State: state, ID: in.ID, Entries: entries}
	if len(entries) > c.Limit {
		result.Entries = entries[:c.Limit]
		c.Offset += c.Limit
		result.NextCursor = s.encodeCursor(c)
	}
	return result, nil
}

// reserveOwnedOperation charges owned-space read or write quotas after the
// caller has authorized the operation. Human browsing and agent reads share the
// same owned-space read buckets.
func (s *Service) reserveOwnedOperation(space, client string, write bool) error {
	g, rates := s.config.Global.Rates, s.config.Defaults.Rates
	kind, globalLimit, spaceLimit, clientLimit := "read", g.Reads, rates.SpaceReads, rates.ClientReads
	clientBucket := "owned:" + space + ":client:" + client
	if write {
		kind, globalLimit, spaceLimit, clientLimit = "write", g.Writes, rates.SpaceWrites, rates.ClientWrites
		clientBucket = "owned:" + space + ":write:client:" + client
	}
	return s.rates.reserve(
		allowance{"global:" + kind, globalLimit, g.WindowSeconds},
		allowance{"owned:" + space + ":" + kind, spaceLimit, rates.WindowSeconds},
		allowance{clientBucket, clientLimit, rates.WindowSeconds},
	)
}
