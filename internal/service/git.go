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
	"time"
)

const branch = "refs/heads/main"
const indexPath = ".metatrash/files.json"

type repository struct {
	path   string
	limits Storage
	// owned marks account-owned spaces, the only ones that may attach services.
	owned bool
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
			size += i.Size()
		}
		return nil
	})
	return size, err
}

// Stage objects outside the repository. Over-budget writes leave no unreachable objects behind.
// id names the changed file in the commit message; it is passed separately
// because a deleted file is no longer in files.
func (r *repository) commit(ctx context.Context, old string, files map[string]record, changedPath, id string, text *string, operation string) (string, error) {
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
	defer os.RemoveAll(stagingDir)
	stage := filepath.Join(stagingDir, "objects")
	if err := os.Mkdir(stage, 0700); err != nil {
		return "", err
	}
	if text != nil {
		b, err := git(ctx, r.path, stage, []byte(*text), "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		f := files[changedPath]
		f.Blob = strings.TrimSpace(string(b))
		files[changedPath] = f
	}
	metadata, err := json.Marshal(files)
	if err != nil {
		return "", err
	}
	b, err := git(ctx, r.path, stage, metadata, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	entries := []string{"100644 " + strings.TrimSpace(string(b)) + "\t" + indexPath + "\x00"}
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
	message := id + " " + operation + " " + changedPath + "\n"
	b, err = git(ctx, r.path, stage, []byte(message), args...)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(b))
	repoSize, err := dirBytes(r.path)
	if err != nil {
		return "", err
	}
	stageSize, err := dirBytes(stage)
	if err != nil {
		return "", err
	}
	// Reserve reference/config overhead; conservatively count duplicate staged objects too.
	if repoSize+stageSize+4096 > r.limits.MaxRepositoryBytes {
		return "", storageLimit()
	}
	err = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(r.path, "objects", rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if _, err := os.Stat(dest); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		return os.Rename(path, dest)
	})
	if err != nil {
		return "", err
	}
	if old == "" {
		old = strings.Repeat("0", 40)
	}
	if _, err = git(ctx, r.path, "", nil, "update-ref", branch, commit, old); err != nil {
		return "", err
	}
	return commit, nil
}

// provision opens a space repository, creating it with the protected README
// and a starter root .metatrash.json if it is missing. An existing repository
// without a root .metatrash.json gets the starter added as its own commit.
// Callers serialize it with other writes (startup, or the write queue).
func provision(ctx context.Context, path string, limits Storage, readme []byte, name string, owned bool) (*repository, error) {
	r := &repository{path: path, limits: limits, owned: owned}
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("invalid repository directory")
		}
		if err := r.addRootFolderConfig(ctx, name); err != nil {
			var p *Error
			if !errors.As(err, &p) {
				return nil, err
			}
			// A full space keeps working without the starter file.
			log.Printf("space %s: root %s not added: %s", filepath.Base(path), folderConfigName, p.Message)
		}
		return r, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	temp, err := os.MkdirTemp(filepath.Dir(path), ".provision-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temp)
	if _, err = git(ctx, "", "", nil, "init", "--bare", "--object-format=sha1", "--initial-branch=main", temp); err != nil {
		return nil, err
	}
	id, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	text := string(readme)
	if int64(len(readme)) > limits.MaxFileBytes {
		return nil, fmt.Errorf("README exceeds file limit")
	}
	files := map[string]record{"README.md": {File: File{ID: id, Path: "README.md", Bytes: int64(len(readme)), Protected: true}}}
	staged := &repository{path: temp, limits: limits, owned: owned}
	if _, err = staged.commit(ctx, "", files, "README.md", id, &text, "create"); err != nil {
		return nil, err
	}
	if err = staged.addRootFolderConfig(ctx, name); err != nil {
		return nil, err
	}
	if err = os.Rename(temp, path); err != nil {
		return nil, err
	}
	return r, nil
}

// addRootFolderConfig commits the starter root .metatrash.json when the space
// has none. It leaves an existing one alone.
func (r *repository) addRootFolderConfig(ctx context.Context, name string) error {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return err
	}
	if _, exists := files[folderConfigName]; exists {
		return nil
	}
	for p := range files {
		if strings.HasPrefix(p, folderConfigName+"/") {
			return fmt.Errorf("cannot add %s: a folder of that name exists", folderConfigName)
		}
	}
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	text := folderConfigStarter(name)
	files[folderConfigName] = record{File: File{ID: id, Path: folderConfigName, Bytes: int64(len(text))}}
	_, err = r.commit(ctx, head, files, folderConfigName, id, &text, "create")
	return err
}
