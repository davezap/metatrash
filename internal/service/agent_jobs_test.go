package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestDatabaseAgentJobs runs an action job through the account database's
// queue with the runtime grants: queue, claim, finish, counts and restart.
func TestDatabaseAgentJobs(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	store := dbJobStore{w.db.db}
	model := &fakeModel{turns: []func(agentRequest) (agentResponse, error){
		useTools(toolUse("t1", "append", map[string]any{"path": "notes.md", "text": "seen"})),
		func(agentRequest) (agentResponse, error) {
			return agentResponse{Content: []json.RawMessage{textBlock("Noted.")}, StopReason: "end_turn", Usage: agentUsage{InputTokens: 7, OutputTokens: 3}}, nil
		},
	}}
	zone, _ := time.LoadLocation("Pacific/Auckland")
	runner, err := newAgentRunner(w.s, &agentSettings{provider: "minimax", app: "MiniMax", model: "MiniMax-M3", maxTokens: 4096, zone: zone, dailyCap: 1000000}, model, store)
	if err != nil {
		t.Fatal(err)
	}
	w.s.agents.Store(runner)
	runner.start()
	t.Cleanup(func() { w.s.agents.Store(nil); runner.stop() })
	repo := w.s.ownedRepository(w.ownedID)
	write := func(path, text string) {
		t.Helper()
		head, err := repo.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.s.dispatchRepository(ctx, repo, w.ownedName, "write", Input{Path: path, Text: &text, IfInState: head}); err != nil {
			t.Fatal(err)
		}
	}
	write("inbox/.metatrash.json", `{"actions":[{"name":"note","on":"*.txt","do":"Append seen to notes.md."}]}`)
	write("inbox/a.txt", "hello\n")
	var jobs []agentJob
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err = store.list(ctx, "WHERE space_id = ?", w.ownedID)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) == 1 && jobs[0].Status == jobDone {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(jobs) != 1 || jobs[0].Status != jobDone || jobs[0].Steps != 1 || jobs[0].InputTokens != 1007 || jobs[0].Folder != "inbox/" || jobs[0].File != "inbox/a.txt" || jobs[0].StartedAt.IsZero() || jobs[0].FinishedAt.IsZero() {
		t.Fatalf("jobs %+v", jobs)
	}
	if n, err := store.queuedSince(ctx, w.ownedID, "inbox/", time.Now().Add(-time.Hour)); err != nil || n != 1 {
		t.Errorf("queuedSince %d %v", n, err)
	}
	if n, err := store.tokensSince(ctx, w.ownedID, time.Now().Add(-time.Hour)); err != nil || n != 1060 {
		t.Errorf("tokensSince %d %v", n, err)
	}
	deadline = time.Now().Add(5 * time.Second)
	var log string
	for time.Now().Before(deadline) && !strings.Contains(log, "Noted.") {
		time.Sleep(20 * time.Millisecond)
		if v, err := w.s.dispatchRepository(ctx, repo, w.ownedName, "read", Input{Path: "inbox/runner.md"}); err == nil {
			log = v.(ReadResult).File.Text
		}
	}
	if !strings.Contains(log, "· note · done") || !strings.Contains(log, "notes.md (append)") {
		t.Errorf("runner.md:\n%s", log)
	}
	// History names the owner by username.
	_, files, _ := repo.snapshot(ctx, "")
	v, err := w.s.dispatchRepository(ctx, repo, w.ownedName, "history", Input{ID: files["inbox/notes.md"].ID, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if by := v.(HistoryResult).Entries[0].By; by == nil || by.Name != w.owner.Username || by.App != "MiniMax" {
		t.Errorf("by %+v", by)
	}
	// A job left running by a restart fails and says so.
	id, _ := randomHex(16)
	stale := agentJob{ID: id, SpaceID: w.ownedID, Folder: "inbox/", Action: "note", File: "inbox/a.txt", Commit: strings.Repeat("0", 40), Status: jobQueued, QueuedAt: time.Now()}
	if err := store.add(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.claim(ctx, id, time.Now()); err != nil || !ok {
		t.Fatalf("claim %v %v", ok, err)
	}
	if ok, _ := store.claim(ctx, id, time.Now()); ok {
		t.Error("claimed twice")
	}
	running, err := store.running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range running {
		found = found || j.ID == id
	}
	if !found {
		t.Error("running job not listed")
	}
	stale.Status, stale.Error, stale.FinishedAt = jobFailed, "interrupted", time.Now()
	if err := store.finish(ctx, stale); err != nil {
		t.Fatal(err)
	}
}
