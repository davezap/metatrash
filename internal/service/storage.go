package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Measuring storage (0.89.0, schema v23) is phase 1 of the payment plan: it
// changes no limit, it only counts. Each owned space takes up
//
//   - its Git repository on disk (git_bytes): every version of every text
//     file, so history counts and deleting a file frees nothing, and
//   - its objects under files/<space ID>/ in the space-files bucket
//     (file_bytes, file_objects): virtual files' content and text, every
//     version kept until the sweep finds nothing points at it. An object is
//     counted once, however many files or versions point at it.
//
// An account's storage is the sum over the spaces it owns, so a transfer
// moves the space's storage with it. Spaces from spaces.json (public, docs)
// are not counted.
//
// The numbers live on the repository (spaceUsage) and in the table
// metatrash_space_usage, which Settings and the console read. They are kept
// up to date cheaply: commits maintain a separate, verified repository-byte
// counter and measure reference growth after publishing; storing a virtual file
// adds the object when it is new to the space; the sweep takes away what it
// removes. Changed spaces are written to the database a few seconds later
// (storageMeter). Everything is measured again in full on start and then
// daily: the repository walked on disk, and the bucket listed.

// storageMeasureEvery is how often everything is measured in full.
const storageMeasureEvery = 24 * time.Hour

// storageFlushDelay gathers the changes of a burst of writes into one
// database write per space.
const storageFlushDelay = 3 * time.Second

// spaceUsage is what one owned space takes up. Its zero value is "not
// measured yet".
type spaceUsage struct {
	mu          sync.Mutex
	gitBytes    int64
	gitAt       time.Time
	fileBytes   int64
	fileObjects int
	filesAt     time.Time
	// known holds the hashes of the space's objects in the bucket, from the
	// last listing plus what was stored since; nil until the first listing,
	// so an object stored before then is left to that listing.
	known map[string]int64
	// listing is set while the bucket is being listed; recent collects what
	// is stored in the meantime, which the listing may have missed.
	listing bool
	recent  map[string]int64
}

// usageSnapshot is spaceUsage's numbers at one moment.
type usageSnapshot struct {
	GitBytes    int64
	GitAt       time.Time
	FileBytes   int64
	FileObjects int
	FilesAt     time.Time
}

// Total is everything the space takes up.
func (u usageSnapshot) Total() int64 { return u.GitBytes + u.FileBytes }

func (u *spaceUsage) snapshot() usageSnapshot {
	u.mu.Lock()
	defer u.mu.Unlock()
	return usageSnapshot{GitBytes: u.gitBytes, GitAt: u.gitAt, FileBytes: u.fileBytes, FileObjects: u.fileObjects, FilesAt: u.filesAt}
}

// seed fills an unmeasured space from its stored row, so Settings shows the
// last numbers until the space is measured again.
func (u *spaceUsage) seed(row usageSnapshot) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gitAt.IsZero() {
		u.gitBytes, u.gitAt = row.GitBytes, row.GitAt
	}
	if u.filesAt.IsZero() && u.known == nil {
		u.fileBytes, u.fileObjects, u.filesAt = row.FileBytes, row.FileObjects, row.FilesAt
	}
}

func (u *spaceUsage) setGit(bytes int64, at time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gitBytes, u.gitAt = bytes, at
}

// addObject counts an object just stored, if it is new to the space. It
// reports whether the numbers changed.
func (u *spaceUsage) addObject(hash string, size int64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.listing {
		if u.recent == nil {
			u.recent = map[string]int64{}
		}
		u.recent[hash] = size
	}
	if u.known == nil {
		return false
	}
	if _, ok := u.known[hash]; ok {
		return false
	}
	u.known[hash] = size
	u.fileBytes += size
	u.fileObjects++
	return true
}

// removeObject takes away an object the sweep removed.
func (u *spaceUsage) removeObject(hash string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.recent, hash)
	size, ok := u.known[hash]
	if !ok {
		return false
	}
	delete(u.known, hash)
	u.fileBytes -= size
	u.fileObjects--
	return true
}

func (u *spaceUsage) startListing() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.listing, u.recent = true, nil
}

// finishListing replaces the counts with a listing's, keeping what was
// stored while it ran. ok false (the listing failed) keeps the old counts.
func (u *spaceUsage) finishListing(objects map[string]int64, at time.Time, ok bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	recent := u.recent
	u.listing, u.recent = false, nil
	if !ok {
		return
	}
	for hash, size := range recent {
		if _, listed := objects[hash]; !listed {
			objects[hash] = size
		}
	}
	u.known = objects
	u.fileBytes, u.fileObjects = 0, len(objects)
	for _, size := range objects {
		u.fileBytes += size
	}
	u.filesAt = at
}

// storageMeter writes changed spaces' usage to the database and measures
// every space in full on start and daily.
type storageMeter struct {
	mu      sync.Mutex
	dirty   map[string]*repository
	wake    chan struct{}
	wg      sync.WaitGroup
	started bool
	// measured, when set, hears of each full measurement (tests).
	measured func()
}

