package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// syncedFile finds the registered folder holding file and the file's path
// in it (with /). The file need not exist: an old revision may have had a
// name the file no longer has.
func syncedFile(file string) (folderEntry, string, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return folderEntry{}, "", err
	}
	r, err := loadRegistry()
	if err != nil {
		return folderEntry{}, "", err
	}
	for _, e := range r.Folders {
		if !within(abs, e.Folder) {
			continue
		}
		rel, err := filepath.Rel(e.Folder, abs)
		if err != nil {
			return folderEntry{}, "", err
		}
		rel = filepath.ToSlash(rel)
		if lower := strings.ToLower(rel); rel == "." || lower == stateDir || strings.HasPrefix(lower, stateDir+"/") {
			return folderEntry{}, "", fmt.Errorf("%s is not a file in the space", file)
		}
		return e, rel, nil
	}
	return folderEntry{}, "", fmt.Errorf("%s is not in a folder mtsync syncs (see mtsync list)", file)
}

// fileID is the space's ID for rel, from the folder's last agreed state.
func fileID(e folderEntry, rel string) (string, error) {
	st, err := folderState(e)
	if err != nil {
		return "", err
	}
	for p, f := range st.Files {
		if p == rel || (samePath("A", "a") && strings.EqualFold(p, rel)) {
			return f.ID, nil
		}
	}
	return "", fmt.Errorf("%s is not in %s yet (mtsync has not uploaded it)", rel, e.Space)
}

// folderState is a registered folder's last agreed state.
func folderState(e folderEntry) (syncState, error) {
	var st syncState
	b, err := os.ReadFile(filepath.Join(e.Folder, stateDir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return st, fmt.Errorf("%s has not synced yet", e.Folder)
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s is damaged: %v", filepath.Join(e.Folder, stateDir, "state.json"), err)
	}
	return st, nil
}

// historyJSON is mtsync history -json's answer, for editors.
type historyJSON struct {
	Space   string        `json:"space"`
	Folder  string        `json:"folder"`
	ID      string        `json:"id"`
	Entries []historyItem `json:"entries"`
	// NextOffset is the -offset of the next page, or null at the end.
	NextOffset *int `json:"nextOffset"`
}

type historyItem struct {
	historyEntry
	// File is where that revision's path is in the folder, for mtsync show;
	// absent when the path was outside the synced folder.
	File string `json:"file,omitempty"`
}

// revisionPattern is a Git commit hash: all 40 characters, as history -json
// gives them, or at least 7 of its first ones, as the plain listing prints.
var revisionPattern = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// historyScanLimit is how far back show looks in a file's history for a
// revision: 10 pages of 100.
const historyScanLimit = 1000

// showHistory lists changes to a synced file, newest first.
func showHistory(ctx context.Context, file string, limit, offset int, asJSON bool, out io.Writer) error {
	if limit < 1 || limit > 100 {
		return fmt.Errorf("-limit must be 1..100")
	}
	if offset < 0 {
		return fmt.Errorf("-offset must be 0 or more")
	}
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	id, err := fileID(e, rel)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	entries, more, err := c.history(ctx, id, limit, offset)
	if err != nil {
		return err
	}
	answer := historyJSON{Space: e.Space, Folder: e.Folder, ID: id, Entries: []historyItem{}}
	for _, h := range entries {
		item := historyItem{historyEntry: h}
		if strings.HasPrefix(h.Path, c.prefix) {
			item.File = filepath.Join(e.Folder, filepath.FromSlash(strings.TrimPrefix(h.Path, c.prefix)))
		}
		answer.Entries = append(answer.Entries, item)
	}
	if more {
		next := offset + len(entries)
		answer.NextOffset = &next
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(answer)
	}
	if len(entries) == 0 {
		fmt.Fprintln(out, "No more changes.")
		return nil
	}
	for _, h := range entries {
		when := h.Timestamp
		if t, err := time.Parse(time.RFC3339, h.Timestamp); err == nil {
			when = t.Local().Format("2006-01-02 15:04")
		}
		line := fmt.Sprintf("%s  %s  %-6s  %s", when, h.Revision[:min(12, len(h.Revision))], h.Operation, h.Path)
		if h.By != nil {
			line += "  by " + h.By.String()
		}
		for _, v := range h.Published {
			line += fmt.Sprintf("  [published v%d]", v)
		}
		fmt.Fprintln(out, line)
	}
	if more {
		fmt.Fprintf(out, "Older changes: mtsync -offset %d history %q\n", offset+len(entries), file)
	}
	fmt.Fprintf(out, "See one: mtsync show %q REVISION\n", file)
	return nil
}

// showRevision prints a synced file's text as it was at revision, which may
// be shortened as history prints it. A revision in the file's history is read
// at the path the file had then, so the file's current name works across
// renames; for a file no longer in the folder, give the path it had then
// (history's "file") and the whole revision.
func showRevision(ctx context.Context, file, revision string, out io.Writer) error {
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	revision = strings.ToLower(revision)
	if !revisionPattern.MatchString(revision) {
		return fmt.Errorf("%q is not a revision; use one from mtsync history", revision)
	}
	c := folderClient(e, tokenSource(e.Server))
	readPath := rel
	if id, idErr := fileID(e, rel); idErr == nil {
		entry, found, err := findRevision(ctx, c, id, revision)
		if err != nil {
			return err
		}
		switch {
		case found && entry.Operation == "delete":
			return fmt.Errorf("%s is the file's deletion, which has no text; show the change before it", entry.Revision[:12])
		case found && !strings.HasPrefix(entry.Path, c.prefix):
			return fmt.Errorf("at %s the file was %s, outside the synced folder", entry.Revision[:12], entry.Path)
		case found:
			revision, readPath = entry.Revision, strings.TrimPrefix(entry.Path, c.prefix)
		case len(revision) < 40:
			return fmt.Errorf("%s is not in the last %d changes to %s; give all 40 characters (mtsync -json history)", revision, historyScanLimit, rel)
		}
	} else if len(revision) < 40 {
		return fmt.Errorf("%w; for a short revision give the file's current name, or give all 40 characters (mtsync -json history)", idErr)
	}
	text, virtual, err := c.readFile(ctx, readPath, revision)
	if errorCode(err) == "not_found" {
		return fmt.Errorf("%s did not exist at %s; a delete entry has no text, so pick the one before it (or the path it had then): %w", path.Join(e.Space, readPath), revision[:12], err)
	}
	if err != nil {
		return err
	}
	if virtual != nil && c.account {
		// A virtual file's revision is its content (a PDF, an image), not
		// the server's text for it; mtsync text prints that.
		_, err = c.download(ctx, readPath, revision, out)
		return err
	}
	_, err = io.WriteString(out, text)
	return err
}

// findRevision looks for the entry in the file's history whose revision
// starts with prefix, newest first. Two different matches are refused.
func findRevision(ctx context.Context, c *client, id, prefix string) (historyEntry, bool, error) {
	var match historyEntry
	found := false
	for offset := 0; offset < historyScanLimit; offset += 100 {
		entries, more, err := c.history(ctx, id, 100, offset)
		if err != nil {
			return historyEntry{}, false, err
		}
		for _, h := range entries {
			if !strings.HasPrefix(h.Revision, prefix) {
				continue
			}
			if found && h.Revision != match.Revision {
				return historyEntry{}, false, fmt.Errorf("%s matches more than one change to this file; give more characters", prefix)
			}
			if !found {
				match, found = h, true
			}
		}
		if !more || (found && len(prefix) == 40) {
			break
		}
	}
	return match, found, nil
}
