package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// Dashboards (metatrash 0.99.0). Slate Viewer draws a dashboard's cards on
// this computer and asks the server for their data through these commands:
//
//	mtsync query DASHBOARD QUERY     a card's query (a JSON file, or - for stdin)
//	mtsync notices FILE...           whether you have done each notice or form
//	mtsync notice FILE BLOB          acknowledge a notice (the text with that blob)

type queryAnswer struct {
	Space  string   `json:"space"`
	State  string   `json:"state"`
	Source string   `json:"source"`
	Kind   string   `json:"kind"`
	Fields []string `json:"fields"`
	Rows   []struct {
		File   string            `json:"file,omitempty"`
		Line   int               `json:"line,omitempty"`
		Values map[string]string `json:"values"`
	} `json:"rows,omitempty"`
	Groups []struct {
		Group string  `json:"group"`
		Count int     `json:"count"`
		Value float64 `json:"value"`
	} `json:"groups,omitempty"`
	Count     int     `json:"count"`
	Value     float64 `json:"value"`
	Measure   string  `json:"measure"`
	Truncated bool    `json:"truncated,omitempty"`
}

type noticeAnswer struct {
	Path string `json:"path"`
	Blob string `json:"blob,omitempty"`
	Kind string `json:"kind"`
	Done bool   `json:"done"`
	At   string `json:"at,omitempty"`
}

func (c *client) query(ctx context.Context, dashboard string, card map[string]any) (queryAnswer, error) {
	var out queryAnswer
	body := map[string]any{}
	for k, v := range card {
		body[k] = v
	}
	body["base"] = c.prefix + dashboard
	if err := c.do(ctx, "POST", "query", nil, body, &out); err != nil {
		if errorCode(err) == "not_found" && strings.Contains(err.Error(), "File or revision") {
			return out, fmt.Errorf("%w; dashboards need metatrash 0.99.0 or later on the server", err)
		}
		return out, err
	}
	// Paths in the synced folder are given relative to it.
	out.Source = strings.TrimPrefix(out.Source, c.prefix)
	for i := range out.Rows {
		out.Rows[i].File = strings.TrimPrefix(out.Rows[i].File, c.prefix)
	}
	return out, nil
}

func (c *client) notices(ctx context.Context, paths []string) ([]noticeAnswer, error) {
	if !c.account {
		return nil, fmt.Errorf("notices need a signed-in space (mtsync login), not a space key")
	}
	full := make([]string, len(paths))
	for i, p := range paths {
		full[i] = c.prefix + p
	}
	var out struct {
		Notices []noticeAnswer `json:"notices"`
	}
	if err := c.do(ctx, "GET", "notices", url.Values{"paths": {strings.Join(full, ",")}}, nil, &out); err != nil {
		return nil, err
	}
	for i := range out.Notices {
		out.Notices[i].Path = strings.TrimPrefix(out.Notices[i].Path, c.prefix)
	}
	return out.Notices, nil
}

func (c *client) acknowledgeNotice(ctx context.Context, path, blob string) (noticeAnswer, error) {
	var out noticeAnswer
	err := c.do(ctx, "POST", "notices", nil, map[string]any{"path": c.prefix + path, "blob": blob}, &out)
	out.Path = strings.TrimPrefix(out.Path, c.prefix)
	return out, err
}

// runQuery answers one card of the dashboard file (a synced file) and prints
// the result as JSON (Slate Viewer reads it) or as lines.
func runQuery(ctx context.Context, dashboard, queryFile string, asJSON bool, stdin io.Reader, out io.Writer) error {
	e, rel, err := syncedFile(dashboard)
	if err != nil {
		return err
	}
	raw, err := readAnswers(queryFile, stdin)
	if err != nil {
		return fmt.Errorf("the query must be a JSON object such as {\"source\": \"jobs/\", \"group\": \"type\"}")
	}
	var card map[string]any
	if err := json.Unmarshal(raw, &card); err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	answer, err := c.query(ctx, rel, card)
	if err != nil {
		return printRefusal(err, asJSON, out)
	}
	if asJSON {
		return printJSON(out, answer)
	}
	if len(answer.Groups) > 0 {
		for _, g := range answer.Groups {
			fmt.Fprintf(out, "%-30s %g (%d)\n", g.Group, g.Value, g.Count)
		}
	} else {
		for _, r := range answer.Rows {
			parts := []string{}
			for _, f := range answer.Fields {
				if v, ok := r.Values[f]; ok && f != "file" {
					parts = append(parts, f+"="+v)
				}
			}
			fmt.Fprintf(out, "%s  %s\n", r.File, strings.Join(parts, "  "))
		}
	}
	fmt.Fprintf(out, "%d rows, %s %g\n", answer.Count, answer.Measure, answer.Value)
	return nil
}

func showNotices(ctx context.Context, files []string, asJSON bool, out io.Writer) error {
	var c *client
	var paths []string
	server := ""
	for _, f := range files {
		e, rel, err := syncedFile(f)
		if err != nil {
			return err
		}
		if c == nil {
			c, server = folderClient(e, tokenSource(e.Server)), e.Folder
		} else if e.Folder != server {
			return fmt.Errorf("the files must be in one synced folder")
		}
		paths = append(paths, rel)
	}
	answer, err := c.notices(ctx, paths)
	if err != nil {
		return err
	}
	if asJSON {
		return printJSON(out, map[string]any{"notices": answer})
	}
	for _, n := range answer {
		state := "to do"
		if n.Done {
			state = "done " + localTime(n.At)
		}
		fmt.Fprintf(out, "%-40s %-7s %s\n", n.Path, n.Kind, state)
	}
	return nil
}

func acknowledgeNoticeFile(ctx context.Context, file, blob string, asJSON bool, out io.Writer) error {
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	answer, err := c.acknowledgeNotice(ctx, rel, blob)
	if err != nil {
		return printRefusal(err, asJSON, out)
	}
	if asJSON {
		return printJSON(out, answer)
	}
	fmt.Fprintf(out, "Acknowledged %s.\n", rel)
	return nil
}
