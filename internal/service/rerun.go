package service

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Running a folder action again (0.101.0). An action runs once for each file
// that arrives; after its instructions change, a person may want it to run
// once more on every file it covers. Slate Viewer's Automation page asks
// first (confirm false: how many files, who pays) and then queues one job
// per file (confirm true), through the site API:
//
//	POST /api/v1/site/spaces/{owner}/{slug}/rerun  {"folder": "inbound/", "action": "tidy-draft", "confirm": false}
//
// Only a person signed in on the website can, and only the one who pays:
// the space's owner (or someone who acts for them) for an owner's action,
// the member whose own action it is, or, for an action whoever brings a file
// pays for, a person who opted in to it (they pay for every file of the
// re-run). At most maxRerunFiles files, and not while an earlier run of the
// action still has jobs waiting.
//
// A re-run's jobs carry rerunCommit instead of the commit a file arrived in,
// which keeps them out of the folder's hourly job count (queuedSince): a
// re-run of many files must not stop the next arriving file from running.
// The agent is told the file is being run again.

const (
	maxRerunFiles = 100
	// rerunsPerSpaceHour bounds how often a space's actions are re-run.
	rerunsPerSpaceHour = 10
)

// rerunCommit marks a re-run's jobs: Git's null hash, never a real commit.
var rerunCommit = strings.Repeat("0", 40)

type rerunRequest struct {
	Folder  string `json:"folder"`
	Action  string `json:"action"`
	Confirm bool   `json:"confirm"`
}

// RerunResult answers a re-run: the files it covers (or queued), a few of
// their names, and who pays, as "you" or the payer's name.
type RerunResult struct {
	Files  int      `json:"files"`
	Sample []string `json:"sample"`
	Payer  string   `json:"payer"`
	Model  string   `json:"model"`
	Queued int      `json:"queued,omitempty"`
}

func (h *httpAdapter) serveRerun(w http.ResponseWriter, r *http.Request, repo *repository) {
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		sendError(w, invalid("Could not read body."))
		return
	}
	var in rerunRequest
	if err := strictJSON(b, &in); err != nil {
		sendError(w, invalid(`Send {"folder", "action", "confirm"}.`))
		return
	}
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	author, ok := authorOf(r.Context())
	if !siteRequest(r.Context()) || identity.oauth == nil || identity.oauth.UserID == "" || !ok || author.Kind != "person" {
		sendError(w, problem(403, "forbidden", "Only a person signed in on the website can run an action again, because it spends someone's agent tokens."))
		return
	}
	value, err := h.service.rerunAction(r.Context(), repo, identity.oauth.UserID, in)
	if err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sendJSON(w, 200, value)
}

func (s *Service) rerunAction(ctx context.Context, r *repository, actorID string, in rerunRequest) (*RerunResult, error) {
	a := s.agents.Load()
	if a == nil || r == nil || r.id == "" || s.ownedRepository(r.id) != r {
		return nil, problem(503, "unavailable", "Folder actions aren't available here.")
	}
	folder := in.Folder
	if folder != "" && (!strings.HasSuffix(folder, "/") || !validPath(strings.TrimSuffix(folder, "/"))) {
		return nil, invalid(`folder is "" for the top of the space, or a folder path ending in /, such as inbound/.`)
	}
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	var action *folderAction
	if f, ok := files[folder+folderConfigName]; ok {
		c, err := r.cachedConfig(ctx, f.Blob)
		if err != nil {
			return nil, err
		}
		for i := range c.Actions {
			if c.Actions[i].Name == in.Action {
				action = &c.Actions[i]
			}
		}
	}
	if action == nil {
		return nil, problem(404, "not_found", fmt.Sprintf("%s has no saved action %q. Save the folder's settings first.", folderLabel(folder), in.Action))
	}
	if len(action.On) == 0 {
		return nil, invalid("This action runs on a schedule, not for files, so there is nothing to run again.")
	}

	// Who pays, and so who may: see the comment at the top.
	owner := s.repositoryOwner(r)
	actsForOwner := actorID == owner || s.ownedDB != nil && s.ownedDB.actsFor(ctx, actorID, owner)
	contributor := actorID
	switch {
	case action.Member != "":
		if s.userIDOf(ctx, action.Member) != actorID {
			return nil, problem(403, "forbidden", fmt.Sprintf("This is %s's own action and runs on their agent, so only they can run it again.", action.Member))
		}
	case actsForOwner:
		contributor = owner
	case action.paysContributor():
		if _, opted, err := a.optInOf(ctx, actorID, r.id, folder, action.Name); err != nil {
			return nil, err
		} else if !opted {
			return nil, problem(409, "conflict", "Whoever brings a file pays for this action, and you haven't agreed to pay for it yet. Add a file it covers: Collumii emails you a link to choose your agent. Then you can run it again.")
		}
	default:
		return nil, problem(403, "forbidden", "This action runs on the space owner's agent, so only the owner can run it again.")
	}
	payer := "you"
	if contributor == owner && actorID != owner {
		payer = orSomeone(a.ownerName(ctx, r))
	}
	model := "the default agent"
	if action.Model != "" {
		model = modelName(action.Model)
	}

	var covered []string
	for p := range files {
		if !strings.HasPrefix(p, folder) || isFolderConfig(p) || isAgentsFile(p) || p == folder+runnerLogName {
			continue
		}
		if action.matches(p[len(folder):]) {
			covered = append(covered, p)
		}
	}
	sort.Strings(covered)
	if len(covered) == 0 {
		return nil, invalid(fmt.Sprintf("No files in %s match this action now.", folderLabel(folder)))
	}
	if len(covered) > maxRerunFiles {
		return nil, invalid(fmt.Sprintf("This action covers %d files, and running again takes at most %d. Narrow its patterns, or move some files out, then try again.", len(covered), maxRerunFiles))
	}
	out := &RerunResult{Files: len(covered), Payer: payer, Model: model, Sample: []string{}}
	for _, p := range covered[:min(5, len(covered))] {
		out.Sample = append(out.Sample, p[len(folder):])
	}

	now := a.clock()
	recent, err := a.store.recent(ctx, r.id, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	waiting := 0
	for _, j := range recent {
		if j.Folder == folder && j.Action == action.Name && (j.Status == jobQueued || j.Status == jobRunning) {
			waiting++
		}
	}
	if waiting > 0 {
		return nil, problem(409, "conflict", fmt.Sprintf("This action still has %d %s waiting or running. Run it again once they finish; its results are in %s.", waiting, plural(waiting, "job", "jobs"), folder+runnerLogName))
	}
	if !in.Confirm {
		return out, nil
	}
	if err := s.rates.take(allowance{"rerun:space:" + r.id, rerunsPerSpaceHour, 3600}); err != nil {
		return nil, problem(429, "rate_limited", fmt.Sprintf("Actions in this space were run again %d times in the last hour, the most allowed. Try again later.", rerunsPerSpaceHour))
	}
	for _, p := range covered {
		id, err := randomHex(16)
		if err != nil {
			return nil, err
		}
		j := agentJob{ID: id, SpaceID: r.id, Folder: folder, Action: action.Name, File: p, Commit: rerunCommit, Status: jobQueued, QueuedAt: now, Contributor: contributor}
		if err := a.store.add(ctx, j); err != nil {
			if out.Queued > 0 {
				a.poke()
			}
			return nil, err
		}
		out.Queued++
	}
	a.poke()
	return out, nil
}

// folderLabel names a folder for people: "the top of the space" for "".
func folderLabel(folder string) string {
	if folder == "" {
		return "the top of the space"
	}
	return folder
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
