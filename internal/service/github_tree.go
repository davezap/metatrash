package service

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Repository trees as GitHub holds them, rebuilt from a baseline: push
// computes the tree it expects GitHub to build and refuses to move the branch
// when GitHub's answer differs.

// treeEntry is one file in a repository tree: its mode and object hash.
type treeEntry struct {
	mode, sha string
}

type treeNode struct {
	files map[string]treeEntry
	dirs  map[string]*treeNode
}

// gitTreeHash returns Git's root tree hash for files (path -> entry), as
// git write-tree would. Paths use "/"; a path that runs through a file is an
// error naming both.
func gitTreeHash(files map[string]treeEntry) (string, error) {
	root := &treeNode{files: map[string]treeEntry{}, dirs: map[string]*treeNode{}}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		e := files[p]
		if !hashPattern.MatchString(e.sha) || (e.mode != "100644" && e.mode != "100755" && e.mode != "120000" && e.mode != "160000") {
			return "", fmt.Errorf("%s: unsupported tree entry", p)
		}
		parts := strings.Split(p, "/")
		node := root
		for i, name := range parts {
			if name == "" {
				return "", fmt.Errorf("%s: empty path segment", p)
			}
			if i == len(parts)-1 {
				if _, clash := node.dirs[name]; clash {
					return "", fmt.Errorf("%s is a file in one place and a folder in another", p)
				}
				node.files[name] = e
				break
			}
			if _, clash := node.files[name]; clash {
				return "", fmt.Errorf("%s is a file in one place and a folder in another", strings.Join(parts[:i+1], "/"))
			}
			next := node.dirs[name]
			if next == nil {
				next = &treeNode{files: map[string]treeEntry{}, dirs: map[string]*treeNode{}}
				node.dirs[name] = next
			}
			node = next
		}
	}
	return root.hash()
}

func (n *treeNode) hash() (string, error) {
	type item struct {
		key, mode, name, sha string
	}
	items := make([]item, 0, len(n.files)+len(n.dirs))
	for name, e := range n.files {
		items = append(items, item{name, e.mode, name, e.sha})
	}
	for name, d := range n.dirs {
		sha, err := d.hash()
		if err != nil {
			return "", err
		}
		// Git sorts a folder as if its name ended in "/".
		items = append(items, item{name + "/", "40000", name, sha})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].key < items[j].key })
	var body bytes.Buffer
	for _, it := range items {
		raw, err := hex.DecodeString(it.sha)
		if err != nil {
			return "", err
		}
		body.WriteString(it.mode + " " + it.name + "\x00")
		body.Write(raw)
	}
	h := sha1.New()
	fmt.Fprintf(h, "tree %d\x00", body.Len())
	h.Write(body.Bytes())
	return hex.EncodeToString(h.Sum(nil)), nil
}

// githubConverted records a file that pull converted to UTF-8: GitHub's
// original blob and its encoding. The baseline's Files entry holds the
// converted blob, so agent edits show as changes.
type githubConverted struct {
	GitHubBlob string `json:"githubBlob"`
	Encoding   string `json:"encoding"`
}

// mode returns the baseline mode of a held file.
func (b githubBaseline) mode(rel string) string {
	if m := b.Modes[rel]; m != "" {
		return m
	}
	return "100644"
}

// githubBlob returns GitHub's blob for a held file.
func (b githubBaseline) githubBlob(rel string) string {
	if c, ok := b.Converted[rel]; ok {
		return c.GitHubBlob
	}
	return b.Files[rel]
}

// githubTree is the repository tree of the baseline commit as GitHub holds
// it: held files with GitHub's blobs, plus every skipped entry.
func (b githubBaseline) githubTree() map[string]treeEntry {
	out := make(map[string]treeEntry, len(b.Files)+len(b.Skipped))
	for rel := range b.Files {
		out[rel] = treeEntry{b.mode(rel), b.githubBlob(rel)}
	}
	for _, sk := range b.Skipped {
		out[sk.Path] = treeEntry{sk.Mode, sk.Blob}
	}
	return out
}

// skippedReason returns why pull skipped a repository path, or "".
func (b githubBaseline) skippedReason(rel string) (githubSkipped, bool) {
	for _, sk := range b.Skipped {
		if sk.Path == rel {
			return sk, true
		}
	}
	return githubSkipped{}, false
}

