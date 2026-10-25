package service

import (
	"context"
	"encoding/json"
	"net/url"
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
	settings := testAgentSettings(t)
	runner, err := newAgentRunner(w.s, settings, store)
	if err != nil {
		t.Fatal(err)
	}
	runner.providerFor = func(agentModel) agentProvider { return model }
	// The owner's agents, through the database with the runtime grants.
	first := testUserAgent(t, settings, w.owner.ID, "MiniMax main", "minimax", "MiniMax-M3")
	second := testUserAgent(t, settings, w.owner.ID, "Claude", "anthropic", "claude-sonnet-5-5")
	for _, u := range []userAgent{first, second} {
		if err := w.db.addUserAgent(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.db.addUserAgent(ctx, testUserAgent(t, settings, w.owner.ID, "Claude", "anthropic", "claude-opus-5-5")); err == nil || !strings.Contains(err.Error(), "already have an agent called Claude") {
		t.Errorf("same name: %v", err)
	}
	if err := w.db.setDefaultUserAgent(ctx, w.owner.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.db.removeUserAgent(ctx, w.owner.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.db.removeUserAgent(ctx, strings.Repeat("9", 32), first.ID); err == nil {
		t.Error("removed another user's agent")
	}
	agents, err := w.db.userAgents(ctx, w.owner.ID)
	if err != nil || len(agents) != 1 || agents[0].ID != first.ID || !agents[0].Default || agents[0].KeyHint != "1234" {
		t.Fatalf("agents %+v %v", agents, err)
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
		c := ctx
		if isFolderConfig(path) {
			c = asOwner(ctx, repo)
		}
		if _, err := w.s.dispatchRepository(c, repo, w.ownedName, "write", Input{Path: path, Text: &text, IfInState: head}); err != nil {
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
	if jobs[0].AgentID != first.ID {
		t.Errorf("agent %q", jobs[0].AgentID)
	}
	if n, err := store.tokensSince(ctx, first.ID, time.Now().Add(-time.Hour)); err != nil || n != 1060 {
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

// Agents in Your account → Services: add, test, make default and remove,
// through the account database with the runtime grants.
func TestDatabaseAgentsPage(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	runner, err := newAgentRunner(w.s, testAgentSettings(t), dbJobStore{w.db.db})
	if err != nil {
		t.Fatal(err)
	}
	var called []agentModel
	runner.providerFor = func(m agentModel) agentProvider {
		called = append(called, m)
		return &fakeModel{turns: []func(agentRequest) (agentResponse, error){func(agentRequest) (agentResponse, error) {
			return agentResponse{Content: []json.RawMessage{textBlock("OK")}, StopReason: "end_turn", Usage: agentUsage{InputTokens: 12, OutputTokens: 1}}, nil
		}}}
	}
	w.s.agents.Store(runner)
	t.Cleanup(func() { w.s.agents.Store(nil) })
	user := w.user("agents")
	session := w.session(user)
	csrf := func(action string) string { return w.s.accounts.agentCSRF("/account/agents/"+action, session.Value) }
	if body := oauthCall(w.h, "GET", "/account/services", nil, session).Body.String(); !strings.Contains(body, "No agents yet.") || !strings.Contains(body, "Add an agent") {
		t.Fatalf("services page:\n%s", body)
	}
	add := url.Values{"csrf": {csrf("add")}, "name": {"MiniMax main"}, "provider": {"minimax"}, "model": {""}, "key": {"sk-test-5678"}, "endpoint": {""}, "cap": {"2000000"}}
	if res := oauthCall(w.h, "POST", "/account/agents/add", url.Values{"csrf": {csrf("test")}, "name": {"x"}}, session); res.Code != 403 {
		t.Errorf("add with the test form's CSRF: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/agents/add", add, session); res.Code != 303 {
		t.Fatalf("add: %d %s", res.Code, res.Body.String())
	}
	bad := url.Values{"csrf": {csrf("add")}, "name": {"Second"}, "provider": {"anthropic"}, "key": {"has spaces in it"}, "endpoint": {"https://10.0.0.1/x"}}
	res := oauthCall(w.h, "POST", "/account/agents/add", bad, session)
	if body := res.Body.String(); res.Code != 400 || !strings.Contains(body, "public internet") || !strings.Contains(body, `value="Second"`) || strings.Contains(body, "has spaces") {
		t.Errorf("bad add: %d\n%s", res.Code, body)
	}
	agents, err := w.db.userAgents(ctx, user.ID)
	if err != nil || len(agents) != 1 || !agents[0].Default || agents[0].Model != "MiniMax-M3" {
		t.Fatalf("agents %+v %v", agents, err)
	}
	if body := oauthCall(w.h, "GET", "/account/services", nil, session).Body.String(); !strings.Contains(body, "MiniMax main") || !strings.Contains(body, "key ending 5678") || strings.Contains(body, "sk-test") {
		t.Errorf("listed:\n%s", body)
	}
	res = oauthCall(w.h, "POST", "/account/agents/test", url.Values{"csrf": {csrf("test")}, "agent": {agents[0].ID}}, session)
	if body := res.Body.String(); res.Code != 200 || !strings.Contains(body, "MiniMax main works") || len(called) != 1 || called[0].apiKey != "sk-test-5678" || called[0].endpoint != "https://api.minimax.io/anthropic/v1/messages" {
		t.Errorf("test: %d %+v\n%s", res.Code, called, body)
	}
	// Another account cannot touch it.
	other := w.session(w.user("other"))
	otherCSRF := w.s.accounts.agentCSRF("/account/agents/remove", other.Value)
	if res := oauthCall(w.h, "POST", "/account/agents/remove", url.Values{"csrf": {otherCSRF}, "agent": {agents[0].ID}}, other); res.Code != 404 {
		t.Errorf("another account's remove: %d", res.Code)
	}
	add.Set("name", "Claude")
	add.Set("provider", "anthropic")
	if res := oauthCall(w.h, "POST", "/account/agents/add", add, session); res.Code != 303 {
		t.Fatalf("second add: %d", res.Code)
	}
	agents, _ = w.db.userAgents(ctx, user.ID)
	if len(agents) != 2 || agents[1].Name != "Claude" || agents[1].Default {
		t.Fatalf("agents %+v", agents)
	}
	if res := oauthCall(w.h, "POST", "/account/agents/default", url.Values{"csrf": {csrf("default")}, "agent": {agents[1].ID}}, session); res.Code != 303 {
		t.Fatalf("default: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/agents/remove", url.Values{"csrf": {csrf("remove")}, "agent": {agents[1].ID}}, session); res.Code != 303 {
		t.Fatalf("remove: %d", res.Code)
	}
	agents, _ = w.db.userAgents(ctx, user.ID)
	if len(agents) != 1 || agents[0].Name != "MiniMax main" || !agents[0].Default {
		t.Errorf("after removing the default: %+v", agents)
	}
}
