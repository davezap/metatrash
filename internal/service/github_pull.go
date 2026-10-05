package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// GitHub step 2c: pull fills a GitHub folder from its repository and records
// the commit it came from (the baseline). Push (2d, github_push.go) builds on
// the baseline: files Metatrash cannot hold are skipped here and carried
// through untouched there, so nothing on GitHub is lost. UTF-16 and UTF-32
// text with a byte order mark is converted to UTF-8 here and goes back to
// GitHub only when an agent edits it.
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
	// Converted lists files pull converted from UTF-16 or UTF-32 to UTF-8;
	// their Files entry is the converted blob.
	Converted map[string]githubConverted `json:"converted,omitempty"`
	// State is the space revision holding the baseline content of the
	// folder's files (set by push; for a pull, the pull commit, found by its
	// summary). FileStates overrides it for files whose local change a merging
	// pull kept.
	State      string            `json:"state,omitempty"`
	FileStates map[string]string `json:"fileStates,omitempty"`
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
	// Converted lists at most maxPullListed files held as UTF-8 that are
	// UTF-16 or UTF-32 on GitHub; ConvertedCount is the total.
	Converted      []PullConverted `json:"converted"`
	ConvertedCount int             `json:"convertedCount"`
	// LocalChanges counts files changed here since the last pull or push that
	// the pull left in place and push would send (see pending).
	LocalChanges int `json:"localChanges"`
}

// PullConverted names a file pull converted to UTF-8 (with a UTF-8 byte
// order mark), by its path in the space.
type PullConverted struct {
	Path     string `json:"path"`
	Encoding string `json:"encoding"`
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

// notTextReason says why content cannot be held, precisely enough for an
// agent to tell the owner what to fix. UTF-16 and UTF-32 text with a byte
// order mark is converted on pull; it lands here only when it does not decode
// cleanly or holds NUL characters.
func notTextReason(b []byte) string {
	const fix = " that cannot be converted to UTF-8 (invalid characters or NUL); Metatrash holds UTF-8 text only, re-save it as UTF-8"
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE, 0, 0}) || bytes.HasPrefix(b, []byte{0, 0, 0xFE, 0xFF}):
		return "UTF-32 text" + fix
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}) || bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return "UTF-16 text" + fix
	case bytes.IndexByte(b, 0) >= 0:
		return "binary file"
	default:
		return "not UTF-8: text in another encoding such as Latin-1 or Windows-1252 (re-save it as UTF-8), or a binary file"
	}
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

// localChanges compares a folder's repository files with its baseline:
// rel -> "added", "modified" or "deleted".
func localChanges(base githubBaseline, local map[string]record) map[string]string {
	out := map[string]string{}
	for rel, f := range local {
		if blob, ok := base.Files[rel]; !ok {
			out[rel] = "added"
		} else if blob != f.Blob {
			out[rel] = "modified"
		}
	}
	for rel := range base.Files {
		if _, ok := local[rel]; !ok {
			out[rel] = "deleted"
		}
	}
	return out
}

// pullPlan is what applyPull writes, worked out before the write queue.
type pullPlan struct {
	result    PullResult
	svc       folderService
	folder    string
	seenHead  string
	base      githubBaseline
	hasBase   bool
	seenLocal map[string]record
	writes    map[string][]byte // rel -> content to create or replace
	deletes   []string          // rel
	baseline  githubBaseline
}

