package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"metatrash.com/metatrash"
)

// The runner runs folder actions (actions.go). After every commit to an owned
// space, whether it came through MCP, REST or mtsync, the files it brought
// are matched against the actions of the folders they landed in, and one job
// per matching file is queued in metatrash_agent_jobs. Jobs run one at a
// time per space (at most maxParallelJobs at once): an agent loop against
// the model provider whose only tools are the space's own file tools, pinned
// to the action's folder, run in-process through dispatchRepository so every
// rule (ifInState, locks, readOnly files, size limits) applies. A job runs on
// the agent of whoever pays (agent_store.go): the space owner, the person
// who brought the file when the action says "pays": "contributor" and they
// opted in (agent_optins.go), or the member whose own action it is. Changes
// are commits by "<provider> (agent) for <payer>". The runner's commits
// never queue jobs. Actions with "every" run on a schedule instead
// (schedules.go), and jobs may use the web (web_tools.go). When a job ends
// the runner appends its outcome to runner.md in the folder.

const (
	maxParallelJobs      = 2
	maxJobsPerFolderHour = 20
	maxJobInputTokens    = 400000
	maxJobTime           = 10 * time.Minute
	maxToolResultBytes   = 100000
	maxAgentsTextBytes   = 20000
	maxRunnerSummary     = 2000
	providerRetries      = 2
	runnerApp            = "Metatrash runner"
)

type agentRunner struct {
	s        *Service
	settings *agentSettings
	store    agentJobStore
	// agentsOf lists a user's agents (userAgents); providerFor calls one;
	// optInOf returns the agent a user chose for an action (optInFor), or
	// false. Tests replace them.
	agentsOf    func(ctx context.Context, userID string) ([]userAgent, error)
	providerFor func(agentModel) agentProvider
	optInOf     func(ctx context.Context, userID, spaceID, folder, action string) (agentID string, ok bool, err error)
	// notice emails a user about a job of theirs that did not run (best
	// effort, at most once a day per action); tests replace it.
	notice func(ctx context.Context, userID string, j agentJob, why string)
	// origin is the site address, for links in notices and runner.md.
	origin  string
	tools   []json.RawMessage
	schemas map[string]*jsonschema.Resolved
	// webTools and webSchemas add the web tools that are set up (nil when
	// none); webRefs keeps references and fetcher downloads. Tests replace
	// them.
	webTools   []json.RawMessage
	webSchemas map[string]*jsonschema.Resolved
	webRefs    webRefStore
	fetcher    webFetcher
	// spaceZone returns a space's own time zone, "" for none (dbSpaceZone).
	spaceZone func(ctx context.Context, spaceID string) string
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}
	mu        sync.Mutex
	running   map[string]bool
	// owner returns a user's username (empty when none) and email: a space
	// owner's, or another payer's.
	owner func(ctx context.Context, userID string) (username, email string)
	// retryWait is the pause before retry n (1, 2); tests shorten it.
	retryWait func(n int) time.Duration
	clock     func() time.Time
}

// runnerWriteKey marks the runner's own changes, which never queue jobs.
type runnerWriteKey struct{}

func runnerWrite(ctx context.Context) bool {
	v, _ := ctx.Value(runnerWriteKey{}).(bool)
	return v
}

