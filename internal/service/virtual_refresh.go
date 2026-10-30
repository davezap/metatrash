package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"
)

// Refresh (0.60.2). A virtual file with a source can be fetched again on an
// "every" schedule (the folder actions' schedules, in the space's time
// zone): the pointer's refresh line. configure sets the source and the
// schedule. A refresh asks the site whether the file changed (web_fetch's
// conditional request) and commits a new pointer only for new content, so
// History shows each version and folder actions fire on it; unchanged
// content makes no commit. A failed refresh keeps the content and marks the
// file stale in the database, not in Git, so failures make no commits;
// reads and the file's page say so until a refresh works again.
//
// Refreshes are made for the space owner: they count toward the owner's
// daily web_fetch limit and follow web_fetch's address rules, and they need
// the server's web downloads (agents.downloads).
//
// metatrash_virtual_refresh holds one row per file with a schedule, keyed by
// file ID so a move keeps it. The runner keeps the rows in step with the
// pointers (hourly, and after configure) and refreshes due files.

// virtualRefresh is one row of metatrash_virtual_refresh.
type virtualRefresh struct {
	SpaceID, FileID, Every string
	NextRun, LastRun       time.Time
	// StaleSince is when refreshing started failing (zero while it works);
	// StaleReason says why.
	StaleSince  time.Time
	StaleReason string
}

type refreshStore interface {
	refreshes(ctx context.Context, spaceID string) ([]virtualRefresh, error)
	dueRefreshes(ctx context.Context, now time.Time, limit int) ([]virtualRefresh, error)
	refreshOf(ctx context.Context, spaceID, fileID string) (virtualRefresh, bool, error)
	addRefresh(ctx context.Context, v virtualRefresh) error
	// setRefresh changes every and next_run_at.
	setRefresh(ctx context.Context, v virtualRefresh) error
	removeRefresh(ctx context.Context, v virtualRefresh) error
	// advanceRefresh claims a due run: it moves next_run_at on only if it
	// is still v.NextRun, so two services never run it twice.
	advanceRefresh(ctx context.Context, v virtualRefresh, next, ran time.Time) (bool, error)
	// markRefresh records a failure (since set) or a success (zero since).
	markRefresh(ctx context.Context, v virtualRefresh, since time.Time, reason string) error
}

const (
	refreshColumns = "space_id, file_id, every, next_run_at, last_run_at, stale_since, stale_reason"
	// refreshSlots is how many refreshes run at once.
	refreshSlots = 2
	maxStaleText = 300
)

type dbRefreshStore struct{ db *sql.DB }

func (s dbRefreshStore) scan(ctx context.Context, query string, args ...any) ([]virtualRefresh, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+refreshColumns+" FROM metatrash_virtual_refresh "+query, args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read refresh schedules")
	}
	defer rows.Close()
	var out []virtualRefresh
	for rows.Next() {
		var v virtualRefresh
		var next int64
		var last, since sql.NullInt64
		var reason sql.NullString
		if err := rows.Scan(&v.SpaceID, &v.FileID, &v.Every, &next, &last, &since, &reason); err != nil {
			return nil, fmt.Errorf("cannot read refresh schedules")
		}
		v.NextRun = time.Unix(next, 0).UTC()
		if last.Valid {
			v.LastRun = time.Unix(last.Int64, 0).UTC()
		}
		if since.Valid {
			v.StaleSince = time.Unix(since.Int64, 0).UTC()
		}
		v.StaleReason = reason.String
		out = append(out, v)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read refresh schedules")
	}
	return out, nil
}

func (s dbRefreshStore) refreshes(ctx context.Context, spaceID string) ([]virtualRefresh, error) {
	return s.scan(ctx, "WHERE space_id = ? LIMIT 10000", spaceID)
}

func (s dbRefreshStore) dueRefreshes(ctx context.Context, now time.Time, limit int) ([]virtualRefresh, error) {
	return s.scan(ctx, "WHERE next_run_at <= ? ORDER BY next_run_at LIMIT ?", now.Unix(), limit)
}

func (s dbRefreshStore) refreshOf(ctx context.Context, spaceID, fileID string) (virtualRefresh, bool, error) {
	rows, err := s.scan(ctx, "WHERE space_id = ? AND file_id = ?", spaceID, fileID)
	if err != nil || len(rows) == 0 {
		return virtualRefresh{}, false, err
	}
	return rows[0], true, nil
}

func (s dbRefreshStore) addRefresh(ctx context.Context, v virtualRefresh) error {
	if _, err := s.db.ExecContext(ctx, "INSERT INTO metatrash_virtual_refresh ("+refreshColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		v.SpaceID, v.FileID, v.Every, v.NextRun.Unix(), unixOrNull(v.LastRun), unixOrNull(v.StaleSince), nullText(v.StaleReason)); err != nil {
		return fmt.Errorf("cannot save the refresh schedule")
	}
	return nil
}

