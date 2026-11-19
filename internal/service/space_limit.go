package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// Space storage limits (0.99.3). A space has no size of its own by default:
// it takes what it needs, up to the server's per-space ceilings in
// spaces.json (file count, single-file and repository size, which protect
// the server's disk) and, on a hosted service, the owner's account-wide
// allowance (an extension's WriteGate). Its owner may also cap it, from
// Settings → Spaces → (space) → General: a change that adds to a space at or
// over its limit is refused, while one that only removes files still goes
// through. The limit counts what the space takes up, as Settings shows it:
// its Git repository with every version, plus its virtual files.
//
// The limit is the column metatrash_spaces.storage_limit_bytes (NULL = none),
// added by deploy/account-space-storage-limit.sql, an additive change any
// earlier executable ignores. Without it the server runs as before: no space
// has a limit and Settings doesn't offer one.

// The smallest and largest limit an owner may set.
const (
	minSpaceLimit = 10 << 20 // 10 MB
	maxSpaceLimit = 1 << 50  // 1 PB, far past anything a server holds
)

// spaceLimitsReady reports whether the account database has the limit
// column. Asked once; a failed look counts as no.
func (s *Service) spaceLimitsReady(ctx context.Context) bool {
	if s.ownedDB == nil {
		return false
	}
	s.spaceLimitOnce.Do(func() {
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var n int
		err := s.ownedDB.db.QueryRowContext(lookup, "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'metatrash_spaces' AND column_name = 'storage_limit_bytes'").Scan(&n)
		s.spaceLimitColumn = err == nil && n == 1
		if !s.spaceLimitColumn {
			log.Printf("storage: space storage limits are off until deploy/account-space-storage-limit.sql is applied")
		}
	})
	return s.spaceLimitColumn
}

// loadSpaceLimit reads an owned space's limit into its repository.
func (s *Service) loadSpaceLimit(ctx context.Context, r *repository) {
	if r == nil || !r.owned || !s.spaceLimitsReady(ctx) {
		return
	}
	var limit sql.NullInt64
	if err := s.ownedDB.db.QueryRowContext(ctx, "SELECT storage_limit_bytes FROM metatrash_spaces WHERE space_id = ?", r.id).Scan(&limit); err != nil {
		log.Printf("space %s: storage limit not read: %v", r.id, err)
		return
	}
	r.storageLimit.Store(max(limit.Int64, 0))
}

// setSpaceStorageLimit saves an owner's limit for a space (0 for none) and
// applies it at once.
func (s *Service) setSpaceStorageLimit(ctx context.Context, ownerID, spaceID string, limit int64) error {
	if !s.spaceLimitsReady(ctx) {
		return problem(503, "unavailable", "Space storage limits aren't set up on this server yet.")
	}
	if !idPattern.MatchString(ownerID) || !idPattern.MatchString(spaceID) {
		return missing()
	}
	if limit != 0 && (limit < minSpaceLimit || limit > maxSpaceLimit) {
		return invalid("A space's limit is at least 10 MB. Leave it empty for no limit of its own.")
	}
	value := sql.NullInt64{Int64: limit, Valid: limit > 0}
	result, err := s.ownedDB.db.ExecContext(ctx, "UPDATE metatrash_spaces SET storage_limit_bytes = ? WHERE space_id = ? AND owner_user_id = ?", value, spaceID, ownerID)
	if err != nil {
		return fmt.Errorf("cannot update the space's storage limit: %w", err)
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		var found string
		if err := s.ownedDB.db.QueryRowContext(ctx, "SELECT space_id FROM metatrash_spaces WHERE space_id = ? AND owner_user_id = ?", spaceID, ownerID).Scan(&found); errors.Is(err, sql.ErrNoRows) {
			return missing()
		}
	}
	if r := s.ownedRepository(spaceID); r != nil {
		r.storageLimit.Store(limit)
	}
	return nil
}

// spaceStorageLimit is a space's limit in bytes, 0 for none.
func (s *Service) spaceStorageLimit(spaceID string) int64 {
	if r := s.ownedRepository(spaceID); r != nil {
		return r.storageLimit.Load()
	}
	return 0
}

// checkSpaceLimit refuses a change that adds to a space at or over its
// owner's limit. Called with the repository's quota lock held.
func checkSpaceLimit(r *repository, bytes int64, onlyRemoves bool) error {
	limit := r.storageLimit.Load()
	if limit <= 0 || onlyRemoves {
		return nil
	}
	used := r.usage.snapshot().Total()
	if used+bytes <= limit {
		return nil
	}
	return problem(507, "space_limit", fmt.Sprintf(
		"This space is limited to %s by its owner and holds %s, so the change doesn't fit. Its owner can raise or remove the limit in Settings → Spaces → (this space) → General. Deleting files frees nothing, as every version is kept.",
		sizeText(limit), sizeText(used)))
}

// sizeText is a size for people: 1.5 GB, 820 MB.
func sizeText(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return humanBytes(n)
}