// agentTools are the space tools an agent job gets: the MCP tools without
// the space argument. spaces, pull and push are left out.
func agentTools(schema []byte) ([]json.RawMessage, map[string]*jsonschema.Resolved, error) {
	var catalog struct {
		Tools []struct {
			Name        string                     `json:"name"`
			Description string                     `json:"description"`
			InputSchema map[string]json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(schema, &catalog); err != nil {
		return nil, nil, err
	}
	var tools []json.RawMessage
	schemas := map[string]*jsonschema.Resolved{}
	for _, tool := range catalog.Tools {
		input := tool.InputSchema
		var properties map[string]json.RawMessage
		if err := json.Unmarshal(input["properties"], &properties); err != nil {
			return nil, nil, fmt.Errorf("tool %s: %w", tool.Name, err)
		}
		delete(properties, "space")
		input["properties"], _ = json.Marshal(properties)
		var required []string
		if raw, ok := input["required"]; ok {
			if err := json.Unmarshal(raw, &required); err != nil {
				return nil, nil, fmt.Errorf("tool %s: %w", tool.Name, err)
			}
		}
		kept := []string{}
		for _, name := range required {
			if name != "space" {
				kept = append(kept, name)
			}
		}
		input["required"], _ = json.Marshal(kept)
		b, _ := json.Marshal(input)
		var s jsonschema.Schema
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, nil, fmt.Errorf("tool %s: %w", tool.Name, err)
		}
		resolved, err := s.Resolve(nil)
		if err != nil {
			return nil, nil, fmt.Errorf("tool %s: %w", tool.Name, err)
		}
		schemas[tool.Name] = resolved
		def, _ := json.Marshal(map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": json.RawMessage(b)})
		tools = append(tools, def)
	}
	return tools, schemas, nil
}

func newAgentRunner(s *Service, settings *agentSettings, store agentJobStore) (*agentRunner, error) {
	tools, schemas, err := agentTools(metatrash.ToolSchema)
	if err != nil {
		return nil, fmt.Errorf("agent tools: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &agentRunner{s: s, settings: settings, store: store, tools: tools, schemas: schemas, ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), running: map[string]bool{}, clock: time.Now,
		retryWait: func(n int) time.Duration { return time.Duration(n*n) * 5 * time.Second }}
	a.owner = func(ctx context.Context, userID string) (string, string) {
		if s.ownedDB == nil {
			return "", ""
		}
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var username, email string
		if err := s.ownedDB.db.QueryRowContext(lookup, "SELECT COALESCE(username, ''), email FROM metatrash_users WHERE user_id = ?", userID).Scan(&username, &email); err != nil {
			return "", ""
		}
		return username, email
	}
	a.agentsOf = func(ctx context.Context, userID string) ([]userAgent, error) {
		if s.ownedDB == nil {
			return nil, fmt.Errorf("the account database is not available")
		}
		return s.ownedDB.userAgents(ctx, userID)
	}
	a.providerFor = func(m agentModel) agentProvider { return newMessagesClient(m, false) }
	a.optInOf = func(ctx context.Context, userID, spaceID, folder, action string) (string, bool, error) {
		if s.ownedDB == nil {
			return "", false, fmt.Errorf("the account database is not available")
		}
		o, ok, err := s.ownedDB.optInFor(ctx, userID, spaceID, folder, action)
		return o.AgentID, ok, err
	}
	if s.accounts != nil {
		a.origin = s.accounts.config.Origin
	}
	a.notice = a.mailNotice
	a.spaceZone = func(ctx context.Context, spaceID string) string { return s.dbSpaceZone(ctx, spaceID) }
	a.fetcher = newWebFetcher(false)
	if s.ownedDB != nil {
		a.webRefs = dbWebStore{s.ownedDB.db}
	}
	if err := a.setWebTools(settings.search != nil, settings.downloads != nil); err != nil {
		return nil, err
	}
	return a, nil
}

// setWebTools prepares the tool list of jobs that may use the web.
func (a *agentRunner) setWebTools(search, fetch bool) error {
	a.webTools, a.webSchemas = nil, nil
	if !search && !fetch {
		return nil
	}
	tools, schemas, err := webToolSet(a.tools, a.schemas, search, fetch)
	if err != nil {
		return fmt.Errorf("web tools: %w", err)
	}
	a.webTools, a.webSchemas = tools, schemas
	return nil
}

// mailNotice emails a user that a job of theirs did not run, at most once a
// day for each action.
func (a *agentRunner) mailNotice(ctx context.Context, userID string, j agentJob, why string) {
	accounts := a.s.accounts
	if accounts == nil || accounts.sendNotice == nil {
		return
	}
	_, email := a.owner(ctx, userID)
	if email == "" || a.s.rates.reserve(
		allowance{"action-notice:" + userID + ":" + j.SpaceID + ":" + j.Folder + j.Action, 1, 86400},
		allowance{"mail:global:day", 100, 86400},
	) != nil {
		return
	}
	body := actionNoticeMail(a.origin, a.spaceAddress(ctx, j.SpaceID), j, why)
	go func() {
		if err := accounts.sendNotice(context.WithoutCancel(ctx), email, "A Metatrash folder action did not run for your file", body); err != nil {
			// SMTP responses may contain addresses; never log them.
			log.Print("agent runner: action notice not sent")
		}
	}()
}

// actionNoticeMail tells someone why an action skipped their file.
func actionNoticeMail(origin, space string, j agentJob, why string) string {
	return "The folder action \"" + mailText(j.Action) + "\" in " + mailText(space) + " did not run for your file " + mailText(j.File) + ".\n\n" +
		mailText(why) + "\n\n" +
		"The action's page shows what it does and which model it needs: " + origin + "/account/actions/" + j.ID + "\n" +
		"You get this email at most once a day for each action. Your files in that folder do not run it, and nothing is charged to you, until this is sorted out.\n"
}

// spaceAddress is a space's owner/slug, or its ID when it cannot be read.
func (a *agentRunner) spaceAddress(ctx context.Context, spaceID string) string {
	if a.s.ownedDB == nil {
		return spaceID
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var owner, slug string
	if err := a.s.ownedDB.db.QueryRowContext(lookup, "SELECT COALESCE(u.username, ''), s.slug FROM metatrash_spaces s JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE s.space_id = ?", spaceID).Scan(&owner, &slug); err != nil {
		return spaceID
	}
	return owner + "/" + slug
}

// optInURL is the page where the person who brought a job's file opts in.
func (a *agentRunner) optInURL(jobID string) string {
	return a.origin + "/account/actions/" + jobID
}

// ownerName is the name History shows for a space's owner.
func (a *agentRunner) ownerName(ctx context.Context, r *repository) string {
	return a.userName(ctx, a.s.repositoryOwner(r))
}

// userName is the name History shows for a user.
func (a *agentRunner) userName(ctx context.Context, userID string) string {
	username, email := a.owner(ctx, userID)
	if username == "" {
		return emailName(email)
	}
	return username
}

// start fails jobs a restart interrupted, then runs the queue.
func (a *agentRunner) start() {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if jobs, err := a.store.running(ctx); err != nil {
		log.Printf("agent runner: %v", err)
	} else {
		for _, j := range jobs {
			j.Status, j.Error, j.FinishedAt = jobFailed, "interrupted: the service restarted while the job ran", a.clock()
			a.record(j, jobOutcome{})
		}
	}
	a.wg.Add(1)
	go a.loop()
}

// stop cancels running jobs and waits for them to record their outcome.
func (a *agentRunner) stop() {
	a.cancel()
	a.wg.Wait()
}

func (a *agentRunner) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *agentRunner) loop() {
	defer a.wg.Done()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	var housekeeping time.Time
	for {
		if now := a.clock(); now.Sub(housekeeping) >= time.Hour {
			// On start and then hourly: catch up schedules with every space's
			// settings, and forget expired downloads.
			housekeeping = now
			a.syncAllSchedules()
			if a.webRefs != nil {
				ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
				if err := a.webRefs.expire(ctx, now); err != nil {
					log.Printf("agent runner: %v", err)
				}
				cancel()
			}
		}
		a.runSchedules()
		a.startReady()
		select {
		case <-a.ctx.Done():
			return
		case <-a.wake:
		case <-tick.C:
		}
	}
}

// startReady starts queued jobs whose space has none running.
func (a *agentRunner) startReady() {
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	jobs, err := a.store.queued(ctx, 100)
	if err != nil {
		log.Printf("agent runner: %v", err)
		return
	}
	for _, j := range jobs {
		a.mu.Lock()
		if len(a.running) >= maxParallelJobs || a.running[j.SpaceID] {
			a.mu.Unlock()
			continue
		}
		now := a.clock()
		ok, err := a.store.claim(ctx, j.ID, now)
		if err != nil || !ok {
			a.mu.Unlock()
			if err != nil {
				log.Printf("agent runner: %v", err)
			}
			continue
		}
		a.running[j.SpaceID] = true
		a.mu.Unlock()
		j.Status, j.StartedAt = jobRunning, now
		a.wg.Add(1)
		go func(j agentJob) {
			defer func() {
				a.mu.Lock()
				delete(a.running, j.SpaceID)
				a.mu.Unlock()
				a.poke()
				a.wg.Done()
			}()
			a.run(j)
		}(j)
	}
}

// githubPullKey marks a GitHub pull's commit, whose files nobody in
// particular brought.
type githubPullKey struct{}

func githubPull(ctx context.Context) bool {
	v, _ := ctx.Value(githubPullKey{}).(bool)
	return v
}

// contributorOf is the user whose change ctx carries: the signed-in user
// behind the app or Metatrash Sync, or "" for a GitHub pull, a space key or
// none.
func contributorOf(ctx context.Context) string {
	identity, _ := ctx.Value(mcpIdentityKey{}).(mcpIdentity)
	if githubPull(ctx) || identity.oauth == nil {
		return ""
	}
	return identity.oauth.UserID
}

// committed is called after every commit to an owned space (see
// repository.committed). It queues the jobs the commit asks for.
func (s *Service) agentCommitted(ctx context.Context, r *repository, old, state string) {
	a := s.agents.Load()
	if a == nil || old == "" || r.id == "" || runnerWrite(ctx) || a.ctx.Err() != nil {
		return
	}
	contributor := contributorOf(ctx)
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.enqueue(r, old, state, contributor)
	}()
}

func (a *agentRunner) enqueue(r *repository, old, state, contributor string) {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	if changed, err := r.configsChanged(ctx, old, state); err != nil {
		log.Printf("agent runner: space %s: %v", r.id, err)
	} else if changed {
		if err := a.syncSchedules(ctx, r, false); err != nil {
			log.Printf("agent runner: space %s schedules: %v", r.id, err)
		}
	}
	triggers, err := r.triggersBetween(ctx, old, state)
	if err != nil {
		log.Printf("agent runner: space %s: %v", r.id, err)
		return
	}
	queued := false
	members := map[string]string{}
	for _, t := range triggers {
		// A member's own action runs only for that member's files.
		if t.Member != "" {
			id, ok := members[t.Member]
			if !ok {
				id = a.s.userIDOf(ctx, t.Member)
				members[t.Member] = id
			}
			if id == "" || id != contributor {
				continue
			}
		}
		ok, err := a.queue(ctx, r.id, t, state, contributor)
		if err != nil {
			log.Printf("agent runner: %v", err)
			return
		}
		queued = queued || ok
	}
	if queued {
		a.poke()
	}
}

// queue adds a job for a trigger (File is "" for a scheduled run), unless
// the folder ran the most jobs allowed in the last hour: then one skipped job
// is recorded, so runner.md says why the rest did not run, and further ones
// are dropped until the hour passes. It reports whether a job was queued.
func (a *agentRunner) queue(ctx context.Context, spaceID string, t actionTrigger, commit, contributor string) (bool, error) {
	now := a.clock()
	n, err := a.store.queuedSince(ctx, spaceID, t.Folder, now.Add(-time.Hour))
	if err != nil {
		return false, err
	}
	id, err := randomHex(16)
	if err != nil {
		return false, err
	}
	j := agentJob{ID: id, SpaceID: spaceID, Folder: t.Folder, Action: t.Action, File: t.File, Commit: commit, Status: jobQueued, QueuedAt: now, Contributor: contributor}
	if n >= maxJobsPerFolderHour {
		if n > maxJobsPerFolderHour {
			log.Printf("agent runner: space %s folder %q: job limit reached, %s %s not run", spaceID, t.Folder, t.Action, t.File)
			return false, nil
		}
		j.Status, j.Error, j.StartedAt, j.FinishedAt = jobSkipped, fmt.Sprintf("skipped: this folder already ran %d jobs in the last hour, the most allowed; later ones in this hour are not run either", maxJobsPerFolderHour), now, now
		if err := a.store.add(ctx, j); err != nil {
			return false, err
		}
		a.record(j, jobOutcome{})
		return false, nil
	}
	if err := a.store.add(ctx, j); err != nil {
		return false, err
	}
	return true, nil
}

// configsChanged reports whether a change touched any .metatrash.json.
func (r *repository) configsChanged(ctx context.Context, old, state string) (bool, error) {
	_, before, err := r.snapshot(ctx, old)
	if err != nil {
		return false, err
	}
	_, after, err := r.snapshot(ctx, state)
	if err != nil {
		return false, err
	}
	for p, f := range after {
		if isFolderConfig(p) && before[p].Blob != f.Blob {
			return true, nil
		}
	}
	for p := range before {
		if _, ok := after[p]; isFolderConfig(p) && !ok {
			return true, nil
		}
	}
	return false, nil
}

// requeue runs a job's file again, as after its contributor opted in.
func (a *agentRunner) requeue(ctx context.Context, j agentJob) error {
	r := a.s.ownedRepository(j.SpaceID)
	if r == nil {
		return missing()
	}
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return err
	}
	if _, ok := files[j.File]; !ok {
		return problem(409, "conflict", "The file was moved or deleted since, so there is nothing to run now; your next file will run.")
	}
	now := a.clock()
	n, err := a.store.queuedSince(ctx, j.SpaceID, j.Folder, now.Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= maxJobsPerFolderHour {
		return problem(429, "rate_limited", "This folder already ran the most jobs allowed in the last hour; your next file will run once the hour has passed.")
	}
	id, err := randomHex(16)
	if err != nil {
		return err
	}
	next := agentJob{ID: id, SpaceID: j.SpaceID, Folder: j.Folder, Action: j.Action, File: j.File, Commit: j.Commit, Status: jobQueued, QueuedAt: now, Contributor: j.Contributor}
	if err := a.store.add(ctx, next); err != nil {
		return err
	}
	a.poke()
	return nil
}

// jobOutcome is what a job did, for runner.md.
type jobOutcome struct {
	summary string
	changed []string
	// model is the agent's model, for the Steps line.
	model string
	// paidBy names who paid, when it was not the space owner.
	paidBy string
	// every is the schedule of a scheduled run.
	every string
}

func (a *agentRunner) run(j agentJob) {
	ctx, cancel := context.WithTimeout(a.ctx, maxJobTime)
	defer cancel()
	out, err := a.execute(ctx, &j)
	j.FinishedAt = a.clock()
	switch {
	case err == nil:
		if j.Status == jobRunning {
			j.Status = jobDone
		}
	case a.ctx.Err() != nil:
		j.Status, j.Error = jobFailed, "stopped: the service shut down while the job ran"
	case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
		j.Status, j.Error = jobFailed, fmt.Sprintf("stopped: the job ran longer than %d minutes", int(maxJobTime/time.Minute))
	default:
		j.Status, j.Error = jobFailed, err.Error()
	}
	if len(j.Error) > 2000 {
		j.Error = j.Error[:2000]
	}
	a.record(j, out)
}

// record stores a finished job and appends its entry to runner.md.
func (a *agentRunner) record(j agentJob, out jobOutcome) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(a.ctx), 20*time.Second)
	defer cancel()
	if err := a.store.finish(ctx, j); err != nil {
		log.Printf("agent runner: job %s: %v", j.ID, err)
	}
	r := a.s.ownedRepository(j.SpaceID)
	if r == nil {
		return
	}
	ctx = context.WithValue(ctx, runnerWriteKey{}, true)
	name := a.ownerName(ctx, r)
	ctx = withAuthor(ctx, func() changeAuthor { return changeAuthor{Name: name, App: runnerApp, Kind: "agent"} })
	text := runnerEntry(j, out, a.zoneOf(ctx, j.SpaceID), strings.TrimPrefix(j.File, j.Folder))
	if _, err := a.s.dispatchRepository(ctx, r, j.SpaceID, "append", Input{Path: j.Folder + runnerLogName, Text: &text}); err != nil {
		log.Printf("agent runner: job %s: %s not written: %v", j.ID, j.Folder+runnerLogName, err)
	}
}

