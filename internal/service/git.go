package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const branch = "refs/heads/main"
const serviceDir = ".metatrash/"
const indexPath = serviceDir + "files.json"

type repository struct {
	// closed prevents already-resolved handles from writing after deletion.
	closed atomic.Bool
	path   string
	limits Storage
	// owned marks account-owned spaces, the only ones that may attach services.
	owned bool
	// owner is the owning account's user ID (owned spaces only); a GitHub
	// folder pulls through that account's GitHub connections.
	owner string
	// id is the space ID of an owned space, which keys its rows in the
	// account database (acknowledgements).
	id string
	// changed is closed and replaced whenever the branch moves, waking list
	// requests that wait with after= (see Service.waitForChange).
	watchMu sync.Mutex
	changed chan struct{}
	// rootAgents is the AGENTS.md text a space root starts with.
	rootAgents string
	// configBlobs caches .metatrash.json texts parsed, by blob hash (see
	// cachedConfig).
	configBlobs sync.Map
	// committed, when set, hears of every commit (old and new state), so
	// folder actions can queue jobs (Service.agentCommitted).
	committed func(ctx context.Context, old, state string)
	// usage is what the space takes up (owned spaces, storage.go).
	usage spaceUsage
	quota repositoryBytes
}

// watch returns a channel that is closed at the next branch update.
func (r *repository) watch() <-chan struct{} {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if r.changed == nil {
		r.changed = make(chan struct{})
	}
	return r.changed
}

func (r *repository) notify() {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if r.changed != nil {
		close(r.changed)
		r.changed = nil
	}
}

// Git receives arguments directly, never through a shell. Ignore inherited Git overrides.
func git(ctx context.Context, repo, objects string, input []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	base := []string{"-c", "gc.auto=0", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}
	if repo != "" {
		base = append(base, "--git-dir="+repo)
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(e), "GIT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Metatrash", "GIT_AUTHOR_EMAIL=service@metatrash.com", "GIT_COMMITTER_NAME=Metatrash", "GIT_COMMITTER_EMAIL=service@metatrash.com")
	if objects != "" {
		cmd.Env = append(cmd.Env, "GIT_OBJECT_DIRECTORY="+objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(repo, "objects"), "GIT_INDEX_FILE="+filepath.Join(filepath.Dir(objects), "index"))
	}
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w (%s)", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (r *repository) head(ctx context.Context) (string, error) {
	b, err := git(ctx, r.path, "", nil, "rev-parse", "--verify", branch)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(b))
	if !hashPattern.MatchString(head) {
		return "", fmt.Errorf("invalid repository head")
	}
	return head, nil
}

func (r *repository) snapshot(ctx context.Context, revision string) (string, map[string]record, error) {
	head, err := r.head(ctx)
	if err != nil {
		return "", nil, err
	}
	if revision == "" {
		revision = head
	}
	if !hashPattern.MatchString(revision) {
		return "", nil, invalid("Invalid revision.")
	}
	if revision != head {
		if _, err := git(ctx, r.path, "", nil, "merge-base", "--is-ancestor", revision, head); err != nil {
			return "", nil, missing()
		}
	}
	b, err := git(ctx, r.path, "", nil, "show", revision+":"+indexPath)
	if err != nil {
		return "", nil, err
	}
	files := map[string]record{}
	if err = json.Unmarshal(b, &files); err != nil {
		return "", nil, err
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return "", nil, err
	}
	readOnly, err := r.readOnlyRules(ctx, files, nil)
	if err != nil {
		return "", nil, err
	}
	markFolderFiles(files, github, readOnly)
	return revision, files, nil
}

func (r *repository) blob(ctx context.Context, hash string) ([]byte, error) {
	if !hashPattern.MatchString(hash) {
		return nil, fmt.Errorf("invalid stored blob")
	}
	return git(ctx, r.path, "", nil, "cat-file", "blob", hash)
}

func dirBytes(path string) (int64, error) {
	var size int64
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks are not allowed in managed storage")
		}
		if !d.IsDir() {
			i, err := d.Info()
			if err != nil {
				return err
			}
			size, err = checkedBytes(size, i.Size())
			if err != nil {
				return err
			}
		}
		return nil
	})
	return size, err
}

// commitChange names one file a commit touches; history finds a file's
// commits by these lines ("<id> <operation> <path>").
type commitChange struct {
	id, operation, path string
}

// commit records a single-file change. id names the changed file in the commit
// message; it is passed separately because a deleted file is no longer in files.
func (r *repository) commit(ctx context.Context, old string, files map[string]record, changedPath, id string, text *string, operation string) (string, error) {
	var texts map[string]string
	if text != nil {
		texts = map[string]string{changedPath: *text}
	}
	return r.commitFiles(ctx, old, files, texts, nil, []commitChange{{id, operation, changedPath}}, "")
}

