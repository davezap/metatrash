package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Agents in Services (0.55.0; in the Settings dialog since 0.80.0, site_services.go): add an agent (provider, model,
// API key, daily cap), test it with one tiny call, make it the default,
// change its name, daily cap or key (0.58.0), or remove it. Since 0.56.0 it also lists the actions the user pays for, and
// /account/actions/<job> shows an action that skipped one of the user's
// files, where they opt in to pay for it. Shown only while folder actions
// are enabled on the server.

var agentRoutes = map[string]bool{
	"/account/agents/add":     true,
	"/account/agents/test":    true,
	"/account/agents/default": true,
	"/account/agents/change":  true,
	"/account/agents/remove":  true,
	"/account/agents/optin":   true,
	"/account/agents/optout":  true,
}

var agentFormFields = map[string][]string{
	"/account/agents/add":     {"name", "provider", "model", "key", "endpoint", "cap"},
	"/account/agents/test":    {"agent"},
	"/account/agents/default": {"agent"},
	"/account/agents/change":  {"agent", "name", "cap", "key"},
	"/account/agents/remove":  {"agent"},
	"/account/agents/optin":   {"job", "agent"},
	"/account/agents/optout":  {"optin"},
}

func (a *accounts) agentCSRF(path, session string) string {
	return a.mac("agents:" + path + ":" + session)
}

// agentView is one row of the Agents list.
type agentView struct {
	userAgent
	UsedToday int64
}

// agentProviderView is one provider choice on the Add agent form.
type agentProviderView struct{ Value, Name, Model, KeysURL string }

// agentsSection is the Agents part of Services.
type agentsSection struct {
	CSRF      map[string]string
	Agents    []agentView
	Providers []agentProviderView
	// OptIns are the actions the user pays for in other people's spaces.
	OptIns      []optInView
	Unavailable bool
	Full        bool
	// Form holds what the Add agent form sent after it failed, without the key.
	Form agentForm
}

func (h *httpAdapter) agentsEnabled() bool {
	return h.service.agents.Load() != nil && h.service.ownedDB != nil
}

// agentsPage builds the Agents section for user; ok is false when the list
// could not be read.
func (h *httpAdapter) agentsPage(r *http.Request, user userAccount) (*agentsSection, bool) {
	a := h.service.accounts
	session := cookieToken(r, h.sessionCookieName())
	page := &agentsSection{CSRF: map[string]string{}}
	for path := range agentRoutes {
		page.CSRF[strings.TrimPrefix(path, "/account/agents/")] = a.agentCSRF(path, session)
	}
	for _, name := range agentProviderNames() {
		p := agentProviders[name]
		page.Providers = append(page.Providers, agentProviderView{name, p.name, p.model, p.keysURL})
	}
	runner := h.service.agents.Load()
	agents, err := h.service.ownedDB.userAgents(r.Context(), user.ID)
	if err != nil || runner == nil {
		page.Unavailable = true
		return page, false
	}
	now := time.Now().In(runner.settings.zone)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, runner.settings.zone)
	for _, u := range agents {
		v := agentView{userAgent: u}
		v.UsedToday, _ = runner.store.tokensSince(r.Context(), u.ID, midnight)
		page.Agents = append(page.Agents, v)
	}
	page.Full = len(agents) >= maxAgentsPerUser
	if page.OptIns, err = h.service.ownedDB.userOptIns(r.Context(), user.ID); err != nil {
		page.Unavailable = true
		return page, false
	}
	return page, true
}

// actionOffer is the page of an action that skipped one of the user's files
// (/account/actions/<job>): what the action does, who pays, and, when
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
	CSRF    string
}

// actionOfferPage builds the page for a job whose file the user brought.
func (h *httpAdapter) actionOfferPage(r *http.Request, user userAccount, jobID string) (*actionOffer, error) {
	offer, err := h.actionOfferFor(r.Context(), user, jobID)
	if err == nil {
		offer.CSRF = h.service.accounts.agentCSRF("/account/agents/optin", cookieToken(r, h.sessionCookieName()))
	}
	return offer, err
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

// submitAgent handles the Agents forms after the form, Origin and CSRF checks.
func (h *httpAdapter) submitAgent(w http.ResponseWriter, r *http.Request, session, path string) {
	a := h.service.accounts
	user, signedIn, err := a.currentUser(r.Context(), session)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return
	}
	if !signedIn {
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return
	}
	if h.service.agents.Load() == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	page := accountPage{SignedIn: true, User: user, CSRF: a.mac("logout:" + session), UsernameCSRF: a.mac("username:" + session), Section: "services"}
	fail := func(err error, form *agentForm) {
		status, message := http.StatusServiceUnavailable, "The agent could not be saved. Reload before retrying."
		var p *Error
		if errors.As(err, &p) {
			status, message = p.Status, p.Message
			if p.RetryAfterSeconds > 0 {
				w.Header().Set("Retry-After", strconv.Itoa(p.RetryAfterSeconds))
			}
		}
		page.Message = message
		page.AgentForm = form
		h.renderAccount(w, r, status, page)
	}
	agentID := r.PostForm.Get("agent")
	switch path {
	case "/account/agents/optin", "/account/agents/optout":
		var notice string
		if path == "/account/agents/optin" {
			notice, err = h.optInToAction(r.Context(), user, r.PostForm.Get("job"), agentID)
		} else {
			notice, err = h.optOutOfAction(r.Context(), user, r.PostForm.Get("optin"))
		}
		if err != nil {
			fail(err, nil)
			return
		}
		accountCookie(w, h.noticeCookieName(), notice, 60)
		http.Redirect(w, r, h.basePath+"/account/services#paying", http.StatusSeeOther)
		return
	case "/account/agents/test":
		result, ok, err := h.testUserAgent(r.Context(), user, agentID)
		if err != nil {
			fail(err, nil)
			return
		}
		if ok {
			page.Notice = result
		} else {
			page.Message = result
		}
		h.renderAccount(w, r, http.StatusOK, page)
		return
	}
	form := agentForm{Name: r.PostForm.Get("name"), Provider: r.PostForm.Get("provider"), Model: r.PostForm.Get("model"), Key: r.PostForm.Get("key"), Endpoint: r.PostForm.Get("endpoint"), Cap: r.PostForm.Get("cap")}
	notice, err := h.saveAgent(r.Context(), user, session, strings.TrimPrefix(path, "/account/agents/"), agentID, form)
	if err != nil {
		if path == "/account/agents/add" {
			form.Key = ""
			fail(err, &form)
		} else {
			fail(err, nil)
		}
		return
	}
	accountCookie(w, h.noticeCookieName(), notice, 60)
	http.Redirect(w, r, h.basePath+"/account/services#agents", http.StatusSeeOther)
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
