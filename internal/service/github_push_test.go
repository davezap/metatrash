package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// serveWrite is the fake GitHub's Git Data API for push: create tree (on a
// base tree), create commit, and a non-forced ref update.
func (f *fakeRepoHost) serveWrite(w http.ResponseWriter, r *http.Request, name string, repo *fakeRepo, parts []string) {
	f.writes++
	rest := ""
	if len(parts) == 3 {
		rest = parts[2]
	}
	var body map[string]json.RawMessage
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		w.WriteHeader(400)
		return
	}
	str := func(key string) string {
		var s string
		_ = json.Unmarshal(body[key], &s)
		return s
	}
	switch {
	case r.Method == http.MethodPost && rest == "git/trees":
		base, ok := f.trees[str("base_tree")]
		if !ok {
			w.WriteHeader(422)
			return
		}
		files := map[string]fakeRepoFile{}
		for p, file := range base {
			files[p] = file
		}
		var entries []map[string]json.RawMessage
		if json.Unmarshal(body["tree"], &entries) != nil {
			w.WriteHeader(400)
			return
		}
		for _, e := range entries {
			var path, mode, kind string
			_ = json.Unmarshal(e["path"], &path)
			_ = json.Unmarshal(e["mode"], &mode)
			_ = json.Unmarshal(e["type"], &kind)
			sha, hasSHA := e["sha"]
			content, hasContent := e["content"]
			if kind != "blob" || hasSHA == hasContent || (mode != "100644" && mode != "100755") {
				f.t.Errorf("tree entry %v", e)
				w.WriteHeader(422)
				return
			}
			if hasSHA {
				if string(sha) != "null" {
					w.WriteHeader(422)
					return
				}
				if _, ok := files[path]; !ok {
					w.WriteHeader(422) // GitHub refuses to delete what is not there
					return
				}
				delete(files, path)
				continue
			}
			var text string
			if json.Unmarshal(content, &text) != nil {
				w.WriteHeader(400)
				return
			}
			files[path] = fakeRepoFile{text: text, mode: mode}
		}
		tree := f.treeHash(files)
		f.trees[tree] = files
		if f.treeOverride != "" {
			tree = f.treeOverride
		}
		fmt.Fprintf(w, `{"sha":%q,"tree":[]}`, tree)
	case r.Method == http.MethodPost && rest == "git/commits":
		var parents []string
		var author map[string]string
		_ = json.Unmarshal(body["parents"], &parents)
		_ = json.Unmarshal(body["author"], &author)
		tree := str("tree")
		files, ok := f.trees[tree]
		if _, known := repo.commits[firstOf(parents)]; !ok || len(parents) != 1 || !known {
			w.WriteHeader(422)
			return
		}
		f.counter++
		commit := fakeSHA("commit", tree, fmt.Sprint(f.counter))
		_, committer := body["committer"]
		repo.commits[commit] = fakeCommit{tree: tree, files: files, parents: parents, message: str("message"), author: author, committerSet: committer}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"sha":%q,"tree":{"sha":%q}}`, commit, tree)
	case r.Method == http.MethodPatch && strings.HasPrefix(rest, "git/refs/heads/"):
		branch := strings.TrimPrefix(rest, "git/refs/heads/")
		if string(body["force"]) != "false" {
			f.t.Errorf("ref update force %s", body["force"])
			w.WriteHeader(400)
			return
		}
		if f.beforeRef != nil {
			f.beforeRef()
		}
		commit := str("sha")
		c, ok := repo.commits[commit]
		if !ok || repo.branches[branch] != firstOf(c.parents) {
			w.WriteHeader(422)
			fmt.Fprint(w, `{"message":"Update is not a fast forward"}`)
			return
		}
		repo.branches[branch] = commit
		fmt.Fprintf(w, `{"ref":"refs/heads/%s","object":{"sha":%q,"type":"commit"}}`, branch, commit)
	default:
		w.WriteHeader(404)
	}
}

func firstOf(list []string) string {
	if len(list) == 0 {
		return ""
	}
	return list[0]
}

// head returns the files at a branch head of a fake repository.
func (f *fakeRepoHost) head(repo, branch string) (string, fakeCommit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[repo]
	commit := r.branches[branch]
	return commit, r.commits[commit]
}

// enablePush wires the push hook to the fake host with a write token.
func (w *pullWorld) enablePush() {
	real := w.s.githubSourceHook
	w.s.githubPushHook = func(ctx context.Context, r *repository, repo string) (githubPusher, error) {
		src, err := real(ctx, r, repo)
		if err != nil {
			return nil, err
		}
		reader := src.(*githubRepoReader)
		token, err := reader.client.installationToken(ctx, 77, repo, "write")
		if err != nil {
			return nil, err
		}
		return &githubRepoReader{client: reader.client, token: token, repo: repo}, nil
	}
}

var pushSpaceCounter int

var testAuthor = pushAuthor{Username: "dave-zap", Agent: "Claude"}

func (w *pullWorld) push(folder, message, ifInState, code string) PushResult {
	w.t.Helper()
	pushSpaceCounter++
	value, err := w.s.push(w.ctx, w.repo, fmt.Sprintf("dave-zap/space-%d", pushSpaceCounter), folder, message, ifInState, testAuthor)
	if code == "" {
		if err != nil {
			w.t.Fatalf("push %s: %v", folder, err)
		}
		return value.(PushResult)
	}
	if p, ok := err.(*Error); !ok || p.Code != code {
		w.t.Fatalf("push %s: got %v, want %s", folder, err, code)
	}
	return PushResult{}
}

func (w *pullWorld) pushMessage(folder, message, ifInState string) string {
	w.t.Helper()
	pushSpaceCounter++
	_, err := w.s.push(w.ctx, w.repo, fmt.Sprintf("dave-zap/space-%d", pushSpaceCounter), folder, message, ifInState, testAuthor)
	if err == nil {
		w.t.Fatalf("push %s succeeded", folder)
	}
	return err.Error()
}

func (w *pullWorld) pending(folder string) PendingResult {
	w.t.Helper()
	value, err := w.s.pending(w.ctx, w.repo, "test", folder)
	if err != nil {
		w.t.Fatalf("pending %s: %v", folder, err)
	}
	return value.(PendingResult)
}

func (w *pullWorld) readAt(path, revision string) string {
	w.t.Helper()
	return w.do("read", Input{Path: path, Revision: revision}, "").(ReadResult).File.Text
}

func changeMap(list []PendingChange) map[string]PendingChange {
	out := map[string]PendingChange{}
	for _, c := range list {
		out[c.Path] = c
	}
	return out
}

func noteMap(list []PendingNote) map[string]string {
	out := map[string]string{}
	for _, n := range list {
		out[n.Path] = n.Reason
	}
	return out
}

const utf16Script = "\xff\xfeW\x00r\x00i\x00t\x00e\x00-\x00H\x00o\x00s\x00t\x00 \x00\xe9\x00\r\x00\n\x00"

func pushRepoFiles() map[string]fakeRepoFile {
	return map[string]fakeRepoFile{
		"README.md":                {text: "# Site\n"},
		"run.sh":                   {text: "#!/bin/sh\necho hi\n", mode: "100755"},
		"docs/guide.md":            {text: "Guide\n"},
		".gitignore":               {text: "dist/\n*.log\n"},
		"keep.log":                 {text: "tracked although *.log is ignored\n"},
		".github/workflows/ci.yml": {text: "on: push\n"},
		"logo.png":                 {text: "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"},
		"latin1.txt":               {text: "caf\xe9\n"},
		"setup.ps1":                {text: utf16Script},
		"current":                  {text: "README.md", mode: "120000"},
		"vendor/lib":               {text: "libcommit", mode: "160000"},
		".metatrash.json":          {text: `{"purpose":"the repository's own"}`},
	}
}

func TestGitHubPush(t *testing.T) {
	w := newPullWorld(t, nil)
	w.enablePush()
	first := w.host.commit("dave-zap/site", "main", pushRepoFiles())
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site"}]}`)

	// Nothing to push before the first pull.
	if msg := w.pushMessage("site/", "x", ""); !strings.Contains(msg, "pull first") {
		t.Fatalf("no baseline: %q", msg)
	}
	if _, err := w.s.pending(w.ctx, w.repo, "test", "site/"); err == nil || err.(*Error).Code != "conflict" {
		t.Fatalf("pending without baseline: %v", err)
	}
	pulled := w.pull("site/", "")
	if pulled.ConvertedCount != 1 || pulled.Converted[0].Path != "site/setup.ps1" {
		t.Fatalf("pull: %+v", pulled)
	}
	// A clean folder: nothing to push, GitHub untouched.
	p := w.pending("site/")
	if !p.UpToDate || len(p.Changes) != 0 || p.BaseCommit != first || p.BaseState != pulled.NewState || p.Push != "agent" || len(p.Converted) != 1 || !strings.Contains(p.Converted[0].Reason, "UTF-16 LE") {
		t.Fatalf("clean pending: %+v", p)
	}
	if r := w.push("site/", "nothing", "", ""); !r.UpToDate || r.Commit != "" || w.host.writes != 0 {
		t.Fatalf("empty push: %+v writes %d", r, w.host.writes)
	}

	// Edits of every kind.
	w.write("site/README.md", "# Site, edited\n")
	w.write("site/run.sh", "#!/bin/sh\necho edited\n")
	w.do("delete", Input{Path: "site/docs/guide.md"}, "")
	w.write("site/docs/new.md", "New page\n")
	w.write("site/dist/out.js", "built\n")                 // ignored, not tracked
	w.write("site/keep.log", "tracked, so pushed\n")       // tracked: .gitignore does not apply
	w.write("site/latin1.txt", "café, now UTF-8\n")        // replaces a file pull skipped
	w.write("site/sub/.metatrash.json", `{"purpose":"x"}`) // Metatrash settings, never pushed
	p = w.pending("site/")
	changes := changeMap(p.Changes)
	want := map[string]string{"site/README.md": "modified", "site/run.sh": "modified", "site/docs/guide.md": "deleted", "site/docs/new.md": "added", "site/keep.log": "modified", "site/latin1.txt": "modified"}
	if p.UpToDate || len(changes) != len(want) {
		t.Fatalf("pending changes: %+v", p.Changes)
	}
	for path, kind := range want {
		if changes[path].Change != kind {
			t.Fatalf("%s: %+v, want %s", path, changes[path], kind)
		}
	}
	if !strings.Contains(changes["site/latin1.txt"].Note, "pull skipped") || changes["site/latin1.txt"].BaseState != "" || changes["site/docs/new.md"].BaseState != "" {
		t.Fatalf("notes: %+v", p.Changes)
	}
	// baseState holds the previous version, for diffs.
	if changes["site/README.md"].BaseState != pulled.NewState || w.readAt("site/README.md", changes["site/README.md"].BaseState) != "# Site\n" || w.readAt("site/docs/guide.md", changes["site/docs/guide.md"].BaseState) != "Guide\n" {
		t.Fatalf("baseState: %+v", changes["site/README.md"])
	}
	if notes := noteMap(p.NotPushed); len(notes) != 1 || notes["site/dist/out.js"] != "excluded by .gitignore" || len(p.Refused) != 0 {
		t.Fatalf("notPushed %+v refused %+v", p.NotPushed, p.Refused)
	}

	// Workflow changes are refused up front, whole push included.
	w.write("site/.github/workflows/ci.yml", "on: [push, pull_request]\n")
	if p := w.pending("site/"); len(p.Refused) != 1 || p.Refused[0].Path != "site/.github/workflows/ci.yml" {
		t.Fatalf("refused: %+v", p.Refused)
	}
	if msg := w.pushMessage("site/", "with workflow", ""); !strings.Contains(msg, "workflow") || !strings.Contains(msg, "site/.github/workflows/ci.yml") {
		t.Fatalf("workflow: %q", msg)
	}
	w.write("site/.github/workflows/ci.yml", "on: push\n")
	// A file where the repository holds a symbolic link pull skipped cannot be a folder.
	w.write("site/current/inside.md", "x\n")
	if msg := w.pushMessage("site/", "clash", ""); !strings.Contains(msg, "current") {
		t.Fatalf("clash: %q", msg)
	}
	w.do("delete", Input{Path: "site/current/inside.md"}, "")
	if w.host.writes != 0 {
		t.Fatalf("refused pushes wrote to GitHub: %d", w.host.writes)
	}

	// Messages are required and bounded.
	w.push("site/", " \n ", "", "invalid_request")
	w.push("site/", strings.Repeat("x", 4001), "", "invalid_request")
	// ifInState: a change in the folder since then refuses; one elsewhere does not.
	checked := w.pending("site/").State
	w.write("site/docs/new.md", "New page, changed after the check\n")
	w.push("site/", "stale", checked, "state_mismatch")
	checked = w.pending("site/").State
	w.write("notes/elsewhere.md", "not in the folder\n")

	pushed := w.push("site/", "Edit the site\n\nSeveral changes at once.", checked, "")
	commit, c := w.host.head("dave-zap/site", "main")
	if pushed.Commit != commit || pushed.Parent != first || c.parents[0] != first || pushed.Added != 1 || pushed.Modified != 4 || pushed.Deleted != 1 || pushed.URL != "https://github.com/dave-zap/site/commit/"+commit || pushed.Warning != "" || pushed.NewState == pushed.State {
		t.Fatalf("push: %+v", pushed)
	}
	if !strings.HasPrefix(c.message, "Edit the site\n\nSeveral changes at once.\n\nCo-authored-by: dave-zap <dave-zap@users.noreply.metatrash.com>\nMetatrash-Space: dave-zap/space-") || !strings.HasSuffix(c.message, "\nMetatrash-Agent: Claude") {
		t.Fatalf("message %q", c.message)
	}
	// No author or committer, so GitHub signs the commit as the app.
	if c.author != nil || c.committerSet {
		t.Fatalf("author %v committer set %v", c.author, c.committerSet)
	}
	// GitHub now holds the edits; everything pull skipped is untouched.
	wantFiles := pushRepoFiles()
	wantFiles["README.md"] = fakeRepoFile{text: "# Site, edited\n", mode: "100644"}
	wantFiles["run.sh"] = fakeRepoFile{text: "#!/bin/sh\necho edited\n", mode: "100755"}
	delete(wantFiles, "docs/guide.md")
	wantFiles["docs/new.md"] = fakeRepoFile{text: "New page, changed after the check\n", mode: "100644"}
	wantFiles["keep.log"] = fakeRepoFile{text: "tracked, so pushed\n", mode: "100644"}
	wantFiles["latin1.txt"] = fakeRepoFile{text: "café, now UTF-8\n", mode: "100644"}
	if len(c.files) != len(wantFiles) {
		t.Fatalf("GitHub files: %v", c.files)
	}
	for path, file := range wantFiles {
		if file.mode == "" {
			file.mode = "100644"
		}
		if got := c.files[path]; got != file {
			t.Fatalf("GitHub %s: %+v, want %+v", path, got, file)
		}
	}
	// The baseline follows: nothing pending, the ignored file still listed.
	p = w.pending("site/")
	if !p.UpToDate || p.BaseCommit != commit || p.BaseState != pushed.State || len(p.NotPushed) != 1 {
		t.Fatalf("after push: %+v", p)
	}
	base, _ := w.baseline("site/")
	if tree, err := gitTreeHash(base.githubTree()); err != nil || tree != base.Tree || tree != c.tree {
		t.Fatalf("baseline tree after push %s %v", tree, err)
	}
	if _, skipped := base.skippedReason("latin1.txt"); skipped || base.Files["latin1.txt"] == "" || base.Modes["run.sh"] != "100755" {
		t.Fatalf("baseline after push: %+v", base)
	}
	if r := w.push("site/", "again", "", ""); !r.UpToDate {
		t.Fatalf("second push: %+v", r)
	}
	// A pull after the push has nothing new.
	if r := w.pull("site/", ""); !r.UpToDate || r.LocalChanges != 0 {
		t.Fatalf("pull after push: %+v", r)
	}

	// A converted file goes back to GitHub only once edited, as UTF-8.
	text := w.read("site/setup.ps1")
	if text != "\ufeffWrite-Host é\r\n" {
		t.Fatalf("converted text %q", text)
	}
	w.write("site/setup.ps1", text+"Write-Host done\r\n")
	p = w.pending("site/")
	if len(p.Changes) != 1 || !strings.Contains(p.Changes[0].Note, "UTF-16 LE") || len(p.Converted) != 0 {
		t.Fatalf("edited converted: %+v", p)
	}
	w.push("site/", "Extend the setup script", "", "")
	_, c = w.host.head("dave-zap/site", "main")
	if c.files["setup.ps1"].text != "\ufeffWrite-Host é\r\nWrite-Host done\r\n" {
		t.Fatalf("pushed script %q", c.files["setup.ps1"].text)
	}
	if base, _ := w.baseline("site/"); len(base.Converted) != 0 {
		t.Fatalf("converted record kept: %+v", base.Converted)
	}

	// GitHub moved ahead: push stops; pull merges; push goes through.
	ahead := c.files
	ahead["CHANGES.md"] = fakeRepoFile{text: "From GitHub\n"}
	aheadCommit := w.host.commit("dave-zap/site", "main", ahead)
	w.write("site/README.md", "# Site, edited again\n")
	writes := w.host.writes
	if msg := w.pushMessage("site/", "behind", ""); !strings.Contains(msg, "moved ahead") || w.host.writes != writes {
		t.Fatalf("moved ahead: %q", msg)
	}
	merged := w.pull("site/", "")
	if merged.Created != 1 || merged.LocalChanges != 1 || w.read("site/README.md") != "# Site, edited again\n" || w.read("site/CHANGES.md") != "From GitHub\n" {
		t.Fatalf("merge pull: %+v", merged)
	}
	p = w.pending("site/")
	if len(p.Changes) != 1 || p.Changes[0].Path != "site/README.md" || p.BaseCommit != aheadCommit {
		t.Fatalf("pending after merge: %+v", p)
	}
	// The kept change's baseline version lives where it was recorded before.
	if w.readAt("site/README.md", p.Changes[0].BaseState) != "# Site, edited\n" {
		t.Fatalf("merge baseState %s", p.Changes[0].BaseState)
	}
	after := w.push("site/", "Edit again", "", "")
	if _, c = w.host.head("dave-zap/site", "main"); after.Parent != aheadCommit || c.files["README.md"].text != "# Site, edited again\n" || c.files["CHANGES.md"].text != "From GitHub\n" {
		t.Fatalf("push after merge: %+v", after)
	}

	// GitHub moving during the push: the non-forced ref update fails and the
	// racing commit stays.
	w.write("site/README.md", "# raced\n")
	var racer string
	w.host.beforeRef = func() {
		_, cur := w.host.repos["dave-zap/site"].branches["main"], w.host.repos["dave-zap/site"]
		files := cur.commits[cur.branches["main"]].files
		files["RACE.md"] = fakeRepoFile{text: "race\n", mode: "100644"}
		racer = w.host.commitLocked("dave-zap/site", "main", files)
	}
	if msg := w.pushMessage("site/", "racing", ""); !strings.Contains(msg, "moved ahead") {
		t.Fatalf("race: %q", msg)
	}
	w.host.beforeRef = nil
	if head, _ := w.host.head("dave-zap/site", "main"); head != racer {
		t.Fatal("racing commit lost")
	}
	if p := w.pending("site/"); len(p.Changes) != 1 {
		t.Fatalf("pending after race: %+v", p)
	}
	w.pull("site/", "")

	// GitHub answering with another tree: nothing is pushed.
	w.host.treeOverride = strings.Repeat("e", 40)
	head, _ := w.host.head("dave-zap/site", "main")
	if msg := w.pushMessage("site/", "odd tree", ""); !strings.Contains(msg, "not the expected") {
		t.Fatalf("tree check: %q", msg)
	}
	w.host.treeOverride = ""
	if now, _ := w.host.head("dave-zap/site", "main"); now != head {
		t.Fatal("branch moved after a tree mismatch")
	}

	// Push modes: review refuses, auto pushes like agent.
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site","push":"review"}]}`)
	if msg := w.pushMessage("site/", "review", ""); !strings.Contains(msg, `"review"`) {
		t.Fatalf("review: %q", msg)
	}
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site","push":"auto"}]}`)
	if r := w.push("site/", "auto mode", "", ""); r.Modified != 1 {
		t.Fatalf("auto: %+v", r)
	}
	// Pushes are signed with a username.
	w.write("site/README.md", "# anonymous\n")
	if _, err := w.s.push(w.ctx, w.repo, "x", "site/", "m", "", pushAuthor{Agent: "Claude"}); err == nil || err.(*Error).Code != "forbidden" {
		t.Fatalf("no username: %v", err)
	}
	if out, err := git(w.ctx, w.repo.path, "", nil, "fsck", "--strict", "--no-dangling"); err != nil {
		t.Fatalf("fsck: %v %s", err, out)
	}
}

