package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Locks. A folder's .metatrash.json can lock its own keys (locked), the
// files in it (readOnly) and, through two names in locked, filling in its
// forms (fillForms) and taking its quizzes (takeQuizzes). Each lock has a
// level saying whom it stops:
//   - "folderAgents": the agents of the space's own folder actions (jobs).
//   - "connectedAgents": every other agent: assistants connected over MCP,
//     apps, space keys and GitHub pulls.
//   - "agents": both kinds of agent. People can (see personWrite): Metatrash
//     Sync and the website's editor, and in a spaces.json space such as the
//     public space only the usernames its people lists.
//   - "everyone": nobody, people included, until a person lifts the lock.
//
// Anything not listed is open to everyone. A person can always edit locked and
// readOnly themselves, so every lock can be lifted, and agents never can
// change locked. readOnly is an ordinary key: agents may change it unless
// locked lists it. Both are maps of name to level; a list, as 0.45.0 wrote
// locked, locks each name at "agents".
const (
	lockFolderAgents    = "folderAgents"
	lockConnectedAgents = "connectedAgents"
	lockAgents          = "agents"
	lockEveryone        = "everyone"
)

// Lock names in locked that are not keys: who may fill in the folder's forms
// and take its quizzes.
const (
	lockFillForms   = "fillForms"
	lockTakeQuizzes = "takeQuizzes"
)

// writer is who makes a change, as far as locks are concerned.
type writer int

const (
	writerPerson writer = 1 << iota
	writerFolderAgent
	writerConnectedAgent
)

// lockStops is the writers each level stops.
var lockStops = map[string]writer{
	lockFolderAgents:    writerFolderAgent,
	lockConnectedAgents: writerConnectedAgent,
	lockAgents:          writerFolderAgent | writerConnectedAgent,
	lockEveryone:        writerPerson | writerFolderAgent | writerConnectedAgent,
}

// writerOf says who makes the changes of a request: a person (Metatrash Sync,
// Slate Viewer, the website's editor), the agent of a folder action's job, or
// any other agent.
func writerOf(ctx context.Context) writer {
	switch {
	case personWrite(ctx):
		return writerPerson
	case runnerWrite(ctx):
		return writerFolderAgent
	}
	return writerConnectedAgent
}

const maxLockEntries = 200

// lockMap maps a key or file pattern to its lock level.
type lockMap map[string]string

func (m *lockMap) UnmarshalJSON(b []byte) error {
	var names []string
	if err := json.Unmarshal(b, &names); err == nil {
		*m = make(lockMap, len(names))
		for _, name := range names {
			(*m)[name] = lockAgents
		}
		return nil
	}
	var levels map[string]string
	if err := json.Unmarshal(b, &levels); err != nil {
		return fmt.Errorf("locks are an object of name to \"folderAgents\", \"connectedAgents\", \"agents\" or \"everyone\"")
	}
	*m = levels
	return nil
}

// blocks reports whether a lock at level stops the writer.
func blocks(level string, w writer) bool {
	return lockStops[level]&w != 0
}

// stronger returns the level that stops everyone either level stops.
func stronger(a, b string) string {
	stops := lockStops[a] | lockStops[b]
	for _, level := range []string{lockFolderAgents, lockConnectedAgents, lockAgents, lockEveryone} {
		if lockStops[level] == stops {
			return level
		}
	}
	return ""
}

// checkLockLevels validates the levels of locked or readOnly.
func checkLockLevels(key string, m lockMap) error {
	if len(m) > maxLockEntries {
		return folderConfigError(fmt.Sprintf("%s is limited to %d entries.", key, maxLockEntries))
	}
	for name, level := range m {
		if _, ok := lockStops[level]; !ok {
			return folderConfigError(fmt.Sprintf("%s %q must be \"folderAgents\" (the agents of folder actions can't), \"connectedAgents\" (other agents can't), \"agents\" (no agent can, people can) or \"everyone\" (nobody can until a person lifts the lock), not %q.", key, name, level))
		}
	}
	return nil
}

