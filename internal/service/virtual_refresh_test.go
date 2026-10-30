package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memoryRefreshStore is metatrash_virtual_refresh for tests.
type memoryRefreshStore struct {
	mu   sync.Mutex
	rows map[[2]string]virtualRefresh
}

func (m *memoryRefreshStore) refreshes(_ context.Context, spaceID string) ([]virtualRefresh, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []virtualRefresh
	for _, v := range m.rows {
		if v.SpaceID == spaceID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *memoryRefreshStore) dueRefreshes(_ context.Context, now time.Time, _ int) ([]virtualRefresh, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []virtualRefresh
	for _, v := range m.rows {
		if !v.NextRun.After(now) {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *memoryRefreshStore) refreshOf(_ context.Context, spaceID, fileID string) (virtualRefresh, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.rows[[2]string{spaceID, fileID}]
	return v, ok, nil
}

func (m *memoryRefreshStore) addRefresh(_ context.Context, v virtualRefresh) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[[2]string{v.SpaceID, v.FileID}] = v
	return nil
}

func (m *memoryRefreshStore) setRefresh(_ context.Context, v virtualRefresh) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row := m.rows[[2]string{v.SpaceID, v.FileID}]
	row.Every, row.NextRun = v.Every, v.NextRun
	m.rows[[2]string{v.SpaceID, v.FileID}] = row
	return nil
}

func (m *memoryRefreshStore) removeRefresh(_ context.Context, v virtualRefresh) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, [2]string{v.SpaceID, v.FileID})
	return nil
}

func (m *memoryRefreshStore) advanceRefresh(_ context.Context, v virtualRefresh, next, ran time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[[2]string{v.SpaceID, v.FileID}]
	if !ok || !row.NextRun.Equal(v.NextRun) {
		return false, nil
	}
	row.NextRun, row.LastRun = next, ran
	m.rows[[2]string{v.SpaceID, v.FileID}] = row
	return true, nil
}

func (m *memoryRefreshStore) markRefresh(_ context.Context, v virtualRefresh, since time.Time, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.rows[[2]string{v.SpaceID, v.FileID}]
	if ok {
		row.StaleSince, row.StaleReason = since, reason
		m.rows[[2]string{v.SpaceID, v.FileID}] = row
	}
	return nil
}

// due makes every row due now and runs the refreshes to the end.
func (m *memoryRefreshStore) runDue(t *testing.T, a *agentRunner) {
	t.Helper()
	m.mu.Lock()
	for k, v := range m.rows {
		v.NextRun = time.Now().Add(-time.Minute)
		m.rows[k] = v
	}
	m.mu.Unlock()
	a.runRefreshes()
	a.refreshWG.Wait()
}

