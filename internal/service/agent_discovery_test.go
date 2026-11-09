package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func discoveryJob(space int, id int) agentJob {
	return agentJob{ID: fmt.Sprintf("%032x", id), SpaceID: fmt.Sprintf("%032x", space),
		Status: jobQueued, QueuedAt: time.Unix(int64(id), 0)}
}

func discoveryRunner(t *testing.T, store agentJobStore) (*agentRunner, <-chan agentJob, chan<- struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan agentJob, maxParallelJobs)
	release := make(chan struct{}, maxParallelJobs)
	a := &agentRunner{ctx: ctx, cancel: cancel, store: store, clock: time.Now,
		running: map[string]bool{}, wake: make(chan struct{}, 1)}
	a.runAdmitted = func(j agentJob) {
		entered <- j
		select {
		case <-release:
		case <-ctx.Done():
		}
		j.Status = jobDone
		if err := store.finish(context.Background(), j); err != nil {
			t.Errorf("finish: %v", err)
		}
	}
	t.Cleanup(a.stop)
	return a, entered, release
}

func nextDiscovered(t *testing.T, entered <-chan agentJob) agentJob {
	t.Helper()
	select {
	case j := <-entered:
		return j
	case <-time.After(3 * time.Second):
		t.Fatal("eligible job did not enter execution")
		return agentJob{}
	}
}

func drainDiscovered(t *testing.T, a *agentRunner) {
	t.Helper()
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("discovered jobs failed to drain")
	}
}

func TestAgentDiscoveryBusyBacklog(t *testing.T) {
	for _, busy := range []bool{true, false} {
		t.Run(fmt.Sprintf("busy=%v", busy), func(t *testing.T) {
			store := &memoryJobStore{jobs: map[string]agentJob{}}
			for id := 1; id <= 150; id++ {
				j := discoveryJob(1, id)
				store.jobs[j.ID] = j
			}
			other := discoveryJob(2, 151)
			store.jobs[other.ID] = other
			a, entered, _ := discoveryRunner(t, store)
			if busy {
				a.running[discoveryJob(1, 1).SpaceID] = true
			}
			a.startReady()
			count := 2
			if busy {
				count = 1
			}
			seen := map[string]bool{}
			for i := 0; i < count; i++ {
				j := nextDiscovered(t, entered)
				if seen[j.SpaceID] || j.SpaceID == discoveryJob(1, 1).SpaceID && j.ID != discoveryJob(1, 1).ID {
					t.Fatalf("overlap or out-of-order job: %+v", j)
				}
				seen[j.SpaceID] = true
			}
			if !seen[other.SpaceID] {
				t.Fatal("backlog hid the independent job")
			}
			// Neither execution has been released: admission must stay bounded.
			a.startReady()
			select {
			case j := <-entered:
				t.Fatalf("extra execution while both action slots occupied: %+v", j)
			default:
			}
		})
	}
}

func TestAgentDiscoveryCursorAndWrap(t *testing.T) {
	store := &memoryJobStore{jobs: map[string]agentJob{}}
	for i := 1; i <= 24; i++ {
		j := discoveryJob(i, i)
		store.jobs[j.ID] = j
	}
	a, entered, release := discoveryRunner(t, store)
	seen := map[string]bool{}
	for i := 0; i < 12; i++ {
		a.startReady()
		for n := 0; n < 2; n++ {
			j := nextDiscovered(t, entered)
			if seen[j.SpaceID] {
				t.Fatal("space repeated before all candidates were inspected")
			}
			seen[j.SpaceID] = true
		}
		release <- struct{}{}
		release <- struct{}{}
		drainDiscovered(t, a)
	}
	// A new job before the cursor must be reached without its former row existing.
	j := discoveryJob(1, 1000)
	store.add(context.Background(), j)
	a.startReady()
	if got := nextDiscovered(t, entered); got.ID != j.ID {
		t.Fatalf("wrap: got %s want %s", got.ID, j.ID)
	}
}

type discoveryRaceStore struct {
	*memoryJobStore
	queries    atomic.Int32
	queryErr   error
	claimErr   error
	loseBefore string
}

func (s *discoveryRaceStore) queued(ctx context.Context, after string, excluded []string, limit int) ([]agentJob, error) {
	s.queries.Add(1)
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	return s.memoryJobStore.queued(ctx, after, excluded, limit)
}

func (s *discoveryRaceStore) claim(ctx context.Context, id string, at time.Time) (bool, error) {
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if id < s.loseBefore {
		return false, nil
	}
	return s.memoryJobStore.claim(ctx, id, at)
}

func TestAgentDiscoveryBoundedClaimRaces(t *testing.T) {
	store := &discoveryRaceStore{memoryJobStore: &memoryJobStore{jobs: map[string]agentJob{}},
		loseBefore: discoveryJob(101, 101).ID}
	for i := 1; i <= 101; i++ {
		j := discoveryJob(i, i)
		store.jobs[j.ID] = j
	}
	a, entered, _ := discoveryRunner(t, store)
	for pass := 0; pass < 7; pass++ {
		before := store.queries.Load()
		more := a.startReady()
		if store.queries.Load()-before > jobDiscoveryPages {
			t.Fatal("unbounded discovery pass")
		}
		if pass < 6 && !more {
			t.Fatal("truncated discovery did not request a delayed continuation")
		}
	}
	if got := nextDiscovered(t, entered); got.ID != store.loseBefore {
		t.Fatalf("claim loser executed or cursor failed: %+v", got)
	}
}

func TestAgentDiscoveryErrorsAndCancellation(t *testing.T) {
	for _, claim := range []bool{false, true} {
		store := &discoveryRaceStore{memoryJobStore: &memoryJobStore{jobs: map[string]agentJob{}}}
		j := discoveryJob(1, 1)
		store.jobs[j.ID] = j
		if claim {
			store.claimErr = errors.New("claim unavailable")
		} else {
			store.queryErr = errors.New("query unavailable")
		}
		a, entered, _ := discoveryRunner(t, store)
		a.startReady()
		if len(a.running) != 0 || len(entered) != 0 {
			t.Fatal("failure fabricated an admitted job")
		}
		store.claimErr, store.queryErr = nil, nil
		a.startReady()
		if got := nextDiscovered(t, entered); got.ID != j.ID {
			t.Fatal("failed discovery did not recover")
		}
		a.cancel()
		drainDiscovered(t, a)
		before := store.queries.Load()
		a.startReady()
		if store.queries.Load() != before {
			t.Fatal("shutdown issued more discovery work")
		}
	}
}