// runnerEntry is a job's entry in runner.md.
func runnerEntry(j agentJob, out jobOutcome, zone *time.Location, file string) string {
	when := j.FinishedAt
	if zone != nil {
		when = when.In(zone)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s · %s · %s\n\n", when.Format("2006-01-02 15:04"), j.Action, j.Status)
	if j.File == "" {
		every := ""
		if out.every != "" {
			every = " (every " + out.every + ")"
		}
		fmt.Fprintf(&b, "- Run: scheduled%s\n", every)
	} else {
		fmt.Fprintf(&b, "- File: %s\n", file)
	}
	if len(out.changed) > 0 {
		fmt.Fprintf(&b, "- Changed: %s\n", strings.Join(out.changed, ", "))
	}
	if out.paidBy != "" {
		fmt.Fprintf(&b, "- Paid by: %s\n", out.paidBy)
	}
	if j.Steps > 0 || j.InputTokens > 0 || out.model != "" {
		model := ""
		if out.model != "" {
			model = " · " + out.model
		}
		fmt.Fprintf(&b, "- Steps: %d · tokens: %d in, %d out%s\n", j.Steps, j.InputTokens, j.OutputTokens, model)
	}
	if j.Error != "" {
		fmt.Fprintf(&b, "- Error: %s\n", oneLine(j.Error))
	}
	if summary := strings.TrimSpace(out.summary); summary != "" {
		if len(summary) > maxRunnerSummary {
			summary = strings.ToValidUTF8(summary[:maxRunnerSummary], "") + " …"
		}
		b.WriteString("\n" + summary + "\n")
	}
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

const runnerPreamble = `You are an unattended agent that Metatrash, a file service, runs for the action %q of the folder %s in a space owned by %s%s. No person is watching and nobody will answer questions, so do the whole task with your tools, then stop.

Your tools work on this folder only: paths are relative to it, and an empty prefix means the whole folder. Each change is saved as you make it; there is nothing to push. Write, move and delete need ifInState from a read, list or search. Prefer edit and append to rewriting a whole file, and read long files by line range. You cannot change .metatrash.json files or %s; Metatrash adds your result to %s itself.

Change only the files your instructions call for. Do not edit, tidy or create any other file, AGENTS.md included, unless the instructions say so; note anything else that needs attention in your summary instead.

When you are done, reply with a short summary of three to five sentences: what you did and anything a person should check. That reply is recorded in %s.`

// execute runs one job's agent loop. It returns what the job did; a nil
// error with j.Status still running means done.
func (a *agentRunner) execute(ctx context.Context, j *agentJob) (jobOutcome, error) {
	var out jobOutcome
	r := a.s.ownedRepository(j.SpaceID)
	if r == nil {
		return out, fmt.Errorf("the space no longer exists")
	}
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return out, err
	}
	var action *folderAction
	if f, ok := files[j.Folder+folderConfigName]; ok {
		c, err := r.cachedConfig(ctx, f.Blob)
		if err != nil {
			return out, err
		}
		for i := range c.Actions {
			if c.Actions[i].Name == j.Action {
				action = &c.Actions[i]
			}
		}
	}
	if action == nil {
		return out, fmt.Errorf("the action %q is no longer in %s%s", j.Action, j.Folder, folderConfigName)
	}
	out.every = action.Every
	if _, ok := files[j.File]; !ok && j.File != "" {
		j.Status, j.Error = jobSkipped, "skipped: the file was moved or deleted before the job started"
		return out, nil
	}
	owner := a.ownerName(ctx, r)
	payer, skip, err := a.payerFor(ctx, r, j, action)
	if err != nil {
		return out, err
	}
	if skip != "" {
		j.Status, j.Error = jobSkipped, skip
		return out, nil
	}
	payerName := owner
	if payer.userID != a.s.repositoryOwner(r) {
		payerName = a.userName(ctx, payer.userID)
		out.paidBy = orSomeone(payerName)
	}
	m, skip, err := a.agentFor(ctx, payer, action, payerName)
	if err != nil {
		return out, err
	}
	if skip != "" {
		j.Status, j.Error = jobSkipped, skip
		if payer.notify {
			a.notice(ctx, payer.userID, *j, skip)
		}
		return out, nil
	}
	j.AgentID, out.model = m.agentID, m.model
	if m.dailyCap > 0 {
		now := a.clock().In(a.settings.zone)
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, a.settings.zone)
		used, err := a.store.tokensSince(ctx, m.agentID, midnight)
		if err != nil {
			return out, err
		}
		if used >= m.dailyCap {
			j.Status, j.Error = jobSkipped, fmt.Sprintf("skipped: the agent %s used %d tokens today, its daily cap is %d (Your account → Services → Agents)", m.name, used, m.dailyCap)
			return out, nil
		}
	}
	provider := a.providerFor(m)
	instructions := action.Do
	if name := action.doFile(); name != "" {
		f, ok := files[j.Folder+name]
		if !ok {
			return out, fmt.Errorf("do names %s, which is not in the folder", name)
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return out, err
		}
		instructions = string(b)
	}
	folderName := j.Folder
	if folderName == "" {
		folderName = "(the space root)"
	}
	behalf := ""
	if out.paidBy != "" {
		behalf = ", on behalf of " + out.paidBy + ", who brought the file and pays for this job"
	}
	system := fmt.Sprintf(runnerPreamble, j.Action, folderName, orSomeone(owner), behalf, runnerLogName, runnerLogName, runnerLogName)
	tools := a.tools
	var web *webSession
	if action.webEnabled() && a.webTools != nil && a.webRefs != nil {
		web = a.newWebSession(payer.userID, a.settings.search != nil, a.settings.downloads != nil)
		tools = a.webTools
		system += webPreamble(web.search, web.fetch)
	}
	if f, ok := files[j.Folder+agentsFileName]; ok {
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return out, err
		}
		if len(b) > maxAgentsTextBytes {
			b = append(bytes.ToValidUTF8(b[:maxAgentsTextBytes], nil), []byte("\n…(cut; read AGENTS.md for the rest)")...)
		}
		system += "\n\n## This folder's AGENTS.md\n\n" + string(b)
	}
	rel := strings.TrimPrefix(j.File, j.Folder)
	trigger := "The file " + rel + " arrived (it was created or moved into the folder)."
	if j.File == "" {
		zone := a.zoneOf(ctx, j.SpaceID)
		trigger = fmt.Sprintf("This is a scheduled run (every %s, %s time; now %s). No file arrived.", action.Every, zone, a.clock().In(zone).Format("Monday 2 January 2006 15:04"))
	} else if before, err := r.commitParent(ctx, j.Commit); err == nil {
		if _, old, err := r.snapshot(ctx, before); err == nil {
			if _, existed := old[j.File]; existed {
				trigger = "The file " + rel + " was changed."
			}
		}
	}
	user := "## Instructions\n\n" + strings.TrimSpace(instructions) + "\n\n## Why you are running\n\n" + trigger
	if web != nil {
		web.see(system)
		web.see(user)
	}
	toolCtx := context.WithValue(ctx, runnerWriteKey{}, true)
	toolCtx = withAuthor(toolCtx, func() changeAuthor { return changeAuthor{Name: payerName, App: m.app, Kind: "agent"} })
	cache := m.provider == "anthropic"
	systemBlock := contentBlock{Type: "text", Text: system}
	if cache {
		systemBlock.CacheControl = &cacheControl{Type: "ephemeral"}
	}
	req := agentRequest{Model: m.model, MaxTokens: m.maxTokens, System: []json.RawMessage{rawBlock(systemBlock)}, Tools: tools,
		Messages: []agentMessage{{Role: "user", Content: []json.RawMessage{textBlock(user)}}}}
	pin := pinnedFolder(j.Folder)
	limit := action.steps()
	lastCall := false
	for {
		trimOldResults(req.Messages)
		if cache {
			markLastBlock(req.Messages)
		}
		resp, err := a.send(ctx, provider, req)
		if err != nil {
			return out, err
		}
		j.InputTokens += resp.Usage.input()
		j.OutputTokens += resp.Usage.OutputTokens
		req.Messages = append(req.Messages, agentMessage{Role: "assistant", Content: resp.Content})
		var uses []contentBlock
		var text []string
		for _, raw := range resp.Content {
			var b contentBlock
			if json.Unmarshal(raw, &b) != nil {
				continue
			}
			switch b.Type {
			case "tool_use":
				uses = append(uses, b)
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					text = append(text, b.Text)
				}
			}
		}
		if len(text) > 0 {
			out.summary = strings.Join(text, "\n\n")
		}
		if j.InputTokens > maxJobInputTokens {
			return out, fmt.Errorf("stopped: the job used more than %d input tokens", maxJobInputTokens)
		}
		if len(uses) == 0 {
			switch resp.StopReason {
			case "pause_turn":
				continue
			case "refusal":
				return out, fmt.Errorf("the model refused the task")
			case "max_tokens":
				return out, fmt.Errorf("stopped: the model's reply reached its limit of %d tokens", m.maxTokens)
			}
			return out, nil
		}
		if lastCall {
			return out, fmt.Errorf("stopped: the job reached its limit of %d tool calls", limit)
		}
		var results []json.RawMessage
		for _, use := range uses {
			if j.Steps >= limit {
				lastCall = true
				results = append(results, rawBlock(contentBlock{Type: "tool_result", ToolUseID: use.ID, IsError: true,
					Content: fmt.Sprintf(`{"error":{"code":"step_limit","message":"This job may make %d tool calls and has made them all. Stop now and reply with your summary."}}`, limit)}))
				continue
			}
			j.Steps++
			var result json.RawMessage
			var change string
			if web != nil && web.handles(use.Name, use.Input) {
				result, change = web.call(toolCtx, r, j.SpaceID, pin, use)
			} else {
				var text string
				var isError bool
				text, isError, change = a.callTool(toolCtx, r, j.SpaceID, pin, use.Name, use.Input)
				if web != nil {
					web.see(text)
				}
				result = rawBlock(contentBlock{Type: "tool_result", ToolUseID: use.ID, Content: text, IsError: isError})
			}
			if change != "" {
				out.changed = append(out.changed, change)
			}
			results = append(results, result)
		}
		req.Messages = append(req.Messages, agentMessage{Role: "user", Content: results})
	}
}

