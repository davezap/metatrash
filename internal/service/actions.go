package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Folder actions (0.54.0). A folder's .metatrash.json can list actions: a
// name, a trigger, and what to do in plain words. When one fires the runner
// (agent_runner.go) runs an agent loop pinned to that folder:
//
//	{"actions": [{"name": "tidy-draft", "on": "inbound/*.md",
//	              "do": "Rewrite this draft ... then move it to completed/."}]}
//
// on fires once for each file that arrives (created, or moved in) and, with
// "when": "change", also for each edit of a file already there. every (a
// schedule) and web (the web tools) are accepted in the shape they will have
// but not run yet. Actions are never inherited, so a subfolder never runs its
// parent's actions twice, and they are allowed only in owned spaces.
//
// Who pays (0.56.0): an owner's action runs on the owner's agent, or with
// "pays": "contributor" on the agent of whoever brought the file, once they
// opt in (agent_optins.go). An action with "member" is that member's own: it
// runs only for their files, on their agent.
type folderAction struct {
	Name string `json:"name"`
	// On holds file patterns relative to the folder, as a list or one
	// comma-separated string: a name or path.Match pattern per segment
	// (inbound/*.md), or a subfolder ending in / for every file under it.
	On    actionPatterns `json:"on,omitempty"`
	Every string         `json:"every,omitempty"`
	// When is "arrive" (the default) or "change".
	When string `json:"when,omitempty"`
	// Do is the instructions, or the name of a .md file in the folder that
	// holds them.
	Do string `json:"do"`
	// Model picks which of the owner's agents runs it, as provider/model
	// ("minimax/MiniMax-M3"); without it, the owner's default agent.
	Model string `json:"model,omitempty"`
	Web   *bool  `json:"web,omitempty"`
	Steps int    `json:"steps,omitempty"`
	// Pays is "owner" (the default) or "contributor".
	Pays string `json:"pays,omitempty"`
	// Member is the username of the member whose own action this is.
	Member string `json:"member,omitempty"`
}

type actionPatterns []string

func (p *actionPatterns) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*p = nil
		for _, part := range strings.Split(one, ",") {
			*p = append(*p, strings.TrimSpace(part))
		}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return fmt.Errorf("on is a pattern, a comma-separated string of patterns or a list of them")
	}
	*p = list
	return nil
}

const (
	maxFolderActions      = 20
	maxActionPatterns     = 20
	maxActionDo           = 4000
	defaultActionSteps    = 40
	maxActionSteps        = 200
	actionWhenArrive      = "arrive"
	actionWhenChange      = "change"
	actionPaysOwner       = "owner"
	actionPaysContributor = "contributor"
	// runnerLogName is the file in an action folder where the runner records
	// each job. Agents read it but never write it.
	runnerLogName = "runner.md"
)

var actionNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

func actionError(name, message string) *Error {
	if name != "" {
		message = fmt.Sprintf("action %q: %s", name, message)
	}
	return folderConfigError(message)
}

// checkActions validates actions on their own (parseFolderConfig).
func checkActions(actions []folderAction) error {
	if len(actions) > maxFolderActions {
		return folderConfigError(fmt.Sprintf("actions is limited to %d entries.", maxFolderActions))
	}
	names := map[string]bool{}
	for _, a := range actions {
		if !actionNamePattern.MatchString(a.Name) {
			return actionError("", fmt.Sprintf("each action needs a name: a short slug of lowercase letters, digits and hyphens, such as tidy-draft (got %q).", a.Name))
		}
		if names[a.Name] {
			return actionError(a.Name, "action names must be unique in the folder.")
		}
		names[a.Name] = true
		switch {
		case len(a.On) == 0 && a.Every == "":
			return actionError(a.Name, `say when it runs: "on" with file patterns, such as "inbound/*.md".`)
		case len(a.On) > 0 && a.Every != "":
			return actionError(a.Name, `use either "on" (files) or "every" (a schedule), not both.`)
		case a.Every != "":
			return actionError(a.Name, `"every" schedules are not supported yet; use "on" with file patterns.`)
		}
		if len(a.On) > maxActionPatterns {
			return actionError(a.Name, fmt.Sprintf("on is limited to %d patterns.", maxActionPatterns))
		}
		for _, pattern := range a.On {
			if err := checkActionPattern(pattern); err != nil {
				return actionError(a.Name, err.Error())
			}
		}
		if a.When != "" && a.When != actionWhenArrive && a.When != actionWhenChange {
			return actionError(a.Name, `when is "arrive" (the default: a file is created or moved in) or "change" (also when a file there is edited).`)
		}
		if strings.TrimSpace(a.Do) == "" {
			return actionError(a.Name, "do says what to do, in plain words, or names a .md file in this folder that does.")
		}
		if utf8.RuneCountInString(a.Do) > maxActionDo {
			return actionError(a.Name, fmt.Sprintf("do is limited to %d characters; put longer instructions in a .md file in this folder and give its name.", maxActionDo))
		}
		if a.Model != "" {
			if _, _, ok := actionModel(a.Model); !ok {
				return actionError(a.Name, fmt.Sprintf(`model is a provider and model, such as "minimax/MiniMax-M3" or "anthropic/claude-sonnet-5-5" (got %q); leave it out to use the owner's default agent.`, a.Model))
			}
		}
		if a.Steps != 0 && (a.Steps < 1 || a.Steps > maxActionSteps) {
			return actionError(a.Name, fmt.Sprintf("steps (most tool calls per job) is 1 to %d; the default is %d.", maxActionSteps, defaultActionSteps))
		}
		switch a.Pays {
		case "", actionPaysOwner:
		case actionPaysContributor:
			if a.Model == "" {
				return actionError(a.Name, `"pays": "contributor" needs a model, such as "minimax/MiniMax-M3", so the model is the same whoever brings the file.`)
			}
		default:
			return actionError(a.Name, `pays is "owner" (the default: the space owner's agent pays for every file) or "contributor" (whoever brings a file pays for it, once they opt in).`)
		}
		if a.Member != "" {
			if len(a.Member) < 3 || len(a.Member) > 32 || !usernamePattern.MatchString(a.Member) {
				return actionError(a.Name, fmt.Sprintf("member is the username of the member whose own action this is (got %q).", a.Member))
			}
			if a.Pays != "" {
				return actionError(a.Name, "a member's own action always runs on that member's agent; leave out pays.")
			}
		}
	}
	return nil
}

