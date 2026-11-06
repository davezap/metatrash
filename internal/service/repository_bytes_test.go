package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func bytesTestRepository(t *testing.T) *repository {
	t.Helper()
	r, err := provision(t.Context(), filepath.Join(t.TempDir(), "r.git"), Storage{
		MaxFiles: 1000, MaxFileBytes: 1 << 20, MaxCurrentTextBytes: 16 << 20, MaxRepositoryBytes: 64 << 20,
	}, []byte("# Agents\n"), "test", false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func bytesTestCommit(t *testing.T, r *repository, text string, stale string) error {
	t.Helper()
	head, files, err := r.snapshot(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if stale != "" {
		head = stale
	}
	f := files["notes.md"]
	f.ID, f.Path, f.Bytes = "notes", "notes.md", int64(len(text))
	files["notes.md"] = f
	_, err = r.commitFiles(t.Context(), head, files, map[string]string{"notes.md": text}, nil, []commitChange{{"notes", "write", "notes.md"}}, "")
	return err
}

func bytesTestOracle(t *testing.T, r *repository) int64 {
	t.Helper()
	walked, err := dirBytes(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if r.quota.valid && r.quota.bytes < walked {
		t.Fatalf("quota undercount: %d < walked %d", r.quota.bytes, walked)
	}
	return walked
}

func TestRepositoryBytesWarmAndReopen(t *testing.T) {
	r := bytesTestRepository(t)
	// Provision returns a different handle from the temporary repository's.
	if r.quota.valid {
		t.Fatal("new handle inherited unverified accounting")
	}
	r.usage.seed(usageSnapshot{GitBytes: 1, GitAt: time.Now()})
	if err := bytesTestCommit(t, r, "first\n", ""); err != nil {
		t.Fatal(err)
	}
	if !r.quota.fast || !r.quota.valid {
		t.Fatal("plain bare repository missed fast path")
	}
	measured := r.quota.measured
	for _, text := range []string{"second\n", "second\n", "third\n"} {
		if err := bytesTestCommit(t, r, text, ""); err != nil {
			t.Fatal(err)
		}
		walked := bytesTestOracle(t, r)
		if r.quota.bytes != walked || r.usage.snapshot().GitBytes != walked {
			t.Fatal("incremental/reporting count differs from oracle")
		}
		if r.quota.measured != measured {
			t.Fatal("warm commit walked repository")
		}
	}
	// A reopened handle cannot trust an old row or a previous process's state.
	reopened := &repository{path: r.path, limits: r.limits}
	reopened.usage.seed(usageSnapshot{GitBytes: 1, GitAt: time.Now()})
	if err := bytesTestCommit(t, reopened, "reopened\n", ""); err != nil {
		t.Fatal(err)
	}
	if !reopened.quota.valid || reopened.quota.measured.IsZero() {
		t.Fatal("reopen did not measure")
	}
	bytesTestOracle(t, reopened)
	// Configured spaces also reconcile daily without a storage meter.
	reopened.quota.measured = time.Now().Add(-25 * time.Hour)
	if err := bytesTestCommit(t, reopened, "daily\n", ""); err != nil {
		t.Fatal(err)
	}
	if time.Since(reopened.quota.measured) > time.Minute {
		t.Fatal("daily reconciliation missing")
	}
}

func TestRepositoryBytesRefFailureAndRestart(t *testing.T) {
	r := bytesTestRepository(t)
	stale, err := r.head(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := bytesTestCommit(t, r, "advance\n", ""); err != nil {
		t.Fatal(err)
	}
	before := bytesTestOracle(t, r)
	if err := bytesTestCommit(t, r, "unreachable after failed ref\n", stale); err == nil {
		t.Fatal("stale ref accepted")
	}
	if r.quota.valid {
		t.Fatal("ref failure retained authority")
	}
	if bytesTestOracle(t, r) <= before {
		t.Fatal("fixture did not leave promoted objects")
	}
	// Reopen rather than repair this handle: models a crash after promotion.
	reopened := &repository{path: r.path, limits: r.limits}
	if err := bytesTestCommit(t, reopened, "after failure\n", ""); err != nil {
		t.Fatal(err)
	}
	bytesTestOracle(t, reopened)
	if reopened.quota.bytes != bytesTestOracle(t, reopened) {
		t.Fatal("restart lost orphaned bytes")
	}
}

func TestRepositoryBytesPartialPromotion(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			r := bytesTestRepository(t)
			stage := t.TempDir()
			for i := 1; i <= 3; i++ {
				if err := os.WriteFile(filepath.Join(stage, fmt.Sprint(i)), []byte(strings.Repeat("x", i)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r.quota.mu.Lock()
			defer r.quota.mu.Unlock()
			if _, err := r.admitRepositoryBytes(6); err != nil {
				t.Fatal(err)
			}
			moves := 0
			injected := errors.New("injected move failure")
			_, err := r.promoteObjects(stage, func(from, to string) error {
				moves++
				if moves == failAt {
					return injected
				}
				return os.Rename(from, to)
			})
			if !errors.Is(err, injected) || r.quota.valid {
				t.Fatalf("failure did not invalidate: %v", err)
			}
			actual := bytesTestOracle(t, r)
			base, err := r.admitRepositoryBytes(0)
			if err != nil || base != actual {
				t.Fatalf("partial objects not recovered: %d != %d: %v", base, actual, err)
			}
		})
	}
}

func TestRepositoryBytesQuotaAndFailedMeasurement(t *testing.T) {
	r := bytesTestRepository(t)
	r.quota.mu.Lock()
	defer r.quota.mu.Unlock()
	actual, err := r.measureRepositoryBytes()
	if err != nil {
		t.Fatal(err)
	}
	r.limits.MaxRepositoryBytes = actual + 4096 + 10
	r.quota.bytes += 1000 // conservative surplus must get one reconciliation.
	base, err := r.admitRepositoryBytes(10)
	if err != nil || base != actual {
		t.Fatalf("exact boundary refused: %v", err)
	}
	if _, err := r.admitRepositoryBytes(11); err == nil {
		t.Fatal("over quota accepted")
	}
	// Reporting is never changed into a reservation.
	if r.usage.snapshot().GitBytes != 0 {
		t.Fatal("reservation became measured usage")
	}
	if err := os.Rename(r.path, r.path+"-missing"); err != nil {
		t.Fatal(err)
	}
	r.quota.valid = false
	if _, err := r.admitRepositoryBytes(0); err == nil || r.quota.valid {
		t.Fatal("failed walk admitted write")
	}
	if _, err := checkedBytes(math.MaxInt64, 1); err == nil {
		t.Fatal("overflow accepted")
	}
	for _, args := range [][3]int64{{math.MaxInt64, 1, math.MaxInt64}, {1, math.MaxInt64, math.MaxInt64}, {-1, 0, 4096}, {0, -1, 4096}} {
		if quotaFits(args[0], args[1], args[2]) {
			t.Fatal("invalid/overflow quota fits")
		}
	}
}

func TestRepositoryBytesUnsupportedMetadata(t *testing.T) {
	for _, mode := range []string{"reflog", "existing-log", "unknown-config", "symbolic-ref"} {
		t.Run(mode, func(t *testing.T) {
			r := bytesTestRepository(t)
			switch mode {
			case "reflog", "unknown-config":
				key, value := "core.logAllRefUpdates", "true"
				if mode == "unknown-config" {
					key, value = "core.abbrev", "12"
				}
				if _, err := git(t.Context(), r.path, "", nil, "config", key, value); err != nil {
					t.Fatal(err)
				}
			case "existing-log":
				p := filepath.Join(r.path, "logs", "refs", "heads")
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(p, "main"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symbolic-ref":
				head, err := r.head(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := git(t.Context(), r.path, "", nil, "update-ref", "refs/heads/target", head); err != nil {
					t.Fatal(err)
				}
				if _, err := git(t.Context(), r.path, "", nil, "symbolic-ref", branch, "refs/heads/target"); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := bytesTestCommit(t, r, fmt.Sprintf("%s %d\n", mode, i), ""); err != nil {
					t.Fatal(err)
				}
				if r.quota.fast {
					t.Fatal("unsupported metadata enabled fast path")
				}
				if r.quota.bytes != bytesTestOracle(t, r) {
					t.Fatal("fallback count differs")
				}
			}
		})
	}
}

func TestRepositoryBytesIndependentSpaces(t *testing.T) {
	repos := []*repository{bytesTestRepository(t), bytesTestRepository(t)}
	var wg sync.WaitGroup
	for _, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 3; i++ {
				if err := bytesTestCommit(t, r, fmt.Sprint(i), ""); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	for _, r := range repos {
		bytesTestOracle(t, r)
	}
}

func TestRepositoryBytesRejectsSymlinks(t *testing.T) {
	r := bytesTestRepository(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(r.path, "unexpected-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := bytesTestCommit(t, r, "must not install\n", ""); err == nil || r.quota.valid {
		t.Fatal("symlink measurement admitted a write")
	}
}

func TestRepositoryBytesCallbacksOutsideLock(t *testing.T) {
	r := bytesTestRepository(t)
	called := false
	r.committed = func(_ context.Context, _, _ string) {
		if !r.quota.mu.TryLock() {
			t.Fatal("callback holds accounting mutex")
		}
		r.quota.mu.Unlock()
		called = true
	}
	if err := bytesTestCommit(t, r, "notify\n", ""); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("commit callback lost")
	}
}
