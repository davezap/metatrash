package service

import (
	"context"
	"net/http"
	"regexp"
	"time"
)

// Services in the site app's Settings dialog (0.80.0), in place of
// /account/services, /account/apps/{id} and /account/actions/{job}:
//
//	GET  /api/v1/site/account/services                         connected apps, agents and actions paid for, GitHub
//	GET  /api/v1/site/account/services/apps/{id}               one connection's spaces, to change them
//	POST /api/v1/site/account/services/apps/{id}/spaces        {"spaces": {"<space id>": "none|read_only|read_write"}}
//	POST /api/v1/site/account/services/apps/{id}/revoke
//	POST /api/v1/site/account/services/agents                  {"name", "provider", "model", "key", "endpoint", "cap"}
//	POST /api/v1/site/account/services/agents/{id}/{action}    test, default, change ({"name", "cap", "key"}), remove
//	GET  /api/v1/site/account/services/actions/{job}           an action that skipped one of your files
//	POST /api/v1/site/account/services/actions/{job}/optin     {"agent"}
//	POST /api/v1/site/account/services/optins/{id}/optout
//	POST /api/v1/site/account/services/github/{connect|link}   answers {"redirect"}: the github.com address to go to
//	POST /api/v1/site/account/services/github/{installation}/disconnect
//
// They use the same calls, rate limits and notices as the forms, behind the
// site API's checks (serveSiteAccount). Two steps need a sign-in within the
// last 10 minutes, so a script in a page can't take them alone: giving a
// connected app a space or write access it did not have, and adding an
// agent (its endpoint receives the files folder actions read).

type siteAppSpace struct {
	Name   string `json:"name"`
	Access string `json:"access"`
}

type siteApp struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Host     string         `json:"host"`
	Kind     string         `json:"kind"`
	Created  string         `json:"created"`
	LastUsed string         `json:"lastUsed,omitempty"`
	Spaces   []siteAppSpace `json:"spaces"`
}

type siteApps struct {
	// ConnectURL is the address to add to an app as a custom connector.
	ConnectURL string    `json:"connectURL"`
	List       []siteApp `json:"list"`
}

type siteAgent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Provider     string `json:"provider"`
	ProviderName string `json:"providerName"`
	Model        string `json:"model"`
	KeyHint      string `json:"keyHint"`
	Endpoint     string `json:"endpoint,omitempty"`
	Default      bool   `json:"default,omitempty"`
	DailyCap     int64  `json:"dailyCap"`
	UsedToday    int64  `json:"usedToday"`
	Created      string `json:"created"`
}

type siteAgentProvider struct {
	Value   string `json:"value"`
	Name    string `json:"name"`
	Model   string `json:"model"`
	KeysURL string `json:"keysURL"`
}

type siteOptIn struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Space     string `json:"space"`
	SpaceName string `json:"spaceName"`
	Folder    string `json:"folder"`
	AgentName string `json:"agentName"`
	Created   string `json:"created"`
}

type siteAgents struct {
	List      []siteAgent         `json:"list"`
	Providers []siteAgentProvider `json:"providers"`
	OptIns    []siteOptIn         `json:"optIns"`
	Max       int                 `json:"max"`
}

type siteGitHubInstallation struct {
	ID           string `json:"id"`
	AccountLogin string `json:"accountLogin"`
	AccountType  string `json:"accountType"`
	GitHubLogin  string `json:"githubLogin"`
	Status       string `json:"status"`
	Connected    string `json:"connected"`
	SettingsURL  string `json:"settingsURL"`
}

type siteGitHub struct {
	List []siteGitHubInstallation `json:"list"`
}

// siteServices is GET /services: each part is there only while the server
// offers it, and Unavailable names the parts that could not be read just now.
type siteServices struct {
	Apps        *siteApps   `json:"apps,omitempty"`
	Agents      *siteAgents `json:"agents,omitempty"`
	GitHub      *siteGitHub `json:"github,omitempty"`
	Fresh       bool        `json:"fresh,omitempty"`
	Unavailable []string    `json:"unavailable,omitempty"`
}

