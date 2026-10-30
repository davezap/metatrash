package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pull, pending and push name the GitHub folder by its path in the space
// (with a registered folder's prefix), and show paths as paths in the synced
// folder.
func TestGitHubCommands(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/pull"):
			_, _ = io.WriteString(w, `{"folder":"docs/site/","repo":"dave/site","branch":"main","commit":"0123456789abcdef","created":2,"updated":1,"skipped":[{"path":"logo.psd","reason":"too big"}],"skippedCount":1,"localChanges":1}`)
		case strings.HasSuffix(r.URL.Path, "/pending"):
			_, _ = io.WriteString(w, `{"folder":"docs/site/","repo":"dave/site","branch":"main","push":"agent","baseCommit":"0123456789abcdef","changes":[{"path":"docs/site/a.md","change":"modified","baseState":"s"}],"notPushed":[{"path":"docs/site/tmp.log","reason":".gitignore"}]}`)
		case strings.HasSuffix(r.URL.Path, "/push"):
			var body map[string]string
			_ = json.Unmarshal(b, &body)
			if body["message"] == "refuse" {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, `{"error":{"code":"forbidden","message":"This folder's push mode is review."}}`)
				return
			}
			_, _ = io.WriteString(w, `{"folder":"docs/site/","repo":"dave/site","branch":"main","commit":"fedcba9876543210","url":"https://github.com/dave/site/commit/fedcba9","modified":1,"files":[{"path":"docs/site/a.md","change":"modified"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	saved := folderClient
	t.Cleanup(func() { folderClient = saved })
	folderClient = func(e folderEntry, _ func(context.Context, bool) (string, error)) *client {
		return newClient(e.Server, e.Space, func(context.Context, bool) (string, error) { return "t", nil }, true)
	}
	dir := t.TempDir()
	if err := saveRegistry(registry{Folders: []folderEntry{{Space: "dave/work/docs", Folder: dir, Server: server.URL}}}); err != nil {
		t.Fatal(err)
	}
	site := filepath.Join(dir, "site")
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	out := &strings.Builder{}
	if err := githubPull(ctx, site, false, out); err != nil {
		t.Fatal(err)
	}
	if got[0] != `POST /api/v1/account/spaces/dave/work/pull? {"folder":"docs/site/"}` || !strings.Contains(out.String(), "Pulled dave/site main (0123456) into") || !strings.Contains(out.String(), "skipped logo.psd: too big") || !strings.Contains(out.String(), "1 changed here and not pushed yet") {
		t.Fatalf("pull: %q %q", got, out)
	}

	out.Reset()
	if err := githubPending(ctx, site, true, out); err != nil {
		t.Fatal(err)
	}
	var pending pendingAnswer
	if err := json.Unmarshal([]byte(out.String()), &pending); err != nil || got[1] != "GET /api/v1/account/spaces/dave/work/pending?folder=docs%2Fsite%2F " || pending.Folder != "site/" || len(pending.Changes) != 1 || pending.Changes[0].Path != "site/a.md" || pending.NotPushed[0].Path != "site/tmp.log" || pending.Refused == nil {
		t.Fatalf("pending -json: %v %q %+v", err, got, pending)
	}
	out.Reset()
	if err := githubPending(ctx, site, false, out); err != nil || !strings.Contains(out.String(), "1 to push to dave/site main") || !strings.Contains(out.String(), "modified site/a.md") {
		t.Fatalf("pending: %v %q", err, out)
	}

	if err := githubPush(ctx, site, " ", false, out); err == nil || !strings.Contains(err.Error(), "-m") {
		t.Fatalf("push without a message: %v", err)
	}
	out.Reset()
	if err := githubPush(ctx, site, "Fix a typo", false, out); err != nil || !strings.Contains(got[len(got)-1], `"message":"Fix a typo"`) || !strings.Contains(out.String(), "Pushed to dave/site main as fedcba9: 0 added, 1 changed, 0 deleted.") {
		t.Fatalf("push: %v %q %q", err, got, out)
	}
	out.Reset()
	if err := githubPush(ctx, site, "refuse", true, out); errorCode(err) != "forbidden" || !strings.Contains(out.String(), `"code": "forbidden"`) {
		t.Fatalf("refused push: %v %q", err, out)
	}

	// The registered folder itself is docs/ in the space; outside a
	// registered folder, or the root of a whole space, is refused.
	if _, folder, err := githubFolder(dir); err != nil || folder != "docs/" {
		t.Fatalf("registered folder: %q %v", folder, err)
	}
	if err := githubPull(ctx, t.TempDir(), false, out); err == nil {
		t.Fatal("pull outside a registered folder")
	}
	whole := t.TempDir()
	if err := saveRegistry(registry{Folders: []folderEntry{{Space: "dave/work", Folder: whole, Server: server.URL}}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := githubFolder(whole); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("space root: %v", err)
	}
	if _, folder, err := githubFolder(filepath.Join(whole, "site")); err != nil || folder != "site/" {
		t.Fatalf("folder in a whole space: %q %v", folder, err)
	}
}
