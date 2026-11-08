package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
)

// Every folder of a space, the root included, holds two files: AGENTS.md,
// instructions for agents that work there, and .metatrash.json, the folder's
// settings. The service adds them when a folder first gets a file and removes
// them when its last other file is deleted or moved away; nobody can delete or
// move them in between, though agents with write access can rewrite them.
// GitHub folders are the exception: everything inside one belongs to its
// repository, so the service adds nothing there and AGENTS.md is an ordinary
// repository file. The GitHub folder's own .metatrash.json, which attaches the
// repository, stays protected.
const agentsFileName = "AGENTS.md"

// folderAgentsStarter is the AGENTS.md a new folder starts with. The space
// root starts from the built-in template instead (docs/space-AGENTS.template.md).
const folderAgentsStarter = "# Agent instructions\n\nNothing written here yet. Agents may describe what this folder holds and how to work in it.\n"

// folderConfigEmpty is the .metatrash.json a new space root starts with: the
// default locks and nothing else (0.95.12; "{}" before). Other folders start
// with folderConfigInherit, so they share the root's locks.
const folderConfigEmpty = `{
  "locked": {
    "actions": "folderAgents",
    "publishedVersion": "folderAgents",
    "readOnly": "folderAgents",
    "services": "folderAgents",
    "takeQuizzes": "agents"
  }
}
`

// defaultLocks are the locks every space starts with (0.95.12): the agents
// of folder actions may not change actions, publish, change which files are
// read-only, or attach services, and no agent may take a quiz. Everything
// else, filling in forms included, is open until the owner locks it.
var defaultLocks = lockMap{
	"actions":          lockFolderAgents,
	"publishedVersion": lockFolderAgents,
	"readOnly":         lockFolderAgents,
	"services":         lockFolderAgents,
	lockTakeQuizzes:    lockAgents,
}

// defaultLocksPath is the service file that records a space has had the
// default locks added (addDefaultLocks), so a lock its owner later lifts
// stays lifted.
const defaultLocksPath = serviceDir + "locks.json"

const defaultLocksMarker = "{\"defaultLocks\": 1}\n"

// fileFolder returns the folder holding path: "" for the space root,
// otherwise a prefix ending in "/".
func fileFolder(path string) string {
	return path[:strings.LastIndex(path, "/")+1]
}

func isAgentsFile(path string) bool {
	return strings.EqualFold(path[strings.LastIndex(path, "/")+1:], agentsFileName)
}

// isFolderFile reports whether path is a folder's AGENTS.md or .metatrash.json.
func isFolderFile(path string) bool {
	return isFolderConfig(path) || isAgentsFile(path)
}

// folderChain returns the folders holding path, the root first.
func folderChain(path string) []string {
	folders := []string{""}
	for i, c := range path {
		if c == '/' {
			folders = append(folders, path[:i+1])
		}
	}
	return folders
}

// cachedConfig returns the parsed .metatrash.json stored as blob; a text
// that no longer validates counts as no settings.
func (r *repository) cachedConfig(ctx context.Context, blob string) (folderConfig, error) {
	configs, err := r.cachedConfigs(ctx, []string{blob})
	if err != nil {
		return folderConfig{}, err
	}
	return configs[blob].config, nil
}

// githubFolders returns the folders whose .metatrash.json attaches a GitHub
// service. written, when set, holds configuration texts about to be written,
// which decide for their own folders.
func (r *repository) githubFolders(ctx context.Context, files map[string]record, written map[string]string) (map[string]bool, error) {
	folders := map[string]bool{}
	var hashes []string
	for p, f := range files {
		if _, ok := written[p]; !ok && isFolderConfig(p) && p != folderConfigName {
			hashes = append(hashes, f.Blob)
		}
	}
	configs, err := r.cachedConfigs(ctx, hashes)
	if err != nil {
		return nil, err
	}
	for p, f := range files {
		if !isFolderConfig(p) || p == folderConfigName {
			continue
		}
		if _, ok := written[p]; ok {
			continue
		}
		c := configs[f.Blob].config
		if c.has("github") {
			folders[configFolder(p)] = true
		}
	}
	for p, text := range written {
		if c, err := parseFolderConfig(text); err == nil && c.has("github") && p != folderConfigName {
			folders[configFolder(p)] = true
		}
	}
	return folders, nil
}

