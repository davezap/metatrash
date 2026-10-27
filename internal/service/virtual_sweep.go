package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
)

// The sweep (0.60.1) removes virtual files' content that no pointer in any
// commit of its space names any more, so old revisions always resolve and
// only content nothing can reach goes. It runs once a day in the agent
// runner's housekeeping. Content younger than sweepGrace is kept whatever
// the history says: an upload stores its content before it commits the
// pointer, so a sweep running in between must not take it.
//
// In B2 a delete hides the object; the bucket's lifecycle rule for files/
// deletes hidden objects after a few days (docs/deployment.md), which
// leaves time to recover from a mistake.

const (
	sweepGrace = 24 * time.Hour
	// sweepBatch is how many index revisions are read at once.
	sweepBatch = 200
)

var objectHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// sweepResult is one space's sweep, for the log.
type sweepResult struct {
	Objects, Removed int
	RemovedBytes     int64
}

// sweepVirtual sweeps every loaded account space.
func (s *Service) sweepVirtual(ctx context.Context, now time.Time) {
	store, _ := s.spaceFiles()
	sweeper, ok := store.(objectSweeper)
	if !ok {
		return
	}
	s.ownedMu.RLock()
	repos := make([]*repository, 0, len(s.ownedRepos))
	for _, r := range s.ownedRepos {
		repos = append(repos, r)
	}
	s.ownedMu.RUnlock()
	for _, r := range repos {
		if ctx.Err() != nil {
			return
		}
		result, err := sweepSpace(ctx, r, sweeper, now)
		if err != nil {
			log.Printf("virtual files sweep: space %s: %v", r.id, err)
			continue
		}
		if result.Removed > 0 {
			log.Printf("virtual files sweep: space %s: removed %d of %d objects (%s)", r.id, result.Removed, result.Objects, humanBytes(result.RemovedBytes))
		}
	}
}

// sweepSpace removes the space's unused content. It lists the objects
// before it reads the history, so anything stored after the history was
// read is in neither and is kept by the grace period.
func sweepSpace(ctx context.Context, r *repository, sweeper objectSweeper, now time.Time) (sweepResult, error) {
	var result sweepResult
	if !r.owned || !idPattern.MatchString(r.id) {
		return result, nil
	}
	prefix := "files/" + r.id + "/"
	objects, err := sweeper.list(ctx, prefix)
	if err != nil || len(objects) == 0 {
		return result, err
	}
	result.Objects = len(objects)
	used, err := r.virtualHashes(ctx)
	if err != nil {
		return result, err
	}
	for _, o := range objects {
		hash, ok := strings.CutPrefix(o.Key, prefix)
		if !ok || !objectHashPattern.MatchString(hash) || used[hash] || now.Sub(o.Modified) < sweepGrace {
			continue
		}
		if err := sweeper.remove(ctx, o.Key); err != nil {
			return result, err
		}
		result.Removed++
		result.RemovedBytes += o.Size
	}
	return result, nil
}

// virtualHashes is every content and text hash a pointer names in any
// commit of the space: every revision of the index is read.
func (r *repository) virtualHashes(ctx context.Context) (map[string]bool, error) {
	out, err := git(ctx, r.path, "", nil, "rev-list", "--all", "--objects")
	if err != nil {
		return nil, err
	}
	var indexes []string
	for line := range strings.Lines(string(out)) {
		hash, path, ok := strings.Cut(strings.TrimRight(line, "\n"), " ")
		if ok && path == indexPath {
			indexes = append(indexes, hash)
		}
	}
	used := map[string]bool{}
	for start := 0; start < len(indexes); start += sweepBatch {
		texts, err := r.blobs(ctx, indexes[start:min(start+sweepBatch, len(indexes))])
		if err != nil {
			return nil, err
		}
		for hash, text := range texts {
			if !bytes.Contains(text, []byte(`"virtual"`)) {
				continue
			}
			var index map[string]storedRecord
			if err := json.Unmarshal(text, &index); err != nil {
				// An index that cannot be read might name anything: keep
				// everything rather than guess.
				return nil, fmt.Errorf("index %s unreadable: %v", hash, err)
			}
			for _, f := range index {
				if f.Virtual != nil {
					used[f.Virtual.SHA256] = true
					if f.Virtual.TextSHA256 != "" {
						used[f.Virtual.TextSHA256] = true
					}
				}
			}
		}
	}
	return used, nil
}
