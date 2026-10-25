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

func (m *memoryJobStore) tokensSince(_ context.Context, space string, since time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, j := range m.jobs {
		if j.SpaceID == space && !j.FinishedAt.IsZero() && !j.FinishedAt.Before(since) {
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
	zone, _ := time.LoadLocation("Pacific/Auckland")
	settings := &agentSettings{provider: "minimax", app: "MiniMax", model: "MiniMax-M3", maxTokens: 4096, zone: zone, owners: []string{"dave"}}
	runner, err := newAgentRunner(s, settings, model, store)
	if err != nil {
		t.Fatal(err)
	}
	runner.owner = func(context.Context, string) (string, string) { return "dave", "dave@example.com" }
	runner.retryWait = func(int) time.Duration { return time.Millisecond }
	s.agents.Store(runner)
	runner.start()
	w := &runnerWorld{t: t, s: s, repo: repo, model: model, store: store, runner: runner}
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
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("secret-key\n"), 0600)
	settings, err := (&agentsConfig{Provider: "minimax", Owners: []string{"dave"}, BaseURL: server.URL + "/anthropic", APIKeyFile: key}).settings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.model != "MiniMax-M3" || settings.app != "MiniMax" || settings.maxTokens != defaultAgentMaxTokens || settings.zone.String() != "Pacific/Auckland" {
		t.Errorf("defaults %+v", settings)
	}
	c := newMessagesClient(settings)
	c.http = server.Client()
	resp, err := c.send(context.Background(), agentRequest{Model: settings.model, MaxTokens: 100, Messages: []agentMessage{{Role: "user", Content: []json.RawMessage{textBlock("hello")}}}})
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
	minus := int64(-1)
	o := []string{"dave"}
	for _, bad := range []agentsConfig{{Provider: "openai", Owners: o, APIKeyFile: key}, {Provider: "minimax", Owners: o}, {Provider: "minimax", Owners: o, APIKeyFile: key, BaseURL: "http://example.com"},
		{Provider: "anthropic", Owners: o, APIKeyFile: key, Timezone: "Mars/Olympus"}, {Provider: "anthropic", Owners: o, APIKeyFile: key, MaxTokens: 10}, {Provider: "anthropic", Owners: o, APIKeyFile: key, DailyTokenCap: &minus}} {
		if _, err := bad.settings(); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// The key pays only for the spaces of agents.owners: other owners' actions
// queue nothing, and an owner without a username never matches.
func TestRunnerOtherOwners(t *testing.T) {
	for _, owner := range [][2]string{{"mallory", "mallory@example.com"}, {"", "dave@example.com"}} {
		w := newRunnerWorld(t)
		w.runner.owner = func(context.Context, string) (string, string) { return owner[0], owner[1] }
		w.write("hr/.metatrash.json", tidyConfig)
		w.write("hr/inbound/a.md", "a\n")
		time.Sleep(200 * time.Millisecond)
		if jobs := len(w.store.list("")); jobs != 0 || len(w.model.requests) != 0 || w.exists("hr/runner.md") {
			t.Errorf("owner %v: %d jobs, %d model calls", owner, jobs, len(w.model.requests))
		}
	}
	// A job queued before the owner left agents.owners is skipped unrun.
	w := newRunnerWorld(t)
	w.write("hr/.metatrash.json", tidyConfig)
	w.runner.owner = func(context.Context, string) (string, string) { return "mallory", "" }
	w.write("hr/inbound/a.md", "a\n")
	_ = w.store.add(context.Background(), agentJob{ID: strings.Repeat("c", 32), SpaceID: w.repo.id, Folder: "hr/", Action: "tidy-draft", File: "hr/inbound/a.md", Status: jobQueued, QueuedAt: time.Now()})
	w.runner.poke()
	if jobs := w.wait(1); jobs[0].Status != jobSkipped || !strings.Contains(jobs[0].Error, "not enabled for this space's owner") || len(w.model.requests) != 0 {
		t.Errorf("jobs %+v", jobs)
	}
}

func TestAgentsSettings(t *testing.T) {
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("sk-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := (&agentsConfig{Enabled: true, Provider: " MiniMax", Owners: []string{"dave-zap"}, APIKeyFile: key}).settings()
	if err != nil || s.provider != "minimax" || s.app != "MiniMax" || !s.allows("dave-zap") || s.allows("dave") || s.allows("") || s.dailyCap != 2000000 {
		t.Fatalf("%+v %v", s, err)
	}
	none := int64(0)
	if s, err := (&agentsConfig{Provider: "minimax", Owners: []string{"dave"}, APIKeyFile: key, DailyTokenCap: &none}).settings(); err != nil || s.dailyCap != 0 {
		t.Errorf("no cap: %+v %v", s, err)
	}
	for c, want := range map[*agentsConfig]string{
		{Provider: "openai", Owners: []string{"dave"}, APIKeyFile: key}:  `not "openai"`,
		{Provider: "minimax", APIKeyFile: key}:                           "owners must list",
		{Provider: "minimax", Owners: []string{"Dave"}, APIKeyFile: key}: `"Dave" is not a username`,
	} {
		if _, err := c.settings(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want %q", c, err, want)
		}
	}
}