// paysContributor reports whether whoever brings a file pays for its job.
func (a folderAction) paysContributor() bool { return a.Pays == actionPaysContributor }

// checkActionPattern validates one on pattern: a relative path whose
// segments are names or path.Match patterns, or a subfolder ending in /.
func checkActionPattern(pattern string) error {
	name := strings.TrimSuffix(pattern, "/")
	bad := fmt.Errorf("on pattern %q must be a file name or pattern relative to this folder, such as *.md or inbound/*.pdf, or a subfolder ending in /, such as inbound/.", pattern)
	if name == "" || len(pattern) > 240 || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") || strings.Count(name, "/") > 7 {
		return bad
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, "**") {
			return bad
		}
		if _, err := path.Match(segment, ""); err != nil {
			return bad
		}
	}
	return nil
}

// matches reports whether rel, a path relative to the action's folder,
// matches one of its patterns.
func (a folderAction) matches(rel string) bool {
	for _, pattern := range a.On {
		if folder, ok := strings.CutSuffix(pattern, "/"); ok {
			if matchSegments(folder, rel, true) {
				return true
			}
		} else if matchSegments(pattern, rel, false) {
			return true
		}
	}
	return false
}

// matchSegments matches pattern against rel segment by segment. With prefix
// set, pattern need only match rel's leading folders (rel lies below them).
func matchSegments(pattern, rel string, prefix bool) bool {
	want, have := strings.Split(pattern, "/"), strings.Split(rel, "/")
	if prefix && len(have) <= len(want) || !prefix && len(have) != len(want) {
		return false
	}
	for i, segment := range want {
		if ok, _ := path.Match(segment, have[i]); !ok {
			return false
		}
	}
	return true
}

func (a folderAction) steps() int {
	if a.Steps == 0 {
		return defaultActionSteps
	}
	return a.Steps
}

// webEnabled reports whether the action may use the web tools (to come).
func (a folderAction) webEnabled() bool { return a.Web == nil || *a.Web }

// doFile returns the instructions file the action names, relative to its
// folder, or "" when do holds the instructions themselves.
func (a folderAction) doFile() string {
	do := strings.TrimSpace(a.Do)
	if strings.HasSuffix(strings.ToLower(do), ".md") && !strings.ContainsAny(do, " \t\n") && validPath(do) {
		return do
	}
	return ""
}

// actionTrigger is one job a commit asks for: an action of a folder, and the
// file that set it off. Member is the action's member, for a member's own
// action, which runs only for that member's files.
type actionTrigger struct {
	Folder, Action, File, Member string
}

