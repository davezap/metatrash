package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

// Removing the index object in a disposable repo makes an accidental snapshot
// load fail, rather than relying on a timing threshold to prove the fast path.
func TestListUnchangedWithoutIndex(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	ctx := context.Background()
	head, err := r.head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	index, err := git(ctx, r.path, "", nil, "rev-parse", head+":"+indexPath)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.TrimSpace(string(index))
	if !hashPattern.MatchString(hash) {
		t.Fatalf("invalid index hash %q", hash)
	}
	// This fixture tests the no-index fast path, independently of warm caches
	// populated during service startup.
	r.snapshots = snapshotCache{}
	if err := os.Remove(filepath.Join(r.path, "objects", hash[:2], hash[2:])); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.snapshot(ctx, ""); err == nil {
		t.Fatal("fixture must fail when an index is loaded")
	}
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "&prefix=inbox/&blobs=true", "&after=" + head} {
		// Avoid waiting in the combined after/since case by exercising the
		// existing waiter-cap fallback. The normal wait/wake is tested in
		// TestListWaitsForChange.
		s.listWaiters.Store(maxListWaiters)
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/spaces/public/files?since="+head+suffix, nil)
		req.RemoteAddr = "127.0.0.1:1"
		h.ServeHTTP(w, req)
		var out ListResult
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || out.Space != "public" || out.State != head || out.Files == nil || len(out.Files) != 0 || len(out.Removed) != 0 || out.NextCursor != nil {
			t.Fatalf("%s: %d %s", suffix, w.Code, w.Body)
		}
	}
	// An equal state must not bypass query validation.
	for _, suffix := range []string{"&limit=0", "&prefix=../", "&cursor=invalid", "&unknown=true"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/v1/spaces/public/files?since="+head+suffix, nil)
		req.RemoteAddr = "127.0.0.1:1"
		h.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("%s: %d %s", suffix, w.Code, w.Body)
		}
	}
	// Ordinary and after-only listings still require actual file data.
	s.listWaiters.Store(maxListWaiters)
	for _, in := range []Input{{}, {After: head}} {
		if _, err := s.Dispatch(ctx, "public", "list", in); err == nil {
			t.Fatal("ordinary/after-only list unexpectedly skipped the missing index")
		}
	}
}

// Compare the former two-snapshot data path and the new path on the same
// repository, with real Git subprocesses and no network/auth/rate-limit costs.
func BenchmarkListUnchanged(b *testing.B) {
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
			texts := make(map[string]string)
			for i := 0; i < count; i++ {
				path := fmt.Sprintf("file-%04d.md", i)
				text := "# Example\n" + strings.Repeat("A representative line of text.\n", 30)
				files[path] = record{File: File{ID: fmt.Sprintf("%032x", i+1), Path: path, Bytes: int64(len(text))}}
				texts[path] = text
			}
			state, err = r.commitFiles(ctx, state, files, texts, nil, nil, "benchmark seed")
			if err != nil {
				b.Fatal(err)
			}
			s := &Service{}
			in := Input{Since: state}
			b.Run("previous", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					head, snapshot, err := r.snapshot(ctx, "")
					if err != nil {
						b.Fatal(err)
					}
					if _, err := s.listChanges(ctx, r, "public", head, snapshot, in); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("fast", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if _, err := s.page(ctx, r, "public", "list", in); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