// configure makes and changes virtual files kept up to date from a URL, and
// refreshes commit only new content, marking failures stale.
func TestVirtualRefresh(t *testing.T) {
	w, _, _, _ := webWorld(t)
	store := &memoryRefreshStore{rows: map[[2]string]virtualRefresh{}}
	w.runner.refreshes = store
	ctx := context.Background()
	var mu sync.Mutex
	body, status, hits := "# Guide\n\nVersion 1\n", 200, 0
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if status != 200 {
			http.Error(rw, "down", status)
			return
		}
		rw.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		rw.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	set := func(text string, code int) {
		mu.Lock()
		body, status = text, code
		mu.Unlock()
	}
	configure := func(path string, source, refresh *string, code string) WriteResult {
		t.Helper()
		result, err := w.s.configureVirtual(ctx, w.repo, w.repo.id, Input{Path: path, Source: source, Refresh: refresh})
		if code == "" && err != nil {
			t.Fatalf("configure %s: %v", path, err)
		}
		if code != "" {
			if p, ok := err.(*Error); !ok || p.Code != code {
				t.Fatalf("configure %s: want %s, got %v", path, code, err)
			}
		}
		return result
	}
	str := func(s string) *string { return &s }
	source := srv.URL + "/guide.md"

	// A new file from a URL, refreshed daily.
	made := configure("hr/guide.md", str(source), str("day 06:00"), "")
	v := made.File.Virtual
	if !made.Created || v == nil || v.Source != source || v.Refresh != "day 06:00" || !strings.Contains(v.pointer(), "\nrefresh day 06:00\n") {
		t.Fatalf("made %+v %+v", made, v)
	}
	if got := w.read("hr/guide.md"); got != "# Guide\n\nVersion 1\n" {
		t.Errorf("read %q", got)
	}
	rows, _ := store.refreshes(ctx, w.repo.id)
	if len(rows) != 1 || rows[0].FileID != made.File.ID || rows[0].Every != "day 06:00" || !rows[0].NextRun.After(time.Now()) {
		t.Fatalf("rows %+v", rows)
	}

	// Refused: an ordinary file, refresh without a source, a bad schedule,
	// a new file without a source, nothing to change.
	w.write("hr/notes.md", "plain\n")
	configure("hr/notes.md", str(source), nil, "not_virtual")
	configure("hr/new.md", nil, str("day 06:00"), "invalid_request")
	configure("hr/guide.md", nil, str("fortnightly"), "invalid_request")
	configure("hr/guide.md", nil, nil, "invalid_request")
	configure("hr/x.md", str("ftp://example.org/x"), nil, "invalid_request")

	// Changing only the schedule rewrites the pointer without fetching.
	before := hits
	changed := configure("hr/guide.md", nil, str("hour"), "")
	if changed.File.Virtual.Refresh != "hour" || hits != before || changed.File.Virtual.SHA256 != v.SHA256 {
		t.Errorf("schedule change %+v, %d fetches", changed.File.Virtual, hits-before)
	}
	if rows, _ := store.refreshes(ctx, w.repo.id); len(rows) != 1 || rows[0].Every != "hour" {
		t.Errorf("rows after change %+v", rows)
	}

	// A refresh with new content commits a new version, made by the refresh.
	older, _ := w.repo.head(ctx)
	set("# Guide\n\nVersion 2\n", 200)
	store.runDue(t, w.runner)
	if got := w.read("hr/guide.md"); got != "# Guide\n\nVersion 2\n" {
		t.Fatalf("after refresh %q", got)
	}
	history := w.do(ctx, "history", Input{ID: made.File.ID}, "").(HistoryResult)
	if len(history.Entries) == 0 || history.Entries[0].By == nil || history.Entries[0].By.App != refreshApp {
		t.Errorf("history %+v", history.Entries)
	}
	// Unchanged content makes no commit.
	head, _ := w.repo.head(ctx)
	store.runDue(t, w.runner)
	if now, _ := w.repo.head(ctx); now != head {
		t.Error("unchanged refresh committed")
	}

	// A failing source keeps the content and marks the file stale.
	set("", 500)
	store.runDue(t, w.runner)
	got := w.do(ctx, "read", Input{Path: "hr/guide.md"}, "").(ReadResult)
	if got.File.Text != "# Guide\n\nVersion 2\n" || got.Stale == nil || !strings.Contains(got.Note, "has failed since") {
		t.Fatalf("stale read %+v", got)
	}
	if now, _ := w.repo.head(ctx); now != head {
		t.Error("failed refresh committed")
	}
	// list marks it too (0.62.0), in full and with since.
	staleIn := func(l ListResult) *VirtualStale {
		for _, f := range l.Files {
			if f.Path == "hr/guide.md" {
				return f.Stale
			}
		}
		t.Fatalf("hr/guide.md not listed: %+v", l.Files)
		return nil
	}
	if l := w.do(ctx, "list", Input{Prefix: "hr/"}, "").(ListResult); staleIn(l) == nil || staleIn(l).Since != got.Stale.Since || staleIn(l).Reason == "" {
		t.Errorf("stale list %+v", l.Files)
	}
	if l := w.do(ctx, "list", Input{Since: older}, "").(ListResult); staleIn(l) == nil {
		t.Errorf("stale list since %+v", l.Files)
	}
	// It stays stale from the first failure, then clears once a refresh works.
	first := got.Stale.Since
	store.runDue(t, w.runner)
	if got := w.do(ctx, "read", Input{Path: "hr/guide.md"}, "").(ReadResult); got.Stale == nil || got.Stale.Since != first {
		t.Errorf("second failure %+v", got.Stale)
	}
	set("# Guide\n\nVersion 3\n", 200)
	store.runDue(t, w.runner)
	if got := w.do(ctx, "read", Input{Path: "hr/guide.md"}, "").(ReadResult); got.Stale != nil || got.File.Text != "# Guide\n\nVersion 3\n" {
		t.Errorf("recovered %+v", got)
	}
	if l := w.do(ctx, "list", Input{Prefix: "hr/"}, "").(ListResult); staleIn(l) != nil {
		t.Errorf("recovered list %+v", l.Files)
	}

	// A move keeps the schedule (rows are by file ID).
	w.do(ctx, "move", Input{From: "hr/guide.md", To: "hr/docs/guide.md"}, "")
	set("# Guide\n\nVersion 4\n", 200)
	store.runDue(t, w.runner)
	if got := w.read("hr/docs/guide.md"); got != "# Guide\n\nVersion 4\n" {
		t.Errorf("after move %q", got)
	}

	// Removing the source removes the schedule; the content stays.
	plain := configure("hr/docs/guide.md", str(""), nil, "")
	if plain.File.Virtual.Source != "" || plain.File.Virtual.Refresh != "" {
		t.Errorf("source removed %+v", plain.File.Virtual)
	}
	if rows, _ := store.refreshes(ctx, w.repo.id); len(rows) != 0 {
		t.Errorf("rows after removal %+v", rows)
	}
	if got := w.read("hr/docs/guide.md"); got != "# Guide\n\nVersion 4\n" {
		t.Errorf("content after removal %q", got)
	}

	// The runner's agents are not offered configure.
	for _, tool := range w.runner.tools {
		if strings.Contains(string(tool), `"name":"configure"`) {
			t.Error("runner offers configure")
		}
	}
}