func orSomeone(name string) string {
	if name == "" {
		return "its owner"
	}
	return name
}

// markLastBlock keeps one prompt-cache breakpoint on the newest message (and
// one on the system prompt), so each call reuses the conversation so far.
func markLastBlock(messages []agentMessage) {
	for i := range messages {
		content := messages[i].Content
		for k, raw := range content {
			var b map[string]json.RawMessage
			if json.Unmarshal(raw, &b) != nil {
				continue
			}
			_, marked := b["cache_control"]
			last := i == len(messages)-1 && k == len(content)-1
			if marked == last {
				continue
			}
			if last {
				b["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
			} else {
				delete(b, "cache_control")
			}
			content[k], _ = json.Marshal(b)
		}
	}
}

// send calls the provider, retrying failures worth retrying.
func (a *agentRunner) send(ctx context.Context, provider agentProvider, req agentRequest) (agentResponse, error) {
	for attempt := 0; ; attempt++ {
		resp, err := provider.send(ctx, req)
		var p *providerError
		if err == nil || !errors.As(err, &p) || !p.retry || attempt == providerRetries {
			return resp, err
		}
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-time.After(a.retryWait(attempt + 1)):
		}
	}
}

// jobPayer is whose agent a job runs on: agentID is the agent they chose
// for the action when they opted in, or "" for their usual choice; notify
// says they are not the space owner, so a skip is emailed to them.
type jobPayer struct {
	userID, agentID string
	notify          bool
}

