package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/extension"
)

type recordingWrites struct {
	extension.WriteScheduler
	mu     sync.Mutex
	keys   [][2]string
	closed int
}

func (r *recordingWrites) Submit(ctx context.Context, space, account string, fn func() (any, error)) (any, error) {
	r.mu.Lock()
	r.keys = append(r.keys, [2]string{space, account})
	r.mu.Unlock()
	return r.WriteScheduler.Submit(ctx, space, account, fn)
}
func (r *recordingWrites) Close() {
	r.WriteScheduler.Close()
	r.mu.Lock()
	r.closed++
	r.mu.Unlock()
}

func TestWriteSchedulerCompositionContract(t *testing.T) {
	dir := t.TempDir()
	var recorder *recordingWrites
	ctx := context.Background()
	s, err := OpenWithWriteScheduler(ctx, "../../config/spaces.example.json", "", filepath.Join(dir, "data"), metatrash.SpaceAGENTS, func(budget int) (extension.WriteScheduler, error) {
		if budget != 32 {
			t.Fatalf("wrong pending budget %d", budget)
		}
		recorder = &recordingWrites{WriteScheduler: newSerialWrites(budget)}
		return recorder, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	text := "through the hook\n"
	if _, err := s.Dispatch(ctx, "public", "append", Input{Path: "hook.txt", Text: &text}); err != nil {
		t.Fatal(err)
	}
	r := &repository{path: filepath.Join(dir, "owned.git"), owned: true, owner: "alice", id: strings.Repeat("a", 32)}
	s.ownedMu.Lock()
	s.ownedRepos[r.id] = r
	s.ownedMu.Unlock()
	noop := func() (any, error) { return nil, nil }
	if _, err := s.queued(ctx, r, noop); err != nil {
		t.Fatal(err)
	}
	s.setRepositoryOwner(r.id, "bob")
	if _, err := s.queued(ctx, r, noop); err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	keys := append([][2]string(nil), recorder.keys...)
	recorder.mu.Unlock()
	if len(keys) != 3 || keys[0] != [2]string{s.repos["public"].path, "configured:" + s.repos["public"].path} || keys[1] != [2]string{r.path, "owner:alice"} || keys[2] != [2]string{r.path, "owner:bob"} {
		t.Fatalf("unstable keys/owner grouping: %v", keys)
	}
}

type fullWrites struct{ closed int }

func (*fullWrites) Submit(context.Context, string, string, func() (any, error)) (any, error) {
	return nil, extension.ErrWriteQueueFull
}
func (*fullWrites) WaitSpace(context.Context, string) error { return nil }
func (q *fullWrites) Close()                                { q.closed++ }

func TestWriteSchedulerFactoryFailureAndSaturation(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	q := &fullWrites{}
	factoryError := errors.New("factory failed")
	for _, kind := range []string{"failure", "nil"} {
		called := false
		_, err := OpenWithWriteScheduler(ctx, "../../config/spaces.example.json", "", filepath.Join(dir, "data"), metatrash.SpaceAGENTS, func(int) (extension.WriteScheduler, error) {
			called = true
			if kind == "nil" {
				return nil, nil
			}
			return q, factoryError
		})
		if !called {
			t.Fatalf("%s factory was not reached; startup failed earlier: %v", kind, err)
		}
		if kind == "failure" && !errors.Is(err, factoryError) {
			t.Fatalf("wrong factory error: %v", err)
		}
		if kind == "nil" && (err == nil || !strings.Contains(err.Error(), "returned no scheduler")) {
			t.Fatalf("wrong nil-factory error: %v", err)
		}
	}
	if q.closed != 1 {
		t.Fatalf("partly created scheduler Close called %d times, want 1", q.closed)
	}
	// Reopening the same directory proves failed construction released its lock.
	saturated := &fullWrites{}
	s, err := OpenWithWriteScheduler(ctx, "../../config/spaces.example.json", "", filepath.Join(dir, "data"), metatrash.SpaceAGENTS, func(int) (extension.WriteScheduler, error) { return saturated, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	text := "x"
	_, err = s.Dispatch(ctx, "public", "append", Input{Path: "not-written.txt", Text: &text})
	var problem *Error
	if !errors.As(err, &problem) || problem.Status != 503 || problem.Code != "queue_full" || problem.RetryAfterSeconds != 1 {
		t.Fatalf("saturation mapping: %v", err)
	}
}

func TestWriteSchedulerClosesOnProvisionFailure(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.MkdirAll(filepath.Join(data, "repos"), 0700); err != nil {
		t.Fatal(err)
	}
	// A file occupying the repository path fails deterministically before Git,
	// exercising cleanup after successful scheduler construction on every OS.
	if err := os.WriteFile(filepath.Join(data, "repos", "public.git"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	q := &fullWrites{}
	called := false
	_, err := OpenWithWriteScheduler(context.Background(), "../../config/spaces.example.json", "", data, metatrash.SpaceAGENTS, func(int) (extension.WriteScheduler, error) {
		called = true
		return q, nil
	})
	if !called || err == nil || !strings.Contains(err.Error(), "invalid repository directory") {
		t.Fatalf("wrong startup path: factory called=%v, error=%v", called, err)
	}
	if q.closed != 1 {
		t.Fatalf("scheduler Close called %d times after provisioning failure, want 1", q.closed)
	}
	if _, err := os.Stat(filepath.Join(data, ".service-lock")); !os.IsNotExist(err) {
		t.Fatalf("failed construction kept its data-directory lock: %v", err)
	}
}

func TestSerialWriteDefaultAndRetirement(t *testing.T) {
	s := openPublicTestService(t)
	q, ok := s.writes.(*serialWrites)
	if !ok {
		t.Fatal("Core default is not serial")
	}
	r := s.repos["public"]
	gate, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	active := make(chan error, 1)
	go func() {
		_, err := s.queued(context.Background(), r, func() (any, error) { close(entered); <-gate; return nil, nil })
		active <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("active callback did not enter")
	}
	// A different key still waits on Core's single global worker.
	pending := make(chan error, 1)
	go func() {
		_, err := q.Submit(context.Background(), "other-space", "other-owner", func() (any, error) { return nil, nil })
		pending <- err
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(q.jobs) != 1 {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("pending callback not admitted")
		}
	}
	select {
	case <-pending:
		t.Fatal("Core ran independent spaces concurrently")
	default:
	}
	r.closed.Store(true)
	if _, err := s.queued(context.Background(), r, func() (any, error) { t.Error("retired callback executed"); return nil, nil }); err == nil {
		t.Fatal("accepted retired handle")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.WaitSpace(ctx, r.path); !errors.Is(err, context.Canceled) {
		t.Fatalf("active write not joined: %v", err)
	}
	closing := make(chan struct{})
	go func() { q.Close(); close(closing) }()
	select {
	case err := <-pending:
		if !errors.Is(err, extension.ErrWriteSchedulerClosed) {
			t.Fatalf("pending close: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pending write not rejected")
	}
	select {
	case <-closing:
		t.Fatal("close did not wait for executing callback")
	default:
	}
	release()
	select {
	case err := <-active:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("active write did not finish")
	}
	select {
	case <-closing:
	case <-time.After(10 * time.Second):
		t.Fatal("serial queue did not close")
	}
}

func TestCoreRejectsCompanySchedulingSettings(t *testing.T) {
	data, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"maxQueuedWrites": 32`, `"maxQueuedWrites": 32, "maxConcurrentWrites": 4`, 1))
	path := filepath.Join(t.TempDir(), "spaces.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(path, ""); err == nil {
		t.Fatal("Core accepted company scheduling policy")
	}
}

func TestWriteSchedulerRetiredPendingWrite(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	q := s.writes.(*serialWrites)
	gate, entered := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	active := make(chan error, 1)
	go func() {
		_, err := s.queued(context.Background(), r, func() (any, error) { close(entered); <-gate; return nil, nil })
		active <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("active callback did not enter")
	}
	pending := make(chan error, 1)
	go func() {
		_, err := s.queued(context.Background(), r, func() (any, error) { t.Error("pending callback mutated retired space"); return nil, nil })
		pending <- err
	}()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for len(q.jobs) != 1 {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("pending write not admitted")
		}
	}
	const id = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	s.ownedMu.Lock()
	s.ownedRepos[id] = r
	s.ownedMu.Unlock()
	// Retire before joining so a stale accepted callback cannot run after the
	// active callback completes, even if it has retained the old handle.
	r.closed.Store(true)
	closed := make(chan struct{})
	go func() { s.closeOwnedSpace(id); close(closed) }()
	release()
	select {
	case err := <-active:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("active callback stalled")
	}
	select {
	case err := <-pending:
		var problem *Error
		if !errors.As(err, &problem) || problem.Status != 404 {
			t.Fatalf("pending retirement: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pending callback stalled")
	}
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("repository close stalled")
	}
	if s.ownedRepository(id) != nil {
		t.Fatal("closed handle retained after join")
	}
}