// commitFiles writes one commit holding files (the complete new index). texts
// gives the content of files whose blob is new; their records get the blob
// hash. service replaces or adds the service's own files under .metatrash/
// (other than the index); the ones already in old are carried forward. The
// message is the optional summary line followed by one line per change.
//
// Stage objects outside the repository. Over-budget writes leave no unreachable objects behind.
func (r *repository) commitFiles(ctx context.Context, old string, files map[string]record, texts map[string]string, service map[string][]byte, changes []commitChange, summary string) (string, error) {
	state, err := func() (string, error) {
		r.quota.mu.Lock()
		defer r.quota.mu.Unlock()
		return r.commitFilesLocked(ctx, old, files, texts, service, changes, summary)
	}()
	if err == nil {
		r.notify()
		if r.committed != nil {
			previous := old
			if previous == "" {
				previous = strings.Repeat("0", 40)
			}
			r.committed(ctx, previous, state)
		}
	}
	return state, err
}

func (r *repository) commitFilesLocked(ctx context.Context, old string, files map[string]record, texts map[string]string, service map[string][]byte, changes []commitChange, summary string) (string, error) {
	if len(files) > r.limits.MaxFiles {
		return "", storageLimit()
	}
	var currentBytes int64
	for _, f := range files {
		currentBytes += f.Bytes
	}
	if currentBytes > r.limits.MaxCurrentTextBytes {
		return "", storageLimit()
	}
	stagingDir, err := os.MkdirTemp(filepath.Dir(r.path), ".objects-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Printf("storage: staging cleanup %s: %v", stagingDir, err)
		}
	}()
	stage := filepath.Join(stagingDir, "objects")
	if err := os.Mkdir(stage, 0700); err != nil {
		return "", err
	}
	if len(texts) == 1 {
		for p, text := range texts {
			b, err := git(ctx, r.path, stage, []byte(text), "hash-object", "-w", "--stdin")
			if err != nil {
				return "", err
			}
			f := files[p]
			f.Blob = strings.TrimSpace(string(b))
			files[p] = f
		}
	} else if len(texts) > 1 {
		// Many blobs: one git process reading files written to the staging area.
		contentDir := filepath.Join(stagingDir, "content")
		if err := os.Mkdir(contentDir, 0700); err != nil {
			return "", err
		}
		paths := make([]string, 0, len(texts))
		for p := range texts {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		var list bytes.Buffer
		for i, p := range paths {
			name := filepath.Join(contentDir, fmt.Sprintf("%06d", i))
			if err := os.WriteFile(name, []byte(texts[p]), 0600); err != nil {
				return "", err
			}
			list.WriteString(name + "\n")
		}
		b, err := git(ctx, r.path, stage, list.Bytes(), "hash-object", "-w", "--no-filters", "--stdin-paths")
		if err != nil {
			return "", err
		}
		hashes := strings.Fields(string(b))
		if len(hashes) != len(paths) {
			return "", fmt.Errorf("git hash-object returned %d hashes for %d files", len(hashes), len(paths))
		}
		for i, p := range paths {
			if !hashPattern.MatchString(hashes[i]) {
				return "", fmt.Errorf("invalid blob hash")
			}
			f := files[p]
			f.Blob = hashes[i]
			files[p] = f
		}
		if err := os.RemoveAll(contentDir); err != nil {
			return "", err
		}
	}
	index := make(map[string]storedRecord, len(files))
	for p, f := range files {
		index[p] = storedRecord{ID: f.ID, Path: f.Path, Bytes: f.Bytes, Blob: f.Blob, Virtual: f.Virtual}
	}
	metadata, err := json.Marshal(index)
	if err != nil {
		return "", err
	}
	b, err := git(ctx, r.path, stage, metadata, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	entries := []string{"100644 " + strings.TrimSpace(string(b)) + "\t" + indexPath + "\x00"}
	serviceBlobs := map[string]string{}
	if old != "" {
		carried, err := r.serviceFiles(ctx, old)
		if err != nil {
			return "", err
		}
		for p, blob := range carried {
			serviceBlobs[p] = blob
		}
	}
	for p, content := range service {
		if !strings.HasPrefix(p, serviceDir) || p == indexPath {
			return "", fmt.Errorf("invalid service file %s", p)
		}
		b, err := git(ctx, r.path, stage, content, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		serviceBlobs[p] = strings.TrimSpace(string(b))
	}
	for p, blob := range serviceBlobs {
		entries = append(entries, "100644 "+blob+"\t"+p+"\x00")
	}
	for p, f := range files {
		entries = append(entries, "100644 "+f.Blob+"\t"+p+"\x00")
	}
	sort.Strings(entries)
	if _, err = git(ctx, r.path, stage, nil, "read-tree", "--empty"); err != nil {
		return "", err
	}
	if _, err = git(ctx, r.path, stage, []byte(strings.Join(entries, "")), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	b, err = git(ctx, r.path, stage, nil, "write-tree")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(b))
	args := []string{"commit-tree", root}
	if old != "" {
		args = append(args, "-p", old)
	}
	var message strings.Builder
	if summary != "" {
		message.WriteString(summary + "\n\n")
	}
	for _, c := range changes {
		message.WriteString(c.id + " " + c.operation + " " + c.path + "\n")
	}
	if author, ok := authorOf(ctx); ok {
		message.WriteString(author.trailers())
	}
	b, err = git(ctx, r.path, stage, []byte(message.String()), args...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(b))
	stageSize, err := dirBytes(stage)
	if err != nil {
		return "", err
	}
	repoSize, err := r.admitRepositoryBytes(stageSize)
	if err != nil {
		return "", err
	}
	// Any failure after reservation invalidates quota authority, including a
	// partial promotion or an ambiguous update-ref. A subsequent write scans.
	completed := false
	defer func() {
		if !completed {
			r.quota.valid = false
		}
	}()
	refBefore, err := repositoryRefBytes(r.path)
	if err != nil {
		return "", err
	}
	// added is what the objects moved in take up, so the repository's new
	// size is known without walking it again (storage.go).
	added, err := r.promoteObjects(stage, os.Rename)
	if err != nil {
		return "", err
	}
	if old == "" {
		old = strings.Repeat("0", 40)
	}
	if _, err = git(ctx, r.path, "", nil, "update-ref", branch, commit, old); err != nil {
		return "", err
	}
	// Ref publication has succeeded: observation trouble must not report a
	// committed operation as failed. Unsupported metadata layouts keep scanning.
	refAfter, observeErr := repositoryRefBytes(r.path)
	if r.quota.fast && observeErr == nil && refAfter == 41 && (refBefore == 0 || refBefore == 41) {
		r.quota.bytes = repoSize + added + refAfter - refBefore
		r.usage.setGit(r.quota.bytes, time.Now())
	} else {
		r.quota.valid = false
		// Reporting for unsupported layouts needs an actual measurement too.
		if size, err := r.measureRepositoryBytes(); err == nil {
			r.usage.setGit(size, time.Now())
		} else {
			log.Printf("storage: repository accounting %s: %v", r.path, err)
		}
	}
	completed = true
	return commit, nil
}

// promoteObjects runs under quota.mu after reservation. The move primitive is
// supplied so regressions can inject failure after each real filesystem move.
func (r *repository) promoteObjects(stage string, move func(string, string) error) (added int64, err error) {
	defer func() {
		if err != nil {
			r.quota.valid = false
		}
	}()
	err = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("invalid staged object")
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(r.path, "objects", rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if info, err := os.Lstat(dest); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("invalid stored object")
			}
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := move(path, dest); err != nil {
			return err
		}
		added, err = checkedBytes(added, info.Size())
		if err != nil {
			return err
		}
		return nil
	})
	return added, err
}