// treeFromEntries is a fetched tree as path -> entry, folders left out.
func treeFromEntries(entries []githubTreeEntry) map[string]treeEntry {
	out := map[string]treeEntry{}
	for _, e := range entries {
		if e.Type != "tree" {
			out[e.Path] = treeEntry{e.Mode, e.SHA}
		}
	}
	return out
}

// changedPaths lists the paths whose entry differs between two trees.
func changedPaths(a, b map[string]treeEntry) map[string]bool {
	out := map[string]bool{}
	for p, e := range a {
		if f, ok := b[p]; !ok || f != e {
			out[p] = true
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			out[p] = true
		}
	}
	return out
}

// isWorkflowPath reports a GitHub Actions workflow file, which push does not
// change (workflows can reach CI secrets).
func isWorkflowPath(rel string) bool {
	return strings.HasPrefix(rel, ".github/workflows/")
}

// convertUnicode turns UTF-16 or UTF-32 text that starts with a byte order
// mark into UTF-8 that starts with the UTF-8 byte order mark (EF BB BF), a
// hint to PowerShell and similar tools. ok is false for anything else and for
// text that does not decode cleanly (unpaired surrogates, values beyond
// U+10FFFF) or holds NUL characters: the conversion is always lossless.
func convertUnicode(b []byte) (out []byte, encoding string, ok bool) {
	var unit int
	var big bool
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE, 0, 0}):
		unit, big, encoding = 4, false, "UTF-32 LE"
	case bytes.HasPrefix(b, []byte{0, 0, 0xFE, 0xFF}):
		unit, big, encoding = 4, true, "UTF-32 BE"
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		unit, big, encoding = 2, false, "UTF-16 LE"
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		unit, big, encoding = 2, true, "UTF-16 BE"
	default:
		return nil, "", false
	}
	if len(b)%unit != 0 {
		return nil, "", false
	}
	value := func(i int) uint32 {
		var v uint32
		for k := 0; k < unit; k++ {
			shift := uint(8 * k)
			if big {
				shift = uint(8 * (unit - 1 - k))
			}
			v |= uint32(b[i+k]) << shift
		}
		return v
	}
	result := make([]byte, 0, len(b)+3)
	result = append(result, 0xEF, 0xBB, 0xBF)
	for i := unit; i < len(b); i += unit {
		v := value(i)
		if unit == 2 && v >= 0xD800 && v <= 0xDBFF {
			if i+unit >= len(b) {
				return nil, "", false
			}
			low := value(i + unit)
			if low < 0xDC00 || low > 0xDFFF {
				return nil, "", false
			}
			v = 0x10000 + (v-0xD800)<<10 + (low - 0xDC00)
			i += unit
		} else if v >= 0xD800 && v <= 0xDFFF || v > 0x10FFFF {
			return nil, "", false
		}
		if v == 0 {
			return nil, "", false
		}
		result = utf8.AppendRune(result, rune(v))
	}
	return result, encoding, true
}

// githubFolderLock allows one pull or push per GitHub folder at a time.
func (s *Service) githubFolderLock(r *repository, folder string) (func(), error) {
	value, _ := s.githubLocks.LoadOrStore(r.path+"\x00"+folder, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, githubConflict("A pull or push of " + folder + " is already running; try again in a moment.")
	}
	return mu.Unlock, nil
}

// baselineState returns the space revision holding the baseline content of
// most files: recorded for pushes, and for pulls the commit that wrote them
// (found by its summary line).
func (r *repository) baselineState(ctx context.Context, head, folder string, b githubBaseline) (string, error) {
	if b.State != "" {
		return b.State, nil
	}
	out, err := git(ctx, r.path, "", nil, "log", "--first-parent", "--fixed-strings", "--grep="+pullSummary(b.Repo, b.Branch, b.Commit, folder), "--max-count=1", "--format=%H", head, "--")
	if err != nil {
		return "", err
	}
	state := strings.TrimSpace(string(out))
	if !hashPattern.MatchString(state) {
		return "", nil
	}
	return state, nil
}

func pullSummary(repo, branch, commit, folder string) string {
	return fmt.Sprintf("pull %s %s@%s into %s", repo, branch, short(commit), folder)
}
