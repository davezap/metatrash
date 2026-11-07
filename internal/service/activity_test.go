package service

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Activity (0.95.9): conversions, pairing results, agent jobs and presence.
func TestActivity(t *testing.T) {
	w, _, _, _ := webWorld(t)
	ctx := context.Background()
	q := newDoclingQueue(w.s, &doclingSettings{command: "/opt/x", version: "metatrash-docling 3 docling test", ident: "abcdef012345", ocr: "auto", timeout: time.Minute, maxPages: 10, workers: 1, threads: 1, maxBytes: 1 << 20, pair: true})
	release := make(chan error)
	started := make(chan string, 10)
	q.run = func(ctx context.Context, name string) (string, error) {
		started <- name
		if err := <-release; err != nil {
			return "", err
		}
		return "# Minutes\n", nil
	}
	paired := make(chan int, 10)
	q.paired = func(n int, err error) { paired <- n }
	w.runner.docling = q
	qctx, cancel := context.WithCancel(ctx)
	defer func() { cancel(); q.wait() }()
	q.start(qctx)

	activity := func(viewer *presenceEntry) map[string]ActivityItem {
		t.Helper()
		res, err := w.s.activity(ctx, w.repo, w.repo.id, viewer)
		if err != nil {
			t.Fatal(err)
		}
		if res.Docling != "ready" {
			t.Fatalf("docling %q", res.Docling)
		}
		out := map[string]ActivityItem{}
		for _, it := range res.Items {
			out[it.Kind+":"+it.Path+it.Who] = it
		}
		return out
	}
	put := func(path, body string) {
		t.Helper()
		head, _ := w.repo.head(ctx)
		if _, err := w.s.putVirtual(ctx, w.repo, w.repo.id, path, head, []byte(body), "", "", ""); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(c chan string) {
		t.Helper()
		select {
		case <-c:
		case <-time.After(10 * time.Second):
			t.Fatal("conversion did not start")
		}
	}

	// One converting, one queued behind it.
	put("a/one.pdf", "%PDF-1.4 one")
	wait(started)
	put("a/two.pdf", "%PDF-1.4 two")
	got := activity(nil)
	if it := got["conversion:a/one.pdf"]; it.State != "converting" || it.Since == "" {
		t.Fatalf("one: %+v", got)
	}
	if it := got["conversion:a/two.pdf"]; it.State != "queued" || it.Ahead != 1 {
		t.Fatalf("two: %+v", it)
	}

	// one converts and pairs: its page shows converted, from the original.
	release <- nil
	if n := <-paired; n != 1 {
		t.Fatalf("paired %d", n)
	}
	wait(started)
	got = activity(nil)
	if it := got["conversion:a/one.md"]; it.State != "converted" || it.From != "a/assets/one.pdf" || !strings.Contains(it.Message, "one.pdf") {
		t.Fatalf("one.md: %+v", got)
	}
	if it := got["conversion:a/two.pdf"]; it.State != "converting" {
		t.Fatalf("two: %+v", it)
	}

	// two fails: listed as failed, with the reason.
	release <- errors.New("no pages")
	deadline := time.Now().Add(5 * time.Second)
	for activity(nil)["conversion:a/two.pdf"].State != "failed" {
		if time.Now().After(deadline) {
			t.Fatalf("two: %+v", activity(nil))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if it := activity(nil)["conversion:a/two.pdf"]; !strings.Contains(it.Message, "no pages") || it.RetryAt == "" {
		t.Fatalf("two failed: %+v", it)
	}

	// A busy converter is retrying, not failed.
	put("b/three.pdf", "%PDF-1.4 three")
	wait(started)
	release <- doclingRetrySoon{errors.New("converter not reachable")}
	deadline = time.Now().Add(5 * time.Second)
	for activity(nil)["conversion:b/three.pdf"].State != "retrying" {
		if time.Now().After(deadline) {
			t.Fatalf("three: %+v", activity(nil))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Kept: the page exists already.
	page := "mine\n"
	w.do(ctx, "write", Input{Path: "c/four.md", Text: &page}, "")
	put("c/four.pdf", "%PDF-1.4 four")
	wait(started)
	release <- nil
	if n := <-paired; n != 0 {
		t.Fatalf("four paired %d", n)
	}
	if it := activity(nil)["conversion:c/four.pdf"]; it.State != "kept" || !strings.Contains(it.Message, "four.md already exists") {
		t.Fatalf("four: %+v", it)
	}

	// Kept: too large to convert.
	q.settings.maxBytes = 4
	put("c/big.pdf", "%PDF-1.4 big")
	q.settings.maxBytes = 1 << 20
	if it := activity(nil)["conversion:c/big.pdf"]; it.State != "kept" || !strings.Contains(it.Message, "largest the converter takes") {
		t.Fatalf("big: %+v", it)
	}

	// A deleted file drops out.
	head, _ := w.repo.head(ctx)
	w.do(ctx, "delete", Input{Path: "c/four.pdf", IfInState: head}, "")
	if _, ok := activity(nil)["conversion:c/four.pdf"]; ok {
		t.Fatal("deleted file still listed")
	}

	// Agent jobs: waiting, running and recently finished; old ones not.
	now := time.Now()
	store := w.runner.store
	for _, j := range []agentJob{
		{ID: "j1", SpaceID: w.repo.id, Folder: "news/", Action: "summarise", File: "news/x.md", Status: jobQueued, QueuedAt: now},
		{ID: "j2", SpaceID: w.repo.id, Folder: "news/", Action: "summarise", File: "news/y.md", Status: jobRunning, QueuedAt: now, StartedAt: now},
		{ID: "j3", SpaceID: w.repo.id, Folder: "news/", Action: "summarise", File: "news/z.md", Status: jobFailed, QueuedAt: now, StartedAt: now, FinishedAt: now, Error: "the agent's daily cap is spent"},
		{ID: "j4", SpaceID: w.repo.id, Folder: "news/", Action: "summarise", File: "news/old.md", Status: jobDone, QueuedAt: now.Add(-2 * time.Hour), FinishedAt: now.Add(-time.Hour)},
		{ID: "j5", SpaceID: "other", Folder: "", Action: "x", File: "q.md", Status: jobRunning, QueuedAt: now},
	} {
		if err := store.add(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	got = activity(nil)
	if got["agent:news/x.md"].State != "queued" || got["agent:news/y.md"].State != "running" || !strings.Contains(got["agent:news/z.md"].Message, "daily cap") {
		t.Fatalf("jobs: %+v", got)
	}
	if _, ok := got["agent:news/old.md"]; ok {
		t.Error("old job listed")
	}
	if _, ok := got["agent:q.md"]; ok {
		t.Error("another space's job listed")
	}

	// Presence: the asker is not listed to themselves; others are, with the
	// file they have open.
	activity(&presenceEntry{userID: "u1", name: "alice", path: "a/one.md"})
	got = activity(&presenceEntry{userID: "u2", name: "bob", path: ""})
	if it := got["presence:a/one.mdalice"]; it.State != "viewing" || it.Who != "alice" {
		t.Fatalf("presence: %+v", got)
	}
	if _, ok := got["presence:bob"]; ok {
		t.Error("asker listed")
	}
	if it := activity(nil)["presence:bob"]; it.Who != "bob" || !strings.Contains(it.Message, "space open") {
		t.Fatalf("bob: %+v", it)
	}

	// The HTTP answer: JSON, no-store, only ?viewing= accepted.
	h := &httpAdapter{service: w.s}
	for query, want := range map[string]int{"": 200, "viewing=a%2Fone.md": 200, "viewing=": 200, "x=1": 400, "viewing=a&viewing=b": 400} {
		rec := httptest.NewRecorder()
		h.serveActivity(rec, httptest.NewRequest("GET", "/api/v1/spaces/s/activity?"+query, nil), w.repo, w.repo.id)
		if rec.Code != want {
			t.Errorf("?%s: %d %s", query, rec.Code, rec.Body)
		}
		if want == 200 && (rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"items":[`)) {
			t.Errorf("?%s: %v %s", query, rec.Header(), rec.Body)
		}
	}
}

func TestPresenceBoard(t *testing.T) {
	var b presenceBoard
	b.see("s", presenceEntry{userID: "u1", name: "alice", path: "a.md"})
	first := b.list("s")[0].since
	time.Sleep(5 * time.Millisecond)
	b.see("s", presenceEntry{userID: "u1", name: "alice", path: "a.md"})
	if got := b.list("s"); len(got) != 1 || !got[0].since.Equal(first) {
		t.Fatalf("same file keeps since: %+v", got)
	}
	b.see("s", presenceEntry{userID: "u1", name: "alice", path: "b.md"})
	if got := b.list("s"); got[0].path != "b.md" || got[0].since.Equal(first) {
		t.Fatalf("new file: %+v", got)
	}
	// Gone after presenceFor.
	b.mu.Lock()
	e := b.spaces["s"]["u1"]
	e.seen = time.Now().Add(-2 * presenceFor)
	b.spaces["s"]["u1"] = e
	b.mu.Unlock()
	if got := b.list("s"); len(got) != 0 {
		t.Fatalf("stale presence: %+v", got)
	}
	if got := b.list("none"); len(got) != 0 {
		t.Fatal("empty space")
	}
}
