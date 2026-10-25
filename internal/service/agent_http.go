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

// Agents in Your account → Services (0.55.0): add an agent (provider, model,
// API key, daily cap), test it with one tiny call, make it the default, or
// remove it. Shown only while folder actions are enabled on the server.

var agentRoutes = map[string]bool{
	"/account/agents/add":     true,
	"/account/agents/test":    true,
	"/account/agents/default": true,
	"/account/agents/remove":  true,
}

var agentFormFields = map[string][]string{
	"/account/agents/add":     {"name", "provider", "model", "key", "endpoint", "cap"},
	"/account/agents/test":    {"agent"},
	"/account/agents/default": {"agent"},
	"/account/agents/remove":  {"agent"},
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
	CSRF        map[string]string
	Agents      []agentView
	Providers   []agentProviderView
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
	return page, true
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
	runner := h.service.agents.Load()
	if runner == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	db := h.service.ownedDB
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
	case "/account/agents/add":
		form := agentForm{Name: r.PostForm.Get("name"), Provider: r.PostForm.Get("provider"), Model: r.PostForm.Get("model"), Key: r.PostForm.Get("key"), Endpoint: r.PostForm.Get("endpoint"), Cap: r.PostForm.Get("cap")}
		if err := h.service.rates.take(allowance{"agents:user:" + user.ID, 20, 600}); err != nil {
			fail(err, &form)
			return
		}
		u, err := newUserAgent(runner.settings.seal, user.ID, form, time.Now())
		if err == nil {
			err = db.addUserAgent(r.Context(), u)
		}
		if err != nil {
			form.Key = ""
			fail(err, &form)
			return
		}
		accountCookie(w, h.noticeCookieName(), "agent-added", 60)
	case "/account/agents/default", "/account/agents/remove":
		if !idPattern.MatchString(agentID) {
			fail(invalid("Choose one of your agents."), nil)
			return
		}
		err := h.service.rates.take(allowance{"agents:user:" + user.ID, 20, 600})
		if err == nil && path == "/account/agents/default" {
			err = db.setDefaultUserAgent(r.Context(), user.ID, agentID)
		} else if err == nil {
			err = db.removeUserAgent(r.Context(), user.ID, agentID)
		}
		if err != nil {
			fail(err, nil)
			return
		}
		notice := "agent-default"
		if path == "/account/agents/remove" {
			notice = "agent-removed"
		}
		accountCookie(w, h.noticeCookieName(), notice, 60)
	case "/account/agents/test":
		if err := h.service.rates.take(allowance{"agents-test:user:" + user.ID, 10, 600}); err != nil {
			fail(err, nil)
			return
		}
		agents, err := db.userAgents(r.Context(), user.ID)
		if err != nil {
			fail(err, nil)
			return
		}
		var found *userAgent
		for i := range agents {
			if agents[i].ID == agentID {
				found = &agents[i]
			}
		}
		if found == nil {
			fail(missing(), nil)
			return
		}
		result, ok := runner.testAgent(r.Context(), *found)
		if ok {
			page.Notice = result
		} else {
			page.Message = result
		}
		h.renderAccount(w, r, http.StatusOK, page)
		return
	}
	http.Redirect(w, r, h.basePath+"/account/services#agents", http.StatusSeeOther)
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