func TestGitHubPullMerge(t *testing.T) {
	w := newPullWorld(t, nil)
	files := map[string]fakeRepoFile{"a.md": {text: "a\n"}, "b.md": {text: "b\n"}, "c.md": {text: "c\n"}, "d.md": {text: "d\n"}, "setup.ps1": {text: utf16Script}}
	w.host.commit("dave-zap/m", "main", files)
	w.write("m/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/m"}]}`)
	w.pull("m/", "")
	firstState, _ := w.repo.head(w.ctx)
	// Here: a modified, b deleted, new.md added. GitHub: c modified, gh.md added.
	w.write("m/a.md", "a here\n")
	w.do("delete", Input{Path: "m/b.md"}, "")
	w.write("m/new.md", "new here\n")
	next := map[string]fakeRepoFile{"a.md": {text: "a\n"}, "b.md": {text: "b\n"}, "c.md": {text: "c on GitHub\n"}, "d.md": {text: "d\n"}, "gh.md": {text: "from GitHub\n"}, "setup.ps1": {text: utf16Script}}
	second := w.host.commit("dave-zap/m", "main", next)
	fetches := w.host.blobFetches + w.host.tarballs
	r := w.pull("m/", "")
	if r.Commit != second || r.Created != 1 || r.Updated != 1 || r.Deleted != 0 || r.LocalChanges != 3 || r.ConvertedCount != 1 {
		t.Fatalf("merge: %+v", r)
	}
	if w.read("m/a.md") != "a here\n" || w.read("m/c.md") != "c on GitHub\n" || w.read("m/gh.md") != "from GitHub\n" || w.read("m/new.md") != "new here\n" {
		t.Fatal("merged content")
	}
	if _, ok := w.files("m/")["m/b.md"]; ok {
		t.Fatal("local delete undone")
	}
	if w.host.blobFetches+w.host.tarballs != fetches+1 {
		t.Fatal("expected one tarball for the changed files")
	}
	changes := changeMap(w.pending("m/").Changes)
	if len(changes) != 3 || changes["m/a.md"].BaseState != firstState || changes["m/b.md"].Change != "deleted" || changes["m/new.md"].Change != "added" {
		t.Fatalf("pending after merge: %+v", changes)
	}
	// Both sides changing a file stops the pull, naming it.
	third := next
	third["a.md"] = fakeRepoFile{text: "a on GitHub\n"}
	third["new.md"] = fakeRepoFile{text: "new on GitHub\n"}
	w.host.commit("dave-zap/m", "main", third)
	msg := w.pullMessage("m/")
	for _, want := range []string{"m/a.md (modified here", "m/new.md (added here", "changed both here and on GitHub"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("overlap %q lacks %q", msg, want)
		}
	}
	if strings.Contains(msg, "b.md") {
		t.Fatalf("overlap names b.md: %q", msg)
	}
	// The same change on both sides is no conflict.
	w.write("m/a.md", "a on GitHub\n")
	w.write("m/new.md", "new on GitHub\n")
	if r := w.pull("m/", ""); r.LocalChanges != 1 {
		t.Fatalf("same changes: %+v", r)
	}
	if changes := w.pending("m/").Changes; len(changes) != 1 || changes[0].Path != "m/b.md" {
		t.Fatalf("pending after same changes: %+v", changes)
	}
	// Both deleting a file is the same change too.
	delete(third, "b.md")
	w.host.commit("dave-zap/m", "main", third)
	if r := w.pull("m/", ""); r.LocalChanges != 0 || !w.pending("m/").UpToDate {
		t.Fatalf("both deleted: %+v", r)
	}
	// A converted file GitHub did not change is not fetched again.
	third["d.md"] = fakeRepoFile{text: "d2\n"}
	w.host.commit("dave-zap/m", "main", third)
	blobs := w.host.blobFetches
	if r := w.pull("m/", ""); r.Updated != 1 || r.ConvertedCount != 1 || w.host.blobFetches != blobs {
		t.Fatalf("converted kept: %+v", r)
	}
	// One pull or push at a time per folder.
	unlock, err := w.s.githubFolderLock(w.repo, "m/")
	if err != nil {
		t.Fatal(err)
	}
	if msg := w.pullMessage("m/"); !strings.Contains(msg, "already running") {
		t.Fatalf("lock: %q", msg)
	}
	unlock()
}

func TestGitTreeHashAndConversion(t *testing.T) {
	// Git's empty tree, and a known tree from git itself.
	if h, _ := gitTreeHash(map[string]treeEntry{}); h != "4b825dc642cb6eb9a060e54bf8d69288fbee4904" {
		t.Fatalf("empty tree %s", h)
	}
	host := newFakeRepoHost(t)
	files := map[string]fakeRepoFile{"a": {text: "1", mode: "100644"}, "a.b": {text: "2", mode: "100755"}, "a/c": {text: "3", mode: "100644"}, "a-b/d": {text: "l", mode: "120000"}, "z/sub": {text: "s", mode: "160000"}}
	entries := map[string]treeEntry{}
	for p, f := range files {
		entries[p] = treeEntry{f.mode, host.entrySHA(f)}
	}
	if _, err := gitTreeHash(entries); err == nil {
		t.Fatal("file and folder a accepted")
	}
	delete(files, "a")
	delete(entries, "a")
	if h, err := gitTreeHash(entries); err != nil || h != host.treeHash(files) {
		t.Fatalf("tree %s %v, git says %s", h, err, host.treeHash(files))
	}
	for in, want := range map[string]string{
		"\xff\xfeh\x00i\x00":               "\ufeffhi",
		"\xfe\xff\x00h\x00i":               "\ufeffhi",
		"\xff\xfe=\xd8\x00\xde":            "\ufeff\U0001F600",
		"\xff\xfe\x00\x00h\x00\x00\x00":    "\ufeffh",
		"\x00\x00\xfe\xff\x00\x01\xf6\x00": "\ufeff\U0001F600",
	} {
		out, _, ok := convertUnicode([]byte(in))
		if !ok || string(out) != want {
			t.Fatalf("convert %q: %q %v", in, out, ok)
		}
	}
	for _, bad := range []string{"\xff\xfe\x00\xd8", "\xff\xfe\x00\xdc", "\xff\xfeh", "\xff\xfe\x00\x00\x00\x00\x11\x00", "plain", "\xef\xbb\xbfutf8"} {
		if _, _, ok := convertUnicode([]byte(bad)); ok {
			t.Fatalf("converted %q", bad)
		}
	}
	if m, err := cleanPushMessage("\n\nFix\r\nmore  \n"); err != nil || m != "Fix\nmore" {
		t.Fatalf("message %q %v", m, err)
	}
	if v := trailerValue("Claude\nInjected: yes"); v != "Claude Injected: yes" {
		t.Fatalf("trailer %q", v)
	}
}

// Pending and push through /mcp/account and the REST resource: the commit
// is signed with the connection's username and app name.
func TestGitHubPushAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	host := newFakeRepoHost(t)
	cfg := githubTestConfig(t, pkcs1PEM(githubTestKey(t)))
	settings, err := cfg.settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.webBase, settings.apiBase = host.server.URL+"/web", host.apiBase()
	w.s.accounts.github = settings
	first := host.commit("dave-zap/site", "main", map[string]fakeRepoFile{"README.md": {text: "# Site\n"}})
	if err := w.db.linkGitHubInstallation(ctx, githubInstallation{InstallationID: randomInstallationID(t), UserID: w.owner.ID, GitHubUserID: 42, GitHubLogin: "dave-zap", AccountLogin: "dave-zap", AccountType: "User", Status: "active"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	write := func(path, text string) {
		t.Helper()
		state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
		w.tool(token, "write", map[string]any{"space": w.ownedName, "path": path, "text": text, "ifInState": state}, "")
	}
	write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site"}]}`)
	w.tool(token, "pull", map[string]any{"space": w.ownedName, "folder": "site/"}, "")
	write("site/README.md", "# Site, from an agent\n")
	pending := w.tool(token, "pending", map[string]any{"space": w.ownedName, "folder": "site"}, "")
	if pending["upToDate"] != false || len(pending["changes"].([]any)) != 1 || pending["baseCommit"] != first {
		t.Fatalf("pending: %v", pending)
	}
	pushed := w.tool(token, "push", map[string]any{"space": w.ownedName, "folder": "site/", "message": "Agent edit", "ifInState": pending["state"]}, "")
	commit, c := host.head("dave-zap/site", "main")
	if pushed["commit"] != commit || pushed["modified"] != float64(1) || c.author != nil {
		t.Fatalf("push: %v author %v", pushed, c.author)
	}
	// The owner linked GitHub account 42 (dave-zap): GitHub credits them.
	if !strings.HasSuffix(c.message, "\n\nCo-authored-by: "+w.owner.Username+" <42+dave-zap@users.noreply.github.com>\nMetatrash-Space: "+w.ownedName+"\nMetatrash-Agent: Claude") {
		t.Fatalf("trailers %q", c.message)
	}
	// Arguments are checked.
	w.tool(token, "push", map[string]any{"space": w.ownedName, "folder": "site/"}, "invalid_request")
	w.tool(token, "pending", map[string]any{"space": w.ownedName, "folder": "site/", "x": 1}, "invalid_request")

	// REST: GET .../pending?folder=, POST .../push.
	restToken := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "https://metatrash.com/api/v1/account")
	base := "/api/v1/account/spaces/" + w.ownedName
	write("site/new.md", "added\n")
	if r := w.rest(restToken, "GET", base+"/pending?folder=site/", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"change":"added"`) {
		t.Fatalf("REST pending: %d %s", r.Code, r.Body.String())
	}
	if r := w.rest(restToken, "GET", base+"/pending", ""); r.Code != 400 {
		t.Fatalf("REST pending without folder: %d", r.Code)
	}
	if r := w.rest(restToken, "POST", base+"/pending?folder=site/", `{}`); r.Code != 405 {
		t.Fatalf("REST POST pending: %d", r.Code)
	}
	if r := w.rest(restToken, "POST", base+"/push", `{"folder":"site/"}`); r.Code != 400 {
		t.Fatalf("REST push without message: %d", r.Code)
	}
	if r := w.rest(restToken, "POST", base+"/push", `{"folder":"site/","message":"REST push"}`); r.Code != 200 || !strings.Contains(r.Body.String(), `"added":1`) {
		t.Fatalf("REST push: %d %s", r.Code, r.Body.String())
	}
	if _, c := host.head("dave-zap/site", "main"); c.files["new.md"].text != "added\n" || !strings.Contains(c.message, "Co-authored-by: "+w.owner.Username+" <42+dave-zap@") {
		t.Fatalf("REST push on GitHub: %+v", c)
	}
	if r := w.rest("", "POST", "/api/v1/spaces/public/push", `{"folder":"site/","message":"m"}`); r.Code != 404 {
		t.Fatalf("anonymous push: %d", r.Code)
	}
	// Read-only connections may look but not push (this replaces the
	// read-write connection above).
	readOnly := w.connect(w.owner, map[string]string{w.ownedID: "read_only"}, "")
	if p := w.tool(readOnly, "pending", map[string]any{"space": w.ownedName, "folder": "site/"}, ""); p["upToDate"] != true {
		t.Fatalf("read-only pending: %v", p)
	}
	w.tool(readOnly, "push", map[string]any{"space": w.ownedName, "folder": "site/", "message": "m"}, "insufficient_scope")
}

func TestGitHubUnpushedHint(t *testing.T) {
	w := newPullWorld(t, nil)
	w.host.commit("dave-zap/h", "main", map[string]fakeRepoFile{"a.md": {text: "a\n"}, "b.md": {text: "b\n"}})
	w.write("h/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/h"}]}`)
	w.pull("h/", "")
	write := func(path, text string) string {
		t.Helper()
		return w.do("write", Input{Path: path, Text: &text}, "").(WriteResult).Hint
	}
	start := time.Now()
	// Fresh changes: no hint, and pending tells when the oldest was written.
	if hint := write("h/a.md", "a2\n"); hint != "" {
		t.Fatalf("early hint %q", hint)
	}
	p := w.pending("h/")
	oldest, err := time.Parse(time.RFC3339, p.OldestChange)
	if err != nil || oldest.Before(start.Add(-2*time.Second)) || oldest.After(time.Now().Add(time.Second)) {
		t.Fatalf("oldestChange %q", p.OldestChange)
	}
	// 31 minutes later the next change in the folder carries the hint, once.
	w.s.clock = func() time.Time { return start.Add(31 * time.Minute) }
	if hint := write("notes/x.md", "outside\n"); hint != "" {
		t.Fatalf("hint outside the folder %q", hint)
	}
	hint := write("h/b.md", "b2\n")
	if !strings.Contains(hint, "h/ has 2 unpushed changes, the oldest from 31 minutes ago") {
		t.Fatalf("hint %q", hint)
	}
	if hint := write("h/c.md", "c\n"); hint != "" {
		t.Fatalf("repeated hint %q", hint)
	}
	// Deletes and moves carry it too, 30 minutes on.
	w.s.clock = func() time.Time { return start.Add(62 * time.Minute) }
	if m := w.do("delete", Input{Path: "h/c.md"}, "").(Mutation); !strings.Contains(m.Hint, "2 unpushed changes, the oldest from 62 minutes ago") {
		t.Fatalf("delete hint %q", m.Hint)
	}
	// After a push the age counts from the next change.
	w.enablePush()
	w.push("h/", "push them", "", "")
	w.s.githubHints.Range(func(k, _ any) bool { w.s.githubHints.Delete(k); return true })
	w.s.clock = nil
	if hint := write("h/a.md", "a3\n"); hint != "" {
		t.Fatalf("hint after push %q", hint)
	}
	// A review folder gets no hints.
	w.write("h/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/h","push":"review"}]}`)
	w.s.clock = func() time.Time { return start.Add(400 * time.Minute) }
	if hint := write("h/b.md", "b3\n"); hint != "" {
		t.Fatalf("review hint %q", hint)
	}
	if roughAge(3*time.Hour) != "3 hours" || roughAge(50*time.Hour) != "2 days" || roughAge(time.Minute) != "1 minute" {
		t.Fatal("roughAge")
	}
}
