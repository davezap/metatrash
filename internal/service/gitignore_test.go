package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitignoreCases are .gitignore files and paths checked against real git.
var gitignoreCases = []struct {
	name       string
	gitignores map[string]string
	paths      []string
}{
	{"basics", map[string]string{".gitignore": "# comment\n\n*.log\nbuild/\n/root-only.txt\nnode_modules\n!keep.log\n\\#literal\n\\!bang\ntrailing   \n"},
		[]string{"a.log", "src/b.log", "keep.log", "src/keep.log", "build/out.js", "src/build/x", "root-only.txt", "src/root-only.txt",
			"node_modules/x/index.js", "src/node_modules/y", "#literal", "!bang", "trailing", "src/main.go", "README.md"}},
	{"anchors and doublestar", map[string]string{".gitignore": "docs/*.md\n**/tmp\nlogs/**\na/**/z\n/dist\n**/cache/**\n"},
		[]string{"docs/a.md", "docs/sub/b.md", "x/docs/a.md", "tmp", "q/tmp", "q/tmp/f", "q/tmpx", "logs/a", "logs/d/e", "logs",
			"a/z", "a/b/z", "a/b/c/z", "b/a/z", "dist/app.js", "src/dist/app.js", "cache/x", "w/cache/y/z", "cachex/y"}},
	{"wildcards and classes", map[string]string{".gitignore": "file?.txt\n*.[oa]\n[!a-c]*.tmp\nfoo*bar\n*~\n.DS_Store\n"},
		[]string{"file1.txt", "file12.txt", "file.txt", "x.o", "lib/y.a", "z.c", "a1.tmp", "d1.tmp", "c.tmp", "fooXbar", "foobar", "foo/bar",
			"notes.md~", ".DS_Store", "sub/.DS_Store"}},
	{"negation and excluded folders", map[string]string{".gitignore": "*\n!*/\n!*.go\nsecret/\n!secret/ok.go\n"},
		[]string{"main.go", "notes.txt", "pkg/a.go", "pkg/b.txt", "secret/ok.go", "secret/x.go"}},
	{"excluded folder not re-included", map[string]string{".gitignore": "vendor/\n!vendor/keep.txt\n"},
		[]string{"vendor/keep.txt", "vendor/other.txt", "keep.txt"}},
	{"nested gitignore files", map[string]string{".gitignore": "*.log\n/top.txt\n", "web/.gitignore": "!important.log\n/local.txt\ndist/\n", "web/sub/.gitignore": "*.txt\n"},
		[]string{"a.log", "web/a.log", "web/important.log", "important.log", "web/local.txt", "local.txt", "web/x/local.txt", "top.txt", "web/top.txt",
			"web/dist/a.js", "dist/a.js", "web/sub/a.txt", "web/b.txt", "web/sub/c.md"}},
	{"directory-only pattern and a file of that name", map[string]string{".gitignore": "build/\nlogs/**\n"},
		[]string{"build", "x/build/y", "logs"}},
	{"more edge cases", map[string]string{".gitignore": "a/b/\nsrc/*\n!src/keep/\nlit\\*star\n[abc]x\n**/deep/**/*.tmp\n*/mid.txt\n", "src/keep/.gitignore": "*.gen\n"},
		[]string{"a/b/c", "x/a/b/c", "a/bc", "src/x.go", "src/keep/y.go", "src/keep/z.gen", "src/other/q", "lit*star", "litXstar", "ax", "dx", "q/ax",
			"deep/x.tmp", "p/deep/q/r/x.tmp", "p/deep/x.txt", "one/mid.txt", "one/two/mid.txt", "mid.txt"}},
	{"crlf and spaces", map[string]string{".gitignore": "*.bak\r\nwith\\ space\\ \r\n  lead.txt\n"},
		[]string{"a.bak", "with space ", "with space", "  lead.txt", "lead.txt"}},
}

func TestGitignoreMatchesGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, tc := range gitignoreCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			run := func(stdin string, args ...string) string {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
				cmd.Stdin = strings.NewReader(stdin)
				out, _ := cmd.Output() // check-ignore exits 1 when nothing matches
				return string(out)
			}
			run("", "init", "-q")
			for path, text := range tc.gitignores {
				full := filepath.Join(dir, filepath.FromSlash(path))
				_ = os.MkdirAll(filepath.Dir(full), 0700)
				if err := os.WriteFile(full, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range tc.paths {
				full := filepath.Join(dir, filepath.FromSlash(path))
				_ = os.MkdirAll(filepath.Dir(full), 0700)
				_ = os.WriteFile(full, []byte("x"), 0600)
			}
			gitIgnored := map[string]bool{}
			for _, line := range strings.Split(run(strings.Join(tc.paths, "\n")+"\n", "check-ignore", "--stdin"), "\n") {
				if line != "" {
					gitIgnored[line] = true
				}
			}
			m := newIgnoreMatcher(tc.gitignores)
			for _, path := range tc.paths {
				if got, want := m.ignored(path), gitIgnored[path]; got != want {
					t.Errorf("%q: ignored=%v, git says %v", path, got, want)
				}
			}
		})
	}
}

func TestRepoFiles(t *testing.T) {
	r, files := testRepoWith(t, map[string]string{
		"site/.metatrash.json":      `{"services":[{"type":"github","repo":"a/b"}]}`,
		"site/.gitignore":           "*.log\nnode_modules/\n",
		"site/index.html":           "x",
		"site/debug.log":            "x",
		"site/node_modules/a.js":    "x",
		"site/docs/.metatrash.json": `{"purpose":"docs"}`,
		"site/docs/a.md":            "x",
		"other/a.md":                "x",
	})
	send, skip, err := r.repoFiles(t.Context(), files, "site/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(send, ",") != ".gitignore,docs/a.md,index.html" || strings.Join(skip, ",") != ".metatrash.json,debug.log,docs/.metatrash.json,node_modules/a.js" {
		t.Fatalf("send %v skip %v", send, skip)
	}
}

// testRepoWith makes a bare repository holding the given texts as blobs and
// returns it with records pointing at them (no commit is needed to read blobs).
func testRepoWith(t *testing.T, texts map[string]string) (*repository, map[string]record) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.git")
	if _, err := git(t.Context(), "", "", nil, "init", "-q", "--bare", path); err != nil {
		t.Fatal(err)
	}
	r := &repository{path: path}
	files := map[string]record{}
	for p, text := range texts {
		b, err := git(t.Context(), path, "", []byte(text), "hash-object", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		files[p] = record{File: File{Path: p}, Blob: strings.TrimSpace(string(b))}
	}
	return r, files
}