// serviceFiles lists the service's own files under .metatrash/ in a revision,
// other than the index, as path -> blob hash. Agents never see or write them.
func (r *repository) serviceFiles(ctx context.Context, revision string) (map[string]string, error) {
	b, err := git(ctx, r.path, "", nil, "ls-tree", "-r", "-z", revision, "--", serviceDir)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, entry := range strings.Split(string(b), "\x00") {
		if entry == "" {
			continue
		}
		meta, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" || !hashPattern.MatchString(fields[2]) || !strings.HasPrefix(path, serviceDir) {
			return nil, fmt.Errorf("invalid service file entry")
		}
		if path != indexPath {
			out[path] = fields[2]
		}
	}
	return out, nil
}

// serviceFile returns one service file's content in a revision, or nil when
// the revision has none.
func (r *repository) serviceFile(ctx context.Context, revision, path string) ([]byte, error) {
	files, err := r.serviceFiles(ctx, revision)
	if err != nil {
		return nil, err
	}
	blob, ok := files[path]
	if !ok {
		return nil, nil
	}
	return r.blob(ctx, blob)
}

// provision opens a space repository, creating it with its root AGENTS.md
// (rootAgents) and .metatrash.json when missing. An existing space gets the
// folder files it lacks (see addMissingFolderFiles).
func provision(ctx context.Context, path string, limits Storage, rootAgents []byte, name string, owned bool) (*repository, error) {
	r := &repository{path: path, limits: limits, owned: owned, rootAgents: string(rootAgents)}
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("invalid repository directory")
		}
		if err := r.addMissingFolderFiles(ctx, r.rootAgents); err != nil {
			var p *Error
			if !errors.As(err, &p) {
				return nil, err
			}
			// A full space keeps working without them.
			log.Printf("space %s (%s): folder files not added: %s", name, filepath.Base(path), p.Message)
		}
		return r, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if int64(len(rootAgents)) > limits.MaxFileBytes {
		return nil, fmt.Errorf("%s template exceeds file limit", agentsFileName)
	}
	temp, err := os.MkdirTemp(filepath.Dir(path), ".provision-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	if _, err = git(ctx, "", "", nil, "init", "--bare", "--object-format=sha1", "--initial-branch=main", temp); err != nil {
		return nil, err
	}
	files, texts := map[string]record{}, map[string]string{}
	changes, err := addFolderFiles(files, texts, nil, r.rootAgents, folderConfigName)
	if err != nil {
		return nil, err
	}
	staged := &repository{path: temp, limits: limits, owned: owned}
	if _, err = staged.commitFiles(ctx, "", files, texts, nil, changes, ""); err != nil {
		return nil, err
	}
	if err = os.Rename(temp, path); err != nil {
		return nil, err
	}
	return r, nil
}
