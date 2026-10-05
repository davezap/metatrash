package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// GitHub step 2c: pull fills a GitHub folder from its repository and records
// the commit it came from (the baseline). Push (2d) builds on the baseline:
// files Metatrash cannot hold are skipped here and carried through untouched
// there, so nothing on GitHub is lost.
//
// Baselines live in the space's own Git, in the service file
// .metatrash/github.json, written in the same commit as the pulled files.
// Agents cannot read or write .metatrash/.

const githubBaselinePath = serviceDir + "github.json"

// Bounds on one pull, before the space's own storage limits apply.
const (
	maxPullListed       = 50  // skipped files and clashes named in a result or error
	githubPullsPerSpace = 10  // per githubPullWindow seconds
	githubPullWindow    = 600 // seconds
	githubPullTimeout   = 3 * time.Minute
)

type githubBaseline struct {
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Tree     string `json:"tree"`
	PulledAt string `json:"pulledAt"`
	// Files are the repository files held in the folder, by path relative to
	// the folder, with their blob hash (the same in the space and on GitHub).
	Files map[string]string `json:"files"`
	// Modes lists files whose mode is not 100644 (executables).
	Modes   map[string]string `json:"modes,omitempty"`
	Skipped []githubSkipped   `json:"skipped,omitempty"`
}

// githubSkipped is a repository file the folder does not hold.
type githubSkipped struct {
	Path   string `json:"path"`
	Blob   string `json:"blob,omitempty"`
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

type githubTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// githubSource reads one repository. contents returns the files asked for
// (path -> blob hash), each checked against its hash.
type githubSource interface {
	branchHead(ctx context.Context, branch string) (commit, tree string, err error)
	tree(ctx context.Context, tree string) ([]githubTreeEntry, error)
	contents(ctx context.Context, commit string, want map[string]string) (map[string][]byte, error)
}

// PullSkipped names a repository file the folder does not hold, by its path
// in the repository.
type PullSkipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type PullResult struct {
	Space     string `json:"space"`
	Folder    string `json:"folder"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Commit    string `json:"commit"`
	OldState  string `json:"oldState"`
	NewState  string `json:"newState"`
	UpToDate  bool   `json:"upToDate"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Deleted   int    `json:"deleted"`
	Unchanged int    `json:"unchanged"`
	// Skipped lists at most maxPullListed entries; SkippedCount is the total.
	Skipped      []PullSkipped `json:"skipped"`
	SkippedCount int           `json:"skippedCount"`
}

func githubConflict(message string) *Error { return problem(409, "conflict", message) }

// githubBaselines reads the baselines recorded in a revision, by folder.
func (r *repository) githubBaselines(ctx context.Context, revision string) (map[string]githubBaseline, error) {
	out := map[string]githubBaseline{}
	b, err := r.serviceFile(ctx, revision, githubBaselinePath)
	if err != nil || b == nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("invalid stored GitHub baselines")
	}
	return out, nil
}

// githubFolderArg turns the folder argument ("site" or "site/") into the
// folder prefix ("site/").
func githubFolderArg(arg string) (string, error) {
	p := strings.TrimSuffix(arg, "/")
	if !validPath(p) || strings.Count(p, "/") > 6 {
		return "", invalid("folder must be a folder path such as site/ (at most 7 levels).")
	}
	return p + "/", nil
}

// githubFolderService returns the folder's github service with defaults.
func (r *repository) githubFolderService(ctx context.Context, files map[string]record, folder string) (folderService, error) {
	f, ok := files[folder+folderConfigName]
	if ok {
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return folderService{}, err
		}
		if c, err := parseFolderConfig(string(b)); err == nil {
			for _, svc := range c.Services {
				if svc.Type == "github" {
					return svc.withDefaults(), nil
				}
			}
		}
	}
	return folderService{}, invalid(folder + " is not a GitHub folder: its " + folderConfigName + " needs a github service, for example {\"services\":[{\"type\":\"github\",\"repo\":\"owner/name\"}]}.")
}

// localRepoFiles is what the folder holds that pull manages: every file under
// it except Metatrash folder settings files, by path relative to the folder.
func localRepoFiles(files map[string]record, folder string) map[string]record {
	out := map[string]record{}
	for p, f := range files {
		if strings.HasPrefix(p, folder) && !isFolderConfig(p) {
			out[strings.TrimPrefix(p, folder)] = f
		}
	}
	return out
}