// checkReadOnlyEntry validates one readOnly name.
func checkReadOnlyEntry(entry string) error {
	name := strings.TrimSuffix(entry, "/")
	if name == "" || len(entry) > 240 || strings.Contains(name, "/") || strings.Contains(name, `\`) || name == "." || name == ".." {
		return folderConfigError(fmt.Sprintf("readOnly name %q must be one file name or pattern in this folder, such as plan.md or *.pdf, or a subfolder ending in /, such as archive/.", entry))
	}
	if _, err := path.Match(name, ""); err != nil {
		return folderConfigError(fmt.Sprintf("readOnly name %q is not a valid pattern.", entry))
	}
	return nil
}

// readOnlyRules returns each folder's readOnly entries. written, when set,
// holds configuration texts about to be written, which decide for their own
// folders. readOnly is never inherited: its names are relative to its folder.
func (r *repository) readOnlyRules(ctx context.Context, files map[string]record, written map[string]string) (map[string]lockMap, error) {
	rules := map[string]lockMap{}
	var hashes []string
	for p, f := range files {
		if _, ok := written[p]; !ok && isFolderConfig(p) {
			hashes = append(hashes, f.Blob)
		}
	}
	configs, err := r.cachedConfigs(ctx, hashes)
	if err != nil {
		return nil, err
	}
	for p, f := range files {
		if !isFolderConfig(p) {
			continue
		}
		if _, ok := written[p]; ok {
			continue
		}
		c := configs[f.Blob].config
		if len(c.ReadOnly) > 0 {
			rules[configFolder(p)] = c.ReadOnly
		}
	}
	for p, text := range written {
		if c, err := parseFolderConfig(text); err == nil && len(c.ReadOnly) > 0 {
			rules[configFolder(p)] = c.ReadOnly
		} else {
			delete(rules, configFolder(p))
		}
	}
	return rules, nil
}

// fileLock returns the strongest lock readOnly entries of the folders holding
// p put on it: a file name or pattern in its own folder, or a subfolder (name
// ending in /) holding it at any depth. A .metatrash.json is locked at most for
// agents, so a person can always lift its locks.
func fileLock(rules map[string]lockMap, p string) string {
	level := ""
	if len(rules) == 0 {
		return level
	}
	for _, folder := range folderChain(p) {
		name, _, below := strings.Cut(p[len(folder):], "/")
		for entry, l := range rules[folder] {
			pattern, isFolder := strings.CutSuffix(entry, "/")
			if isFolder != below {
				continue
			}
			if ok, _ := path.Match(pattern, name); ok {
				level = stronger(level, l)
			}
		}
	}
	if level == lockEveryone && isFolderConfig(p) {
		level = lockAgents
	}
	return level
}

// lockedAgents names, for messages, the agents a level below everyone stops.
func lockedAgents(level string) string {
	switch level {
	case lockFolderAgents:
		return "The agents of folder actions"
	case lockConnectedAgents:
		return "Connected agents (assistants and apps other than Metatrash Sync and Slate Viewer)"
	}
	return "Agents"
}

// readOnlyFile is the refusal of a change to a locked file.
func readOnlyFile(p, level string) *Error {
	who := lockedAgents(level) + " cannot write, edit, append to, move, rename or delete it, or create a file of that name; a person can, through Metatrash Sync or Slate Viewer."
	if level == lockEveryone {
		who = "Nobody can write, edit, append to, move, rename or delete it, or create a file of that name, until a person lifts the lock in " + folderConfigName + "."
	}
	return problem(403, "read_only_file", p+" is read-only (\""+level+"\"): a readOnly entry in its folder's "+folderConfigName+" names it. "+who)
}

// checkLocks refuses a write of a .metatrash.json that adds, changes or
// removes a key locked against the writer. The locks are those the folder has
// before the write (a new file counts as the one the folder would start
// with), inherited ones included, and they guard the values the folder ends
// up with, so overriding an inherited value is refused too. locked itself is
// always locked for agents, and so is inherit while any lock applies, so
// leaving the parent cannot shed its locks.
func (r *repository) checkLocks(ctx context.Context, files map[string]record, path, text string, w writer) error {
	folder := configFolder(path)
	old, err := r.configText(ctx, files, folder)
	if err != nil {
		return err
	}
	if _, exists := files[path]; !exists && folder != "" {
		// Inside a GitHub folder nothing is added, so a new settings file
		// there is a repository file that starts from nothing.
		github, err := r.githubFolders(ctx, files, nil)
		if err != nil {
			return err
		}
		if g := githubFolderAround(github, folder); g != "" && g != folder {
			old = "{}\n"
		}
	}
	before, err := r.effectiveSettings(ctx, files, folder, &old)
	if err != nil {
		return err
	}
	after, err := r.effectiveSettings(ctx, files, folder, &text)
	if err != nil {
		return err
	}
	var locked lockMap
	if raw, ok := before["locked"]; ok {
		_ = json.Unmarshal([]byte(raw), &locked)
	}
	levels := map[string]string{}
	for key, level := range locked {
		levels[key] = level
	}
	if len(levels) > 0 {
		levels["inherit"] = stronger(levels["inherit"], lockAgents)
	}
	// A person can always lift locks.
	levels["locked"] = lockAgents
	for key, level := range levels {
		if blocks(level, w) && before[key] != after[key] {
			if level == lockEveryone {
				return problem(403, "locked_setting", fmt.Sprintf("%s: %q is locked for everyone; nobody can add, change or remove it until a person lifts the lock (removes it from locked, through Metatrash Sync or Slate Viewer).", path, key))
			}
			keep := ""
			if key == "inherit" {
				keep = ` Keep "inherit": true: while any lock applies, leaving the parent folder's settings would drop its locks.`
			}
			return problem(403, "locked_setting", fmt.Sprintf("%s: %q is locked (%q); %s cannot add, change or remove it. A person can, by editing the file through Metatrash Sync or Slate Viewer.%s", path, key, level, strings.ToLower(lockedAgents(level)[:1])+lockedAgents(level)[1:], keep))
		}
	}
	return nil
}