// listResult fills the skipped and converted lists of a result from a baseline.
func (result *PullResult) listFrom(b githubBaseline, folder string) {
	result.SkippedCount = len(b.Skipped)
	for i, sk := range b.Skipped {
		if i == maxPullListed {
			break
		}
		result.Skipped = append(result.Skipped, PullSkipped{Path: sk.Path, Reason: sk.Reason})
	}
	paths := make([]string, 0, len(b.Converted))
	for rel := range b.Converted {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	result.ConvertedCount = len(paths)
	for i, rel := range paths {
		if i == maxPullListed {
			break
		}
		result.Converted = append(result.Converted, PullConverted{Path: folder + rel, Encoding: b.Converted[rel].Encoding})
	}
}

// pull runs the pull tool on a repository the caller's access check admitted
// for writing. GitHub is read outside the write queue; the queue re-checks
// that the folder, its settings and its baseline did not change meanwhile.
//
// A folder with local changes (since the last pull or push) still pulls when
// no file changed on both sides: GitHub's changes are applied, the local ones
// stay, and they remain pending for push. A file changed on both sides stops
// the pull (conflict) unless both made the same change. No line-level merging.
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
	changed := map[string]string{}
	if hasBase {
		changed = localChanges(base, local)
	}
	unlock, err := s.githubFolderLock(r, folder)
	if err != nil {
		return nil, err
	}
	defer unlock()
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
	result := PullResult{Space: space, Folder: folder, Repo: svc.Repo, Branch: svc.Branch, Commit: commit, OldState: head, NewState: head, Skipped: []PullSkipped{}, Converted: []PullConverted{}}
	// Local changes push would send: files .gitignore excludes stay out of
	// the count (they still count as changes when GitHub adds the same path).
	ignore, err := r.folderIgnore(ctx, local)
	if err != nil {
		return nil, err
	}
	pushable := func(changed map[string]string) int {
		n := 0
		for rel, how := range changed {
			if _, tracked := base.skippedReason(rel); how != "added" || tracked || !ignore.ignored(rel) {
				n++
			}
		}
		return n
	}
	if hasBase && base.Commit == commit {
		result.UpToDate = true
		result.LocalChanges = pushable(changed)
		for rel := range base.Files {
			if _, ok := changed[rel]; !ok {
				result.Unchanged++
			}
		}
		result.listFrom(base, folder)
		return result, nil
	}
	entries, err := source.tree(ctx, treeHash)
	if err != nil {
		return nil, err
	}
	keep, skipped := classifyRepoEntries(folder, entries, r.limits.MaxFileBytes)
	newTree := treeFromEntries(entries)
	githubChanged := map[string]bool{}
	if hasBase {
		githubChanged = changedPaths(base.githubTree(), newTree)
		var overlap []string
		for rel, how := range changed {
			if !githubChanged[rel] {
				continue
			}
			e, onGitHub := newTree[rel]
			f, here := local[rel]
			if onGitHub == here && (!here || e.sha == f.Blob) {
				// The same change on both sides: both deleted, or the same content.
				delete(changed, rel)
				continue
			}
			overlap = append(overlap, folder+rel+" ("+how+" here, changed on GitHub)")
		}
		if len(overlap) > 0 {
			return nil, githubConflict(fmt.Sprintf("Files changed both here and on GitHub since commit %s (GitHub is now at %s): %s. Pull merges only when no file changed on both sides; it never merges lines. To keep your version of a file, copy it outside %s, restore the pulled version (pending gives baseState; read the file with that revision and write it back, or delete a file you added), pull, then reapply your edit and push.", short(base.Commit), short(commit), listSome(overlap), folder))
		}
		// A local file standing where the repository holds a file pull skipped
		// keeps that file skipped while GitHub leaves it alone.
		for rel, how := range changed {
			if sk, wasSkipped := base.skippedReason(rel); how == "added" && wasSkipped && !githubChanged[rel] {
				if _, ok := keep[rel]; ok {
					delete(keep, rel)
					skipped = append(skipped, sk)
				}
			}
		}
	} else {
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
	// Files GitHub did not change and the folder already holds need no
	// download; neither do files the folder holds identical copies of.
	reuse := map[string]bool{}
	want := map[string]string{}
	for rel, e := range keep {
		if _, held := base.Files[rel]; hasBase && held && !githubChanged[rel] {
			reuse[rel] = true
			continue
		}
		if f, ok := local[rel]; ok && f.Blob == e.SHA {
			continue
		}
		want[rel] = e.SHA
	}
	content := map[string][]byte{}
	if len(want) > 0 {
		if content, err = source.contents(ctx, commit, want); err != nil {
			return nil, err
		}
	}
	converted := map[string]string{}
	for rel := range want {
		b, ok := content[rel]
		if !ok || blobHash(b) != want[rel] {
			return nil, errGitHubUnavailable
		}
		if textContent(b) {
			continue
		}
		e := keep[rel]
		if out, encoding, ok := convertUnicode(b); ok {
			if int64(len(out)) <= r.limits.MaxFileBytes {
				content[rel] = out
				converted[rel] = encoding
				continue
			}
			delete(keep, rel)
			delete(content, rel)
			skipped = append(skipped, githubSkipped{Path: e.Path, Blob: e.SHA, Mode: e.Mode, Reason: fmt.Sprintf("%s text, larger than the space's file limit (%d bytes) once converted to UTF-8", encoding, r.limits.MaxFileBytes)})
			continue
		}
		delete(keep, rel)
		delete(content, rel)
		skipped = append(skipped, githubSkipped{Path: e.Path, Blob: e.SHA, Mode: e.Mode, Reason: notTextReason(b)})
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Path < skipped[j].Path })
	plan := pullPlan{result: result, svc: svc, folder: folder, seenHead: head, base: base, hasBase: hasBase, seenLocal: local, writes: map[string][]byte{}}
	plan.baseline = githubBaseline{Repo: svc.Repo, Branch: svc.Branch, Commit: commit, Tree: treeHash, PulledAt: time.Now().UTC().Format(time.RFC3339), Files: map[string]string{}, Skipped: skipped}
	baseline := &plan.baseline
	for rel, e := range keep {
		blob := e.SHA
		switch {
		case reuse[rel]:
			blob = base.Files[rel]
			if c, ok := base.Converted[rel]; ok {
				if baseline.Converted == nil {
					baseline.Converted = map[string]githubConverted{}
				}
				baseline.Converted[rel] = c
			}
		case converted[rel] != "":
			blob = blobHash(content[rel])
			if baseline.Converted == nil {
				baseline.Converted = map[string]githubConverted{}
			}
			baseline.Converted[rel] = githubConverted{GitHubBlob: e.SHA, Encoding: converted[rel]}
		}
		baseline.Files[rel] = blob
		if e.Mode != "100644" {
			if baseline.Modes == nil {
				baseline.Modes = map[string]string{}
			}
			baseline.Modes[rel] = e.Mode
		}
		if _, mine := changed[rel]; mine {
			continue // the local change stays, still pending for push
		}
		if f, ok := local[rel]; ok && f.Blob == blob {
			plan.result.Unchanged++
			continue
		}
		b, ok := content[rel]
		if !ok {
			return nil, errGitHubUnavailable
		}
		plan.writes[rel] = b
	}
	for rel := range local {
		_, kept := keep[rel]
		_, mine := changed[rel]
		if !kept && !mine {
			plan.deletes = append(plan.deletes, rel)
		}
	}
	sort.Strings(plan.deletes)
	// Kept local changes to files the baseline holds: their baseline content
	// stays where it was recorded before.
	if len(changed) > 0 {
		state, err := r.baselineState(ctx, head, folder, base)
		if err != nil {
			return nil, err
		}
		for rel, how := range changed {
			if _, held := baseline.Files[rel]; !held || how == "added" {
				continue
			}
			if baseline.FileStates == nil {
				baseline.FileStates = map[string]string{}
			}
			if s, ok := base.FileStates[rel]; ok {
				baseline.FileStates[rel] = s
			} else if state != "" {
				baseline.FileStates[rel] = state
			}
		}
	}
	plan.result.LocalChanges = pushable(changed)
	// Exact space totals after the pull.
	count, total := 0, int64(0)
	for p, f := range files {
		if !strings.HasPrefix(p, folder) || isFolderConfig(p) {
			count++
			total += f.Bytes
		}
	}
	final := map[string]int64{}
	for rel, f := range local {
		final[rel] = f.Bytes
	}
	for _, rel := range plan.deletes {
		delete(final, rel)
	}
	for rel, b := range plan.writes {
		final[rel] = int64(len(b))
	}
	var folderBytes int64
	for _, n := range final {
		folderBytes += n
	}
	if count+len(final) > r.limits.MaxFiles || total+folderBytes > r.limits.MaxCurrentTextBytes {
		return nil, problem(507, "storage_limit", fmt.Sprintf("%s does not fit in this space: the folder needs %d files and %d bytes of text, and with the rest of the space that makes %d files and %d bytes (the space holds at most %d files and %d bytes).", svc.Repo, len(final), folderBytes, count+len(final), total+folderBytes, r.limits.MaxFiles, r.limits.MaxCurrentTextBytes))
	}
	return s.queued(ctx, func() (any, error) { return s.applyPull(ctx, r, plan) })
}