// payerFor decides who pays for a job. A non-empty skip says why the job
// does not run; payerFor emails the person concerned itself.
func (a *agentRunner) payerFor(ctx context.Context, r *repository, j *agentJob, action *folderAction) (jobPayer, string, error) {
	owner := a.s.repositoryOwner(r)
	switch {
	case action.Member != "":
		id := a.s.userIDOf(ctx, action.Member)
		if id == "" || id != j.Contributor {
			return jobPayer{}, fmt.Sprintf("skipped: %s's own action runs only for their files", action.Member), nil
		}
		return jobPayer{userID: id, notify: id != owner}, "", nil
	case action.paysContributor() && j.Contributor != "" && j.Contributor != owner:
		agentID, ok, err := a.optInOf(ctx, j.Contributor, j.SpaceID, j.Folder, j.Action)
		if err != nil {
			return jobPayer{}, "", err
		}
		if !ok {
			who := orSomeone(a.userName(ctx, j.Contributor))
			why := fmt.Sprintf("skipped: whoever brings a file pays for this action with their own %s agent, and %s has not opted in. They can opt in at %s", modelName(action.Model), who, a.optInURL(j.ID))
			a.notice(ctx, j.Contributor, *j, why)
			return jobPayer{}, why, nil
		}
		return jobPayer{userID: j.Contributor, agentID: agentID, notify: true}, "", nil
	}
	return jobPayer{userID: owner}, "", nil
}

