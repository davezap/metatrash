package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// Locks. A folder's .metatrash.json can lock its own keys (locked) and the
// files in it (readOnly), each at one of two levels:
//   - "agents": agents can't add, change or remove the key, or write, edit,
//     move, rename, delete or create the file. People can (see personWrite):
//     Metatrash Sync and the website's editor, and in a spaces.json space
//     such as the public space only the usernames its people lists.
//   - "everyone": nobody can, people included, until a person lifts the lock.
//
// Anything not listed is read-write for everyone. A person can always edit
// locked and readOnly themselves, so every lock can be lifted, and agents never
// can change locked. readOnly is an ordinary key: agents may change it unless
// locked lists it. Both are maps of name to level; a list, as 0.45.0 wrote
// locked, locks each name at "agents".
const (
	lockAgents   = "agents"
	lockEveryone = "everyone"
)

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
		return fmt.Errorf("locks are an object of name to \"agents\" or \"everyone\"")
	}
	*m = levels
	return nil
}

// blocks reports whether a lock at level stops the writer.
func blocks(level string, person bool) bool {
	return level == lockEveryone || level == lockAgents && !person
}

// stronger returns the stronger of two lock levels.
func stronger(a, b string) string {
	if a == lockEveryone || b == lockEveryone {
		return lockEveryone
	}
	if a == lockAgents || b == lockAgents {
		return lockAgents
	}
	return ""
}

// checkLockLevels validates the levels of locked or readOnly.
func checkLockLevels(key string, m lockMap) error {
	if len(m) > maxLockEntries {
		return folderConfigError(fmt.Sprintf("%s is limited to %d entries.", key, maxLockEntries))
	}
	for name, level := range m {
		if level != lockAgents && level != lockEveryone {
			return folderConfigError(fmt.Sprintf("%s %q must be \"agents\" (agents can't change it, people can) or \"everyone\" (nobody can until a person lifts the lock), not %q.", key, name, level))
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
	for p, f := range files {
		if !isFolderConfig(p) {
			continue
		}
		if _, ok := written[p]; ok {
			continue
		}
		c, err := r.cachedConfig(ctx, f.Blob)
		if err != nil {
			return nil, err
		}
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

// readOnlyFile is the refusal of a change to a locked file.
func readOnlyFile(p, level string) *Error {
	who := "Agents cannot write, edit, append to, move, rename or delete it, or create a file of that name; only a person can, through Metatrash Sync or Slate Viewer."
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
func (r *repository) checkLocks(ctx context.Context, files map[string]record, path, text string, person bool) error {
	folder := configFolder(path)
	old, err := r.configText(ctx, files, folder)
	if err != nil {
		return err
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
		if blocks(level, person) && before[key] != after[key] {
			if level == lockEveryone {
				return problem(403, "locked_setting", fmt.Sprintf("%s: %q is locked for everyone; nobody can add, change or remove it until a person lifts the lock (removes it from locked, through Metatrash Sync or Slate Viewer).", path, key))
			}
			return problem(403, "locked_setting", fmt.Sprintf("%s: %q is locked; agents cannot add, change or remove it. Only a person can, by editing the file through Metatrash Sync or Slate Viewer.", path, key))
		}
	}
	return nil
}
