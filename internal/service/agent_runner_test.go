package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"metatrash.com/metatrash"
)

// memoryJobStore is the job queue for tests.
type memoryJobStore struct {
	mu   sync.Mutex
	jobs map[string]agentJob
	rows map[string]actionSchedule
}

func scheduleKey(s actionSchedule) string { return s.SpaceID + "/" + s.Folder + "/" + s.Action }

func (m *memoryJobStore) scheduleList(spaceID string, due time.Time) []actionSchedule {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []actionSchedule
	for _, s := range m.rows {
		if (spaceID == "" || s.SpaceID == spaceID) && (due.IsZero() || !s.NextRun.After(due)) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(a, b int) bool { return scheduleKey(out[a]) < scheduleKey(out[b]) })
	return out
}

func (m *memoryJobStore) schedules(_ context.Context, spaceID string) ([]actionSchedule, error) {
	return m.scheduleList(spaceID, time.Time{}), nil
}

func (m *memoryJobStore) dueSchedules(_ context.Context, now time.Time, _ int) ([]actionSchedule, error) {
	return m.scheduleList("", now), nil
}

func (m *memoryJobStore) addSchedule(_ context.Context, s actionSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string]actionSchedule{}
	}
	m.rows[scheduleKey(s)] = s
	return nil
}

func (m *memoryJobStore) setSchedule(ctx context.Context, s actionSchedule) error {
	return m.addSchedule(ctx, s)
}

func (m *memoryJobStore) removeSchedule(_ context.Context, s actionSchedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, scheduleKey(s))
	return nil
}

func (m *memoryJobStore) advanceSchedule(_ context.Context, s actionSchedule, next, ran time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.rows[scheduleKey(s)]
	if !ok || !cur.NextRun.Equal(s.NextRun) {
		return false, nil
	}
	cur.NextRun, cur.LastRun = next, ran
	m.rows[scheduleKey(s)] = cur
	return true, nil
}

func (m *memoryJobStore) add(_ context.Context, j agentJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = j
	return nil
}

func (m *memoryJobStore) queuedSince(_ context.Context, space, folder string, since time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if j.SpaceID == space && j.Folder == folder && !j.QueuedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (m *memoryJobStore) tokensSince(_ context.Context, agent string, since time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, j := range m.jobs {
		if j.AgentID == agent && !j.FinishedAt.IsZero() && !j.FinishedAt.Before(since) {
			n += j.InputTokens + j.OutputTokens
		}
	}
	return n, nil
}

func (m *memoryJobStore) list(status string) []agentJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []agentJob
	for _, j := range m.jobs {
		if status == "" || j.Status == status {
			out = append(out, j)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		return out[a].QueuedAt.Before(out[b].QueuedAt) || out[a].QueuedAt.Equal(out[b].QueuedAt) && out[a].ID < out[b].ID
	})
	return out
}

func (m *memoryJobStore) queued(_ context.Context, limit int) ([]agentJob, error) {
	jobs := m.list(jobQueued)
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	return jobs, nil
}

func (m *memoryJobStore) running(context.Context) ([]agentJob, error) { return m.list(jobRunning), nil }

func (m *memoryJobStore) get(_ context.Context, id string) (agentJob, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok, nil
}

func (m *memoryJobStore) claim(_ context.Context, id string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok || j.Status != jobQueued {
		return false, nil
	}
	j.Status, j.StartedAt = jobRunning, at
	m.jobs[id] = j
	return true, nil
}

func (m *memoryJobStore) finish(_ context.Context, j agentJob) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs[j.ID] = j
	return nil
}

// fakeModel answers each request with the next scripted turn, given the
// conversation so far.
type fakeModel struct {
	mu       sync.Mutex
	turns    []func(req agentRequest) (agentResponse, error)
	requests []agentRequest
}

func (f *fakeModel) send(_ context.Context, req agentRequest) (agentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := req
	copied.Messages = append([]agentMessage(nil), req.Messages...)
	f.requests = append(f.requests, copied)
	if len(f.turns) == 0 {
		return agentResponse{Content: []json.RawMessage{textBlock("Nothing more to do.")}, StopReason: "end_turn"}, nil
	}
	turn := f.turns[0]
	f.turns = f.turns[1:]
	return turn(req)
}

func toolUse(id, name string, input any) json.RawMessage {
	b, _ := json.Marshal(input)
	return rawBlock(contentBlock{Type: "tool_use", ID: id, Name: name, Input: b})
}

