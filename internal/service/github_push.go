package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// GitHub step 2d: pending lists what a GitHub folder holds that its baseline
// (the last pull or push) does not, and push sends it to GitHub as one
// commit. Push uses the Git Data API, never a clone: a tree built on the
// baseline commit's tree holding only the changed entries (so files pull
// skipped ride along untouched), a commit whose parent is the baseline
// commit, then a non-forced branch update. Before moving the branch, push
// checks that GitHub built exactly the tree it computed itself.
//
// Rules: all or nothing; never force; GitHub moved ahead -> stop (pull merges
// when no file changed on both sides); .gitignore excludes only files the
// baseline does not hold (git's tracked-file rule); workflow files are refused
// (workflows can reach CI secrets, so they are changed on GitHub directly).

const (
	githubPushesPerSpace   = 10  // per githubPushWindow seconds
	githubPushWindow       = 600 // seconds
	githubPushTimeout      = 3 * time.Minute
	maxPushFiles           = 1000
	maxPushBytes           = 40 << 20
	maxPushMessage         = 4000 // characters
	maxPushTreeBatchBytes  = 4 << 20
	maxPushTreeBatchFiles  = 250
	githubNoreplyDomain    = "users.noreply.metatrash.com"
	githubWorkflowsRefusal = "GitHub workflow file: push does not change files in .github/workflows/, because workflows can reach the repository's CI secrets; change workflows on GitHub directly"
)

// PendingChange is one file push would send, by its path in the space.
type PendingChange struct {
	Path   string `json:"path"`
	Change string `json:"change"` // added, modified or deleted
	Note   string `json:"note,omitempty"`
	// BaseState is the space revision holding the baseline version of a
	// modified or deleted file: read the path at that revision to diff.
	BaseState string `json:"baseState,omitempty"`
}

// PendingNote names a file and why it is listed apart from the changes.
type PendingNote struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type PendingResult struct {
	Space  string `json:"space"`
	Folder string `json:"folder"`
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	Push   string `json:"push"` // the folder's push mode
	// BaseCommit is the GitHub commit of the last pull or push; BaseState the
	// space revision holding its files (see PendingChange.BaseState).
	BaseCommit string `json:"baseCommit"`
	BaseState  string `json:"baseState"`
	State      string `json:"state"`
	// OldestChange is when the oldest pending change was written (RFC 3339),
	// when it can be told.
	OldestChange string          `json:"oldestChange,omitempty"`
	UpToDate     bool            `json:"upToDate"` // nothing to push
	Changes      []PendingChange `json:"changes"`
	// NotPushed: files .gitignore excludes. Refused: changes push refuses, so
	// push sends nothing until they are undone. Converted: files pull
	// converted to UTF-8 that are unchanged, so push leaves GitHub's copy.
	NotPushed []PendingNote `json:"notPushed"`
	Refused   []PendingNote `json:"refused"`
	Converted []PendingNote `json:"converted"`
}

