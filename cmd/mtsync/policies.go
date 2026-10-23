package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Published versions and acknowledgements (metatrash 0.48.0). A folder in a
// space whose .metatrash.json sets publishedVersion has an official version;
// with an acknowledge service, everyone in the space is asked to read its
// files. mtsync policies lists them for this computer's person, mtsync ack
// confirms one, and mtsync readers reports who has read what.

type policyAck struct {
	Version   int    `json:"version"`
	Timestamp string `json:"timestamp"`
	Current   bool   `json:"current"`
}

type policyFile struct {
	ID            string     `json:"id"`
	Path          string     `json:"path"`
	PublishedPath string     `json:"publishedPath"`
	Blob          string     `json:"blob"`
	Draft         bool       `json:"draft"`
	Tracked       bool       `json:"tracked"`
	Outstanding   bool       `json:"outstanding"`
	Acknowledged  *policyAck `json:"acknowledged,omitempty"`
	// File and PublishedFile are where Path and PublishedPath are in the
	// synced folder (mtsync adds them), for mtsync show FILE REVISION with
	// the folder's revision.
	File          string `json:"file,omitempty"`
	PublishedFile string `json:"publishedFile,omitempty"`
	// A quiz (0.50.0) names the file it Covers; that file names its Quiz.
	Covers string      `json:"covers,omitempty"`
	Quiz   *policyQuiz `json:"quiz,omitempty"`
}

// policyQuiz is the quiz that covers a file, and the person's state in it:
// status is new, waiting (for a reviewer), failed or passed.
type policyQuiz struct {
	Path          string `json:"path"`
	PublishedPath string `json:"publishedPath"`
	Status        string `json:"status"`
	Score         *int   `json:"score,omitempty"`
	RetryAt       string `json:"retryAt,omitempty"`
	Older         bool   `json:"older,omitempty"`
	File          string `json:"file,omitempty"`
	PublishedFile string `json:"publishedFile,omitempty"`
}

type policyFolder struct {
	Folder      string        `json:"folder"`
	Version     int           `json:"version"`
	Revision    string        `json:"revision"`
	Timestamp   string        `json:"timestamp"`
	By          *changeAuthor `json:"by,omitempty"`
	Acknowledge bool          `json:"acknowledge"`
	Files       []policyFile  `json:"files"`
}

type policiesAnswer struct {
	Space       string         `json:"space"`
	State       string         `json:"state"`
	Folders     []policyFolder `json:"folders"`
	Outstanding int            `json:"outstanding"`
}

type reportAnswer struct {
	Space string `json:"space"`
	State string `json:"state"`
	Files []struct {
		ID      string `json:"id"`
		Path    string `json:"path"`
		Folder  string `json:"folder"`
		Version int    `json:"version"`
	} `json:"files"`
	People []struct {
		Name        string `json:"name"`
		Outstanding int    `json:"outstanding"`
		Files       []struct {
			Status       string     `json:"status"`
			Acknowledged *policyAck `json:"acknowledged,omitempty"`
		} `json:"files"`
	} `json:"people"`
}

// policies reads the signed-in person's published folders under the prefix.
func (c *client) policies(ctx context.Context) (policiesAnswer, error) {
	var out policiesAnswer
	if !c.account {
		return out, fmt.Errorf("published versions need a signed-in space (mtsync login), not a space key")
	}
	if err := c.do(ctx, "GET", "acknowledgements", nil, nil, &out); err != nil {
		if errorCode(err) == "not_found" {
			return out, fmt.Errorf("%w; acknowledgements need metatrash 0.48.0 or later on the server", err)
		}
		return out, err
	}
	folders := []policyFolder{}
	out.Outstanding = 0
	for _, f := range out.Folders {
		files := []policyFile{}
		for _, file := range f.Files {
			if !strings.HasPrefix(file.Path, c.prefix) {
				continue
			}
			file.Path = strings.TrimPrefix(file.Path, c.prefix)
			file.PublishedPath = strings.TrimPrefix(file.PublishedPath, c.prefix)
			if file.Outstanding {
				out.Outstanding++
			}
			files = append(files, file)
		}
		if strings.HasPrefix(f.Folder, c.prefix) || len(files) > 0 {
			f.Folder = strings.TrimPrefix(f.Folder, c.prefix)
			f.Files = files
			folders = append(folders, f)
		}
	}
	out.Folders = folders
	return out, nil
}

// acknowledge confirms that the person read path's published text, version.
func (c *client) acknowledge(ctx context.Context, path string, version int) (policyFile, error) {
	var out policyFile
	err := c.do(ctx, "POST", "acknowledgements", nil, map[string]any{"path": c.prefix + path, "version": version}, &out)
	out.Path = strings.TrimPrefix(out.Path, c.prefix)
	out.PublishedPath = strings.TrimPrefix(out.PublishedPath, c.prefix)
	return out, err
}