func useTools(uses ...json.RawMessage) func(agentRequest) (agentResponse, error) {
	return func(agentRequest) (agentResponse, error) {
		content := append([]json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"plan","signature":"sig"}`)}, uses...)
		return agentResponse{Content: content, StopReason: "tool_use", Usage: agentUsage{InputTokens: 1000, OutputTokens: 50}}, nil
	}
}

// lastResults decodes the tool results of the newest user message.
func lastResults(t *testing.T, req agentRequest) []contentBlock {
	t.Helper()
	m := req.Messages[len(req.Messages)-1]
	if m.Role != "user" {
		t.Fatalf("last message is %s", m.Role)
	}
	var out []contentBlock
	for _, raw := range m.Content {
		var b contentBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

type runnerWorld struct {
	t      *testing.T
	s      *Service
	repo   *repository
	model  *fakeModel
	store  *memoryJobStore
	runner *agentRunner
	do     func(ctx context.Context, op string, in Input, code string) any
	mu     sync.Mutex
	// agents are everyone's agents; models records each job's agent.
	agents []userAgent
	models []agentModel
	// optIns maps user/folder/action to the agent they opted in with;
	// notices records the notices sent, as "user: why".
	optIns  map[string]string
	notices []string
	// zones are the spaces' own time zones.
	zones map[string]string
}

// alice is a member of the test space, who brings files of her own.
var aliceID = strings.Repeat("c", 32)

// testAgentSettings are runner settings with a fresh sealing key.
func testAgentSettings(t *testing.T) *agentSettings {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agents-key")
	if err := os.WriteFile(path, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seal, err := loadSealKey(path)
	if err != nil {
		t.Fatal(err)
	}
	zone, _ := time.LoadLocation("Pacific/Auckland")
	return &agentSettings{zone: zone, seal: seal, keyFile: path}
}

func testUserAgent(t *testing.T, settings *agentSettings, userID, name, provider, model string) userAgent {
	t.Helper()
	u, err := newUserAgent(settings.seal, userID, agentForm{Name: name, Provider: provider, Model: model, Key: "sk-" + name[:3] + "-1234"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func newRunnerWorld(t *testing.T) *runnerWorld {
	t.Helper()
	s, _, do := folderFilesHarness(t)
	ctx := context.Background()
	repo, err := provision(ctx, filepath.Join(t.TempDir(), "space.git"), s.config.Defaults.Storage, metatrash.SpaceAGENTS, "HR", true)
	if err != nil {
		t.Fatal(err)
	}
	repo.id, repo.owner = strings.Repeat("a", 32), strings.Repeat("b", 32)
	repo.committed = func(ctx context.Context, old, state string) { s.agentCommitted(ctx, repo, old, state) }
	s.ownedMu.Lock()
	s.ownedRepos[repo.id] = repo
	s.ownedMu.Unlock()
	model, store := &fakeModel{}, &memoryJobStore{jobs: map[string]agentJob{}}
	runner, err := newAgentRunner(s, testAgentSettings(t), store)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{repo.owner: "dave", aliceID: "alice"}
	runner.owner = func(_ context.Context, id string) (string, string) { return names[id], names[id] + "@example.com" }
	s.usernameHook = func(id string) string { return names[id] }
	s.userIDHook = func(name string) string {
		for id, n := range names {
			if n == name {
				return id
			}
		}
		return ""
	}
	agent := testUserAgent(t, runner.settings, repo.owner, "MiniMax main", "minimax", "MiniMax-M3")
	agent.Default = true
	w := &runnerWorld{t: t, s: s, repo: repo, model: model, store: store, runner: runner, agents: []userAgent{agent}, optIns: map[string]string{}, zones: map[string]string{}}
	runner.spaceZone = func(_ context.Context, spaceID string) string {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.zones[spaceID]
	}
	runner.agentsOf = func(_ context.Context, userID string) ([]userAgent, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		var out []userAgent
		for _, u := range w.agents {
			if u.UserID == userID {
				out = append(out, u)
			}
		}
		return out, nil
	}
	runner.optInOf = func(_ context.Context, userID, _, folder, action string) (string, bool, error) {
		w.mu.Lock()
		defer w.mu.Unlock()
		id, ok := w.optIns[userID+"/"+folder+action]
		return id, ok, nil
	}
	runner.notice = func(_ context.Context, userID string, _ agentJob, why string) {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.notices = append(w.notices, names[userID]+": "+why)
	}
	runner.providerFor = func(m agentModel) agentProvider {
		w.mu.Lock()
		w.models = append(w.models, m)
		w.mu.Unlock()
		return model
	}
	runner.retryWait = func(int) time.Duration { return time.Millisecond }
	s.agents.Store(runner)
	runner.start()
	w.do = func(ctx context.Context, op string, in Input, code string) any {
		t.Helper()
		head, err := repo.head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in.IfInState = head
		value, err := s.dispatchRepository(ctx, repo, repo.id, op, in)
		if code == "" && err != nil {
			t.Fatalf("%s %s%s: %v", op, in.Path, in.From, err)
		}
		if p, ok := err.(*Error); code != "" && (!ok || p.Code != code) {
			t.Fatalf("%s %s%s: got %v, want %s", op, in.Path, in.From, err, code)
		}
		return value
	}
	_ = do
	return w
}

func (w *runnerWorld) write(path, text string) {
	w.t.Helper()
	ctx := context.Background()
	if isFolderConfig(path) {
		ctx = asOwner(ctx, w.repo)
	}
	w.do(ctx, "write", Input{Path: path, Text: &text}, "")
}

// as is a context writing as user through Metatrash Sync.
func as(userID string) context.Context {
	ctx := context.WithValue(context.Background(), personWriteKey{}, true)
	return context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{oauth: &oauthIdentity{UserID: userID}})
}

func (w *runnerWorld) writeAs(userID, path, text string) {
	w.t.Helper()
	w.do(as(userID), "write", Input{Path: path, Text: &text}, "")
}

func (w *runnerWorld) read(path string) string {
	w.t.Helper()
	return w.do(context.Background(), "read", Input{Path: path}, "").(ReadResult).File.Text
}

func (w *runnerWorld) exists(path string) bool {
	_, files, err := w.repo.snapshot(context.Background(), "")
	if err != nil {
		w.t.Fatal(err)
	}
	_, ok := files[path]
	return ok
}

// wait waits until n jobs have finished and none is queued or running.
func (w *runnerWorld) wait(n int) []agentJob {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		jobs := w.store.list("")
		finished := 0
		for _, j := range jobs {
			if j.Status != jobQueued && j.Status != jobRunning {
				finished++
			}
		}
		if finished >= n && finished == len(jobs) && w.exists("hr/"+runnerLogName) {
			time.Sleep(50 * time.Millisecond) // let the runner.md append land
			return w.store.list("")
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatalf("jobs did not finish: %+v", w.store.list(""))
	return nil
}

const tidyConfig = `{"actions":[{"name":"tidy-draft","on":"inbound/*.md","do":"Rewrite this draft. Put the result in for-review/ under the same name, then move the draft to completed/."}]}`

func TestRunnerTidyDraft(t *testing.T) {
	w := newRunnerWorld(t)
	w.write("hr/.metatrash.json", tidyConfig)
	w.write("hr/AGENTS.md", "# HR policies\n\nDrafts arrive in inbound/.\n")
	w.write("other/secret.md", "not for the agent\n")
	var state string
	w.model.turns = []func(agentRequest) (agentResponse, error){
		useTools(toolUse("t1", "list", map[string]any{"prefix": "inbound/"}), toolUse("t2", "read", map[string]any{"path": "../other/secret.md"})),
		func(req agentRequest) (agentResponse, error) {
			results := lastResults(t, req)
			if len(results) != 2 || results[0].IsError || !results[1].IsError {
				t.Errorf("results %+v", results)
			}
			var list struct {
				Space *string `json:"space"`
				State string  `json:"state"`
				Files []File  `json:"files"`
			}
			if err := json.Unmarshal([]byte(results[0].Content), &list); err != nil {
				t.Fatal(err)
			}
			if list.Space != nil || len(list.Files) != 3 || list.Files[2].Path != "inbound/leave.md" {
				t.Errorf("list for the agent: %s", results[0].Content)
			}
			state = list.State
			return useTools(toolUse("t3", "search", map[string]any{"query": "not for the agent"}), toolUse("t4", "read", map[string]any{"path": "inbound/leave.md"}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastResults(t, req)
			if !strings.Contains(results[0].Content, `"matches":[]`) {
				t.Errorf("search reached outside the folder: %s", results[0].Content)
			}
			if !strings.Contains(results[1].Content, "leave draft") || strings.Contains(results[1].Content, "hr/") {
				t.Errorf("read: %s", results[1].Content)
			}
			return useTools(
				toolUse("t5", "write", map[string]any{"path": "for-review/leave.md", "text": "# Leave\n\nTidy.\n", "ifInState": state}),
				toolUse("t6", "move", map[string]any{"from": "inbound/leave.md", "to": "completed/leave.md", "ifInState": state}),
				toolUse("t7", "write", map[string]any{"path": ".metatrash.json", "text": "{}", "ifInState": state}),
				toolUse("t8", "append", map[string]any{"path": "runner.md", "text": "fake entry"}),
				toolUse("t9", "list", map[string]any{"space": "public"}),
			)(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastResults(t, req)
			for i, want := range []bool{false, false, true, true, true} {
				if results[i].IsError != want {
					t.Errorf("result %d: error %v, want %v: %s", i, results[i].IsError, want, results[i].Content)
				}
			}
			if !strings.Contains(results[2].Content, "protected_file") || !strings.Contains(results[3].Content, "protected_file") {
				t.Errorf("own files: %s %s", results[2].Content, results[3].Content)
			}
			return agentResponse{Content: []json.RawMessage{textBlock("Rewrote the leave draft into for-review/leave.md and moved it to completed/.")}, StopReason: "end_turn", Usage: agentUsage{InputTokens: 2000, OutputTokens: 80}}, nil
		},
	}
	w.write("hr/inbound/leave.md", "leave draft\n")
	jobs := w.wait(1)
	if len(jobs) != 1 || jobs[0].Status != jobDone || jobs[0].Steps != 9 || jobs[0].InputTokens != 5000 || jobs[0].File != "hr/inbound/leave.md" {
		t.Fatalf("jobs %+v", jobs)
	}
	if w.read("hr/for-review/leave.md") != "# Leave\n\nTidy.\n" || !w.exists("hr/completed/leave.md") || w.exists("hr/inbound/leave.md") {
		t.Error("the job's changes are missing")
	}
	log := w.read("hr/runner.md")
	for _, want := range []string{"· tidy-draft · done", "- File: inbound/leave.md", "for-review/leave.md (write)", "inbound/leave.md → completed/leave.md (moved)", "Steps: 9 · tokens: 5000 in, 230 out · MiniMax-M3", "Rewrote the leave draft"} {
		if !strings.Contains(log, want) {
			t.Errorf("runner.md lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "fake entry") {
		t.Error("the agent wrote runner.md")
	}
	// The prompt: preamble, the folder's AGENTS.md, the instructions and the trigger.
	first := w.model.requests[0]
	var system contentBlock
	_ = json.Unmarshal(first.System[0], &system)
	if !strings.Contains(system.Text, `"tidy-draft"`) || !strings.Contains(system.Text, "Drafts arrive in inbound/.") || !strings.Contains(system.Text, "owned by dave") {
		t.Errorf("system prompt: %s", system.Text)
	}
	if user := string(first.Messages[0].Content[0]); !strings.Contains(user, "Rewrite this draft") || !strings.Contains(user, "inbound/leave.md arrived") {
		t.Errorf("first message: %s", user)
	}
	for _, tool := range first.Tools {
		if strings.Contains(string(tool), `"space"`) {
			t.Errorf("tool offers space: %s", tool)
		}
	}
	if len(first.Tools) != 9 {
		t.Errorf("%d tools", len(first.Tools))
	}
	// Thinking blocks go back unchanged.
	if !strings.Contains(string(w.model.requests[1].Messages[1].Content[0]), `"signature":"sig"`) {
		t.Error("thinking block not passed back")
	}
	// The agent's changes are signed as MiniMax for dave; the runner's own
	// commits (its changes and runner.md) queued no further jobs.
	_, files, _ := w.repo.snapshot(context.Background(), "")
	history := w.do(context.Background(), "history", Input{ID: files["hr/for-review/leave.md"].ID}, "").(HistoryResult)
	if by := history.Entries[0].By; by == nil || by.Name != "dave" || by.App != "MiniMax" || by.Kind != "agent" {
		t.Errorf("by %+v", history.Entries[0].By)
	}
	history = w.do(context.Background(), "history", Input{ID: files["hr/runner.md"].ID}, "").(HistoryResult)
	if by := history.Entries[0].By; by == nil || by.App != runnerApp {
		t.Errorf("runner.md by %+v", history.Entries[0].By)
	}
	if n := len(w.store.list("")); n != 1 {
		t.Errorf("%d jobs", n)
	}
}

func TestRunnerLimitsAndFailures(t *testing.T) {
	w := newRunnerWorld(t)
	w.write("hr/.metatrash.json", `{"actions":[{"name":"loop","on":"inbound/*.md","do":"Keep listing.","steps":2}]}`)
	busy := useTools(toolUse("x", "list", map[string]any{}))
	w.model.turns = []func(agentRequest) (agentResponse, error){busy, busy, busy, busy}
	w.write("hr/inbound/a.md", "a\n")
	jobs := w.wait(1)
	if jobs[0].Status != jobFailed || !strings.Contains(jobs[0].Error, "limit of 2 tool calls") || jobs[0].Steps != 2 {
		t.Errorf("step limit: %+v", jobs[0])
	}
	// Two retryable failures are retried; a third fails the job.
	overloaded := func(agentRequest) (agentResponse, error) {
		return agentResponse{}, &providerError{status: 529, message: "overloaded", retry: true}
	}
	w.model.turns = []func(agentRequest) (agentResponse, error){overloaded, overloaded}
	w.write("hr/inbound/b.md", "b\n")
	jobs = w.wait(2)
	if jobs[1].Status != jobDone {
		t.Errorf("after two retries: %+v", jobs[1])
	}
	w.model.turns = []func(agentRequest) (agentResponse, error){overloaded, overloaded, overloaded}
	w.write("hr/inbound/c.md", "c\n")
	jobs = w.wait(3)
	if jobs[2].Status != jobFailed || !strings.Contains(jobs[2].Error, "529") {
		t.Errorf("after three failures: %+v", jobs[2])
	}
	if log := w.read("hr/runner.md"); !strings.Contains(log, "· loop · failed") || !strings.Contains(log, "Error: model provider answered 529: overloaded") {
		t.Errorf("runner.md:\n%s", log)
	}
}

func TestRunnerFolderHourLimit(t *testing.T) {
	w := newRunnerWorld(t)
	w.write("hr/.metatrash.json", tidyConfig)
	for i := 0; i < maxJobsPerFolderHour; i++ {
		_ = w.store.add(context.Background(), agentJob{ID: fmt.Sprintf("%032d", i), SpaceID: w.repo.id, Folder: "hr/", Action: "tidy-draft", File: "hr/inbound/old.md", Status: jobDone, QueuedAt: time.Now()})
	}
	w.write("hr/inbound/a.md", "a\n")
	w.write("hr/inbound/b.md", "b\n")
	deadline := time.Now().Add(5 * time.Second)
	for !w.exists("hr/runner.md") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	skipped := w.store.list(jobSkipped)
	if len(skipped) != 1 || len(w.model.requests) != 0 {
		t.Fatalf("skipped %+v, %d model calls", skipped, len(w.model.requests))
	}
	if log := w.read("hr/runner.md"); !strings.Contains(log, "· tidy-draft · skipped") || !strings.Contains(log, "20 jobs in the last hour") {
		t.Errorf("runner.md:\n%s", log)
	}
}

func TestUnpin(t *testing.T) {
	got, err := unpin([]byte(`{"space":"x","state":"s","files":[{"path":"hr/a.md"}],"removed":["hr/b.md"],"file":{"path":"hr/c.md","text":"see hr/d.md"},"hint":"push hr/ soon","n":12345678901234567890}`), "hr/")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"file":{"path":"c.md","text":"see hr/d.md"},"files":[{"path":"a.md"}],"hint":"push  soon","n":12345678901234567890,"removed":["b.md"],"state":"s"}`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestMessagesClient(t *testing.T) {
	var got struct {
		key, version string
		body         agentRequest
	}
	status := 200
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.key, got.version = r.Header.Get("x-api-key"), r.Header.Get("anthropic-version")
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		if r.URL.Path != "/anthropic/v1/messages" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
		if status != 200 {
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","thinking":"t","signature":"s"},{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":5}}`))
	}))
	defer server.Close()
	m := agentModel{endpoint: server.URL + "/anthropic/v1/messages", apiKey: "secret-key", model: "MiniMax-M3"}
	c := newMessagesClient(m, true)
	c.http.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig
	resp, err := c.send(context.Background(), agentRequest{Model: m.model, MaxTokens: 100, Messages: []agentMessage{{Role: "user", Content: []json.RawMessage{textBlock("hello")}}}})
	if err != nil || len(resp.Content) != 2 || resp.Usage.input() != 15 || got.key != "secret-key" || got.version != "2023-06-01" || got.body.Model != "MiniMax-M3" {
		t.Errorf("resp %+v err %v got %+v", resp, err, got)
	}
	status = 429
	_, err = c.send(context.Background(), agentRequest{})
	var p *providerError
	if !errors.As(err, &p) || !p.retry || !strings.Contains(err.Error(), "429: slow down") {
		t.Errorf("429: %v", err)
	}
	status = 400
	if _, err = c.send(context.Background(), agentRequest{}); !errors.As(err, &p) || p.retry {
		t.Errorf("400: %v", err)
	}
	// Without the test switch, the client refuses addresses that are not public.
	if _, err := newMessagesClient(m, false).send(context.Background(), agentRequest{}); err == nil {
		t.Error("reached a loopback endpoint")
	}
}

// A job runs on the owner's default agent, or on the agent for the model an
// action names; without one it is skipped, and the daily cap is per agent.
func TestRunnerChoosesAgent(t *testing.T) {
	w := newRunnerWorld(t)
	claude := testUserAgent(t, w.runner.settings, w.repo.owner, "Claude review", "anthropic", "claude-sonnet-5-5")
	w.agents = append(w.agents, claude)
	w.write("hr/.metatrash.json", `{"actions":[{"name":"tidy","on":"a/*.md","do":"Tidy."},{"name":"review","on":"b/*.md","do":"Review.","model":"anthropic/claude-sonnet-5-5"},{"name":"other","on":"c/*.md","do":"Go.","model":"minimax/MiniMax-M3.1-Flash"}]}`)
	w.write("hr/a/1.md", "x\n")
	w.wait(1)
	w.write("hr/b/1.md", "x\n")
	w.wait(2)
	w.write("hr/c/1.md", "x\n")
	jobs := w.wait(3)
	if len(w.models) != 2 || w.models[0].name != "MiniMax main" || w.models[0].apiKey != "sk-Min-1234" || w.models[0].endpoint != "https://api.minimax.io/anthropic/v1/messages" ||
		w.models[1].name != "Claude review" || w.models[1].app != "Claude" || w.models[1].model != "claude-sonnet-5-5" {
		t.Errorf("models %+v", w.models)
	}
	if jobs[0].AgentID != w.agents[0].ID || jobs[1].AgentID != claude.ID || jobs[2].Status != jobSkipped || !strings.Contains(jobs[2].Error, "needs a MiniMax MiniMax-M3.1-Flash agent") {
		t.Errorf("jobs %+v", jobs)
	}
	if log := w.read("hr/runner.md"); !strings.Contains(log, "· claude-sonnet-5-5") || !strings.Contains(log, "Your account → Services → Agents") {
		t.Errorf("runner.md:\n%s", log)
	}
	// Over the agent's daily cap: skipped before any call.
	w.mu.Lock()
	w.agents[0].DailyCap = 100
	w.mu.Unlock()
	_ = w.store.add(context.Background(), agentJob{ID: strings.Repeat("d", 32), SpaceID: w.repo.id, Folder: "hr/", Action: "tidy", File: "hr/a/old.md", AgentID: w.agents[0].ID, Status: jobDone, QueuedAt: time.Now(), FinishedAt: time.Now(), InputTokens: 90, OutputTokens: 10})
	w.write("hr/a/2.md", "x\n")
	if j := w.job(5, "hr/a/2.md"); j.Status != jobSkipped || !strings.Contains(j.Error, "used 100 tokens today, its daily cap is 100") || len(w.models) != 2 {
		t.Errorf("cap: %+v", j)
	}
	// No agents at all.
	w.mu.Lock()
	w.agents = nil
	w.mu.Unlock()
	w.write("hr/a/3.md", "x\n")
	if j := w.job(6, "hr/a/3.md"); j.Status != jobSkipped || !strings.Contains(j.Error, "dave has no agent for this action: it needs an agent") {
		t.Errorf("no agent: %+v", j)
	}
}

// job waits for n jobs and returns the one for file.
func (w *runnerWorld) job(n int, file string) agentJob {
	w.t.Helper()
	for _, j := range w.wait(n) {
		if j.File == file {
			return j
		}
	}
	w.t.Fatalf("no job for %s", file)
	return agentJob{}
}

func TestAgentsSettings(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte(strings.Repeat("0f", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := (&agentsConfig{Enabled: true, KeyFile: key}).settings()
	if err != nil || s.seal == nil || s.zone.String() != "Pacific/Auckland" {
		t.Fatalf("%+v %v", s, err)
	}
	short := filepath.Join(t.TempDir(), "short")
	_ = os.WriteFile(short, []byte("abcd"), 0o600)
	for c, want := range map[*agentsConfig]string{
		{}:                                       "keyFile is required",
		{KeyFile: short}:                         "64 hex digits",
		{KeyFile: key, Timezone: "Mars/Olympus"}: "IANA",
		{KeyFile: key, Provider: json.RawMessage(`"minimax"`)}:  "provider is no longer a server setting",
		{KeyFile: key, Owners: json.RawMessage(`["dave-zap"]`)}: "owners is no longer a server setting",
		{KeyFile: key, APIKeyFile: json.RawMessage(`"/etc/x"`)}: "apiKeyFile is no longer",
	} {
		if _, err := c.settings(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want %q", c, err, want)
		}
	}
}

// Agents from the Add agent form: checks, defaults, sealing and choosing.
func TestUserAgents(t *testing.T) {
	settings := testAgentSettings(t)
	u, err := newUserAgent(settings.seal, "u1", agentForm{Name: " MiniMax main ", Provider: "MiniMax", Key: " sk-abc-9876 ", Cap: "1,000,000"}, time.Now())
	if err != nil || u.Name != "MiniMax main" || u.Provider != "minimax" || u.Model != "MiniMax-M3" || u.DailyCap != 1000000 || u.KeyHint != "9876" || u.BaseURL != "" || u.Ref() != "minimax/MiniMax-M3" {
		t.Fatalf("%+v %v", u, err)
	}
	if key, err := openAgentKey(settings.seal, "u1", u.ID, u.Sealed); err != nil || key != "sk-abc-9876" {
		t.Errorf("open: %q %v", key, err)
	}
	if _, err := openAgentKey(settings.seal, "u2", u.ID, u.Sealed); err == nil {
		t.Error("a key opened for another user")
	}
	for f, want := range map[agentForm]string{
		{Name: "", Provider: "minimax", Key: "k"}:                                           "name",
		{Name: "a", Provider: "openai", Key: "k"}:                                           "MiniMax or Anthropic",
		{Name: "a", Provider: "minimax", Model: "bad model", Key: "k"}:                      "model name",
		{Name: "a", Provider: "minimax", Key: "two words"}:                                  "one line",
		{Name: "a", Provider: "minimax", Key: "k", Cap: "-5"}:                               "whole number",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "http://api.example.com"}:      "https",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "https://127.0.0.1/anthropic"}: "public internet",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "https://10.1.2.3/x"}:          "public internet",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "https://localhost/x"}:         "public internet",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "https://metadata.internal/x"}: "public internet",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "https://[::1]/x"}:             "public internet",
		{Name: "a", Provider: "minimax", Key: "k", Endpoint: "https://169.254.169.254/x"}:   "public internet",
	} {
		if _, err := newUserAgent(settings.seal, "u1", f, time.Now()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want %q", f, err, want)
		}
	}
	if u, err := newUserAgent(settings.seal, "u1", agentForm{Name: "Own", Provider: "anthropic", Key: "k", Endpoint: "https://llm.example.com/v1/", Cap: "0"}, time.Now()); err != nil || u.BaseURL != "https://llm.example.com/v1" || u.DailyCap != 0 || u.Model != "claude-sonnet-5-5" {
		t.Errorf("own endpoint: %+v %v", u, err)
	}
	a := userAgent{ID: "a", Provider: "minimax", Model: "MiniMax-M3"}
	b := userAgent{ID: "b", Provider: "minimax", Model: "MiniMax-M3", Default: true}
	c := userAgent{ID: "c", Provider: "anthropic", Model: "claude-sonnet-5-5"}
	for want, cases := range map[string][]string{"b": {"", "minimax/MiniMax-M3"}, "c": {"anthropic/claude-sonnet-5-5"}, "": {"anthropic/claude-opus-5-5"}} {
		for _, ref := range cases {
			got, ok := pickAgent([]userAgent{a, b, c}, ref)
			if ok != (want != "") || got.ID != want {
				t.Errorf("pickAgent(%q) = %q, want %q", ref, got.ID, want)
			}
		}
	}
	if err := checkActions([]folderAction{{Name: "x", On: actionPatterns{"*.md"}, Do: "y", Model: "openai/gpt"}}); err == nil {
		t.Error("accepted an unknown provider")
	}
}

// With "pays": "contributor", whoever brings a file pays once they opt in;
// until then their files are skipped and they get a notice. The owner's own
// files, and files nobody clear brought, run on the owner's agent.
func TestRunnerContributorPays(t *testing.T) {
	w := newRunnerWorld(t)
	mine := testUserAgent(t, w.runner.settings, aliceID, "Alice MiniMax", "minimax", "MiniMax-M3")
	mine.Default = true
	w.agents = append(w.agents, mine)
	w.write("hr/.metatrash.json", `{"actions":[{"name":"tidy","on":"in/*.md","do":"Tidy.","model":"minimax/MiniMax-M3","pays":"contributor"}]}`)
	w.writeAs(aliceID, "hr/in/a.md", "a\n")
	skipped := w.job(1, "hr/in/a.md")
	if skipped.Status != jobSkipped || skipped.Contributor != aliceID || !strings.Contains(skipped.Error, "alice has not opted in") || !strings.Contains(skipped.Error, "/account/actions/"+skipped.ID) || len(w.models) != 0 {
		t.Fatalf("not opted in: %+v", skipped)
	}
	if len(w.notices) != 1 || !strings.HasPrefix(w.notices[0], "alice: skipped: whoever brings a file pays") {
		t.Errorf("notices %q", w.notices)
	}
	// Opted in: the skipped file runs again on her agent.
	w.mu.Lock()
	w.optIns[aliceID+"/hr/tidy"] = mine.ID
	w.mu.Unlock()
	if err := w.runner.requeue(context.Background(), skipped); err != nil {
		t.Fatal(err)
	}
	var done agentJob
	for _, j := range w.wait(2) {
		if j.ID != skipped.ID {
			done = j
		}
	}
	if done.Status != jobDone || done.AgentID != mine.ID || done.Contributor != aliceID || len(w.models) != 1 || w.models[0].apiKey != "sk-Ali-1234" {
		t.Fatalf("opted in: %+v %+v", done, w.models)
	}
	if log := w.read("hr/runner.md"); !strings.Contains(log, "- Paid by: alice") {
		t.Errorf("runner.md:\n%s", log)
	}
	var system contentBlock
	_ = json.Unmarshal(w.model.requests[0].System[0], &system)
	if !strings.Contains(system.Text, "owned by dave, on behalf of alice") {
		t.Errorf("system prompt: %s", system.Text)
	}
	// The owner's own file, and one from a space key, run on the owner's agent.
	w.writeAs(w.repo.owner, "hr/in/b.md", "b\n")
	w.do(context.Background(), "write", Input{Path: "hr/in/c.md", Text: ptr("c\n")}, "")
	w.wait(4)
	if len(w.models) != 3 || w.models[1].agentID != w.agents[0].ID || w.models[2].agentID != w.agents[0].ID {
		t.Errorf("owner pays: %+v", w.models)
	}
	// Her chosen agent no longer runs the action's model: skipped, never
	// another agent.
	w.mu.Lock()
	w.agents[1].Model = "MiniMax-M3.1"
	w.mu.Unlock()
	w.writeAs(aliceID, "hr/in/d.md", "d\n")
	if j := w.job(5, "hr/in/d.md"); j.Status != jobSkipped || !strings.Contains(j.Error, "no longer runs MiniMax MiniMax-M3") || len(w.models) != 3 || len(w.notices) != 2 {
		t.Errorf("changed agent: %+v %q", j, w.notices)
	}
	// GitHub pulls have no contributor.
	if contributorOf(context.WithValue(as(aliceID), githubPullKey{}, true)) != "" || contributorOf(as(aliceID)) != aliceID || contributorOf(context.Background()) != "" {
		t.Error("contributorOf")
	}
}

func ptr(s string) *string { return &s }

// A member's own action runs only for that member's files, on their agent.
func TestRunnerMemberAction(t *testing.T) {
	w := newRunnerWorld(t)
	mine := testUserAgent(t, w.runner.settings, aliceID, "Alice Claude", "anthropic", "claude-sonnet-5-5")
	mine.Default = true
	w.agents = append(w.agents, mine)
	w.writeAs(aliceID, "hr/.metatrash.json", `{"actions":[{"name":"mine","on":"in/*.md","do":"Tidy.","member":"alice"}]}`)
	w.writeAs(w.repo.owner, "hr/in/x.md", "x\n")
	w.writeAs(aliceID, "hr/in/y.md", "y\n")
	jobs := w.wait(1)
	if len(jobs) != 1 || jobs[0].File != "hr/in/y.md" || jobs[0].Status != jobDone || jobs[0].AgentID != mine.ID || w.models[0].app != "Claude" {
		t.Fatalf("jobs %+v", jobs)
	}
	_, files, _ := w.repo.snapshot(context.Background(), "")
	if _, ok := files["hr/in/y.md"]; !ok {
		t.Fatal("file missing")
	}
	if log := w.read("hr/runner.md"); !strings.Contains(log, "- Paid by: alice") {
		t.Errorf("runner.md:\n%s", log)
	}
	// Without an agent of hers, her file is skipped and she is told.
	w.mu.Lock()
	w.agents = w.agents[:1]
	w.mu.Unlock()
	w.writeAs(aliceID, "hr/in/z.md", "z\n")
	if j := w.job(2, "hr/in/z.md"); j.Status != jobSkipped || !strings.Contains(j.Error, "alice has no agent for this action") || !strings.Contains(j.Error, "They add one") || len(w.notices) != 1 {
		t.Errorf("no agent: %+v %q", j, w.notices)
	}
}
