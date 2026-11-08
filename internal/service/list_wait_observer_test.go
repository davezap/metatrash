package service

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash/extension"
)

type waitRecorder struct {
	mu                           sync.Mutex
	started, finished, saturated int
	outcome                      extension.ListWaitOutcome
	elapsed                      time.Duration
}

func (w *waitRecorder) ListWaitStarted()   { w.mu.Lock(); w.started++; w.mu.Unlock() }
func (w *waitRecorder) ListWaitSaturated() { w.mu.Lock(); w.saturated++; w.mu.Unlock() }
func (w *waitRecorder) ListWaitFinished(outcome extension.ListWaitOutcome, elapsed time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.finished++
	w.outcome = outcome
	w.elapsed = elapsed
}

func TestListWaitObserverLifecycle(t *testing.T) {
	s := openPublicTestService(t)
	r := s.repos["public"]
	head, err := r.head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"timeout", "changed", "stopped", "failed", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			observer := &waitRecorder{}
			waiter := &Service{stop: make(chan struct{}), listWait: time.Millisecond}
			if err := waiter.SetListWaitObserver(observer); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			repo, after, want := r, head, extension.ListWaitTimeout
			switch name {
			case "changed":
				after = strings.Repeat("0", 40)
				want = extension.ListWaitChanged
			case "stopped":
				waiter.listWait = time.Hour
				close(waiter.stop)
				want = extension.ListWaitStopped
			case "failed":
				repo = &repository{path: filepath.Join(t.TempDir(), "missing.git")}
				want = extension.ListWaitFailed
			case "cancelled":
				cancel()
				want = extension.ListWaitCancelled
			}
			waiter.waitForChange(ctx, repo, after)
			if observer.started != 1 || observer.finished != 1 || observer.saturated != 0 || observer.outcome != want || observer.elapsed <= 0 || waiter.listWaiters.Load() != 0 {
				t.Fatalf("unbalanced/wrong lifecycle: %+v", observer)
			}
		})
	}
	observer := &waitRecorder{}
	waiter := &Service{stop: make(chan struct{}), listWaitObserver: observer}
	waiter.config.Global.MaxListWaiters = 1
	waiter.listWaiters.Store(1)
	if retry := waiter.waitForChange(context.Background(), r, head); retry != 5 || observer.started != 0 || observer.finished != 0 || observer.saturated != 1 || waiter.listWaiters.Load() != 1 {
		t.Fatal("saturation changed lifecycle/slot balance")
	}
	waiter.configurationFrozen = true
	if waiter.SetListWaitObserver(nil) == nil {
		t.Fatal("observer can change after serving")
	}
}
