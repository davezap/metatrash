package service

import (
	"context"
	"metatrash.com/metatrash/extension"
	"sync"
)

type serialWriteJob struct {
	ctx    context.Context
	space  string
	run    func() (any, error)
	result chan outcome
}

// serialWrites preserves Core's single-worker default. Active-space tracking
// supports safe retirement without admitting a barrier into a full queue.
// No tenant policy, concurrency settings or scheduling metrics live here.
type serialWrites struct {
	mu          sync.Mutex
	jobs        chan serialWriteJob
	stop        chan struct{}
	done        chan struct{}
	closed      bool
	activeSpace string
	activeDone  chan struct{}
}

func newSerialWrites(maxQueued int) *serialWrites {
	q := &serialWrites{jobs: make(chan serialWriteJob, maxQueued), stop: make(chan struct{}), done: make(chan struct{})}
	go q.worker()
	return q
}

func (q *serialWrites) Submit(ctx context.Context, space, _ string, fn func() (any, error)) (any, error) {
	q.mu.Lock()
	if err := ctx.Err(); err != nil {
		q.mu.Unlock()
		return nil, err
	}
	if q.closed {
		q.mu.Unlock()
		return nil, extension.ErrWriteSchedulerClosed
	}
	j := serialWriteJob{ctx: ctx, space: space, run: fn, result: make(chan outcome, 1)}
	select {
	case q.jobs <- j:
		q.mu.Unlock()
	default:
		q.mu.Unlock()
		return nil, extension.ErrWriteQueueFull
	}
	select {
	case out := <-j.result:
		return out.value, out.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (q *serialWrites) worker() {
	defer close(q.done)
	for {
		select {
		case <-q.stop:
			return
		case j := <-q.jobs:
			q.mu.Lock()
			if q.closed {
				q.mu.Unlock()
				j.result <- outcome{err: extension.ErrWriteSchedulerClosed}
				continue
			}
			if err := j.ctx.Err(); err != nil {
				q.mu.Unlock()
				j.result <- outcome{err: err}
				continue
			}
			q.activeSpace, q.activeDone = j.space, make(chan struct{})
			q.mu.Unlock()
			value, err := j.run()
			q.mu.Lock()
			close(q.activeDone)
			q.activeSpace, q.activeDone = "", nil
			q.mu.Unlock()
			j.result <- outcome{value: value, err: err}
		}
	}
}

func (q *serialWrites) WaitSpace(ctx context.Context, space string) error {
	q.mu.Lock()
	if q.activeDone == nil || q.activeSpace != space {
		q.mu.Unlock()
		return nil
	}
	idle := q.activeDone
	q.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *serialWrites) Close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.stop)
		for {
			select {
			case j := <-q.jobs:
				j.result <- outcome{err: extension.ErrWriteSchedulerClosed}
			default:
				q.mu.Unlock()
				<-q.done
				return
			}
		}
	}
	q.mu.Unlock()
	<-q.done
}