// modelName is an action model for people: "MiniMax MiniMax-M3".
func modelName(value string) string {
	if provider, model, ok := actionModel(value); ok {
		return agentProviders[provider].name + " " + model
	}
	return "default"
}

// agentFor chooses the payer's agent for an action and opens its key: the
// agent they opted in with, or their default one (for the action's model, if
// it names one). Nothing falls back to another agent or model. A non-empty
// skip says why the job does not run.
func (a *agentRunner) agentFor(ctx context.Context, payer jobPayer, action *folderAction, name string) (m agentModel, skip string, err error) {
	agents, err := a.agentsOf(ctx, payer.userID)
	if err != nil {
		return m, "", err
	}
	var u userAgent
	ok := false
	if payer.agentID != "" {
		for _, candidate := range agents {
			if candidate.ID == payer.agentID && candidate.Ref() == action.Model {
				u, ok = candidate, true
			}
		}
		if !ok {
			return m, fmt.Sprintf("skipped: the agent %s chose for this action no longer runs %s; they can choose another in Your account → Services → Agents", orSomeone(name), modelName(action.Model)), nil
		}
	} else if u, ok = pickAgent(agents, action.Model); !ok {
		needs := "an agent"
		if provider, model, ok := actionModel(action.Model); ok {
			needs = "a " + agentProviders[provider].name + " " + model + " agent"
		}
		who := "The space owner adds"
		if payer.notify {
			who = "They add"
		}
		return m, fmt.Sprintf("skipped: %s has no agent for this action: it needs %s. %s one in Your account → Services → Agents.", orSomeone(name), needs, who), nil
	}
	key, err := openAgentKey(a.settings.seal, u.UserID, u.ID, u.Sealed)
	if err != nil {
		return m, "", err
	}
	return agentModel{agentID: u.ID, name: u.Name, provider: u.Provider, app: u.App(), endpoint: strings.TrimSuffix(u.Endpoint(), "/") + "/v1/messages",
		model: u.Model, apiKey: key, maxTokens: u.MaxTokens, dailyCap: u.DailyCap}, "", nil
}