func (s dbRefreshStore) setRefresh(ctx context.Context, v virtualRefresh) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE metatrash_virtual_refresh SET every = ?, next_run_at = ? WHERE space_id = ? AND file_id = ?", v.Every, v.NextRun.Unix(), v.SpaceID, v.FileID); err != nil {
		return fmt.Errorf("cannot save the refresh schedule")
	}
	return nil
}

func (s dbRefreshStore) removeRefresh(ctx context.Context, v virtualRefresh) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM metatrash_virtual_refresh WHERE space_id = ? AND file_id = ?", v.SpaceID, v.FileID); err != nil {
		return fmt.Errorf("cannot remove the refresh schedule")
	}
	return nil
}

func (s dbRefreshStore) advanceRefresh(ctx context.Context, v virtualRefresh, next, ran time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, "UPDATE metatrash_virtual_refresh SET next_run_at = ?, last_run_at = ? WHERE space_id = ? AND file_id = ? AND next_run_at = ?", next.Unix(), ran.Unix(), v.SpaceID, v.FileID, v.NextRun.Unix())
	if err != nil {
		return false, fmt.Errorf("cannot advance the refresh schedule")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("cannot advance the refresh schedule")
	}
	return n == 1, nil
}

func (s dbRefreshStore) markRefresh(ctx context.Context, v virtualRefresh, since time.Time, reason string) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE metatrash_virtual_refresh SET stale_since = ?, stale_reason = ? WHERE space_id = ? AND file_id = ?", unixOrNull(since), nullText(reason), v.SpaceID, v.FileID); err != nil {
		return fmt.Errorf("cannot save the refresh outcome")
	}
	return nil
}

// refreshing are a space's files with a source and a schedule, by file ID.
func refreshing(files map[string]record) map[string]record {
	out := map[string]record{}
	for _, f := range files {
		if f.Virtual != nil && f.Virtual.Refresh != "" && f.Virtual.Source != "" {
			out[f.ID] = f
		}
	}
	return out
}

// syncRefreshes brings a space's rows in step with its pointers: new
// schedules get their first run, changed ones a new next run, removed ones
// go. Stale marks stay with a file until a refresh works.
func (a *agentRunner) syncRefreshes(ctx context.Context, r *repository) error {
	if a.refreshes == nil || !r.owned || r.id == "" {
		return nil
	}
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return err
	}
	want := refreshing(files)
	have, err := a.refreshes.refreshes(ctx, r.id)
	if err != nil {
		return err
	}
	zone, now := a.zoneOf(ctx, r.id), a.clock()
	for _, row := range have {
		f, ok := want[row.FileID]
		delete(want, row.FileID)
		if !ok {
			if err := a.refreshes.removeRefresh(ctx, row); err != nil {
				return err
			}
			continue
		}
		if f.Virtual.Refresh == row.Every {
			continue
		}
		s, err := parseEvery(f.Virtual.Refresh)
		if err != nil {
			continue
		}
		row.Every, row.NextRun = f.Virtual.Refresh, s.next(now, zone)
		if err := a.refreshes.setRefresh(ctx, row); err != nil {
			return err
		}
	}
	for id, f := range want {
		s, err := parseEvery(f.Virtual.Refresh)
		if err != nil {
			continue
		}
		if err := a.refreshes.addRefresh(ctx, virtualRefresh{SpaceID: r.id, FileID: id, Every: f.Virtual.Refresh, NextRun: s.next(now, zone)}); err != nil {
			return err
		}
	}
	return nil
}

// syncAllRefreshes syncs every loaded space, as on start.
func (a *agentRunner) syncAllRefreshes() {
	if a.refreshes == nil {
		return
	}
	a.s.ownedMu.RLock()
	repos := make([]*repository, 0, len(a.s.ownedRepos))
	for _, r := range a.s.ownedRepos {
		repos = append(repos, r)
	}
	a.s.ownedMu.RUnlock()
	for _, r := range repos {
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		if err := a.syncRefreshes(ctx, r); err != nil {
			log.Printf("virtual refresh: space %s: %v", r.id, err)
		}
		cancel()
	}
}

