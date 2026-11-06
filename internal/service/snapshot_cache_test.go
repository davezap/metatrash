package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"metatrash.com/metatrash"
)

func TestSnapshotCacheCopies(t *testing.T) {
	var c snapshotCache
	loads := 0
	load := func() (map[string]record, int64, error) {
		loads++
		return map[string]record{"a.md": {File: File{Path: "a.md", Protected: true, Virtual: &VirtualInfo{Source: "original"}, Stale: &VirtualStale{Reason: "original"}}}}, 1024, nil
	}
	first, err := c.load(context.Background(), "a", load)
	if err != nil {
		t.Fatal(err)
	}
	f := first["a.md"]
	f.Virtual.Source, f.Stale.Reason = "changed", "changed"
	f.Protected = false
	first["a.md"] = f
	delete(first, "a.md")
	second, err := c.load(context.Background(), "a", load)
	if err != nil || loads != 1 || !second["a.md"].Protected || second["a.md"].Virtual.Source != "original" || second["a.md"].Stale.Reason != "original" {
		t.Fatalf("cache leaked caller changes: %#v, loads=%d, err=%v", second, loads, err)
	}
}

func TestSnapshotCacheBoundsAndRetry(t *testing.T) {
	var c snapshotCache
	ctx := context.Background()
	load := func() (map[string]record, int64, error) { return map[string]record{}, snapshotCacheBytes / 2, nil }
	for _, revision := range []string{"a", "b", "a", "c"} {
		if _, err := c.load(ctx, revision, load); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.entries) != 2 || c.entries[0].revision != "c" || c.entries[1].revision != "a" || c.bytes != snapshotCacheBytes {
		t.Fatalf("byte budget/LRU failed: %#v bytes=%d", c.entries, c.bytes)
	}
	oversize := func() (map[string]record, int64, error) { return map[string]record{}, snapshotCacheBytes + 1, nil }
	if _, err := c.load(ctx, "large", oversize); err != nil || len(c.entries) != 2 {
		t.Fatalf("oversize load: %v", err)
	}
	for _, entry := range c.entries {
		if entry.revision == "large" {
			t.Fatal("retained oversized index")
		}
	}
	broken := errors.New("load failed")
	if _, err := c.load(ctx, "retry", func() (map[string]record, int64, error) { return nil, 0, broken }); !errors.Is(err, broken) {
		t.Fatal(err)
	}
	if _, err := c.load(ctx, "retry", load); err != nil {
		t.Fatalf("error was retained: %v", err)
	}
	var small snapshotCache
	for i := 0; i < 10; i++ {
		_, err := small.load(ctx, fmt.Sprint(i), func() (map[string]record, int64, error) { return map[string]record{}, 1, nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(small.entries) != snapshotCacheEntries || small.bytes != snapshotCacheEntries {
		t.Fatalf("entry budget failed: %d/%d", len(small.entries), small.bytes)
	}
}

func TestSnapshotCacheConcurrentAndCancellation(t *testing.T) {
	var c snapshotCache
	var loads atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	load := func() (map[string]record, int64, error) {
		if loads.Add(1) == 1 {
			close(started)
		}
		<-release
		return map[string]record{"a": {File: File{Path: "a"}}}, 1024, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files, err := c.load(context.Background(), "a", load)
			if err != nil || files["a"].Path != "a" {
				t.Errorf("load: %v %v", files, err)
			}
			delete(files, "a")
		}()
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.load(ctx, "a", load); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled waiter: %v", err)
	}
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("concurrent loads=%d", loads.Load())
	}
}

func TestSnapshotCacheHeadAncestryAndReopen(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	ctx := context.Background()
	old, files, err := r.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	text := "new content"
	files["cache.md"] = record{File: File{ID: strings.Repeat("a", 32), Path: "cache.md", Bytes: int64(len(text))}}
	state, err := r.commit(ctx, old, files, "cache.md", files["cache.md"].ID, &text, "create")
	if err != nil {
		t.Fatal(err)
	}
	got, current, err := r.snapshot(ctx, "")
	if err != nil || got != state || current["cache.md"].Path != "cache.md" {
		t.Fatalf("stale HEAD: %s %v", got, err)
	}
	before, after, err := r.snapshotPair(ctx, old, state)
	if err != nil || before["cache.md"].ID != "" || after["cache.md"].ID == "" {
		t.Fatalf("paired revisions: %v", err)
	}
	// A cached revision still has to be reachable from today's HEAD.
	if _, err := git(ctx, r.path, "", nil, "update-ref", branch, old, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.snapshot(ctx, state); err == nil {
		t.Fatal("served cached revision outside current history")
	}
	if _, _, err := r.snapshotPair(ctx, old, state); err == nil {
		t.Fatal("paired snapshots served cached revision outside current history")
	}
	if _, err := git(ctx, r.path, "", nil, "update-ref", branch, state, old); err != nil {
		t.Fatal(err)
	}
	// Warm data avoids both Git show and decoding; a reopened handle is cold.
	b, err := git(ctx, r.path, "", nil, "rev-parse", state+":"+indexPath)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(b))
	if !hashPattern.MatchString(hash) {
		t.Fatal("invalid index hash")
	}
	if err := os.Remove(filepath.Join(r.path, "objects", hash[:2], hash[2:])); err != nil {
		t.Fatal(err)
	}
	delete(current, "cache.md")
	if _, warm, err := r.snapshot(ctx, ""); err != nil || warm["cache.md"].Path != "cache.md" {
		t.Fatalf("warm snapshot reload/alias: %v", err)
	}
	reopened := &repository{path: r.path, limits: r.limits}
	if _, _, err := reopened.snapshot(ctx, ""); err == nil {
		t.Fatal("reopened repository inherited cached data")
	}
}

func TestSnapshotCacheFolderFlags(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	ctx := context.Background()
	old, files, err := r.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if !files[folderConfigName].Protected || files[agentsFileName].ReadOnly != "" {
		t.Fatal("unexpected initial folder flags")
	}
	text := "{\"readOnly\":{\"AGENTS.md\":\"everyone\"}}\n"
	f := files[folderConfigName]
	f.Bytes = int64(len(text))
	files[folderConfigName] = f
	state, err := r.commit(ctx, old, files, folderConfigName, f.ID, &text, "update")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, current, err := r.snapshot(ctx, "")
		if err != nil || !current[folderConfigName].Protected || current[agentsFileName].ReadOnly != "everyone" {
			t.Fatalf("new folder flags (pass %d): %v", i, err)
		}
		_, previous, err := r.snapshot(ctx, old)
		if err != nil || previous[agentsFileName].ReadOnly != "" {
			t.Fatalf("historical flags changed: %v", err)
		}
	}
	if head, err := r.head(ctx); err != nil || head != state {
		t.Fatalf("unexpected HEAD: %s %v", head, err)
	}
}