// storageChanged queues a space's usage for the database.
func (s *Service) storageChanged(r *repository) {
	if r == nil || !r.owned || !idPattern.MatchString(r.id) {
		return
	}
	m := &s.storage
	m.mu.Lock()
	if m.dirty == nil {
		m.dirty = map[string]*repository{}
	}
	m.dirty[r.id] = r
	wake := m.wake
	m.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// noteStoredObject counts a virtual file's object just stored in the
// bucket under files/<space ID>/<hash>.
func (s *Service) noteStoredObject(r *repository, hash string, size int64) {
	if r.usage.addObject(hash, size) {
		s.storageChanged(r)
	}
}

// startStorageMeter seeds the loaded spaces from their stored rows and
// starts the background loop. Called once the owned spaces are loaded.
func (s *Service) startStorageMeter(ctx context.Context) {
	if s.ownedDB == nil {
		return
	}
	if rows, err := s.ownedDB.spaceUsageRows(ctx, ""); err != nil {
		log.Printf("storage: %v", err)
	} else {
		for _, r := range s.ownedRepositories() {
			if row, ok := rows[r.id]; ok {
				r.usage.seed(row.usageSnapshot)
			}
		}
	}
	m := &s.storage
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.wake = make(chan struct{}, 1)
	m.mu.Unlock()
	m.wg.Add(1)
	go s.storageLoop()
}

// stopStorageMeter waits for the loop, which ends when the service stops,
// after writing what changed.
func (s *Service) stopStorageMeter() {
	s.storage.wg.Wait()
}

func (s *Service) ownedRepositories() []*repository {
	s.ownedMu.RLock()
	defer s.ownedMu.RUnlock()
	repos := make([]*repository, 0, len(s.ownedRepos))
	for _, r := range s.ownedRepos {
		repos = append(repos, r)
	}
	return repos
}

func (s *Service) storageLoop() {
	m := &s.storage
	defer m.wg.Done()
	measure := time.NewTimer(0)
	defer measure.Stop()
	for {
		select {
		case <-s.stop:
			s.flushStorage()
			return
		case <-measure.C:
			ctx, cancel := s.backgroundContext(time.Hour)
			s.measureAllStorage(ctx)
			cancel()
			measure.Reset(storageMeasureEvery)
			if m.measured != nil {
				m.measured()
			}
		case <-m.wake:
			select {
			case <-s.stop:
				s.flushStorage()
				return
			case <-time.After(storageFlushDelay):
			}
		}
		s.flushStorage()
	}
}

// flushStorage writes every changed space's usage to the database.
func (s *Service) flushStorage() {
	m := &s.storage
	m.mu.Lock()
	dirty := m.dirty
	m.dirty = nil
	m.mu.Unlock()
	if len(dirty) == 0 || s.ownedDB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for id, r := range dirty {
		u := r.usage.snapshot()
		if u.GitAt.IsZero() {
			continue
		}
		if err := s.ownedDB.saveSpaceUsage(ctx, id, u); err != nil {
			log.Printf("storage: space %s: %v", id, err)
		}
	}
}

// measureAllStorage measures every loaded owned space in full.
func (s *Service) measureAllStorage(ctx context.Context) {
	store, _ := s.spaceFiles()
	sweeper, _ := store.(objectSweeper)
	for _, r := range s.ownedRepositories() {
		if ctx.Err() != nil {
			return
		}
		if err := s.measureSpace(ctx, r, sweeper); err != nil {
			log.Printf("storage: space %s: %v", r.id, err)
		}
	}
}

// measureSpace measures one space: its repository in the write queue, so
// no commit is half done while it is walked, and its bucket objects when
// the server keeps virtual files.
func (s *Service) measureSpace(ctx context.Context, r *repository, sweeper objectSweeper) error {
	if !r.owned || !idPattern.MatchString(r.id) {
		return nil
	}
	_, err := s.queued(ctx, r, func() (any, error) {
		r.quota.mu.Lock()
		defer r.quota.mu.Unlock()
		size, err := r.measureRepositoryBytes()
		if err != nil {
			return nil, err
		}
		r.usage.setGit(size, s.now())
		return nil, nil
	})
	if err != nil {
		return err
	}
	if sweeper != nil {
		prefix := "files/" + r.id + "/"
		r.usage.startListing()
		objects, err := sweeper.list(ctx, prefix)
		found := map[string]int64{}
		for _, o := range objects {
			if hash, ok := strings.CutPrefix(o.Key, prefix); ok && objectHashPattern.MatchString(hash) {
				found[hash] = o.Size
			}
		}
		r.usage.finishListing(found, s.now(), err == nil)
		if err != nil {
			s.storageChanged(r)
			return fmt.Errorf("list files: %v", err)
		}
	}
	s.storageChanged(r)
	return nil
}

// The table metatrash_space_usage.

func (db *accountDatabase) checkStorageSchema(ctx context.Context) error {
	rows, err := db.db.QueryContext(ctx, "SELECT space_id, git_bytes, file_bytes, file_objects, git_measured_at, files_measured_at FROM metatrash_space_usage LIMIT 0")
	if err != nil {
		return fmt.Errorf("account schema v23 required (measuring storage); follow docs/deployment.md")
	}
	rows.Close()
	return nil
}

func (db *accountDatabase) saveSpaceUsage(ctx context.Context, spaceID string, u usageSnapshot) error {
	var filesAt sql.NullInt64
	if !u.FilesAt.IsZero() {
		filesAt = sql.NullInt64{Int64: u.FilesAt.Unix(), Valid: true}
	}
	// INSERT IGNORE then UPDATE: ON DUPLICATE KEY UPDATE would need UPDATE
	// on the key column too, which the login does not have.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "INSERT IGNORE INTO metatrash_space_usage (space_id, git_bytes, file_bytes, file_objects, git_measured_at, files_measured_at) VALUES (?, ?, ?, ?, ?, ?)",
		spaceID, u.GitBytes, u.FileBytes, u.FileObjects, u.GitAt.Unix(), filesAt); err != nil {
		return fmt.Errorf("cannot save storage usage")
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_space_usage SET git_bytes = ?, file_bytes = ?, file_objects = ?, git_measured_at = ?, files_measured_at = ? WHERE space_id = ?",
		u.GitBytes, u.FileBytes, u.FileObjects, u.GitAt.Unix(), filesAt, spaceID); err != nil {
		return fmt.Errorf("cannot save storage usage")
	}
	return nil
}

