package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// GitHub folders (metatrash 0.39.0 and later). A folder in a space whose
// .metatrash.json has a github service mirrors a repository branch; the
// server pulls from it and pushes to it. mtsync pull, pending and push ask
// the server to do that for a GitHub folder inside a registered folder, so
// a person can without an agent. The server works on the space, so a pull
// reaches this computer with the next sync, and a push sends what the space
// holds (local edits once they are uploaded).

type githubChange struct {
	Path      string `json:"path"`
	Change    string `json:"change"`
	Note      string `json:"note,omitempty"`
	BaseState string `json:"baseState,omitempty"`
}

// githubNote is a file pull skipped or converted, or one push leaves out.
type githubNote struct {
	Path     string `json:"path"`
	Reason   string `json:"reason,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

type pullAnswer struct {
	Space          string       `json:"space"`
	Folder         string       `json:"folder"`
	Repo           string       `json:"repo"`
	Branch         string       `json:"branch"`
	Commit         string       `json:"commit"`
	OldState       string       `json:"oldState"`
	NewState       string       `json:"newState"`
	UpToDate       bool         `json:"upToDate"`
	Created        int          `json:"created"`
	Updated        int          `json:"updated"`
	Deleted        int          `json:"deleted"`
	Unchanged      int          `json:"unchanged"`
	Skipped        []githubNote `json:"skipped"`
	SkippedCount   int          `json:"skippedCount"`
	Converted      []githubNote `json:"converted"`
	ConvertedCount int          `json:"convertedCount"`
	LocalChanges   int          `json:"localChanges"`
}

type pendingAnswer struct {
	Space        string         `json:"space"`
	Folder       string         `json:"folder"`
	Repo         string         `json:"repo"`
	Branch       string         `json:"branch"`
	Push         string         `json:"push"`
	BaseCommit   string         `json:"baseCommit"`
	BaseState    string         `json:"baseState"`
	State        string         `json:"state"`
	OldestChange string         `json:"oldestChange,omitempty"`
	UpToDate     bool           `json:"upToDate"`
	Changes      []githubChange `json:"changes"`
	NotPushed    []githubNote   `json:"notPushed"`
	Refused      []githubNote   `json:"refused"`
	Converted    []githubNote   `json:"converted"`
}

type pushAnswer struct {
	Space     string         `json:"space"`
	Folder    string         `json:"folder"`
	Repo      string         `json:"repo"`
	Branch    string         `json:"branch"`
	UpToDate  bool           `json:"upToDate"`
	Commit    string         `json:"commit,omitempty"`
	URL       string         `json:"url,omitempty"`
	Parent    string         `json:"parent"`
	Added     int            `json:"added"`
	Modified  int            `json:"modified"`
	Deleted   int            `json:"deleted"`
	Files     []githubChange `json:"files"`
	NotPushed []githubNote   `json:"notPushed"`
	State     string         `json:"state"`
	NewState  string         `json:"newState"`
	Warning   string         `json:"warning,omitempty"`
}

// githubFolder finds the registered folder holding the local folder place
// and the folder's path in the space ("site/"), for pull, pending and push.
func githubFolder(place string) (folderEntry, string, error) {
	abs, err := filepath.Abs(place)
	if err != nil {
		return folderEntry{}, "", err
	}
	e, err := syncedPlace(abs)
	if err != nil {
		return folderEntry{}, "", err
	}
	rel, err := filepath.Rel(e.Folder, abs)
	if err != nil {
		return folderEntry{}, "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		rel = ""
	}
	if lower := strings.ToLower(rel); lower == stateDir || strings.HasPrefix(lower, stateDir+"/") {
		return folderEntry{}, "", fmt.Errorf("%s is not a folder in the space", place)
	}
	_, prefix := splitTarget(e.Space, true)
	folder := prefix + rel
	if folder == "" {
		return folderEntry{}, "", fmt.Errorf("%s is the space's root; name the GitHub folder inside it", place)
	}
	return e, strings.TrimSuffix(folder, "/") + "/", nil
}

// githubClient reaches the space of a registered folder for pull, pending and
// push, which the server may take up to ten minutes over.
func githubClient(e folderEntry) (*client, error) {
	c := folderClient(e, tokenSource(e.Server))
	if !c.account {
		return nil, fmt.Errorf("GitHub folders need a signed-in space (mtsync login), not a space key")
	}
	c.http = &http.Client{Timeout: 11 * time.Minute, Transport: c.http.Transport}
	return c, nil
}

// trimChanges and trimNotes show the server's space paths as paths in the
// synced folder, and an absent list as empty.
func trimChanges(list []githubChange, prefix string) []githubChange {
	out := []githubChange{}
	for _, c := range list {
		c.Path = strings.TrimPrefix(c.Path, prefix)
		out = append(out, c)
	}
	return out
}

func trimNotes(list []githubNote, prefix string) []githubNote {
	out := []githubNote{}
	for _, n := range list {
		n.Path = strings.TrimPrefix(n.Path, prefix)
		out = append(out, n)
	}
	return out
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// githubPull brings the GitHub folder at place up to its branch.
func githubPull(ctx context.Context, place string, asJSON bool, out io.Writer) error {
	e, folder, err := githubFolder(place)
	if err != nil {
		return err
	}
	c, err := githubClient(e)
	if err != nil {
		return err
	}
	var answer pullAnswer
	if err := c.do(ctx, "POST", "pull", nil, map[string]string{"folder": folder}, &answer); err != nil {
		return printRefusal(err, asJSON, out)
	}
	answer.Folder = strings.TrimPrefix(answer.Folder, c.prefix)
	answer.Converted = trimNotes(answer.Converted, c.prefix)
	answer.Skipped = trimNotes(answer.Skipped, "") // paths in the repository
	if asJSON {
		return printJSON(out, answer)
	}
	if answer.UpToDate {
		fmt.Fprintf(out, "%s is up to date with %s %s (%s).\n", place, answer.Repo, answer.Branch, short(answer.Commit))
	} else {
		fmt.Fprintf(out, "Pulled %s %s (%s) into %s: %d new, %d changed, %d deleted, %d unchanged.\n", answer.Repo, answer.Branch, short(answer.Commit), place, answer.Created, answer.Updated, answer.Deleted, answer.Unchanged)
		fmt.Fprintln(out, "The files reach this computer with the next sync.")
	}
	for _, s := range answer.Skipped {
		fmt.Fprintf(out, "  skipped %s: %s\n", s.Path, s.Reason)
	}
	if more := answer.SkippedCount - len(answer.Skipped); more > 0 {
		fmt.Fprintf(out, "  and %d more skipped\n", more)
	}
	if answer.LocalChanges > 0 {
		fmt.Fprintf(out, "%d changed here and not pushed yet; see mtsync pending.\n", answer.LocalChanges)
	}
	return nil
}

// githubPending lists what a push of the GitHub folder at place would send.
func githubPending(ctx context.Context, place string, asJSON bool, out io.Writer) error {
	e, folder, err := githubFolder(place)
	if err != nil {
		return err
	}
	c, err := githubClient(e)
	if err != nil {
		return err
	}
	var answer pendingAnswer
	if err := c.do(ctx, "GET", "pending", url.Values{"folder": {folder}}, nil, &answer); err != nil {
		return printRefusal(err, asJSON, out)
	}
	answer.Folder = strings.TrimPrefix(answer.Folder, c.prefix)
	answer.Changes = trimChanges(answer.Changes, c.prefix)
	answer.NotPushed = trimNotes(answer.NotPushed, c.prefix)
	answer.Refused = trimNotes(answer.Refused, c.prefix)
	answer.Converted = trimNotes(answer.Converted, c.prefix)
	if asJSON {
		return printJSON(out, answer)
	}
	if answer.UpToDate {
		fmt.Fprintf(out, "Nothing to push: %s matches %s %s (%s).\n", place, answer.Repo, answer.Branch, short(answer.BaseCommit))
	} else {
		fmt.Fprintf(out, "%d to push to %s %s:\n", len(answer.Changes), answer.Repo, answer.Branch)
	}
	for _, ch := range answer.Changes {
		line := fmt.Sprintf("  %-8s %s", ch.Change, ch.Path)
		if ch.Note != "" {
			line += " (" + ch.Note + ")"
		}
		fmt.Fprintln(out, line)
	}
	for _, n := range answer.Refused {
		fmt.Fprintf(out, "  refused  %s: %s\n", n.Path, n.Reason)
	}
	for _, n := range answer.NotPushed {
		fmt.Fprintf(out, "  not pushed (.gitignore) %s\n", n.Path)
	}
	if answer.Push == "review" && !answer.UpToDate {
		fmt.Fprintln(out, "This folder's push mode is review, so push is refused until that is changed in its .metatrash.json.")
	}
	return nil
}

// githubPush sends the GitHub folder's pending changes as one commit.
func githubPush(ctx context.Context, place, message string, asJSON bool, out io.Writer) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("push needs a commit message: mtsync -m \"what changed\" push FOLDER")
	}
	e, folder, err := githubFolder(place)
	if err != nil {
		return err
	}
	c, err := githubClient(e)
	if err != nil {
		return err
	}
	var answer pushAnswer
	if err := c.do(ctx, "POST", "push", nil, map[string]string{"folder": folder, "message": message}, &answer); err != nil {
		return printRefusal(err, asJSON, out)
	}
	answer.Folder = strings.TrimPrefix(answer.Folder, c.prefix)
	answer.Files = trimChanges(answer.Files, c.prefix)
	answer.NotPushed = trimNotes(answer.NotPushed, c.prefix)
	if asJSON {
		return printJSON(out, answer)
	}
	if answer.UpToDate {
		fmt.Fprintf(out, "Nothing to push: %s matches %s %s.\n", place, answer.Repo, answer.Branch)
		return nil
	}
	fmt.Fprintf(out, "Pushed to %s %s as %s: %d added, %d changed, %d deleted.\n", answer.Repo, answer.Branch, short(answer.Commit), answer.Added, answer.Modified, answer.Deleted)
	if answer.URL != "" {
		fmt.Fprintln(out, answer.URL)
	}
	if answer.Warning != "" {
		fmt.Fprintln(out, "Warning: "+answer.Warning)
	}
	return nil
}