// githubFolderAround returns the GitHub folder that is folder or holds it, or "".
func githubFolderAround(github map[string]bool, folder string) string {
	for g := range github {
		if strings.HasPrefix(folder, g) {
			return g
		}
	}
	return ""
}

// protectedFolderFile reports whether path is a folder file the service keeps:
// any AGENTS.md or .metatrash.json outside GitHub folders, and the GitHub
// folder's own .metatrash.json.
func protectedFolderFile(github map[string]bool, path string) bool {
	folder := fileFolder(path)
	g := githubFolderAround(github, folder)
	switch {
	case isFolderConfig(path):
		return g == "" || g == folder
	case isAgentsFile(path):
		return g == ""
	}
	return false
}

// markFolderFiles sets Protected, Deletable and ReadOnly on every record.
func markFolderFiles(files map[string]record, github map[string]bool, readOnly map[string]lockMap) {
	for p, f := range files {
		f.Protected = protectedFolderFile(github, p)
		f.Deletable = !f.Protected
		f.ReadOnly = fileLock(readOnly, p)
		files[p] = f
	}
}

// hasAgentsFile reports whether folder holds AGENTS.md in any letter case,
// so a folder whose file is agents.md does not get a second one.
func hasAgentsFile(files map[string]record, folder string) bool {
	if _, ok := files[folder+agentsFileName]; ok {
		return true
	}
	for p := range files {
		if strings.HasPrefix(p, folder) && !strings.Contains(p[len(folder):], "/") && isAgentsFile(p) {
			return true
		}
	}
	return false
}

// addFolderFiles adds the AGENTS.md and .metatrash.json missing from every
// folder that holds one of paths, recording their texts and the commit
// changes. rootAgents is the root's AGENTS.md text. A folder in a GitHub
// folder gets nothing; the GitHub folder itself already has its settings.
func addFolderFiles(files map[string]record, texts map[string]string, github map[string]bool, rootAgents string, paths ...string) ([]commitChange, error) {
	var changes []commitChange
	add := func(path, text string) error {
		for p := range files {
			if strings.HasPrefix(p, path+"/") {
				return invalid(fmt.Sprintf("%s is a folder here, but every folder needs a file of that name; move its files elsewhere first.", path))
			}
		}
		id, err := randomHex(16)
		if err != nil {
			return err
		}
		files[path] = record{File: File{ID: id, Path: path, Bytes: int64(len(text))}}
		texts[path] = text
		changes = append(changes, commitChange{id, "create", path})
		return nil
	}
	for _, path := range paths {
		for _, folder := range folderChain(path) {
			if githubFolderAround(github, folder) != "" {
				break
			}
			if _, ok := files[folder+folderConfigName]; !ok {
				text := folderConfigInherit
				if folder == "" {
					text = folderConfigEmpty
				}
				if err := add(folder+folderConfigName, text); err != nil {
					return nil, err
				}
			}
			if !hasAgentsFile(files, folder) {
				text := folderAgentsStarter
				if folder == "" {
					text = rootAgents
				}
				if err := add(folder+agentsFileName, text); err != nil {
					return nil, err
				}
			}
		}
	}
	return changes, nil
}

// removeEmptyFolderFiles removes the folder files of each folder that held
// one of paths (just deleted or moved away) and now holds nothing else, the
// deepest first so a parent left empty goes too. The root keeps its files,
// and GitHub folders are left alone. A folder file keep reports true for
// counts as another file, so its folder stays.
func removeEmptyFolderFiles(files map[string]record, github map[string]bool, keep func(string) bool, paths ...string) []commitChange {
	var changes []commitChange
	var folders []string
	seen := map[string]bool{}
	for _, path := range paths {
		for _, folder := range folderChain(path)[1:] {
			if !seen[folder] {
				seen[folder] = true
				folders = append(folders, folder)
			}
		}
	}
	sort.Slice(folders, func(i, j int) bool { return len(folders[i]) > len(folders[j]) })
	for _, folder := range folders {
		if githubFolderAround(github, folder) != "" {
			continue
		}
		var left []string
		empty := true
		for g := range github {
			if strings.HasPrefix(g, folder) {
				// The GitHub folder's settings attach its repository.
				empty = false
			}
		}
		for p := range files {
			if !empty {
				break
			}
			if !strings.HasPrefix(p, folder) {
				continue
			}
			if !isFolderFile(p) || keep != nil && keep(p) {
				empty = false
				continue
			}
			left = append(left, p)
		}
		if !empty {
			continue
		}
		sort.Strings(left)
		for _, p := range left {
			changes = append(changes, commitChange{files[p].ID, "delete", p})
			delete(files, p)
		}
	}
	return changes
}