// commitParent returns a commit's first parent.
func (r *repository) commitParent(ctx context.Context, commit string) (string, error) {
	if !hashPattern.MatchString(commit) {
		return "", invalid("Invalid revision.")
	}
	b, err := git(ctx, r.path, "", nil, "rev-parse", "--verify", commit+"^1")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// pinnedFolder is the folder a job is pinned to: "" for the space root,
// otherwise a prefix ending in /. Paths the agent sends are relative to it;
// results come back relative to it.
type pinnedFolder string

// callTool runs one tool call for an agent job. It returns the result text
// for the model, whether it is an error, and a note of the file it changed.
func (a *agentRunner) callTool(ctx context.Context, r *repository, space string, pin pinnedFolder, name string, raw json.RawMessage) (string, bool, string) {
	fail := func(err error) (string, bool, string) {
		var p *Error
		if !errors.As(err, &p) {
			log.Printf("agent runner: tool %s: %v", name, err)
			p = problem(500, "internal_error", "Service operation failed.")
		}
		e := *p
		e.Message = strings.ReplaceAll(e.Message, string(pin), "")
		b, _ := json.Marshal(struct {
			Error Error `json:"error"`
		}{e})
		return string(b), true, ""
	}
	schema, ok := a.schemas[name]
	if !ok {
		return fail(invalid(fmt.Sprintf("Unknown tool %q.", name)))
	}
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if int64(len(raw)) > r.limits.MaxRequestBytes {
		return fail(problem(413, "payload_too_large", "Arguments exceed the space request limit."))
	}
	args := mcpInput{Limit: 50}
	if err := decodeMCPInput(raw, &args); err != nil {
		return fail(err)
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return fail(invalid("Invalid arguments."))
	}
	if err := schema.Validate(instance); err != nil {
		return fail(invalid("Arguments do not match the tool input schema: " + err.Error()))
	}
	p := string(pin)
	in := Input{Revision: args.Revision, ID: args.ID, Cursor: args.Cursor, Limit: args.Limit, Text: args.Text, IfInState: args.IfInState,
		CreateOnly: args.CreateOnly, Strict: args.Strict, StartLine: args.StartLine, LineCount: args.LineCount, Edits: args.Edits, Query: args.Query,
		Regex: args.Regex, IgnoreCase: args.IgnoreCase, Context: args.Context, IfBlob: args.IfBlob, Line: args.Line, Since: args.Since}
	if args.Path != "" {
		in.Path = p + args.Path
	}
	if args.From != "" {
		in.From = p + args.From
	}
	if args.To != "" {
		in.To = p + args.To
	}
	if name == "list" || name == "search" {
		in.Prefix = p + args.Prefix
	}
	switch name {
	case "write", "edit", "append", "delete":
		if err := runnerOwnFile(pin, args.Path); err != nil {
			return fail(err)
		}
	case "move":
		if err := runnerOwnFile(pin, args.From); err != nil {
			return fail(err)
		}
		if err := runnerOwnFile(pin, args.To); err != nil {
			return fail(err)
		}
	}
	value, err := a.s.dispatchRepository(ctx, r, space, name, in)
	if err != nil {
		return fail(err)
	}
	if h, ok := value.(HistoryResult); ok && p != "" {
		kept := []HistoryEntry{}
		for _, e := range h.Entries {
			if strings.HasPrefix(e.Path, p) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			return fail(missing())
		}
		h.Entries = kept
		value = h
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fail(err)
	}
	text, err := unpin(b, p)
	if err != nil {
		return fail(err)
	}
	if len(text) > maxToolResultBytes {
		return fail(problem(413, "result_too_large", fmt.Sprintf("The result is over %d bytes. Read long files by line range (startLine, lineCount) and narrow searches.", maxToolResultBytes)))
	}
	change := ""
	switch name {
	case "write", "edit", "append":
		if m, ok := mutationOf(value); ok && m.NewState != m.OldState {
			change = args.Path + " (" + name + ")"
		}
	case "delete":
		change = args.Path + " (deleted)"
	case "move":
		change = args.From + " → " + args.To + " (moved)"
	}
	return text, false, change
}

func mutationOf(value any) (Mutation, bool) {
	switch v := value.(type) {
	case WriteResult:
		return v.Mutation, true
	case Mutation:
		return v, true
	}
	return Mutation{}, false
}

// runnerOwnFile refuses agent changes to .metatrash.json files (so a job
// cannot change actions) and to the folder's runner.md.
func runnerOwnFile(pin pinnedFolder, rel string) error {
	if isFolderConfig(rel) {
		return problem(403, "protected_file", folderConfigName+" files cannot be changed by an action's agent; a person or a connected agent can change them.")
	}
	if rel == runnerLogName {
		return problem(403, "protected_file", runnerLogName+" is the runner's record of jobs; read it, but Metatrash writes it.")
	}
	return nil
}

// unpin rewrites a result for the agent: paths relative to the pinned
// folder and no space name.
func unpin(b []byte, prefix string) (string, error) {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return "", err
	}
	if m, ok := v.(map[string]any); ok {
		delete(m, "space")
	}
	if prefix != "" {
		v = unpinValue(v, prefix, "")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func unpinValue(v any, prefix, key string) any {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			x[k] = unpinValue(x[k], prefix, k)
		}
		return x
	case []any:
		for i := range x {
			x[i] = unpinValue(x[i], prefix, key)
		}
		return x
	case string:
		switch key {
		case "path", "from", "to", "removed":
			return strings.TrimPrefix(x, prefix)
		case "hint", "message":
			return strings.ReplaceAll(x, prefix, "")
		}
	}
	return v
}
