package service

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"sync"
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
	// Change its name, cap and key (0.58.0); another account cannot.
	change := url.Values{"csrf": {csrf("change")}, "agent": {agents[0].ID}, "name": {"MiniMax renamed"}, "cap": {"5,000,000"}, "key": {""}}
	if res := oauthCall(w.h, "POST", "/account/agents/change", url.Values{"csrf": {w.s.accounts.agentCSRF("/account/agents/change", other.Value)}, "agent": {agents[0].ID}, "name": {"x"}, "cap": {"1"}, "key": {""}}, other); res.Code != 404 {
		t.Errorf("another account's change: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/agents/change", change, session); res.Code != 303 {
		t.Fatalf("change: %d %s", res.Code, res.Body.String())
	}
	changed, _ := w.db.userAgents(ctx, user.ID)
	if len(changed) != 1 || changed[0].Name != "MiniMax renamed" || changed[0].DailyCap != 5000000 || changed[0].KeyHint != "5678" || string(changed[0].Sealed) != string(agents[0].Sealed) {
		t.Fatalf("after change: %+v", changed)
	}
	change.Set("key", "sk-new-4321")
	change.Set("cap", "0")
	if res := oauthCall(w.h, "POST", "/account/agents/change", change, session); res.Code != 303 {
		t.Fatalf("change key: %d %s", res.Code, res.Body.String())
	}
	res = oauthCall(w.h, "POST", "/account/agents/test", url.Values{"csrf": {csrf("test")}, "agent": {agents[0].ID}}, session)
	if res.Code != 200 || len(called) != 2 || called[1].apiKey != "sk-new-4321" {
		t.Errorf("test after a new key: %d %+v", res.Code, called)
	}
	if body := oauthCall(w.h, "GET", "/account/services", nil, session).Body.String(); !strings.Contains(body, "key ending 4321") || !strings.Contains(body, "No cap") || strings.Contains(body, "sk-new") {
		t.Errorf("listed after change:\n%s", body)
	}
	change.Set("cap", "-1")
	if res := oauthCall(w.h, "POST", "/account/agents/change", change, session); res.Code != 400 || !strings.Contains(res.Body.String(), "whole number") {
		t.Errorf("bad cap: %d", res.Code)
	}
	change.Set("name", "MiniMax main")
	change.Set("cap", "1")
	change.Set("key", "")
	if res := oauthCall(w.h, "POST", "/account/agents/change", change, session); res.Code != 303 {
		t.Fatalf("rename back: %d", res.Code)
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
	clash := url.Values{"csrf": {csrf("change")}, "agent": {agents[1].ID}, "name": {"MiniMax main"}, "cap": {"1"}, "key": {""}}
	if res := oauthCall(w.h, "POST", "/account/agents/change", clash, session); res.Code != 409 || !strings.Contains(res.Body.String(), "already have an agent called") {
		t.Errorf("rename to a name in use: %d", res.Code)
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

// Bring your own key through the account database with the runtime grants:
// a member's file skips an action whose contributor pays, they open its page
// from the notice, opt in with the right agent, the file runs on their key,
// and removing that agent removes the opt-in.
func TestDatabaseActionOptIn(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	store := dbJobStore{w.db.db}
	settings := testAgentSettings(t)
	runner, err := newAgentRunner(w.s, settings, store)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var called []agentModel
	var notices []string
	runner.providerFor = func(m agentModel) agentProvider {
		mu.Lock()
		called = append(called, m)
		mu.Unlock()
		return &fakeModel{}
	}
	runner.notice = func(_ context.Context, userID string, _ agentJob, why string) {
		mu.Lock()
		notices = append(notices, userID+": "+why)
		mu.Unlock()
	}
	// In the shared space, w.member owns it and w.owner is a member.
	spaceOwner, contributor := w.member, w.owner
	ownerAgent := testUserAgent(t, settings, spaceOwner.ID, "Owner MiniMax", "minimax", "MiniMax-M3")
	theirs := testUserAgent(t, settings, contributor.ID, "Their MiniMax", "minimax", "MiniMax-M3")
	other := testUserAgent(t, settings, contributor.ID, "Their Claude", "anthropic", "claude-sonnet-5-5")
	for _, u := range []userAgent{ownerAgent, theirs, other} {
		if err := w.db.addUserAgent(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	w.s.agents.Store(runner)
	runner.start()
	t.Cleanup(func() { w.s.agents.Store(nil); runner.stop() })
	repo := w.s.ownedRepository(w.joinID)
	write := func(c context.Context, path, text string) {
		t.Helper()
		head, err := repo.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.s.dispatchRepository(c, repo, w.joinName, "write", Input{Path: path, Text: &text, IfInState: head}); err != nil {
			t.Fatal(err)
		}
	}
	waitJobs := func(n int) []agentJob {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			jobs, err := store.list(ctx, "WHERE space_id = ? ORDER BY queued_at, job_id", w.joinID)
			if err != nil {
				t.Fatal(err)
			}
			finished := 0
			for _, j := range jobs {
				if j.Status != jobQueued && j.Status != jobRunning {
					finished++
				}
			}
			if len(jobs) >= n && finished == len(jobs) {
				time.Sleep(100 * time.Millisecond) // let runner.md land
				return jobs
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("jobs did not finish")
		return nil
	}
	write(asOwner(ctx, repo), "in/.metatrash.json", `{"actions":[{"name":"tidy","on":"*.md","do":"Tidy.","model":"minimax/MiniMax-M3","pays":"contributor"}]}`)
	as := context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: contributor.ID}})
	write(as, "in/a.md", "a\n")
	jobs := waitJobs(1)
	skipped := jobs[0]
	if skipped.Status != jobSkipped || skipped.Contributor != contributor.ID || !strings.Contains(skipped.Error, "has not opted in") || len(called) != 0 || len(notices) != 1 {
		t.Fatalf("not opted in: %+v %q", skipped, notices)
	}
	// The action's page: only for the person whose file it was.
	session := w.session(contributor)
	page := oauthCall(w.h, "GET", "/account/actions/"+skipped.ID, nil, session)
	if body := page.Body.String(); page.Code != 200 || !strings.Contains(body, "Opt in and run it") || !strings.Contains(body, "Their MiniMax") || strings.Contains(body, "Their Claude") || strings.Contains(body, "They can opt in at") {
		t.Fatalf("page: %d\n%s", page.Code, body)
	}
	if res := oauthCall(w.h, "GET", "/account/actions/"+skipped.ID, nil, w.session(spaceOwner)); res.Code != 404 {
		t.Errorf("the space owner opened it: %d", res.Code)
	}
	csrf := func(action string) string { return w.s.accounts.agentCSRF("/account/agents/"+action, session.Value) }
	if res := oauthCall(w.h, "POST", "/account/agents/optin", url.Values{"csrf": {csrf("optin")}, "job": {skipped.ID}, "agent": {other.ID}}, session); res.Code != 400 || !strings.Contains(res.Body.String(), "MiniMax MiniMax-M3 agents") {
		t.Errorf("opt in with the wrong model: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/agents/optin", url.Values{"csrf": {csrf("optin")}, "job": {skipped.ID}, "agent": {theirs.ID}}, session); res.Code != 303 {
		t.Fatalf("opt in: %d %s", res.Code, res.Body.String())
	}
	// Jobs queued in the same second list in any order.
	find := func(jobs []agentJob, match func(agentJob) bool) agentJob {
		t.Helper()
		for _, j := range jobs {
			if match(j) {
				return j
			}
		}
		t.Fatalf("no such job in %+v", jobs)
		return agentJob{}
	}
	rerun := find(waitJobs(2), func(j agentJob) bool { return j.ID != skipped.ID })
	if rerun.Status != jobDone || rerun.AgentID != theirs.ID || rerun.File != "in/a.md" || len(called) != 1 || called[0].apiKey != "sk-The-1234" {
		t.Fatalf("after opting in: %+v %+v", rerun, called)
	}
	if body := oauthCall(w.h, "GET", "/account/services", nil, session).Body.String(); !strings.Contains(body, "Actions you pay for") || !strings.Contains(body, "<strong>tidy</strong>") || !strings.Contains(body, w.joinName) {
		t.Errorf("services page:\n%s", body)
	}
	// The space owner's own file runs on the owner's agent.
	write(context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: spaceOwner.ID}}), "in/b.md", "b\n")
	if j := find(waitJobs(3), func(j agentJob) bool { return j.File == "in/b.md" }); j.AgentID != ownerAgent.ID {
		t.Errorf("owner's file: %+v", j)
	}
	// Opting out, and removing the agent, end the opt-in.
	optIns, err := w.db.userOptIns(ctx, contributor.ID)
	if err != nil || len(optIns) != 1 || optIns[0].AgentName != "Their MiniMax" || optIns[0].Folder != "in/" {
		t.Fatalf("opt-ins %+v %v", optIns, err)
	}
	if res := oauthCall(w.h, "POST", "/account/agents/optout", url.Values{"csrf": {csrf("optout")}, "optin": {optIns[0].ID}}, session); res.Code != 303 {
		t.Fatalf("opt out: %d", res.Code)
	}
	if _, ok, err := w.db.optInFor(ctx, contributor.ID, w.joinID, "in/", "tidy"); ok || err != nil {
		t.Errorf("still opted in: %v", err)
	}
	if err := w.db.saveOptIn(ctx, actionOptIn{ID: strings.Repeat("e", 32), UserID: contributor.ID, SpaceID: w.joinID, Folder: "in/", Action: "tidy", AgentID: theirs.ID, Created: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := w.db.saveOptIn(ctx, actionOptIn{ID: strings.Repeat("f", 32), UserID: contributor.ID, SpaceID: w.joinID, Folder: "in/", Action: "tidy", AgentID: ownerAgent.ID, Created: time.Now()}); err == nil {
		t.Error("opted in with someone else's agent")
	}
	if err := w.db.removeUserAgent(ctx, contributor.ID, theirs.ID); err != nil {
		t.Fatal(err)
	}
	if optIns, err := w.db.userOptIns(ctx, contributor.ID); err != nil || len(optIns) != 0 {
		t.Errorf("opt-ins after removing the agent: %+v %v", optIns, err)
	}
}