// addDefaultLocks adds the default locks, once, to a space made before
// 0.95.12: to every .metatrash.json that does not inherit (the root, and any
// folder that left its parent's settings), outside GitHub folders, keeping
// any lock already set on the same name. Settings files that do not validate
// are left alone. The marker file is written in the same commit, or alone
// when nothing needed a lock, so this runs once.
func (r *repository) addDefaultLocks(ctx context.Context) error {
	head, files, err := r.snapshot(ctx, "")
	if err != nil || head == "" {
		return err
	}
	if marker, err := r.serviceFile(ctx, head, defaultLocksPath); err != nil || marker != nil {
		return err
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return err
	}
	paths := make([]string, 0)
	for p := range files {
		if isFolderConfig(p) && githubFolderAround(github, fileFolder(p)) == "" {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	texts := map[string]string{}
	var changes []commitChange
	for _, p := range paths {
		f := files[p]
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return err
		}
		text, changed := withDefaultLocks(string(b))
		if !changed || int64(len(text)) > r.limits.MaxFileBytes {
			continue
		}
		f.Bytes = int64(len(text))
		files[p] = f
		texts[p] = text
		changes = append(changes, commitChange{f.ID, "write", p})
	}
	summary := "add the default locks to " + folderConfigName
	if len(changes) == 0 {
		summary = "record the default locks"
	}
	if _, err = r.commitFiles(ctx, head, files, texts, map[string][]byte{defaultLocksPath: []byte(defaultLocksMarker)}, changes, summary); err != nil {
		return err
	}
	if len(changes) > 0 {
		log.Printf("space %s: added the default locks to %d settings files", r.path, len(changes))
	}
	return nil
}

// withDefaultLocks returns a settings text with the default locks it lacks
// added to locked, its other keys kept in order, and whether that changed
// anything. A text that does not validate, or inherits, is left alone.
func withDefaultLocks(text string) (string, bool) {
	c, err := parseFolderConfig(text)
	if err != nil || c.Inherit != nil && *c.Inherit {
		return text, false
	}
	locked := lockMap{}
	for name, level := range c.Locked {
		locked[name] = level
	}
	added := false
	for name, level := range defaultLocks {
		if _, ok := locked[name]; !ok {
			locked[name] = level
			added = true
		}
	}
	if !added {
		return text, false
	}
	keys, values, err := orderedConfig(text)
	if err != nil {
		return text, false
	}
	lockedJSON, _ := json.Marshal(map[string]string(locked))
	if _, ok := values["locked"]; !ok {
		keys = append(keys, "locked")
	}
	values["locked"] = lockedJSON
	var b bytes.Buffer
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		name, _ := json.Marshal(k)
		b.Write(name)
		b.WriteString(":")
		b.Write(values[k])
	}
	b.WriteString("}")
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return text, false
	}
	out.WriteString("\n")
	if _, err := parseFolderConfig(out.String()); err != nil {
		return text, false
	}
	return out.String(), true
}

// orderedConfig returns a settings object's top-level keys in their order
// and their raw values.
func orderedConfig(text string) ([]string, map[string]json.RawMessage, error) {
	d := json.NewDecoder(strings.NewReader(text))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, fmt.Errorf("not an object")
	}
	var keys []string
	values := map[string]json.RawMessage{}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return nil, nil, err
		}
		keys = append(keys, k)
		values[k] = v
	}
	return keys, values, nil
}

// addMissingFolderFiles gives every folder of the space the files it lacks,
// in one commit: spaces made before 0.45.0 have none below the root.
func (r *repository) addMissingFolderFiles(ctx context.Context, rootAgents string) error {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return err
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(files)+1)
	paths = append(paths, folderConfigName)
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	texts := map[string]string{}
	changes, err := addFolderFiles(files, texts, github, rootAgents, paths...)
	if err != nil || len(changes) == 0 {
		return err
	}
	if _, err = r.commitFiles(ctx, head, files, texts, nil, changes, "add AGENTS.md and "+folderConfigName+" to every folder"); err != nil {
		return err
	}
	log.Printf("space %s: added %d folder files", r.path, len(changes))
	return nil
}