func BenchmarkSnapshotCache(b *testing.B) {
	for _, count := range []int{10, 900} {
		b.Run(fmt.Sprintf("files_%d", count), func(b *testing.B) {
			ctx := context.Background()
			cfg, _, err := loadConfig("../../config/spaces.example.json", "")
			if err != nil {
				b.Fatal(err)
			}
			r, err := provision(ctx, filepath.Join(b.TempDir(), "bench.git"), cfg.Defaults.Storage, metatrash.SpaceAGENTS, "public", false)
			if err != nil {
				b.Fatal(err)
			}
			state, files, err := r.snapshot(ctx, "")
			if err != nil {
				b.Fatal(err)
			}
			texts := make(map[string]string, count)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("file-%04d.md", i)
				files[path] = record{File: File{ID: fmt.Sprintf("%032x", i+1), Path: path, Bytes: 5}}
				texts[path] = "text\n"
			}
			if _, err := r.commitFiles(ctx, state, files, texts, nil, nil, "seed"); err != nil {
				b.Fatal(err)
			}
			for _, warm := range []bool{false, true} {
				b.Run(fmt.Sprintf("warm_%t", warm), func(b *testing.B) {
					if _, _, err := r.snapshot(ctx, ""); err != nil {
						b.Fatal(err)
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						if !warm {
							r.snapshots = snapshotCache{}
						}
						if _, _, err := r.snapshot(ctx, ""); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