type siteAppSpaceChoice struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Space        string `json:"space"`
	Owned        bool   `json:"owned,omitempty"`
	Choice       string `json:"choice"`
	WriteAllowed bool   `json:"writeAllowed,omitempty"`
}

// siteAppSpaces is GET /services/apps/{id}: the consent page's chooser,
// with the connection's current choices.
type siteAppSpaces struct {
	ID       string               `json:"id"`
	Name     string               `json:"name"`
	Host     string               `json:"host"`
	Kind     string               `json:"kind"`
	Created  string               `json:"created"`
	CanWrite bool                 `json:"canWrite,omitempty"`
	Spaces   []siteAppSpaceChoice `json:"spaces"`
	// Kept: connected spaces not listed (suspended), which stay connected.
	Kept  int  `json:"kept,omitempty"`
	Fresh bool `json:"fresh,omitempty"`
}

type siteActionAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	KeyHint string `json:"keyHint"`
}

// siteAction is GET /services/actions/{job}.
type siteAction struct {
	Job       string            `json:"job"`
	Space     string            `json:"space"`
	Folder    string            `json:"folder"`
	Action    string            `json:"action"`
	File      string            `json:"file"`
	Status    string            `json:"status"`
	Reason    string            `json:"reason,omitempty"`
	Model     string            `json:"model,omitempty"`
	ModelName string            `json:"modelName"`
	KeysURL   string            `json:"keysURL,omitempty"`
	Do        string            `json:"do,omitempty"`
	DoFile    string            `json:"doFile,omitempty"`
	Steps     int               `json:"steps"`
	Pays      string            `json:"pays"`
	Agents    []siteActionAgent `json:"agents"`
	Current   string            `json:"current,omitempty"`
}

var installationPattern = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)

var siteAgentActions = map[string]bool{"test": true, "default": true, "change": true, "remove": true}