type PushResult struct {
	Space    string `json:"space"`
	Folder   string `json:"folder"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	UpToDate bool   `json:"upToDate"`
	// Commit is the new GitHub commit (with URL); Parent the baseline commit
	// it follows.
	Commit   string          `json:"commit,omitempty"`
	URL      string          `json:"url,omitempty"`
	Parent   string          `json:"parent"`
	Added    int             `json:"added"`
	Modified int             `json:"modified"`
	Deleted  int             `json:"deleted"`
	Files    []PendingChange `json:"files"`
	// NotPushed: files .gitignore excludes (still here, never sent).
	NotPushed []PendingNote `json:"notPushed"`
	// State is the space revision whose files were pushed; NewState the
	// revision after recording the new baseline.
	State    string `json:"state"`
	NewState string `json:"newState"`
	Warning  string `json:"warning,omitempty"`
}

// pushAuthor is the Metatrash user and agent behind a push. GitHubID and
// GitHubLogin are set when the user linked a GitHub account.
type pushAuthor struct {
	Username    string
	Agent       string
	GitHubID    int64
	GitHubLogin string
}

// coAuthor is the Co-authored-by trailer naming the pushing user: their
// GitHub noreply address when they linked GitHub (GitHub then links the
// commit to their account), otherwise a Metatrash noreply address.
func (a pushAuthor) coAuthor() string {
	email := a.Username + "@" + githubNoreplyDomain
	if a.GitHubID > 0 && githubLoginPattern.MatchString(a.GitHubLogin) {
		email = fmt.Sprintf("%d+%s@users.noreply.github.com", a.GitHubID, a.GitHubLogin)
	}
	return "Co-authored-by: " + a.Username + " <" + email + ">"
}

// pendingFile is one change push would send.
type pendingFile struct {
	rel, change, note string
	blob, mode        string // added and modified files
}

// folderPending is a GitHub folder compared with its baseline.
type folderPending struct {
	svc       folderService
	base      githubBaseline
	baseState string
	changes   []pendingFile
	notPushed []PendingNote
	refused   []PendingNote
	converted []PendingNote
}

// folderPending compares a GitHub folder at a snapshot with its baseline.
func (r *repository) folderPending(ctx context.Context, head string, files map[string]record, folder string, svc folderService) (folderPending, error) {
	p := folderPending{svc: svc}
	baselines, err := r.githubBaselines(ctx, head)
	if err != nil {
		return p, err
	}
	base, ok := baselines[folder]
	if !ok || !strings.EqualFold(base.Repo, svc.Repo) || base.Branch != svc.Branch {
		return p, githubConflict(fmt.Sprintf("%s has not been pulled from %s@%s yet; pull first. Push sends changes made since the last pull or push.", folder, svc.Repo, svc.Branch))
	}
	p.base = base
	if p.baseState, err = r.baselineState(ctx, head, folder, base); err != nil {
		return p, err
	}
	local := localRepoFiles(files, folder)
	ignore, err := r.folderIgnore(ctx, local)
	if err != nil {
		return p, err
	}
	for rel, f := range local {
		blob, held := base.Files[rel]
		switch {
		case held && blob == f.Blob:
			if c, ok := base.Converted[rel]; ok {
				p.converted = append(p.converted, PendingNote{folder + rel, fmt.Sprintf("%s on GitHub, held here as UTF-8 (with a UTF-8 byte order mark); unchanged, so push leaves GitHub's %s copy. Push sends it as UTF-8 only once edited: some Windows files need UTF-16 (PowerShell 5.1 scripts with non-ASCII text, .reg files)", c.Encoding, c.Encoding)})
			}
		case held:
			note := ""
			if c, ok := base.Converted[rel]; ok {
				note = fmt.Sprintf("converted from %s on pull; push replaces GitHub's %s file with this UTF-8 version", c.Encoding, c.Encoding)
			}
			p.changes = append(p.changes, pendingFile{rel: rel, change: "modified", note: note, blob: f.Blob, mode: base.mode(rel)})
		default:
			if sk, tracked := base.skippedReason(rel); tracked {
				mode := "100644"
				if sk.Mode == "100755" {
					mode = sk.Mode
				}
				p.changes = append(p.changes, pendingFile{rel: rel, change: "modified", note: "replaces the repository's copy, which pull skipped (" + sk.Reason + ")", blob: f.Blob, mode: mode})
			} else if ignore.ignored(rel) {
				p.notPushed = append(p.notPushed, PendingNote{folder + rel, "excluded by .gitignore"})
			} else {
				p.changes = append(p.changes, pendingFile{rel: rel, change: "added", blob: f.Blob, mode: "100644"})
			}
		}
	}
	for rel := range base.Files {
		if _, ok := local[rel]; !ok {
			p.changes = append(p.changes, pendingFile{rel: rel, change: "deleted"})
		}
	}
	sort.Slice(p.changes, func(i, j int) bool { return p.changes[i].rel < p.changes[j].rel })
	for _, c := range p.changes {
		if isWorkflowPath(c.rel) {
			p.refused = append(p.refused, PendingNote{folder + c.rel, githubWorkflowsRefusal})
		}
	}
	for _, list := range [][]PendingNote{p.notPushed, p.converted} {
		sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	}
	return p, nil
}

// folderIgnore reads the .gitignore files among a folder's files.
func (r *repository) folderIgnore(ctx context.Context, local map[string]record) (*ignoreMatcher, error) {
	gitignores := map[string]string{}
	for rel, f := range local {
		if rel == ".gitignore" || strings.HasSuffix(rel, "/.gitignore") {
			b, err := r.blob(ctx, f.Blob)
			if err != nil {
				return nil, err
			}
			gitignores[rel] = string(b)
		}
	}
	return newIgnoreMatcher(gitignores), nil
}

// changeState gives the revision holding a change's baseline version.
func (p folderPending) changeState(c pendingFile) string {
	if c.change == "added" {
		return ""
	}
	if _, held := p.base.Files[c.rel]; !held {
		return "" // a file pull skipped: its baseline version is on GitHub only
	}
	if s, ok := p.base.FileStates[c.rel]; ok {
		return s
	}
	return p.baseState
}

func (p folderPending) listed(folder string) []PendingChange {
	out := make([]PendingChange, 0, len(p.changes))
	for _, c := range p.changes {
		out = append(out, PendingChange{Path: folder + c.rel, Change: c.change, Note: c.note, BaseState: p.changeState(c)})
	}
	return out
}

func nonNil(list []PendingNote) []PendingNote {
	if list == nil {
		return []PendingNote{}
	}
	return list
}

// pending runs the pending tool: read access is enough.
func (s *Service) pending(ctx context.Context, r *repository, space, folderArg string) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	p, err := r.folderPending(ctx, head, files, folder, svc)
	if err != nil {
		return nil, err
	}
	result := PendingResult{Space: space, Folder: folder, Repo: svc.Repo, Branch: svc.Branch, Push: svc.Push,
		BaseCommit: p.base.Commit, BaseState: p.baseState, State: head, UpToDate: len(p.changes) == 0,
		Changes: p.listed(folder), NotPushed: nonNil(p.notPushed), Refused: nonNil(p.refused), Converted: nonNil(p.converted)}
	if oldest, ok, err := r.oldestChange(ctx, head, folder, p); err != nil {
		return nil, err
	} else if ok {
		result.OldestChange = oldest.UTC().Format(time.RFC3339)
	}
	return result, nil
}

// cleanPushMessage checks a push message and normalizes its line endings.
func cleanPushMessage(m string) (string, error) {
	m = strings.ReplaceAll(m, "\r\n", "\n")
	m = strings.TrimRightFunc(strings.TrimLeft(m, "\n"), unicode.IsSpace)
	if strings.TrimSpace(m) == "" {
		return "", invalid("push needs a message describing the change, like a commit message: a short first line, then details if useful.")
	}
	if !utf8.ValidString(m) || strings.ContainsRune(m, 0) {
		return "", invalid("The push message must be UTF-8 text without NUL.")
	}
	if n := utf8.RuneCountInString(m); n > maxPushMessage {
		return "", invalid(fmt.Sprintf("The push message is limited to %d characters (it has %d).", maxPushMessage, n))
	}
	return m, nil
}

// trailerValue makes a single-line trailer value of at most 100 characters.
func trailerValue(v string) string {
	v = strings.Join(strings.FieldsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if utf8.RuneCountInString(v) > 100 {
		v = string([]rune(v)[:100])
	}
	if v == "" {
		v = "unknown"
	}
	return v
}

// push runs the push tool on a repository the caller's access check admitted
// for writing. GitHub is written outside the write queue (under the folder
// lock); the new baseline is then recorded through the queue.
func (s *Service) push(ctx context.Context, r *repository, space, folderArg, message, ifInState string, author pushAuthor) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, githubPushTimeout)
	defer cancel()
	if r == nil {
		return nil, missing()
	}
	folder, err := githubFolderArg(folderArg)
	if err != nil {
		return nil, err
	}
	msg, err := cleanPushMessage(message)
	if err != nil {
		return nil, err
	}
	if ifInState != "" && !hashPattern.MatchString(ifInState) {
		return nil, invalid("ifInState must be a commit hash from read, list or pending.")
	}
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	svc, err := r.githubFolderService(ctx, files, folder)
	if err != nil {
		return nil, err
	}
	if svc.Push == "review" {
		return nil, problem(403, "forbidden", fmt.Sprintf("%s is set to push \"review\", which waits for a review screen that does not exist yet; nothing was pushed. To push with this tool, set \"push\":\"agent\" (or leave it out) in %s%s.", folder, folder, folderConfigName))
	}
	if ifInState != "" && ifInState != head {
		_, then, err := r.snapshot(ctx, ifInState)
		if err != nil {
			return nil, invalid("ifInState is not a state of this space.")
		}
		var moved []string
		for p, f := range files {
			if g, ok := then[p]; strings.HasPrefix(p, folder) && (!ok || g.Blob != f.Blob || g.ID != f.ID) {
				moved = append(moved, p)
			}
		}
		for p := range then {
			if _, ok := files[p]; strings.HasPrefix(p, folder) && !ok {
				moved = append(moved, p)
			}
		}
		if len(moved) > 0 {
			return nil, &Error{Status: 409, Code: "state_mismatch", Message: fmt.Sprintf("%s changed since ifInState: %s. Nothing was pushed; call pending again and check the changes before pushing.", folder, listSome(moved)), CurrentState: head}
		}
	}
	p, err := r.folderPending(ctx, head, files, folder, svc)
	if err != nil {
		return nil, err
	}
	result := PushResult{Space: space, Folder: folder, Repo: svc.Repo, Branch: svc.Branch, Parent: p.base.Commit, Files: []PendingChange{}, NotPushed: nonNil(p.notPushed), State: head, NewState: head}
	if len(p.changes) == 0 {
		result.UpToDate = true
		return result, nil
	}
	if len(p.refused) > 0 {
		paths := make([]string, len(p.refused))
		for i, n := range p.refused {
			paths[i] = n.Path
		}
		return nil, problem(403, "forbidden", fmt.Sprintf("Nothing was pushed: push refuses changes to GitHub workflow files (%s), because workflows can reach the repository's CI secrets. Undo those changes (read the file at baseState from pending and write it back, or delete a file you added), then push again; workflow changes have to be made on GitHub.", listSome(paths)))
	}
	if len(p.changes) > maxPushFiles {
		return nil, problem(413, "payload_too_large", fmt.Sprintf("%d files changed; one push sends at most %d. Nothing was pushed.", len(p.changes), maxPushFiles))
	}
	var pushBytes int64
	for _, c := range p.changes {
		if c.change != "deleted" {
			pushBytes += files[folder+c.rel].Bytes
		}
	}
	if pushBytes > maxPushBytes {
		return nil, problem(413, "payload_too_large", fmt.Sprintf("The changes hold %d bytes; one push sends at most %d. Nothing was pushed.", pushBytes, maxPushBytes))
	}
	if !usernamePattern.MatchString(author.Username) {
		return nil, problem(403, "forbidden", "Pushes name you by your public Metatrash username; choose one on Your account, then push again.")
	}
	// The tree GitHub holds now, and the one it should hold after the push.
	baseTree := p.base.githubTree()
	if h, err := gitTreeHash(baseTree); err != nil || h != p.base.Tree {
		return nil, githubConflict(fmt.Sprintf("Metatrash cannot rebuild the tree of %s from its record of the last pull, so it cannot check a push; nothing was pushed. Pull again once %s has a new commit, or report this.", short(p.base.Commit), svc.Repo))
	}
	target := make(map[string]treeEntry, len(baseTree)+len(p.changes))
	for path, e := range baseTree {
		target[path] = e
	}
	for _, c := range p.changes {
		if c.change == "deleted" {
			delete(target, c.rel)
		} else {
			target[c.rel] = treeEntry{c.mode, c.blob}
		}
	}
	expected, err := gitTreeHash(target)
	if err != nil {
		return nil, invalid(fmt.Sprintf("Nothing was pushed: in %s, %s (a file pull skipped may stand there). Move the file, then push again.", folder, err.Error()))
	}
	unlock, err := s.githubFolderLock(r, folder)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := s.rates.reserve(allowance{"github:push:" + space, githubPushesPerSpace, githubPushWindow}); err != nil {
		return nil, err
	}
	// Deletions first, then content in batches small enough for one request.
	var batches [][]githubTreeChange
	var batch []githubTreeChange
	var batchBytes int
	flush := func() {
		if len(batch) > 0 {
			batches = append(batches, batch)
			batch, batchBytes = nil, 0
		}
	}
	ordered := append([]pendingFile(nil), p.changes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].change == "deleted" && ordered[j].change != "deleted"
	})
	for _, c := range ordered {
		change := githubTreeChange{Path: c.rel, Mode: "100644"}
		if c.change != "deleted" {
			b, err := r.blob(ctx, c.blob)
			if err != nil {
				return nil, err
			}
			text := string(b)
			change.Content, change.Mode = &text, c.mode
			if batchBytes+len(b) > maxPushTreeBatchBytes {
				flush()
			}
			batchBytes += len(b)
		} else {
			change.Mode = p.base.mode(c.rel)
		}
		batch = append(batch, change)
		if len(batch) == maxPushTreeBatchFiles {
			flush()
		}
	}
	flush()
	pusher, err := s.githubPusherFor(ctx, r, svc.Repo)
	if err != nil {
		return nil, err
	}
	movedAhead := func(now string) error {
		return githubConflict(fmt.Sprintf("%s@%s moved ahead of %s (the last pull or push): GitHub is at %s. Nothing was pushed. Pull to bring GitHub's changes in (it keeps your changes when no file changed on both sides), check pending, then push again.", svc.Repo, svc.Branch, short(p.base.Commit), now))
	}
	ghHead, _, err := pusher.branchHead(ctx, svc.Branch)
	if err != nil {
		return nil, err
	}
	if ghHead != p.base.Commit {
		return nil, movedAhead(short(ghHead))
	}
	tree := p.base.Tree
	for _, b := range batches {
		if tree, err = pusher.createTree(ctx, tree, b); err != nil {
			return nil, err
		}
	}
	if tree != expected {
		return nil, problem(502, "github_unavailable", fmt.Sprintf("GitHub built tree %s, not the expected %s; nothing was pushed (the branch was not moved). Try again; if it happens again, report it.", short(tree), short(expected)))
	}
	trailers := "\n\n" + author.coAuthor() + "\nMetatrash-Space: " + trailerValue(space) + "\nMetatrash-Agent: " + trailerValue(author.Agent)
	commit, commitTree, err := pusher.createCommit(ctx, githubNewCommit{Message: msg + trailers, Tree: tree, Parent: p.base.Commit})
	if err != nil {
		return nil, err
	}
	if commitTree != expected {
		return nil, problem(502, "github_unavailable", "GitHub recorded a different tree in the new commit; nothing was pushed (the branch was not moved).")
	}
	if err := pusher.updateRef(ctx, svc.Branch, commit); err != nil {
		if errors.Is(err, errGitHubMovedAhead) {
			return nil, movedAhead("a newer commit")
		}
		return nil, err
	}
	result.Commit, result.URL = commit, "https://github.com/"+svc.Repo+"/commit/"+commit
	result.Files = p.listed(folder)
	for _, c := range p.changes {
		switch c.change {
		case "added":
			result.Added++
		case "modified":
			result.Modified++
		case "deleted":
			result.Deleted++
		}
	}
	// The new baseline: GitHub's new commit, holding what this push read.
	next := p.base
	next.Commit, next.Tree, next.State, next.FileStates = commit, expected, head, nil
	next.Files = map[string]string{}
	for rel, blob := range p.base.Files {
		next.Files[rel] = blob
	}
	next.Modes, next.Converted = copyModes(p.base.Modes), copyConverted(p.base.Converted)
	for _, c := range p.changes {
		delete(next.Converted, c.rel)
		delete(next.Modes, c.rel)
		if c.change == "deleted" {
			delete(next.Files, c.rel)
			continue
		}
		next.Files[c.rel] = c.blob
		if c.mode != "100644" {
			if next.Modes == nil {
				next.Modes = map[string]string{}
			}
			next.Modes[c.rel] = c.mode
		}
	}
	next.Skipped = nil
	for _, sk := range p.base.Skipped {
		if _, replaced := next.Files[sk.Path]; !replaced {
			next.Skipped = append(next.Skipped, sk)
		}
	}
	if len(next.Modes) == 0 {
		next.Modes = nil
	}
	if len(next.Converted) == 0 {
		next.Converted = nil
	}
	value, err := s.queued(ctx, func() (any, error) {
		now, current, err := r.snapshot(ctx, "")
		if err != nil {
			return nil, err
		}
		baselines, err := r.githubBaselines(ctx, now)
		if err != nil {
			return nil, err
		}
		baselines[folder] = next
		stored, err := json.Marshal(baselines)
		if err != nil {
			return nil, err
		}
		return r.commitFiles(ctx, now, current, nil, map[string][]byte{githubBaselinePath: stored}, nil, fmt.Sprintf("push %s to %s %s@%s", folder, svc.Repo, svc.Branch, short(commit)))
	})
	if err != nil {
		result.Warning = "Pushed to GitHub, but Metatrash could not record the new baseline, so pending still lists these changes. Pull to catch up: files changed the same way on both sides do not conflict."
		return result, nil
	}
	result.NewState = value.(string)
	return result, nil
}

func copyModes(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyConverted(m map[string]githubConverted) map[string]githubConverted {
	out := map[string]githubConverted{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Hints for changes left unpushed: once the oldest pending change in a
// GitHub folder is older than unpushedHintAfter, the next write, move or
// delete in that folder carries a hint, at most once per unpushedHintAfter.
const unpushedHintAfter = 30 * time.Minute

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// oldestChange finds when the oldest pending change was written: the
// earliest commit after a change's baseline revision whose message names one
// of the pending paths. ok is false when it cannot be told.
func (r *repository) oldestChange(ctx context.Context, head, folder string, p folderPending) (time.Time, bool, error) {
	if len(p.changes) == 0 {
		return time.Time{}, false, nil
	}
	byStart := map[string]map[string]bool{} // start revision -> paths
	for _, c := range p.changes {
		start := p.changeState(c)
		if start == "" {
			start = p.baseState
		}
		if start == "" {
			continue
		}
		if byStart[start] == nil {
			byStart[start] = map[string]bool{}
		}
		byStart[start][folder+c.rel] = true
	}
	var oldest time.Time
	found := false
	for start, paths := range byStart {
		if start == head {
			continue
		}
		out, err := git(ctx, r.path, "", nil, "log", "--first-parent", "--max-count=5000", "--format=%x1e%cI%n%B", start+".."+head, "--")
		if err != nil {
			return time.Time{}, false, err
		}
		for _, entry := range strings.Split(string(out), "\x1e") {
			stamp, body, ok := strings.Cut(entry, "\n")
			if !ok {
				continue
			}
			when, err := time.Parse(time.RFC3339, strings.TrimSpace(stamp))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(body, "\n") {
				if fields := strings.SplitN(line, " ", 3); len(fields) == 3 && idPattern.MatchString(fields[0]) && paths[fields[2]] {
					if !found || when.Before(oldest) {
						oldest, found = when, true
					}
					break
				}
			}
		}
	}
	return oldest, found, nil
}

// unpushedHint returns a reminder when paths (just written, moved or
// deleted) lie in a GitHub folder whose oldest pending change has waited
// longer than unpushedHintAfter, at most once per unpushedHintAfter per
// folder. Errors give no hint: the write itself already succeeded.
func (s *Service) unpushedHint(ctx context.Context, r *repository, state string, paths ...string) string {
	if !r.owned {
		return ""
	}
	baselines, err := r.githubBaselines(ctx, state)
	if err != nil || len(baselines) == 0 {
		return ""
	}
	folder := ""
	for f := range baselines {
		for _, p := range paths {
			if p != "" && strings.HasPrefix(p, f) {
				folder = f
			}
		}
	}
	if folder == "" {
		return ""
	}
	key := r.path + "\x00" + folder
	now := s.now()
	if next, ok := s.githubHints.Load(key); ok && now.Before(next.(time.Time)) {
		return ""
	}
	wait := func(until time.Time) string {
		s.githubHints.Store(key, until)
		return ""
	}
	_, files, err := r.snapshot(ctx, state)
	if err != nil {
		return ""
	}
	svc, err := r.githubFolderService(ctx, files, folder)
	if err != nil || svc.Push == "review" {
		return wait(now.Add(unpushedHintAfter))
	}
	p, err := r.folderPending(ctx, state, files, folder, svc)
	if err != nil || len(p.changes) == 0 {
		return wait(now.Add(unpushedHintAfter))
	}
	oldest, ok, err := r.oldestChange(ctx, state, folder, p)
	if err != nil || !ok {
		return wait(now.Add(unpushedHintAfter))
	}
	if age := now.Sub(oldest); age < unpushedHintAfter {
		return wait(oldest.Add(unpushedHintAfter))
	}
	wait(now.Add(unpushedHintAfter))
	changes := "1 unpushed change"
	if len(p.changes) != 1 {
		changes = fmt.Sprintf("%d unpushed changes", len(p.changes))
	}
	return fmt.Sprintf("%s has %s, the oldest from %s ago; pending lists them. Push them when the work is ready.", folder, changes, roughAge(now.Sub(oldest)))
}

// roughAge says how long ago, in minutes, hours or days.
func roughAge(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return fmt.Sprintf("%d %ss", n, unit)
	}
	switch {
	case d < 2*time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/(24*time.Hour)), "day")
	}
}
