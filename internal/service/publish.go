package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Published versions (0.48.0). A folder's .metatrash.json may say
// "publishedVersion": N. Version N is the folder's files as they were in the
// commit that first set N, so publishing is one settings write: a person in
// Slate Viewer or an agent raises the number (locked can keep agents from it).
// Edits after that are drafts until the next version. Nothing is copied or
// tagged; the commit that set the number is found from the settings file's
// history. The number only goes up (checkFolderConfig).

// publishPoint is one published version of a folder.
type publishPoint struct {
	Version int `json:"version"`
	// Revision is the commit that set the version: the folder's files as
	// published.
	Revision  string        `json:"revision"`
	Timestamp string        `json:"timestamp"`
	By        *changeAuthor `json:"by,omitempty"`
	// folder is the folder as it was named at Revision.
	folder string
}

// maxPublishPoints bounds the versions read from one settings file's history.
const maxPublishPoints = 1000

// publishCache keeps a settings file's published versions by the newest
// commit that touched it; history only grows, so an entry never goes stale.
var publishCache sync.Map

type publishKey struct {
	repo, id, newest string
}

// publishPoints returns the versions folder's .metatrash.json has published up
// to state, oldest first. files is the snapshot at state. When a number was
// set, removed and set again, the latest setting counts.
func (r *repository) publishPoints(ctx context.Context, state string, files map[string]record, folder string) ([]publishPoint, error) {
	cfg, ok := files[folder+folderConfigName]
	if !ok {
		return nil, nil
	}
	b, err := git(ctx, r.path, "", nil, "log", "--first-parent", "--fixed-strings", "--grep="+cfg.ID+" ", "--format=%x1e%H%x09%cI%n%B", "--max-count="+strconv.Itoa(maxPublishPoints), state, "--")
	if err != nil {
		return nil, err
	}
	type change struct {
		commit, stamp, op, path string
		by                      *changeAuthor
	}
	var changes []change // newest first
	for _, rec := range strings.Split(string(b), "\x1e") {
		if strings.TrimSpace(rec) == "" {
			continue
		}
		header, body, _ := strings.Cut(rec, "\n")
		commit, stamp, ok := strings.Cut(header, "\t")
		if !ok || !hashPattern.MatchString(commit) {
			return nil, fmt.Errorf("invalid settings history entry")
		}
		for _, line := range strings.Split(body, "\n") {
			if fields := strings.SplitN(line, " ", 3); len(fields) == 3 && fields[0] == cfg.ID {
				changes = append(changes, change{commit: commit, stamp: stamp, op: fields[1], path: fields[2], by: parseAuthor(body)})
				break
			}
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	key := publishKey{r.path, cfg.ID, changes[0].commit}
	if cached, ok := publishCache.Load(key); ok {
		return cached.([]publishPoint), nil
	}
	// The settings text at each change, in one git process.
	var batch strings.Builder
	for _, c := range changes {
		if c.op != "delete" && validPath(c.path) {
			batch.WriteString(c.commit + ":" + c.path + "\n")
		}
	}
	texts := map[string]string{}
	if batch.Len() > 0 {
		out, err := git(ctx, r.path, "", []byte(batch.String()), "cat-file", "--batch")
		if err != nil {
			return nil, err
		}
		rest := string(out)
		for _, c := range changes {
			if c.op == "delete" || !validPath(c.path) {
				continue
			}
			line, after, _ := strings.Cut(rest, "\n")
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[1] == "blob" {
				size, err := strconv.Atoi(fields[2])
				if err != nil || size > len(after) {
					return nil, fmt.Errorf("invalid settings history blob")
				}
				texts[c.commit] = after[:size]
				rest = strings.TrimPrefix(after[size:], "\n")
			} else {
				rest = after // "<object> missing"
			}
		}
	}
	version := func(c change) int {
		text, ok := texts[c.commit]
		if !ok {
			return 0
		}
		parsed, err := parseFolderConfig(text)
		if err != nil || parsed.PublishedVersion == nil {
			return 0
		}
		return *parsed.PublishedVersion
	}
	// Oldest first: a point is each change to a new, nonzero number.
	byVersion := map[int]int{}
	var points []publishPoint
	previous := 0
	for i := len(changes) - 1; i >= 0; i-- {
		c := changes[i]
		v := version(c)
		if v != 0 && v != previous {
			stamp := c.stamp
			if t, err := time.Parse(time.RFC3339, c.stamp); err == nil {
				stamp = t.UTC().Format(time.RFC3339)
			}
			p := publishPoint{Version: v, Revision: c.commit, Timestamp: stamp, By: c.by, folder: configFolder(c.path)}
			if at, seen := byVersion[v]; seen {
				points = append(points[:at], points[at+1:]...)
				for n, i := range byVersion {
					if i > at {
						byVersion[n] = i - 1
					}
				}
			}
			byVersion[v] = len(points)
			points = append(points, p)
		}
		previous = v
	}
	publishCache.Store(key, points)
	return points, nil
}

// publishingFolder returns the nearest folder holding p (p's own folder or
// one above it) whose settings set publishedVersion, and its settings.
func publishingFolder(configs map[string]folderConfig, p string) (string, folderConfig, bool) {
	chain := folderChain(p)
	for i := len(chain) - 1; i >= 0; i-- {
		if c, ok := configs[chain[i]]; ok && c.PublishedVersion != nil {
			return chain[i], c, true
		}
	}
	return "", folderConfig{}, false
}

// acknowledgeService returns the folder's acknowledge service, if any.
func (c folderConfig) acknowledgeService() (folderService, bool) {
	for _, svc := range c.Services {
		if svc.Type == "acknowledge" {
			return svc, true
		}
	}
	return folderService{}, false
}

// tracked reports whether an acknowledge service in folder asks people to
// read p, a file in folder or below it.
func (svc folderService) tracked(folder, p string) bool {
	if isFolderFile(p) || !strings.HasPrefix(p, folder) {
		return false
	}
	if len(svc.Files) == 0 {
		return strings.HasSuffix(strings.ToLower(p), ".md")
	}
	entries := lockMap{}
	for _, e := range svc.Files {
		entries[e] = lockAgents
	}
	return fileLock(map[string]lockMap{folder: entries}, p) != ""
}

// indexAt reads the file index of a commit, without the derived marks a
// snapshot adds.
func (r *repository) indexAt(ctx context.Context, revision string) (map[string]record, error) {
	b, err := git(ctx, r.path, "", nil, "show", revision+":"+indexPath)
	if err != nil {
		return nil, err
	}
	files := map[string]record{}
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, err
	}
	return files, nil
}

// markPublished sets Published on history entries: an entry is version N's
// text of the file when it is the file's newest change at or before the
// commit that published N. Only the file's current publishing folder counts.
func (r *repository) markPublished(ctx context.Context, state string, files map[string]record, id string, entries []HistoryEntry) error {
	if len(entries) == 0 {
		return nil
	}
	current := entries[0].Path
	for p, f := range files {
		if f.ID == id {
			current = p
			break
		}
	}
	configs, err := r.folderConfigs(ctx, files)
	if err != nil {
		return err
	}
	folder, _, ok := publishingFolder(configs, current)
	if !ok {
		return nil
	}
	points, err := r.publishPoints(ctx, state, files, folder)
	if err != nil || len(points) == 0 {
		return err
	}
	at := map[string]int{}
	for i, e := range entries {
		at[e.Revision] = i
	}
	for _, p := range points {
		b, err := git(ctx, r.path, "", nil, "log", "--first-parent", "--fixed-strings", "--grep="+id+" ", "--format=%H", "--max-count=1", p.Revision, "--")
		if err != nil {
			return err
		}
		if i, ok := at[strings.TrimSpace(string(b))]; ok && entries[i].Operation != "delete" {
			entries[i].Published = append(entries[i].Published, p.Version)
		}
	}
	return nil
}