// applyPull commits a pull plan. It runs in the write queue and refuses if
// the folder, its settings or its baseline changed since pull looked at them.
func (s *Service) applyPull(ctx context.Context, r *repository, plan pullPlan) (any, error) {
	folder, result := plan.folder, plan.result
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	if head != plan.seenHead {
		current, err := r.githubFolderService(ctx, files, folder)
		if err != nil || current != plan.svc {
			return nil, githubConflict(folder + " settings changed during the pull; pull again.")
		}
		baselines, err := r.githubBaselines(ctx, head)
		if err != nil {
			return nil, err
		}
		now, has := baselines[folder]
		if has != plan.hasBase || (has && now.Commit != plan.base.Commit) {
			return nil, githubConflict(folder + " was pulled or pushed by someone else meanwhile; pull again.")
		}
		if !sameRecords(localRepoFiles(files, folder), plan.seenLocal) {
			return nil, githubConflict(folder + " changed during the pull; pull again.")
		}
	}
	result.OldState = head
	texts := map[string]string{}
	var changes []commitChange
	for _, rel := range plan.deletes {
		f := plan.seenLocal[rel]
		delete(files, folder+rel)
		changes = append(changes, commitChange{f.ID, "delete", folder + rel})
		result.Deleted++
	}
	paths := make([]string, 0, len(plan.writes))
	for rel := range plan.writes {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		b := plan.writes[rel]
		p := folder + rel
		f, exists := plan.seenLocal[rel]
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
	baselines[folder] = plan.baseline
	stored, err := json.Marshal(baselines)
	if err != nil {
		return nil, err
	}
	state, err := r.commitFiles(ctx, head, files, texts, map[string][]byte{githubBaselinePath: stored}, changes, pullSummary(plan.svc.Repo, plan.svc.Branch, result.Commit, folder))
	if err != nil {
		return nil, err
	}
	result.NewState = state
	result.listFrom(plan.baseline, folder)
	return result, nil
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}