// checkFormLock refuses sending the form or quiz at p when its folder's
// locked (inherited ones included) holds fillForms, or takeQuizzes for a
// quiz, at a level that stops the writer.
func (r *repository) checkFormLock(ctx context.Context, files map[string]record, p string, quiz bool, w writer) error {
	folder := p[:strings.LastIndex(p, "/")+1]
	text, err := r.configText(ctx, files, folder)
	if err != nil {
		return err
	}
	settings, err := r.effectiveSettings(ctx, files, folder, &text)
	if err != nil {
		return err
	}
	var locked lockMap
	if raw, ok := settings["locked"]; ok {
		_ = json.Unmarshal([]byte(raw), &locked)
	}
	name, what := lockFillForms, "filling in the forms"
	if quiz {
		name, what = lockTakeQuizzes, "taking the quizzes"
	}
	level := locked[name]
	if !blocks(level, w) {
		return nil
	}
	if level == lockEveryone {
		return problem(403, "locked_form", fmt.Sprintf("%s: %s of this folder is locked for everyone (%s in %s); nobody can until a person lifts the lock.", p, what, name, folderConfigName))
	}
	return problem(403, "locked_form", fmt.Sprintf("%s: %s of this folder is locked (%s is %q in %s): %s cannot; a person can, in Slate Viewer or with mtsync submit.", p, what, name, level, folderConfigName, strings.ToLower(lockedAgents(level)[:1])+lockedAgents(level)[1:]))
}

// submittedVia names the agent sending a form, for its filed copy: empty
// when a person sends it.
func submittedVia(ctx context.Context) string {
	if writerOf(ctx) == writerPerson {
		return ""
	}
	app := "an agent"
	if a, ok := authorOf(ctx); ok && a.App != "" {
		app = a.App
	}
	if r := []rune(app); len(r) > 80 {
		app = string(r[:80])
	}
	return app + " (agent)"
}
