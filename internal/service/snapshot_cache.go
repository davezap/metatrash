package service

import (
	"context"
	"sync"
)

// Per repository, retain at most four revisions and 8 MiB of estimated decoded
// storage. Oversize indexes are served but not retained. HEAD and authorization
// are never cached here; only immutable revision contents and derived flags are.
const snapshotCacheBytes int64 = 8 << 20
const snapshotCacheEntries = 4

type snapshotEntry struct {
	revision string
	files    map[string]record
	bytes    int64
}

type snapshotFlight struct {
	done  chan struct{}
	files map[string]record
	err   error
}

type snapshotCache struct {
	mu      sync.Mutex
	entries []snapshotEntry // most recently used first
	bytes   int64
	pending map[string]*snapshotFlight
}

func (c *snapshotCache) load(ctx context.Context, revision string, load func() (map[string]record, int64, error)) (map[string]record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	for i, entry := range c.entries {
		if entry.revision == revision {
			copy(c.entries[1:i+1], c.entries[:i])
			c.entries[0] = entry
			c.mu.Unlock()
			return cloneSnapshot(entry.files), nil
		}
	}
	if flight := c.pending[revision]; flight != nil {
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-flight.done:
			return cloneSnapshot(flight.files), flight.err
		}
	}
	flight := &snapshotFlight{done: make(chan struct{})}
	if c.pending == nil {
		c.pending = make(map[string]*snapshotFlight)
	}
	c.pending[revision] = flight
	c.mu.Unlock()

	// The initiating request owns the Git operation and its cancellation. A
	// canceled/failed load is shared with waiters, never retained; callers retry
	// normally. Waiters can cancel independently without blocking on this load.
	files, size, err := load()
	c.mu.Lock()
	if err == nil && size <= snapshotCacheBytes {
		for len(c.entries) > 0 && (len(c.entries) >= snapshotCacheEntries || c.bytes+size > snapshotCacheBytes) {
			last := len(c.entries) - 1
			c.bytes -= c.entries[last].bytes
			c.entries[last] = snapshotEntry{}
			c.entries = c.entries[:last]
		}
		c.entries = append([]snapshotEntry{{revision, files, size}}, c.entries...)
		c.bytes += size
	}
	flight.files, flight.err = files, err
	delete(c.pending, revision)
	close(flight.done)
	c.mu.Unlock()
	return cloneSnapshot(files), err
}

func cloneSnapshot(files map[string]record) map[string]record {
	if files == nil {
		return nil
	}
	out := make(map[string]record, len(files))
	for path, f := range files {
		if f.Virtual != nil {
			v := *f.Virtual
			f.Virtual = &v
		}
		if f.Stale != nil {
			stale := *f.Stale
			f.Stale = &stale
		}
		out[path] = f
	}
	return out
}

func snapshotCost(files map[string]record, encoded int) int64 {
	// Include encoded bytes conservatively as well as map/record/pointer
	// overhead and all retained strings. This is an eviction budget, not RSS.
	size := int64(encoded)
	for path, f := range files {
		size += 512 + int64(len(path)+len(f.ID)+len(f.Path)+len(f.Blob)+len(f.File.Blob)+len(f.ReadOnly))
		if v := f.Virtual; v != nil {
			size += int64(len(v.SHA256) + len(v.Type) + len(v.TextSHA256) + len(v.Source) + len(v.Refresh) + len(v.GitBlob))
		}
		if s := f.Stale; s != nil {
			size += int64(len(s.Since) + len(s.Reason))
		}
	}
	return size
}