// runRefreshes starts a refresh for each file that came round, at most
// refreshSlots at a time; one that finds every slot busy waits for the next
// tick. A run missed while the service was down happens once.
func (a *agentRunner) runRefreshes() {
	if a.refreshes == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	now := a.clock()
	due, err := a.refreshes.dueRefreshes(ctx, now, 20)
	if err != nil {
		log.Printf("virtual refresh: %v", err)
		return
	}
	for _, row := range due {
		r := a.s.ownedRepository(row.SpaceID)
		if r == nil {
			continue
		}
		_, files, err := r.snapshot(ctx, "")
		if err != nil {
			log.Printf("virtual refresh: space %s: %v", r.id, err)
			continue
		}
		f, ok := refreshing(files)[row.FileID]
		var s schedule
		if ok {
			s, err = parseEvery(f.Virtual.Refresh)
		}
		if !ok || err != nil {
			if err := a.refreshes.removeRefresh(ctx, row); err != nil {
				log.Printf("virtual refresh: %v", err)
			}
			continue
		}
		select {
		case a.refreshSlots <- struct{}{}:
		default:
			return
		}
		won, err := a.refreshes.advanceRefresh(ctx, row, s.next(now, a.zoneOf(ctx, r.id)), now)
		if err != nil || !won {
			<-a.refreshSlots
			if err != nil {
				log.Printf("virtual refresh: %v", err)
			}
			continue
		}
		a.wg.Add(1)
		a.refreshWG.Add(1)
		go func() {
			defer a.wg.Done()
			defer a.refreshWG.Done()
			defer func() { <-a.refreshSlots }()
			ctx, cancel := context.WithTimeout(a.ctx, 5*time.Minute)
			defer cancel()
			a.refreshAndMark(ctx, r, row)
		}()
	}
}

// refreshAndMark refreshes one file and records whether it worked.
func (a *agentRunner) refreshAndMark(ctx context.Context, r *repository, row virtualRefresh) {
	_, err := a.refreshFile(ctx, r, row.FileID)
	since, reason := time.Time{}, ""
	if err != nil {
		since, reason = row.StaleSince, refreshFailure(err)
		if since.IsZero() {
			since = a.clock()
		}
		log.Printf("virtual refresh: space %s file %s: %s", r.id, row.FileID, reason)
	}
	if since.Equal(row.StaleSince) && reason == row.StaleReason {
		return
	}
	if err := a.refreshes.markRefresh(ctx, row, since, reason); err != nil {
		log.Printf("virtual refresh: %v", err)
	}
}

// refreshFailure is a failed refresh's reason, for the stale mark.
func refreshFailure(err error) string {
	text := err.Error()
	if p, ok := err.(*Error); ok {
		text = p.Message
	}
	if len(text) > maxStaleText {
		text = strings.ToValidUTF8(text[:maxStaleText], "")
	}
	return text
}

// refreshFile fetches a file's source again and, when the content changed,
// commits it as a new version. It reports whether it changed.
func (a *agentRunner) refreshFile(ctx context.Context, r *repository, fileID string) (bool, error) {
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return false, err
	}
	var path string
	var f record
	for p, file := range files {
		if file.ID == fileID {
			path, f = p, file
		}
	}
	if path == "" || f.Virtual == nil || f.Virtual.Source == "" {
		return false, nil
	}
	body, media, err := a.fetchSource(ctx, r.owner, f.Virtual.Source)
	if err != nil {
		return false, err
	}
	if webHash(body) == f.Virtual.SHA256 {
		return false, nil
	}
	owner := a.s.usernameOf(ctx, r.owner)
	ctx = withAuthor(ctx, func() changeAuthor { return changeAuthor{Name: owner, App: refreshApp, Kind: "agent"} })
	_, err = a.s.putVirtual(ctx, r, r.id, path, state, body, media, f.Virtual.Source, f.Virtual.Refresh)
	return err == nil, err
}

// refreshApp names refreshes in history.
const refreshApp = "Metatrash refresh"

// fetchSource downloads a URL for userID the way web_fetch does (the same
// address rules, daily limit and conditional request), returning the
// content and its media type.
func (a *agentRunner) fetchSource(ctx context.Context, userID, source string) ([]byte, string, error) {
	if a.webRefs == nil || a.fetcher == nil || a.settings.downloads == nil {
		return nil, "", problem(503, "virtual_unavailable", "This server cannot fetch files: it needs web downloads (agents.downloads; docs/deployment.md).")
	}
	w := a.newWebSession(userID, false, true)
	w.add(source)
	value, err := w.webFetch(ctx, source)
	if err != nil {
		return nil, "", err
	}
	fetched := value.(webFetchResult)
	name := fetched.Original
	if name == "" {
		name = fetched.Path
	}
	ref, hash, err := w.lookup(ctx, name)
	if err != nil {
		return nil, "", err
	}
	body, err := w.object(ctx, hash)
	if err != nil {
		return nil, "", err
	}
	return body, webMedia[ref.Ext], nil
}

// staleOf says whether a virtual file's refreshes are failing: since when
// and why. Zero time when it is fresh or has no schedule.
func (s *Service) staleOf(ctx context.Context, r *repository, fileID string) (time.Time, string) {
	a := s.agents.Load()
	if a == nil || a.refreshes == nil || !r.owned {
		return time.Time{}, ""
	}
	row, ok, err := a.refreshes.refreshOf(ctx, r.id, fileID)
	if err != nil || !ok {
		return time.Time{}, ""
	}
	return row.StaleSince, row.StaleReason
}