// serveSiteServices serves /account/services/...; parts is the path after
// "services". The session is signed in (serveSiteAccount checked).
func (h *httpAdapter) serveSiteServices(w http.ResponseWriter, r *http.Request, user userAccount, session string, parts []string) {
	ctx := r.Context()
	n := len(parts)
	id := func(i int) bool { return n > i && idPattern.MatchString(parts[i]) }
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if !read && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		sendError(w, problem(405, "invalid_request", "Use GET or POST."))
		return
	}
	answer := func(value any, err error, route string) {
		if err != nil {
			sendSiteAccountError(w, err, route)
			return
		}
		sendJSON(w, 200, value)
	}

	// Reads.
	readRoute := n == 0 || n == 2 && (parts[0] == "apps" || parts[0] == "actions") && id(1)
	if readRoute && read {
		switch {
		case n == 0:
			sendJSON(w, 200, h.siteServicesState(ctx, user, session))
		case parts[0] == "apps":
			value, err := h.siteAppSpacesState(ctx, user, session, parts[1])
			answer(value, err, "apps")
		default:
			value, err := h.siteActionState(ctx, user, parts[1])
			answer(value, err, "actions")
		}
		return
	}
	if readRoute {
		w.Header().Set("Allow", "GET")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return
	}

	// Writes.
	known := n == 1 && parts[0] == "agents" ||
		n == 3 && parts[0] == "apps" && id(1) && (parts[2] == "spaces" || parts[2] == "revoke") ||
		n == 3 && parts[0] == "agents" && id(1) && siteAgentActions[parts[2]] ||
		n == 3 && parts[0] == "actions" && id(1) && parts[2] == "optin" ||
		n == 3 && parts[0] == "optins" && id(1) && parts[2] == "optout" ||
		n == 2 && parts[0] == "github" && (parts[1] == "connect" || parts[1] == "link") ||
		n == 3 && parts[0] == "github" && installationPattern.MatchString(parts[1]) && parts[2] == "disconnect"
	if !known {
		sendError(w, missing())
		return
	}
	if read {
		w.Header().Set("Allow", "POST")
		sendError(w, problem(405, "invalid_request", "Use POST."))
		return
	}
	// Change spaces sends one choice per space: up to 400 of them.
	body, ok := h.readSiteAccountBody(w, r, 64*1024)
	if !ok {
		return
	}
	var value siteNotice
	var err error
	route := parts[0]
	switch {
	case parts[0] == "apps" && parts[2] == "spaces":
		route = "app-spaces"
		choices := make([]oauthSpaceChoice, 0, len(body.Spaces))
		for spaceID, permission := range body.Spaces {
			if !idPattern.MatchString(spaceID) || accessRank[permission] == 0 && permission != "none" {
				err = invalid("Invalid space selection.")
				break
			}
			choices = append(choices, oauthSpaceChoice{SpaceID: spaceID, Permission: permission})
		}
		if err == nil {
			err = h.changeAppSpaces(ctx, user, session, parts[1], choices)
		}
		value.Notice = "app-spaces"
	case parts[0] == "apps":
		err = h.revokeApp(ctx, user, parts[1])
		value.Notice = "app-revoked"
	case parts[0] == "agents" && n == 1:
		route = "agent-add"
		value.Notice, err = h.saveAgent(ctx, user, session, "add", "", agentForm{Name: body.Name, Provider: body.Provider, Model: body.Model, Key: body.Key, Endpoint: body.Endpoint, Cap: body.Cap})
	case parts[0] == "agents" && parts[2] == "test":
		var worked bool
		value.Notice, worked, err = h.testUserAgent(ctx, user, parts[1])
		value.Failed = !worked
	case parts[0] == "agents":
		value.Notice, err = h.saveAgent(ctx, user, session, parts[2], parts[1], agentForm{Name: body.Name, Cap: body.Cap, Key: body.Key})
	case parts[0] == "actions":
		value.Notice, err = h.optInToAction(ctx, user, parts[1], body.Agent)
	case parts[0] == "optins":
		value.Notice, err = h.optOutOfAction(ctx, user, parts[1])
	case parts[0] == "github" && n == 2:
		if h.github == nil {
			err = missing()
		} else {
			value.Redirect, err = h.beginGitHubConnect(w, user, parts[1] == "link")
		}
	case parts[0] == "github":
		err = h.disconnectGitHub(ctx, user, parts[1])
		value.Notice = "github-disconnected"
	}
	if err != nil {
		sendSiteAccountError(w, err, route)
		return
	}
	sendSiteNotice(w, value)
}

// siteServicesState is GET /services.
func (h *httpAdapter) siteServicesState(ctx context.Context, user userAccount, session string) siteServices {
	var out siteServices
	out.Fresh, _ = h.service.accounts.sessionFresh(session, time.Now())
	if h.oauth != nil {
		out.Apps = &siteApps{ConnectURL: h.oauth.mcpResource, List: []siteApp{}}
		if apps, err := h.connectedApps(ctx, user); err != nil {
			out.Unavailable = append(out.Unavailable, "apps")
		} else {
			for _, a := range apps {
				item := siteApp{ID: a.ID, Name: a.Name, Host: a.Host, Kind: a.Kind, Created: a.Created, LastUsed: a.LastUsed, Spaces: []siteAppSpace{}}
				for _, s := range a.Spaces {
					item.Spaces = append(item.Spaces, siteAppSpace{Name: s.Name, Access: s.Access})
				}
				out.Apps.List = append(out.Apps.List, item)
			}
		}
	}
	if h.agentsEnabled() {
		out.Agents = &siteAgents{List: []siteAgent{}, Providers: []siteAgentProvider{}, OptIns: []siteOptIn{}, Max: maxAgentsPerUser}
		for _, name := range agentProviderNames() {
			p := agentProviders[name]
			out.Agents.Providers = append(out.Agents.Providers, siteAgentProvider{Value: name, Name: p.name, Model: p.model, KeysURL: p.keysURL})
		}
		if err := h.siteAgentsList(ctx, user, out.Agents); err != nil {
			out.Unavailable = append(out.Unavailable, "agents")
		}
	}
	if h.github != nil {
		out.GitHub = &siteGitHub{List: []siteGitHubInstallation{}}
		list, err := h.service.ownedDB.githubInstallations(ctx, user.ID)
		if err != nil {
			out.Unavailable = append(out.Unavailable, "github")
		}
		for _, inst := range list {
			v := githubInstallationViewOf(h.github, inst)
			out.GitHub.List = append(out.GitHub.List, siteGitHubInstallation{ID: v.ID, AccountLogin: v.AccountLogin, AccountType: v.AccountType,
				GitHubLogin: v.GitHubLogin, Status: v.Status, Connected: v.Connected, SettingsURL: v.SettingsURL})
		}
	}
	return out
}

