package service

import (
	"strings"
	"testing"
)

func TestParseGitHubPush(t *testing.T) {
	repo, branch, inst, ok := parseGitHubPush([]byte(`{"ref":"refs/heads/feature/x","repository":{"full_name":"dave-zap/site"},"installation":{"id":77}}`))
	if !ok || repo != "dave-zap/site" || branch != "feature/x" || inst != 77 {
		t.Fatalf("push: %s %s %d %v", repo, branch, inst, ok)
	}
	for name, body := range map[string]string{
		"tag":             `{"ref":"refs/tags/v1","repository":{"full_name":"dave-zap/site"},"installation":{"id":77}}`,
		"deleted branch":  `{"ref":"refs/heads/main","deleted":true,"repository":{"full_name":"dave-zap/site"},"installation":{"id":77}}`,
		"no installation": `{"ref":"refs/heads/main","repository":{"full_name":"dave-zap/site"}}`,
		"bad repository":  `{"ref":"refs/heads/main","repository":{"full_name":"../x"},"installation":{"id":77}}`,
		"not JSON":        `{`,
	} {
		if _, _, _, ok := parseGitHubPush([]byte(body)); ok {
			t.Errorf("%s accepted", name)
		}
	}
}

// A push delivery pulls the folders that mirror the repository and branch,
// in spaces whose owner linked the installation, unless the folder says pull
// "agent"; a conflict leaves the folder as it was.
func TestGitHubAutoPull(t *testing.T) {
	w := newPullWorld(t, nil)
	w.repo.id, w.repo.owner = strings.Repeat("cd", 16), strings.Repeat("ef", 16)
	w.s.ownedMu.Lock()
	w.s.ownedRepos[w.repo.id] = w.repo
	w.s.ownedMu.Unlock()
	linked := true
	w.s.githubInstalledHook = func(userID string, installation int64) bool {
		return linked && userID == w.repo.owner && installation == 77
	}
	pushed := func(repo, branch string, installation int64) {
		t.Helper()
		w.s.githubPushed(repo, branch, installation)
		w.s.autoPull.wg.Wait()
	}

	w.host.commit("dave-zap/site", "main", map[string]fakeRepoFile{"README.md": {text: "# v1\n"}})
	w.write("site/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site"}]}`)
	w.write("manual/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site","pull":"agent"}]}`)
	w.write("dev/.metatrash.json", `{"services":[{"type":"github","repo":"dave-zap/site","branch":"dev"}]}`)
	pushed("Dave-Zap/Site", "main", 77) // GitHub's name in another case
	if w.read("site/README.md") != "# v1\n" {
		t.Fatal("first push not pulled")
	}
	if _, ok := w.files("manual/")["manual/README.md"]; ok {
		t.Fatal(`pull "agent" folder pulled`)
	}
	if _, ok := w.files("dev/")["dev/README.md"]; ok {
		t.Fatal("other branch's folder pulled")
	}
	history := w.do("history", Input{ID: w.files("site/")["site/README.md"].ID}, "").(HistoryResult)
	if by := history.Entries[0].By; by == nil || by.App != autoPullApp {
		t.Fatalf("pull commit by %+v", by)
	}

	// Another installation, or one the owner did not link, pulls nothing.
	w.host.commit("dave-zap/site", "main", map[string]fakeRepoFile{"README.md": {text: "# v2\n"}})
	pushed("dave-zap/site", "main", 78)
	linked = false
	pushed("dave-zap/site", "main", 77)
	if w.read("site/README.md") != "# v1\n" {
		t.Fatal("pulled for an installation the owner did not link")
	}
	linked = true
	pushed("dave-zap/site", "main", 77)
	if w.read("site/README.md") != "# v2\n" {
		t.Fatal("second push not pulled")
	}

	// The same file changed here and on GitHub: the pull stops.
	w.write("site/README.md", "# mine\n")
	w.host.commit("dave-zap/site", "main", map[string]fakeRepoFile{"README.md": {text: "# v3\n"}})
	pushed("dave-zap/site", "main", 77)
	if w.read("site/README.md") != "# mine\n" {
		t.Fatal("conflicting pull overwrote the local change")
	}

	// Pushes during a pull make one more pull afterwards.
	w.write("site/README.md", "# v2\n")
	unlock, err := w.s.githubFolderLock(w.repo, "site/")
	if err != nil {
		t.Fatal(err)
	}
	w.s.autoPull.mu.Lock()
	w.s.autoPull.again = map[string]bool{w.repo.path + "\x00site/": false}
	w.s.autoPull.mu.Unlock()
	w.s.queueAutoPull(w.repo, "site/") // marks another pull wanted
	w.s.autoPull.mu.Lock()
	again := w.s.autoPull.again[w.repo.path+"\x00site/"]
	delete(w.s.autoPull.again, w.repo.path+"\x00site/")
	w.s.autoPull.mu.Unlock()
	unlock()
	if !again {
		t.Fatal("a push during a pull was not kept for later")
	}
	pushed("dave-zap/site", "main", 77)
	if w.read("site/README.md") != "# v3\n" {
		t.Fatal("third push not pulled")
	}
}