// markStale sets Stale on the listed virtual files whose refreshes are
// failing. Only files with a refresh schedule can be stale, so a list
// without one asks the database nothing; otherwise one query covers the
// space.
func (s *Service) markStale(ctx context.Context, r *repository, files []File) {
	scheduled := false
	for _, f := range files {
		if f.Virtual != nil && f.Virtual.Refresh != "" {
			scheduled = true
			break
		}
	}
	a := s.agents.Load()
	if !scheduled || a == nil || a.refreshes == nil || !r.owned {
		return
	}
	rows, err := a.refreshes.refreshes(ctx, r.id)
	if err != nil {
		return
	}
	stale := map[string]virtualRefresh{}
	for _, row := range rows {
		if !row.StaleSince.IsZero() {
			stale[row.FileID] = row
		}
	}
	for i := range files {
		if row, ok := stale[files[i].ID]; ok && files[i].Virtual != nil {
			files[i].Stale = &VirtualStale{Since: row.StaleSince.UTC().Format(time.RFC3339), Reason: row.StaleReason}
		}
	}
}

// VirtualStale is a read's note that refreshing a file fails.
type VirtualStale struct {
	Since  string `json:"since"`
	Reason string `json:"reason"`
}

// staleNote is the sentence reads and pages add for a stale file.
func staleNote(since time.Time, reason string) string {
	return fmt.Sprintf("Refreshing it from its source has failed since %s (%s), so this is the last version fetched.", since.UTC().Format("2 January 2006 15:04 UTC"), strings.TrimSuffix(reason, "."))
}

// configureVirtual changes a virtual file's source and refresh schedule
// (0.60.2), or with a source makes a new one by fetching it. A new or
// changed source is fetched now, for the space owner; changing only the
// schedule rewrites the pointer. source "" removes the source and the
// schedule; refresh "" keeps the current version.
func (s *Service) configureVirtual(ctx context.Context, r *repository, space string, in Input) (WriteResult, error) {
	if !r.owned || r.id == "" {
		return WriteResult{}, problem(403, "forbidden", "Virtual files are kept in account spaces only.")
	}
	if !validPath(in.Path) || isFolderFile(in.Path) {
		return WriteResult{}, invalid("A valid path for an ordinary file is required.")
	}
	if in.Source == nil && in.Refresh == nil {
		return WriteResult{}, invalid("Give source, refresh or both.")
	}
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return WriteResult{}, err
	}
	f, exists := files[in.Path]
	if exists && f.Virtual == nil {
		return WriteResult{}, problem(409, "not_virtual", in.Path+" is an ordinary file; configure changes virtual files only (upload it, or fetch its source to another path).")
	}
	var current VirtualInfo
	if exists {
		current = *f.Virtual
	}
	source, refresh := current.Source, current.Refresh
	if in.Source != nil {
		source = *in.Source
		if source == "" {
			refresh = ""
		} else if _, err := parseWebURL(source); err != nil {
			return WriteResult{}, err
		}
	}
	if in.Refresh != nil {
		refresh = *in.Refresh
	}
	if refresh != "" {
		if source == "" {
			return WriteResult{}, invalid("refresh needs a source to fetch from.")
		}
		if _, err := parseEvery(refresh); err != nil {
			return WriteResult{}, invalid("refresh: " + err.Error())
		}
	}
	if !exists && source == "" {
		return WriteResult{}, invalid("A new virtual file needs a source to fetch.")
	}
	ifInState := in.IfInState
	if ifInState == "" {
		ifInState = state
	}
	var result WriteResult
	a := s.agents.Load()
	if !exists || source != current.Source && source != "" {
		if a == nil {
			return WriteResult{}, problem(503, "virtual_unavailable", "This server cannot fetch files: it needs web downloads (agents.downloads; docs/deployment.md).")
		}
		body, media, err := a.fetchSource(ctx, r.owner, source)
		if err != nil {
			return WriteResult{}, err
		}
		if result, err = s.putVirtual(ctx, r, space, in.Path, ifInState, body, media, source, refresh); err != nil {
			return WriteResult{}, err
		}
	} else {
		info := current
		info.Source, info.Refresh = source, refresh
		pointer := info.pointer()
		value, err := s.dispatchRepository(ctx, r, space, "write", Input{Path: in.Path, Text: &pointer, IfInState: ifInState, Strict: in.Strict, Virtual: &info})
		if err != nil {
			return WriteResult{}, err
		}
		result = value.(WriteResult)
	}
	if a != nil {
		if err := a.syncRefreshes(ctx, r); err != nil {
			log.Printf("virtual refresh: space %s: %v", r.id, err)
		}
	}
	return result, nil
}