// siteAgentsList fills in user's agents, with today's use, and the actions
// they pay for.
func (h *httpAdapter) siteAgentsList(ctx context.Context, user userAccount, out *siteAgents) error {
	runner := h.service.agents.Load()
	if runner == nil {
		return errAgentsUnavailable
	}
	agents, err := h.service.ownedDB.userAgents(ctx, user.ID)
	if err != nil {
		return err
	}
	now := time.Now().In(runner.settings.zone)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, runner.settings.zone)
	for _, u := range agents {
		used, _ := runner.store.tokensSince(ctx, u.ID, midnight)
		out.List = append(out.List, siteAgent{ID: u.ID, Name: u.Name, Provider: u.Provider, ProviderName: u.ProviderName(), Model: u.Model,
			KeyHint: u.KeyHint, Endpoint: u.BaseURL, Default: u.Default, DailyCap: u.DailyCap, UsedToday: used, Created: u.Created.UTC().Format("2006-01-02")})
	}
	optIns, err := h.service.ownedDB.userOptIns(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, o := range optIns {
		out.OptIns = append(out.OptIns, siteOptIn{ID: o.ID, Action: o.Action, Space: o.Space, SpaceName: o.SpaceName, Folder: o.FolderName(),
			AgentName: o.AgentName, Created: o.Created.UTC().Format("2006-01-02")})
	}
	return nil
}

func (h *httpAdapter) siteAppSpacesState(ctx context.Context, user userAccount, session, grantID string) (siteAppSpaces, error) {
	page, err := h.appSpaces(ctx, user, grantID)
	if err != nil {
		return siteAppSpaces{}, err
	}
	out := siteAppSpaces{ID: page.ID, Name: page.Name, Host: page.Host, Kind: page.Kind, Created: page.Created, CanWrite: page.CanWrite, Kept: page.Kept, Spaces: []siteAppSpaceChoice{}}
	out.Fresh, _ = h.service.accounts.sessionFresh(session, time.Now())
	for _, s := range page.Spaces {
		out.Spaces = append(out.Spaces, siteAppSpaceChoice{ID: s.ID, Name: s.Name, Space: s.Owner + "/" + s.Slug, Owned: s.Owned, Choice: s.Choice, WriteAllowed: s.WriteAllowed})
	}
	return out, nil
}

func (h *httpAdapter) siteActionState(ctx context.Context, user userAccount, jobID string) (siteAction, error) {
	offer, err := h.actionOfferFor(ctx, user, jobID)
	if err != nil {
		return siteAction{}, err
	}
	out := siteAction{Job: offer.JobID, Space: offer.Space, Folder: offer.Folder, Action: offer.Action, File: offer.File, Status: offer.Status,
		Reason: offer.Reason, Model: offer.Model, ModelName: offer.ModelName, KeysURL: offer.KeysURL, Do: offer.Do, DoFile: offer.DoFile,
		Steps: offer.Steps, Pays: offer.Pays, Agents: []siteActionAgent{}, Current: offer.Current}
	if offer.Status != jobSkipped {
		out.Reason = ""
	}
	for _, u := range offer.Agents {
		out.Agents = append(out.Agents, siteActionAgent{ID: u.ID, Name: u.Name, KeyHint: u.KeyHint})
	}
	return out, nil
}