func (c *client) report(ctx context.Context) (reportAnswer, error) {
	var out reportAnswer
	err := c.do(ctx, "GET", "acknowledgements", map[string][]string{"report": {"true"}}, nil, &out)
	return out, err
}

// syncedPlace finds the registered folder holding place, which may be the
// folder itself or anything in it.
func syncedPlace(place string) (folderEntry, error) {
	abs, err := filepath.Abs(place)
	if err != nil {
		return folderEntry{}, err
	}
	r, err := loadRegistry()
	if err != nil {
		return folderEntry{}, err
	}
	for _, e := range r.Folders {
		if within(abs, e.Folder) {
			return e, nil
		}
	}
	return folderEntry{}, fmt.Errorf("%s is not in a folder mtsync syncs (see mtsync list)", place)
}

func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func localTime(stamp string) string {
	if t, err := time.Parse(time.RFC3339, stamp); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return stamp
}

// showPolicies lists the published folders and files of the space synced at
// place, and which the person still has to read.
func showPolicies(ctx context.Context, place string, asJSON bool, out io.Writer) error {
	e, err := syncedPlace(place)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	answer, err := c.policies(ctx)
	if err != nil {
		return err
	}
	for i := range answer.Folders {
		for j := range answer.Folders[i].Files {
			f := &answer.Folders[i].Files[j]
			f.File = filepath.Join(e.Folder, filepath.FromSlash(f.Path))
			f.PublishedFile = filepath.Join(e.Folder, filepath.FromSlash(f.PublishedPath))
			if q := f.Quiz; q != nil {
				q.File = filepath.Join(e.Folder, filepath.FromSlash(q.Path))
				q.PublishedFile = filepath.Join(e.Folder, filepath.FromSlash(q.PublishedPath))
			}
		}
	}
	if asJSON {
		return printJSON(out, answer)
	}
	if len(answer.Folders) == 0 {
		fmt.Fprintln(out, "No published folders.")
		return nil
	}
	for _, f := range answer.Folders {
		name := f.Folder
		if name == "" {
			name = "(whole folder)"
		}
		line := fmt.Sprintf("%s  version %d, published %s", name, f.Version, localTime(f.Timestamp))
		if f.By != nil {
			line += " by " + f.By.String()
		}
		fmt.Fprintln(out, line)
		for _, file := range f.Files {
			state := ""
			switch {
			case file.Covers != "":
				state = "quiz for " + file.Covers
			case file.Outstanding && file.Quiz != nil:
				state = "to read, then pass " + file.Quiz.Path
				switch file.Quiz.Status {
				case "waiting":
					state += " (waiting for a reviewer)"
				case "failed":
					state += " (not passed"
					if file.Quiz.Score != nil {
						state += fmt.Sprintf(", %d%%", *file.Quiz.Score)
					}
					if file.Quiz.RetryAt != "" {
						state += "; try again from " + localTime(file.Quiz.RetryAt)
					}
					state += ")"
				}
			case file.Outstanding:
				state = "to read"
			case file.Tracked:
				state = "read"
			}
			if file.Draft {
				state = strings.TrimPrefix(state+", draft changes", ", ")
			}
			fmt.Fprintf(out, "  %-40s %s\n", file.Path, state)
		}
	}
	fmt.Fprintf(out, "%d to read. Confirm one: mtsync ack FILE VERSION\n", answer.Outstanding)
	return nil
}

// acknowledgeFile confirms the person read file's published text, version.
func acknowledgeFile(ctx context.Context, file, version string, out io.Writer) error {
	v, err := strconv.Atoi(version)
	if err != nil || v < 1 {
		return fmt.Errorf("%q is not a version; use the folder's version from mtsync policies", version)
	}
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	if _, err := c.acknowledge(ctx, rel, v); err != nil {
		return err
	}
	fmt.Fprintf(out, "Confirmed: you read %s, version %d.\n", rel, v)
	return nil
}

// showReport prints who has read what in the space synced at place.
func showReport(ctx context.Context, place string, asJSON bool, out io.Writer) error {
	e, err := syncedPlace(place)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	answer, err := c.report(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(out, answer)
	}
	if len(answer.Files) == 0 {
		fmt.Fprintln(out, "No files to read in this space.")
		return nil
	}
	for _, p := range answer.People {
		fmt.Fprintf(out, "%s: %d to read\n", p.Name, p.Outstanding)
		for i, s := range p.Files {
			if i >= len(answer.Files) || s.Status == "read" {
				continue
			}
			f := answer.Files[i]
			fmt.Fprintf(out, "  %-40s %s (version %d)\n", f.Path, s.Status, f.Version)
		}
	}
	return nil
}
