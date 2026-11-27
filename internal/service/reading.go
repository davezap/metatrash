package service

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// New files need reading (Core 0.102.0). An acknowledge service with
// "newFiles": true, in a folder without a publishedVersion, asks everyone to
// read each tracked file that arrives in the folder after the setting was
// turned on: it gets the red dot and goes on their list of things to read,
// like a file of a published version. Files already there when the setting
// was turned on are not asked for. A file is read once: confirming any of
// its texts counts, so later edits do not ask again. Such a folder is listed
// with the published ones at version 0, its files as they are now.

// newFilesCache keeps the files a folder held when newFiles was turned on,
// by repository, commit and folder; a commit never changes.
var newFilesCache sync.Map

type newFilesKey struct {
	repo, commit, folder string
}

// newFilesStart is the change that last turned newFiles on in a history,
// or nil when it is off now.
func newFilesStart(history []settingsChange) *settingsChange {
	var start *settingsChange
	on := false
	for i := range history {
		c := &history[i]
		now := c.config != nil && c.config.newFiles()
		if now && !on {
			start = c
		}
		on = now
	}
	if !on {
		return nil
	}
	return start
}

// newFiles reports whether the settings ask for new files as they arrive.
func (c folderConfig) newFiles() bool {
	svc, ok := c.acknowledgeService()
	return ok && svc.NewFiles && c.PublishedVersion == nil
}

// newFilesFolder returns the nearest folder holding p whose settings ask
// for new files, unless a folder with a publishedVersion holds p (its
// versions are what people read there).
func newFilesFolder(configs map[string]folderConfig, p string) (string, bool) {
	if _, _, published := publishingFolder(configs, p); published {
		return "", false
	}
	chain := folderChain(p)
	for i := len(chain) - 1; i >= 0; i-- {
		if c, ok := configs[chain[i]]; ok && c.newFiles() {
			return chain[i], true
		}
	}
	return "", false
}

// filesAt returns the IDs of the files under folder at commit.
func (r *repository) filesAt(ctx context.Context, commit, folder string) (map[string]bool, error) {
	key := newFilesKey{r.path, commit, folder}
	if cached, ok := newFilesCache.Load(key); ok {
		return cached.(map[string]bool), nil
	}
	index, err := r.indexAt(ctx, commit)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for p, f := range index {
		if strings.HasPrefix(p, folder) {
			ids[f.ID] = true
		}
	}
	newFilesCache.Store(key, ids)
	return ids, nil
}

// newFilesFolders lists the folders that ask for new files, with the
// tracked files that arrived since, as they are at state.
func (r *repository) newFilesFolders(ctx context.Context, state string, files map[string]record, configs map[string]folderConfig) ([]policyFolder, error) {
	var names []string
	for folder, c := range configs {
		// Inside a folder that publishes versions, its versions are what is read.
		if _, _, published := publishingFolder(configs, folder+"x"); c.newFiles() && !published {
			names = append(names, folder)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	sort.Strings(names)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var folders []policyFolder
	for _, folder := range names {
		history, err := r.settingsHistory(ctx, state, files, folder)
		if err != nil {
			return nil, err
		}
		start := newFilesStart(history)
		if start == nil {
			continue
		}
		before, err := r.filesAt(ctx, start.commit, configFolder(start.path))
		if err != nil {
			return nil, err
		}
		svc, _ := configs[folder].acknowledgeService()
		pf := policyFolder{Folder: folder, publishPoint: publishPoint{Revision: state, Timestamp: start.stamp, By: start.by, folder: folder}, Acknowledge: true, NewFiles: true, Files: []policyFile{}}
		for _, p := range paths {
			f := files[p]
			if !strings.HasPrefix(p, folder) || isFolderFile(p) || before[f.ID] || !svc.tracked(folder, p) {
				continue
			}
			if owner, ok := newFilesFolder(configs, p); !ok || owner != folder {
				continue
			}
			pf.Files = append(pf.Files, policyFile{ID: f.ID, Path: p, PublishedPath: p, Blob: f.Blob, Tracked: true})
		}
		folders = append(folders, pf)
	}
	return folders, nil
}

// readingFolders lists every folder that asks people to read its files: the
// published folders (publishedFolders) and those that ask for new files, by
// folder name.
func (r *repository) readingFolders(ctx context.Context, state string, files map[string]record) ([]policyFolder, error) {
	configs, err := r.folderConfigs(ctx, files)
	if err != nil {
		return nil, err
	}
	folders, err := r.publishedOnly(ctx, state, files, configs)
	if err != nil {
		return nil, err
	}
	live, err := r.newFilesFolders(ctx, state, files, configs)
	if err != nil {
		return nil, err
	}
	if len(live) > 0 {
		folders = append(folders, live...)
		sort.SliceStable(folders, func(i, j int) bool { return folders[i].Folder < folders[j].Folder })
	}
	r.linkQuizzes(ctx, folders)
	return folders, nil
}

// statusIn is a person's state for one file of folder: in a folder that
// asks for new files, confirming any text of the file counts as current.
func (idx ackIndex) statusIn(folder policyFolder, userID string, f policyFile) *policyAck {
	ack := idx.status(userID, f)
	if ack != nil && folder.NewFiles {
		ack.Current, ack.Version = true, 0
	}
	return ack
}

// rowVersion is the version an acknowledgement row keeps for folder: its
// published version, or 1 in a folder that asks for new files (the column
// holds versions from 1, metatrash_acknowledgements_version; such a folder
// counts any row of a file as read, whatever its version).
func rowVersion(folder policyFolder) int {
	if folder.NewFiles {
		return 1
	}
	return folder.Version
}