// spaceUsageRow is a stored row with its space.
type spaceUsageRow struct {
	usageSnapshot
	SpaceID string
	OwnerID string
	State   string
}

// spaceUsageRows reads the stored usage of every space that is not purged,
// or of one owner's spaces.
func (db *accountDatabase) spaceUsageRows(ctx context.Context, ownerID string) (map[string]spaceUsageRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	query := `SELECT u.space_id, s.owner_user_id, s.provisioning_state, u.git_bytes, u.file_bytes, u.file_objects, u.git_measured_at, u.files_measured_at
FROM metatrash_space_usage u JOIN metatrash_spaces s ON s.space_id = u.space_id WHERE s.provisioning_state <> 'purged'`
	var args []any
	if ownerID != "" {
		query += " AND s.owner_user_id = ?"
		args = append(args, ownerID)
	}
	out := map[string]spaceUsageRow{}
	err := eachRow(ctx, db.db, func(scan func(...any) error) error {
		var row spaceUsageRow
		var gitAt int64
		var filesAt sql.NullInt64
		if err := scan(&row.SpaceID, &row.OwnerID, &row.State, &row.GitBytes, &row.FileBytes, &row.FileObjects, &gitAt, &filesAt); err != nil {
			return err
		}
		row.GitAt = time.Unix(gitAt, 0).UTC()
		if filesAt.Valid {
			row.FilesAt = time.Unix(filesAt.Int64, 0).UTC()
		}
		out[row.SpaceID] = row
		return nil
	}, query+" LIMIT 100001", args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read storage usage")
	}
	return out, nil
}

// accountStorage is one account's storage, for Settings: each space's usage
// (the live numbers of loaded spaces, else the stored ones) and the total.
type accountStorage struct {
	Spaces map[string]usageSnapshot
	// Total is the spaces' sum; Deleted what deleted spaces still take up
	// until they are purged (not in Total).
	Total, Git, Files, Deleted int64
	// Oldest is the earliest measurement in the total.
	Oldest time.Time
}

func (s *Service) accountStorage(ctx context.Context, ownerID string) (accountStorage, error) {
	out := accountStorage{Spaces: map[string]usageSnapshot{}}
	rows, err := s.ownedDB.spaceUsageRows(ctx, ownerID)
	if err != nil {
		return out, err
	}
	for id, row := range rows {
		u := row.usageSnapshot
		if row.State == "deleted" {
			out.Deleted += u.Total()
			continue
		}
		if r := s.ownedRepository(id); r != nil {
			if live := r.usage.snapshot(); !live.GitAt.IsZero() {
				u = live
			}
		}
		out.Spaces[id] = u
	}
	// A space measured but not yet written counts too.
	for _, r := range s.ownedRepositories() {
		if _, ok := out.Spaces[r.id]; ok || s.repositoryOwner(r) != ownerID {
			continue
		}
		if live := r.usage.snapshot(); !live.GitAt.IsZero() {
			out.Spaces[r.id] = live
		}
	}
	for _, u := range out.Spaces {
		out.Total += u.Total()
		out.Git += u.GitBytes
		out.Files += u.FileBytes
		if at := u.oldest(); out.Oldest.IsZero() || at.Before(out.Oldest) {
			out.Oldest = at
		}
	}
	return out, nil
}

// oldest is the earlier of the space's two measurements (the Git one when
// the bucket was never listed).
func (u usageSnapshot) oldest() time.Time {
	if !u.FilesAt.IsZero() && u.FilesAt.Before(u.GitAt) {
		return u.FilesAt
	}
	return u.GitAt
}

// siteTime is t in RFC 3339 for the site API, or "" for the zero time.
func siteTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
