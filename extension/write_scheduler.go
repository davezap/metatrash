package extension

import (
	"context"
	"errors"
)

var (
	// Core maps saturation to HTTP 503 queue_full with retry advice.
	ErrWriteQueueFull       = errors.New("write queue is full")
	ErrWriteSchedulerClosed = errors.New("write scheduler is closed")
)

// WriteScheduler is a trusted, build-time application composition hook. Core
// defaults to one serial worker. Implementations must bound pending/active work,
// preserve FIFO for accepted callbacks sharing a space key, never overlap them,
// and skip callbacks already cancelled when dispatched. Callbacks must not recursively
// submit and wait. Keys are opaque, stable within this process; they are not
// permission grants. Account keys may change on ownership transfer, space keys
// do not. Core checks repository retirement inside each callback.
//
// Submit is synchronous but may return on context cancellation while an already
// executing callback finishes. Result/error ownership must be preserved. Return
// ErrWriteQueueFull for admission saturation and ErrWriteSchedulerClosed after
// shutdown. Other errors, including context errors, propagate normally.
//
// WaitSpace joins the executing callback for that key without queue admission.
// Core first retires the repository so subsequent callbacks cannot mutate it.
// Close rejects pending/new work and joins every executing callback; it must be
// idempotent. No callback may execute after Close returns.
type WriteScheduler interface {
	Submit(context.Context, string, string, func() (any, error)) (any, error)
	WaitSpace(context.Context, string) error
	Close()
}

// WriteSchedulerFactory receives Core's existing total pending-write limit and
// returns an owned instance before account repositories are loaded. Core closes
// a non-nil instance on startup failure or shutdown, including when the factory
// returns both an instance and an error. A successful factory must return a
// non-nil instance; implementations must not return a typed nil.
type WriteSchedulerFactory func(maxQueuedWrites int) (WriteScheduler, error)