// triggersBetween compares two states of a space and returns the jobs the
// change asks for: for each folder with actions, each file under it that
// arrived (a path the old state lacked) or, for "when": "change", was edited,
// and matches an action. A folder's own files (AGENTS.md, .metatrash.json)
// and its runner.md never trigger. The actions are those of the new state.
func (r *repository) triggersBetween(ctx context.Context, old, state string) ([]actionTrigger, error) {
	_, before, err := r.snapshot(ctx, old)
	if err != nil {
		return nil, err
	}
	_, after, err := r.snapshot(ctx, state)
	if err != nil {
		return nil, err
	}
	actions := map[string][]folderAction{}
	for p, f := range after {
		if !isFolderConfig(p) {
			continue
		}
		c, err := r.cachedConfig(ctx, f.Blob)
		if err != nil || len(c.Actions) == 0 {
			continue
		}
		actions[configFolder(p)] = c.Actions
	}
	if len(actions) == 0 {
		return nil, nil
	}
	var triggers []actionTrigger
	for p, f := range after {
		was, existed := before[p]
		edited := existed && was.Blob != f.Blob
		if existed && !edited || isFolderConfig(p) || isAgentsFile(p) {
			continue
		}
		for folder, list := range actions {
			if !strings.HasPrefix(p, folder) || p == folder+runnerLogName {
				continue
			}
			rel := p[len(folder):]
			for _, a := range list {
				if (!edited || a.When == actionWhenChange) && a.matches(rel) {
					triggers = append(triggers, actionTrigger{Folder: folder, Action: a.Name, File: p, Member: a.Member})
				}
			}
		}
	}
	return triggers, nil
}

// checkActionsWriter refuses a write to path that adds, changes or removes
// actions unless a person makes it as themselves (Metatrash Sync or Slate
// Viewer), since actions spend someone's tokens. The space owner may change
// their own actions, remove a member's, or adopt one by taking out "member",
// but never write one in a member's name. A member may add, change or remove
// only their own actions, marked "member" with their username. No agent, the
// owner's own included, may change actions.
func (s *Service) checkActionsWriter(ctx context.Context, r *repository, files map[string]record, path, text string) error {
	old, err := r.configText(ctx, files, configFolder(path))
	if err != nil {
		return err
	}
	if configKeys(old)["actions"] == configKeys(text)["actions"] {
		return nil
	}
	identity, _ := ctx.Value(mcpIdentityKey{}).(mcpIdentity)
	owner := s.repositoryOwner(r)
	if !personWrite(ctx) || identity.oauth == nil || owner == "" {
		return problem(403, "forbidden", path+": actions can be added, changed or removed only by a person writing as themselves through Metatrash Sync or Slate Viewer: the space owner, or a member for their own actions. Agents cannot, because actions run on someone's tokens.")
	}
	before, after := actionsByName(old), actionsByName(text)
	if identity.oauth.UserID == owner {
		for name, a := range after {
			if b, ok := before[name]; a.Member != "" && (!ok || !sameAction(a, b)) {
				return problem(403, "forbidden", fmt.Sprintf("%s: action %q is %s's own and runs on their agent; you can remove it, or adopt it by taking out \"member\", but not add or change it in their name.", path, name, a.Member))
			}
		}
		return nil
	}
	username := s.usernameOf(ctx, identity.oauth.UserID)
	if username == "" {
		return problem(403, "forbidden", path+": choose a username first (Your account → Profile); your own actions are marked with it.")
	}
	names := map[string]bool{}
	for name := range before {
		names[name] = true
	}
	for name := range after {
		names[name] = true
	}
	for name := range names {
		a, now := after[name]
		b, was := before[name]
		if now && was && sameAction(a, b) {
			continue
		}
		if was && b.Member != username || now && a.Member != username {
			return problem(403, "forbidden", fmt.Sprintf("%s: you can add, change or remove only your own actions, marked \"member\": %q, which run on your own agent and only for your files; action %q is not yours.", path, username, name))
		}
	}
	return nil
}

// actionsByName reads the actions of a configuration text, by name.
func actionsByName(text string) map[string]folderAction {
	out := map[string]folderAction{}
	if c, err := parseFolderConfig(text); err == nil {
		for _, a := range c.Actions {
			out[a.Name] = a
		}
	}
	return out
}

func sameAction(a, b folderAction) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// usernameOf is a user's username, or "" when they have none or it cannot be
// read.
func (s *Service) usernameOf(ctx context.Context, userID string) string {
	if s.usernameHook != nil {
		return s.usernameHook(userID)
	}
	if s.ownedDB == nil {
		return ""
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var username string
	if err := s.ownedDB.db.QueryRowContext(lookup, "SELECT COALESCE(username, '') FROM metatrash_users WHERE user_id = ?", userID).Scan(&username); err != nil {
		return ""
	}
	return username
}

// userIDOf is the user ID of a username, or "" when nobody has it.
func (s *Service) userIDOf(ctx context.Context, username string) string {
	if s.userIDHook != nil {
		return s.userIDHook(username)
	}
	if s.ownedDB == nil || username == "" {
		return ""
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id string
	if err := s.ownedDB.db.QueryRowContext(lookup, "SELECT user_id FROM metatrash_users WHERE username = ?", username).Scan(&id); err != nil {
		return ""
	}
	return id
}