// classifyRepoEntries splits a repository tree into the blobs the folder can
// hold (by relative path) and the ones it skips. Binary content is checked
// later, once the content is known.
func classifyRepoEntries(folder string, entries []githubTreeEntry, maxFileBytes int64) (map[string]githubTreeEntry, []githubSkipped) {
	keep := map[string]githubTreeEntry{}
	var skipped []githubSkipped
	skip := func(e githubTreeEntry, reason string) {
		skipped = append(skipped, githubSkipped{Path: e.Path, Blob: e.SHA, Mode: e.Mode, Reason: reason})
	}
	for _, e := range entries {
		switch {
		case e.Type == "tree":
			continue
		case e.Type == "commit":
			skip(e, "submodule")
			continue
		case e.Type != "blob" || !hashPattern.MatchString(e.SHA):
			skip(e, "unsupported entry")
			continue
		case e.Mode == "120000":
			skip(e, "symbolic link")
			continue
		case e.Mode != "100644" && e.Mode != "100755":
			skip(e, "unsupported file mode")
			continue
		}
		segments := strings.Split(e.Path, "/")
		switch {
		case segments[len(segments)-1] == folderConfigName:
			skip(e, "Metatrash folder settings file ("+folderConfigName+")")
		case !validPath(folder + e.Path):
			skip(e, "name not allowed in Metatrash (letters, digits, . _ - only, not starting with -; at most 8 levels and 240 characters)")
		case e.Size > maxFileBytes:
			skip(e, fmt.Sprintf("larger than the space's file limit (%d bytes)", maxFileBytes))
		default:
			keep[e.Path] = e
		}
	}
	// A repository path that runs through a folder settings file name, or a
	// settings file through a repository file, cannot share the folder.
	for p := range keep {
		for _, segment := range strings.Split(p, "/") {
			if segment == folderConfigName {
				e := keep[p]
				delete(keep, p)
				skip(e, "clashes with a Metatrash folder settings file")
				break
			}
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	return keep, skipped
}

// textContent reports whether content can be held in a space: UTF-8 text without NUL.
func textContent(b []byte) bool {
	return utf8.Valid(b) && !strings.ContainsRune(string(b), 0)
}

func listSome(items []string) string {
	sort.Strings(items)
	if len(items) > 10 {
		return strings.Join(items[:10], ", ") + fmt.Sprintf(" and %d more", len(items)-10)
	}
	return strings.Join(items, ", ")
}

// sameRecords reports whether two views of a folder hold the same files.
func sameRecords(a, b map[string]record) bool {
	if len(a) != len(b) {
		return false
	}
	for p, f := range a {
		if g, ok := b[p]; !ok || g.Blob != f.Blob || g.ID != f.ID {
			return false
		}
	}
	return true
}

// pull runs the pull tool on a repository the caller's access check admitted
// for writing. GitHub is read outside the write queue; the queue re-checks
// that the folder, its settings and its baseline did not change meanwhile.
func (s *Service) pull(ctx context.Context, r *repository, space, folderArg string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, githubPullTimeout)
	defer cancel()
	if r == nil {
		return nil, missing()
	}
	folder, err := githubFolderArg(folderArg)
	if err != nil {
		return nil, err
	}
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	svc, err := r.githubFolderService(ctx, files, folder)
	if err != nil {
		return nil, err
	}
	baselines, err := r.githubBaselines(ctx, head)
	if err != nil {
		return nil, err
	}
	base, hasBase := baselines[folder]
	// A baseline for another repository or branch (the settings changed) does
	// not count: pull starts again as a first pull.
	if hasBase && (!strings.EqualFold(base.Repo, svc.Repo) || base.Branch != svc.Branch) {
		hasBase = false
	}
	local := localRepoFiles(files, folder)
	if hasBase {
		var changed []string
		for rel, f := range local {
			if blob, ok := base.Files[rel]; !ok {
				changed = append(changed, folder+rel+" (added)")
			} else if blob != f.Blob {
				changed = append(changed, folder+rel+" (changed)")
			}
		}
		for rel := range base.Files {
			if _, ok := local[rel]; !ok {
				changed = append(changed, folder+rel+" (deleted)")
			}
		}
		if len(changed) > 0 {
			return nil, githubConflict(fmt.Sprintf("%s has changes since the last pull (commit %s): %s. Pull only updates an unchanged folder; pushing changes comes in a later version. To discard them, restore those files (history and read with a revision), then pull again.", folder, short(base.Commit), listSome(changed)))
		}
	}
	if err := s.rates.reserve(allowance{"github:pull:" + space, githubPullsPerSpace, githubPullWindow}); err != nil {
		return nil, err
	}
	source, err := s.githubSourceFor(ctx, r, svc.Repo)
	if err != nil {
		return nil, err
	}
	commit, treeHash, err := source.branchHead(ctx, svc.Branch)
	if err != nil {
		return nil, err
	}
	result := PullResult{Space: space, Folder: folder, Repo: svc.Repo, Branch: svc.Branch, Commit: commit, OldState: head, NewState: head, Skipped: []PullSkipped{}}
	if hasBase && base.Commit == commit {
		result.UpToDate = true
		result.Unchanged = len(local)
		result.SkippedCount = len(base.Skipped)
		for i, sk := range base.Skipped {
			if i == maxPullListed {
				break
			}
			result.Skipped = append(result.Skipped, PullSkipped{Path: sk.Path, Reason: sk.Reason})
		}
		return result, nil
	}
	entries, err := source.tree(ctx, treeHash)
	if err != nil {
		return nil, err
	}
	keep, skipped := classifyRepoEntries(folder, entries, r.limits.MaxFileBytes)
	if !hasBase {
		// First pull: what the folder already holds must match the repository.
		skippedPaths := map[string]bool{}
		for _, sk := range skipped {
			skippedPaths[sk.Path] = true
		}
		var clashes []string
		for rel, f := range local {
			e, ok := keep[rel]
			switch {
			case ok && e.SHA == f.Blob:
			case ok:
				clashes = append(clashes, folder+rel+" (differs from the repository)")
			case skippedPaths[rel]:
				clashes = append(clashes, folder+rel+" (the repository's copy cannot be held in Metatrash)")
			default:
				clashes = append(clashes, folder+rel+" (not in the repository)")
			}
		}
		if len(clashes) > 0 {
			return nil, githubConflict(fmt.Sprintf("%s already holds files that do not match %s@%s: %s. A first pull needs the folder empty apart from %s files, or holding identical copies. Move or delete those files, then pull again.", folder, svc.Repo, svc.Branch, listSome(clashes), folderConfigName))
		}
	}
	// Rough bound before downloading: binaries are only found once fetched.
	var candidateBytes int64
	for _, e := range keep {
		candidateBytes += e.Size
	}
	if len(keep) > 4*r.limits.MaxFiles || candidateBytes > 4*r.limits.MaxCurrentTextBytes {
		return nil, problem(507, "storage_limit", fmt.Sprintf("%s is too large for this space: %d files, %d bytes of text (the space holds at most %d files and %d bytes).", svc.Repo, len(keep), candidateBytes, r.limits.MaxFiles, r.limits.MaxCurrentTextBytes))
	}
	want := map[string]string{}
	for rel, e := range keep {
		if f, ok := local[rel]; !ok || f.Blob != e.SHA {
			want[rel] = e.SHA
		}
	}
	content := map[string][]byte{}
	if len(want) > 0 {
		if content, err = source.contents(ctx, commit, want); err != nil {
			return nil, err
		}
	}
	for rel := range want {
		b, ok := content[rel]
		if !ok || blobHash(b) != want[rel] {
			return nil, errGitHubUnavailable
		}
		if !textContent(b) {
			e := keep[rel]
			delete(keep, rel)
			delete(content, rel)
			skipped = append(skipped, githubSkipped{Path: e.Path, Blob: e.SHA, Mode: e.Mode, Reason: "binary file (not UTF-8 text)"})
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	// Exact space totals after the pull.
	count, total := 0, int64(0)
	for p, f := range files {
		if !strings.HasPrefix(p, folder) || isFolderConfig(p) {
			count++
			total += f.Bytes
		}
	}
	var repoBytes int64
	for _, e := range keep {
		repoBytes += e.Size
	}
	if count+len(keep) > r.limits.MaxFiles || total+repoBytes > r.limits.MaxCurrentTextBytes {
		return nil, problem(507, "storage_limit", fmt.Sprintf("%s does not fit in this space: it needs %d files and %d bytes of text, and with the rest of the space that makes %d files and %d bytes (the space holds at most %d files and %d bytes).", svc.Repo, len(keep), repoBytes, count+len(keep), total+repoBytes, r.limits.MaxFiles, r.limits.MaxCurrentTextBytes))
	}
	value, err := s.queued(ctx, func() (any, error) {
		return s.applyPull(ctx, r, result, svc, folder, head, base, hasBase, local, keep, skipped, content, treeHash)
	})
	if err != nil {
		return nil, err
	}
	return value, nil
}

// applyPull commits a fetched repository snapshot into the folder. It runs in
// the write queue and refuses if the folder, its settings or its baseline
// changed since pull looked at them.
func (s *Service) applyPull(ctx context.Context, r *repository, result PullResult, svc folderService, folder, seenHead string, base githubBaseline, hasBase bool, seenLocal map[string]record, keep map[string]githubTreeEntry, skipped []githubSkipped, content map[string][]byte, treeHash string) (any, error) {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	if head != seenHead {
		current, err := r.githubFolderService(ctx, files, folder)
		if err != nil || current != svc {
			return nil, githubConflict(folder + " settings changed during the pull; pull again.")
		}
		baselines, err := r.githubBaselines(ctx, head)
		if err != nil {
			return nil, err
		}
		now, has := baselines[folder]
		if has != hasBase || (has && now.Commit != base.Commit) {
			return nil, githubConflict(folder + " was pulled by someone else meanwhile; pull again.")
		}
		if !sameRecords(localRepoFiles(files, folder), seenLocal) {
			return nil, githubConflict(folder + " changed during the pull; pull again.")
		}
	}
	result.OldState = head
	texts := map[string]string{}
	var changes []commitChange
	for rel, f := range seenLocal {
		if _, ok := keep[rel]; !ok {
			delete(files, folder+rel)
			changes = append(changes, commitChange{f.ID, "delete", folder + rel})
			result.Deleted++
		}
	}
	paths := make([]string, 0, len(keep))
	for rel := range keep {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	baseline := githubBaseline{Repo: svc.Repo, Branch: svc.Branch, Commit: result.Commit, Tree: treeHash, PulledAt: time.Now().UTC().Format(time.RFC3339), Files: map[string]string{}, Skipped: skipped}
	for _, rel := range paths {
		e := keep[rel]
		baseline.Files[rel] = e.SHA
		if e.Mode != "100644" {
			if baseline.Modes == nil {
				baseline.Modes = map[string]string{}
			}
			baseline.Modes[rel] = e.Mode
		}
		p := folder + rel
		f, exists := seenLocal[rel]
		if exists && f.Blob == e.SHA {
			result.Unchanged++
			continue
		}
		b := content[rel]
		operation := "write"
		if !exists {
			id, err := randomHex(16)
			if err != nil {
				return nil, err
			}
			f = record{File: File{ID: id, Path: p}}
			operation = "create"
			result.Created++
		} else {
			result.Updated++
		}
		f.Bytes = int64(len(b))
		f.Deletable = deletable(p, f.Protected)
		files[p] = f
		texts[p] = string(b)
		changes = append(changes, commitChange{f.ID, operation, p})
	}
	for p := range files {
		for _, rel := range paths {
			if q := folder + rel; p != q && (strings.HasPrefix(p, q+"/") || strings.HasPrefix(q, p+"/")) {
				return nil, githubConflict(fmt.Sprintf("%s in the repository clashes with %s in the space; move %s, then pull again.", q, p, p))
			}
		}
	}
	baselines, err := r.githubBaselines(ctx, head)
	if err != nil {
		return nil, err
	}
	baselines[folder] = baseline
	stored, err := json.Marshal(baselines)
	if err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("pull %s %s@%s into %s", svc.Repo, svc.Branch, short(result.Commit), folder)
	state, err := r.commitFiles(ctx, head, files, texts, map[string][]byte{githubBaselinePath: stored}, changes, summary)
	if err != nil {
		return nil, err
	}
	result.NewState = state
	result.SkippedCount = len(skipped)
	for i, sk := range skipped {
		if i == maxPullListed {
			break
		}
		result.Skipped = append(result.Skipped, PullSkipped{Path: sk.Path, Reason: sk.Reason})
	}
	return result, nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
