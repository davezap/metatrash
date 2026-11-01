package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Agents in Settings → Services (0.55.0; in the Settings dialog since 0.80.0,
// site_services.go): add an agent (provider, model, API key, daily cap),
// test it with one tiny call, make it the default, change its name, daily
// cap or key (0.58.0), or remove it. Since 0.56.0 it also lists the actions
// the user pays for, and #settings/services/actions/<job> shows an action
// that skipped one of the user's files, where they opt in to pay for it.
// Shown only while folder actions are enabled on the server.

func (h *httpAdapter) agentsEnabled() bool {
	return h.service.agents.Load() != nil && h.service.ownedDB != nil
}

// actionOffer is the page of an action that skipped one of the user's files
// (#settings/services/actions/<job>): what the action does, who pays, and, when
// whoever brings a file pays, the form to opt in with one of their agents.
type actionOffer struct {
	JobID, Space, Folder, Action, File, Status, Reason string
	// Model is the action's model ("minimax/MiniMax-M3"); ModelName and
	// KeysURL describe it for people.
	Model, ModelName, KeysURL string
	// Do is the instructions, or DoFile the file in the folder holding them.
	Do, DoFile string
	Steps      int
	// Pays is "contributor" (opt in to run it), "member" (the user's own
	// action) or "owner" (the space owner pays; nothing to do).
	Pays string
	// Agents are the user's agents that can pay for it; Current is the one
	// they opted in with.
	Agents  []userAgent
	Current string
}

// actionOfferFor is what an action that skipped one of user's files does and
// who pays for it (the action page, and Settings → Services, 0.80.0).
func (h *httpAdapter) actionOfferFor(ctx context.Context, user userAccount, jobID string) (*actionOffer, error) {
	runner := h.service.agents.Load()
	db := h.service.ownedDB
	if runner == nil || db == nil {
		return nil, missing()
	}
	j, ok, err := runner.store.get(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !ok || j.Contributor != user.ID {
		return nil, missing()
	}
	repo := h.service.ownedRepository(j.SpaceID)
	if repo == nil {
		return nil, missing()
	}
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	var action *folderAction
	if f, ok := files[j.Folder+folderConfigName]; ok {
		if c, err := repo.cachedConfig(ctx, f.Blob); err == nil {
			for i := range c.Actions {
				if c.Actions[i].Name == j.Action {
					action = &c.Actions[i]
				}
			}
		}
	}
	if action == nil {
		return nil, problem(404, "not_found", "The action "+j.Action+" is no longer in that folder, so there is nothing to opt in to.")
	}
	folder := j.Folder
	if folder == "" {
		folder = "/"
	}
	offer := &actionOffer{JobID: j.ID, Space: runner.spaceAddress(ctx, j.SpaceID), Folder: folder, Action: j.Action, File: strings.TrimPrefix(j.File, j.Folder),
		Status: j.Status, Reason: strings.TrimPrefix(j.Error, "skipped: "), Model: action.Model, ModelName: modelName(action.Model), Steps: action.steps(), Pays: "owner"}
	if provider, _, ok := actionModel(action.Model); ok {
		offer.KeysURL = agentProviders[provider].keysURL
	}
	if i := strings.Index(offer.Reason, " They can opt in at "); i >= 0 {
		offer.Reason = offer.Reason[:i]
	}
	if offer.DoFile = action.doFile(); offer.DoFile == "" {
		offer.Do = strings.TrimSpace(action.Do)
	}
	owner := h.service.repositoryOwner(repo)
	switch {
	case action.Member != "" && action.Member == user.Username:
		offer.Pays = "member"
	case action.paysContributor() && user.ID != owner:
		offer.Pays = "contributor"
	}
	agents, err := db.userAgents(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	for _, u := range agents {
		if action.Model == "" || u.Ref() == action.Model {
			offer.Agents = append(offer.Agents, u)
		}
	}
	if o, ok, err := db.optInFor(ctx, user.ID, j.SpaceID, j.Folder, j.Action); err != nil {
		return nil, err
	} else if ok {
		offer.Current = o.AgentID
	}
	return offer, nil
}

// errAgentsUnavailable: folder actions are off on this server.
var errAgentsUnavailable = problem(404, "not_found", "Agents are not available on this server.")

// findUserAgent is one of user's agents by id, or not_found.
func (h *httpAdapter) findUserAgent(ctx context.Context, user userAccount, agentID string) (userAgent, error) {
	if !idPattern.MatchString(agentID) {
		return userAgent{}, invalid("Choose one of your agents.")
	}
	agents, err := h.service.ownedDB.userAgents(ctx, user.ID)
	if err != nil {
		return userAgent{}, err
	}
	for _, u := range agents {
		if u.ID == agentID {
			return u, nil
		}
	}
	return userAgent{}, missing()
}

// saveAgent adds (action "add"), changes, makes default or removes one of
// user's agents, for the Agents forms and Settings → Services, and answers the
// notice to show. Adding one needs a sign-in within the last 10 minutes
// (0.80.0): an agent's endpoint receives the files its folder actions read,
// so a script in a page must not be able to add one of its own.
func (h *httpAdapter) saveAgent(ctx context.Context, user userAccount, session, action, agentID string, form agentForm) (string, error) {
	runner := h.service.agents.Load()
	db := h.service.ownedDB
	if runner == nil || db == nil {
		return "", errAgentsUnavailable
	}
	if action != "add" && !idPattern.MatchString(agentID) {
		return "", invalid("Choose one of your agents.")
	}
	if err := h.service.rates.take(allowance{"agents:user:" + user.ID, 20, 600}); err != nil {
		return "", err
	}
	switch action {
	case "add":
		if fresh, _ := h.service.accounts.sessionFresh(session, time.Now()); !fresh {
			return "", errConfirmFirst
		}
		u, err := newUserAgent(runner.settings.seal, user.ID, form, time.Now())
		if err == nil {
			err = db.addUserAgent(ctx, u)
		}
		return "agent-added", err
	case "change":
		found, err := h.findUserAgent(ctx, user, agentID)
		if err != nil {
			return "", err
		}
		u, err := changedUserAgent(runner.settings.seal, found, agentForm{Name: form.Name, Cap: form.Cap, Key: form.Key})
		if err == nil {
			err = db.changeUserAgent(ctx, u)
		}
		if strings.TrimSpace(form.Key) != "" {
			return "agent-key-changed", err
		}
		return "agent-changed", err
	case "default":
		return "agent-default", db.setDefaultUserAgent(ctx, user.ID, agentID)
	case "remove":
		return "agent-removed", db.removeUserAgent(ctx, user.ID, agentID)
	}
	return "", missing()
}

// testUserAgent makes one tiny call with one of user's agents; ok says
// whether it worked, and the text says how it went either way.
func (h *httpAdapter) testUserAgent(ctx context.Context, user userAccount, agentID string) (string, bool, error) {
	runner := h.service.agents.Load()
	if runner == nil || h.service.ownedDB == nil {
		return "", false, errAgentsUnavailable
	}
	if err := h.service.rates.take(allowance{"agents-test:user:" + user.ID, 10, 600}); err != nil {
		return "", false, err
	}
	found, err := h.findUserAgent(ctx, user, agentID)
	if err != nil {
		return "", false, err
	}
	result, ok := runner.testAgent(ctx, found)
	return result, ok, nil
}

// optInToAction lets user pay with one of their agents for an action whose
// owner set whoever brings a file to pay, and runs the file it skipped.
func (h *httpAdapter) optInToAction(ctx context.Context, user userAccount, jobID, agentID string) (string, error) {
	runner := h.service.agents.Load()
	db := h.service.ownedDB
	if runner == nil || db == nil {
		return "", errAgentsUnavailable
	}
	if !idPattern.MatchString(jobID) || !idPattern.MatchString(agentID) {
		return "", invalid("Choose one of your agents.")
	}
	if err := h.service.rates.take(allowance{"agents:user:" + user.ID, 20, 600}); err != nil {
		return "", err
	}
	offer, err := h.actionOfferFor(ctx, user, jobID)
	if err == nil && offer.Pays != "contributor" {
		err = problem(409, "conflict", "This action is not paid for by whoever brings the file, so there is nothing to opt in to.")
	}
	if err != nil {
		return "", err
	}
	found := false
	for _, u := range offer.Agents {
		found = found || u.ID == agentID
	}
	if !found {
		return "", invalid("Choose one of your " + offer.ModelName + " agents.")
	}
	j, _, err := runner.store.get(ctx, offer.JobID)
	if err != nil {
		return "", err
	}
	id, err := randomHex(16)
	if err == nil {
		err = db.saveOptIn(ctx, actionOptIn{ID: id, UserID: user.ID, SpaceID: j.SpaceID, Folder: j.Folder, Action: j.Action, AgentID: agentID, Created: time.Now()})
	}
	if err != nil {
		return "", err
	}
	// The file that was skipped runs now.
	if j.Status == jobSkipped {
		if err := runner.requeue(ctx, j); err == nil {
			return "action-optin-run", nil
		}
	}
	return "action-optin", nil
}

// optOutOfAction stops user paying for an action in someone else's space.
func (h *httpAdapter) optOutOfAction(ctx context.Context, user userAccount, optInID string) (string, error) {
	if h.service.agents.Load() == nil || h.service.ownedDB == nil {
		return "", errAgentsUnavailable
	}
	if !idPattern.MatchString(optInID) {
		return "", invalid("Choose one of the actions you pay for.")
	}
	if err := h.service.rates.take(allowance{"agents:user:" + user.ID, 20, 600}); err != nil {
		return "", err
	}
	return "action-optout", h.service.ownedDB.removeOptIn(ctx, user.ID, optInID)
}

// testAgent makes one tiny call with an agent and says how it went.
func (a *agentRunner) testAgent(ctx context.Context, u userAgent) (string, bool) {
	key, err := openAgentKey(a.settings.seal, u.UserID, u.ID, u.Sealed)
	if err != nil {
		return u.Name + ": " + err.Error(), false
	}
	m := agentModel{agentID: u.ID, name: u.Name, provider: u.Provider, endpoint: strings.TrimSuffix(u.Endpoint(), "/") + "/v1/messages", model: u.Model, apiKey: key, maxTokens: 64}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := a.providerFor(m).send(ctx, agentRequest{Model: m.model, MaxTokens: m.maxTokens,
		Messages: []agentMessage{{Role: "user", Content: []json.RawMessage{textBlock("Reply with the single word OK.")}}}})
	if err != nil {
		return fmt.Sprintf("%s did not work: %v. Check the key and the model name, then add the agent again if needed.", u.Name, err), false
	}
	reply := ""
	for _, raw := range resp.Content {
		var b contentBlock
		if json.Unmarshal(raw, &b) == nil && b.Type == "text" {
			reply += b.Text
		}
	}
	reply = oneLine(reply)
	if len(reply) > 80 {
		reply = strings.ToValidUTF8(reply[:80], "") + "…"
	}
	return fmt.Sprintf("%s works: %s %s answered “%s” (%d tokens in, %d out).", u.Name, u.ProviderName(), u.Model, reply, resp.Usage.input(), resp.Usage.OutputTokens), true
}
