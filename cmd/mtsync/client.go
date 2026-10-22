package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// apiError is a metatrash error response.
type apiError struct {
	Status       int
	Code         string `json:"code"`
	Message      string `json:"message"`
	CurrentState string `json:"currentState"`
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s (%d %s)", e.Message, e.Status, e.Code)
}

func errorCode(err error) string {
	var e *apiError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

type remoteFile struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Protected bool   `json:"protected"`
	Deletable bool   `json:"deletable"`
	Blob      string `json:"blob"`
}

type mutation struct {
	OldState string     `json:"oldState"`
	NewState string     `json:"newState"`
	File     remoteFile `json:"file"`
}

// client talks to one space over REST: the OAuth account routes, or the
// anonymous routes with a space key.
type client struct {
	http    *http.Client
	account bool
	base    string // .../api/v1/account/spaces/{space} or .../api/v1/spaces/{space}
	// prefix is the space folder synced ("site/"), or empty for the whole
	// space. Paths given to and returned by the methods below are relative
	// to it.
	prefix string
	token  func(ctx context.Context, refresh bool) (string, error)
	// sleep waits out rate limits; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

// newClient reaches target: a space ("owner/slug", "public", an ID, or a key
// space's name), optionally followed by a folder in it ("owner/slug/site").
func newClient(server, target string, token func(ctx context.Context, refresh bool) (string, error), account bool) *client {
	route := "/api/v1/spaces/"
	if account {
		route = "/api/v1/account/spaces/"
	}
	space, prefix := splitTarget(target, account)
	return &client{http: &http.Client{Timeout: 60 * time.Second}, account: account, base: strings.TrimSuffix(server, "/") + route + space, prefix: prefix, token: token, sleep: sleepContext}
}

// splitTarget separates the space from a folder inside it. Signed-in spaces
// are named owner/slug (two segments) unless public or an ID; key spaces by
// one name.
func splitTarget(target string, account bool) (space, prefix string) {
	parts := strings.Split(strings.Trim(target, "/"), "/")
	n := 1
	if account && parts[0] != "public" && !spaceIDPattern.MatchString(parts[0]) {
		n = 2
	}
	if len(parts) <= n {
		return strings.Trim(target, "/"), ""
	}
	return strings.Join(parts[:n], "/"), strings.Join(parts[n:], "/") + "/"
}

var spaceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do sends one JSON request and decodes the JSON answer into out.
func (c *client) do(ctx context.Context, method, resource string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return err
		}
	}
	b, _, err := c.send(ctx, method, resource, query, payload, 8<<20)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// send sends one request and returns the body of a successful answer, read
// up to limit bytes. It refreshes the token once on 401 and waits out 429
// and 503 responses as Retry-After asks.
func (c *client) send(ctx context.Context, method, resource string, query url.Values, payload []byte, limit int64) ([]byte, http.Header, error) {
	target := c.base
	if resource != "" {
		target += "/" + resource
	}
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
		if err != nil {
			return nil, nil, err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.token != nil {
			token, err := c.token(ctx, false)
			if err != nil {
				return nil, nil, err
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		if int64(len(b)) > limit {
			return nil, nil, fmt.Errorf("answer is larger than %d bytes", limit)
		}
		if resp.StatusCode < 300 {
			return b, resp.Header, nil
		}
		var wrapped struct {
			Error apiError `json:"error"`
		}
		_ = json.Unmarshal(b, &wrapped)
		e := wrapped.Error
		e.Status = resp.StatusCode
		if e.Code == "" {
			e.Code, e.Message = "http_error", strings.TrimSpace(string(b))
		}
		if resp.StatusCode == 401 && c.token != nil && !refreshed {
			refreshed = true
			if _, err := c.token(ctx, true); err != nil {
				return nil, nil, err
			}
			continue
		}
		if (resp.StatusCode == 429 || resp.StatusCode == 503) && attempt < 20 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			if wait < 1 {
				wait = 1
			}
			if err := c.sleep(ctx, time.Duration(wait)*time.Second); err != nil {
				return nil, nil, err
			}
			continue
		}
		return nil, nil, &e
	}
}

// export downloads the whole space at its current state as a zip.
func (c *client) export(ctx context.Context) (string, []byte, error) {
	b, header, err := c.send(ctx, "GET", "export", nil, nil, 256<<20)
	if err != nil {
		return "", nil, err
	}
	state := header.Get("X-Metatrash-State")
	if len(state) < 12 || len(b) < 4 || string(b[:4]) != "PK\x03\x04" {
		return "", nil, fmt.Errorf("the server did not send a zip; it may be older than 0.38.0")
	}
	return state, b, nil
}

// head returns the space's current state with a one-file listing.
func (c *client) head(ctx context.Context) (string, error) {
	return c.waitHead(ctx, "")
}

// waitHead is head, but when after is set the server holds the request while
// the space is still at that state (up to about 25 seconds) and answers as
// soon as it moves.
func (c *client) waitHead(ctx context.Context, after string) (string, error) {
	var out struct {
		State string `json:"state"`
	}
	query := url.Values{"limit": {"1"}}
	if c.prefix != "" {
		query.Set("prefix", c.prefix)
	}
	if after != "" {
		query.Set("after", after)
	}
	err := c.do(ctx, "GET", "files", query, nil, &out)
	return out.State, err
}

// list returns every file with its blob hash, from one snapshot.
func (c *client) list(ctx context.Context) (string, []remoteFile, error) {
	files := []remoteFile{}
	query := url.Values{"limit": {"100"}, "blobs": {"true"}}
	if c.prefix != "" {
		query.Set("prefix", c.prefix)
	}
	state := ""
	for {
		var out struct {
			State      string       `json:"state"`
			Files      []remoteFile `json:"files"`
			NextCursor *string      `json:"nextCursor"`
		}
		if err := c.do(ctx, "GET", "files", query, nil, &out); err != nil {
			return "", nil, err
		}
		state = out.State
		for _, f := range out.Files {
			f.Path = strings.TrimPrefix(f.Path, c.prefix)
			files = append(files, f)
		}
		if out.NextCursor == nil {
			return state, files, nil
		}
		query.Set("cursor", *out.NextCursor)
	}
}

// changes lists what changed under the prefix since state: the files added
// or changed (with blobs) and the paths removed, as of the returned state.
func (c *client) changes(ctx context.Context, since string) (string, []remoteFile, []string, error) {
	var out struct {
		State   string       `json:"state"`
		Files   []remoteFile `json:"files"`
		Removed []string     `json:"removed"`
	}
	query := url.Values{"since": {since}}
	if c.prefix != "" {
		query.Set("prefix", c.prefix)
	}
	if err := c.do(ctx, "GET", "files", query, nil, &out); err != nil {
		return "", nil, nil, err
	}
	for i := range out.Files {
		out.Files[i].Path = strings.TrimPrefix(out.Files[i].Path, c.prefix)
	}
	for i := range out.Removed {
		out.Removed[i] = strings.TrimPrefix(out.Removed[i], c.prefix)
	}
	return out.State, out.Files, out.Removed, nil
}

func (c *client) read(ctx context.Context, path, revision string) (string, error) {
	var out struct {
		File struct {
			Text string `json:"text"`
		} `json:"file"`
	}
	query := url.Values{"path": {c.prefix + path}}
	if revision != "" {
		query.Set("revision", revision)
	}
	err := c.do(ctx, "GET", "file", query, nil, &out)
	return out.File.Text, err
}

func (c *client) write(ctx context.Context, path, text, state string) (mutation, error) {
	var out mutation
	err := c.do(ctx, "PUT", "file", url.Values{"path": {c.prefix + path}}, map[string]any{"text": text, "ifInState": state}, &out)
	out.File.Path = strings.TrimPrefix(out.File.Path, c.prefix)
	return out, err
}

func (c *client) remove(ctx context.Context, path, state string) (mutation, error) {
	var out mutation
	err := c.do(ctx, "DELETE", "file", url.Values{"path": {c.prefix + path}}, map[string]any{"ifInState": state}, &out)
	return out, err
}

func (c *client) move(ctx context.Context, from, to, state string) (mutation, error) {
	var out mutation
	err := c.do(ctx, "POST", "move", nil, map[string]any{"from": c.prefix + from, "to": c.prefix + to, "ifInState": state}, &out)
	out.File.Path = strings.TrimPrefix(out.File.Path, c.prefix)
	return out, err
}

// historyEntry is one change to a file, newest first. Path is the file's
// path in the space at that revision.
type historyEntry struct {
	Revision  string `json:"revision"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Timestamp string `json:"timestamp"`
	// By is who made the change (servers from 0.47.0; not on older changes).
	By *changeAuthor `json:"by,omitempty"`
	// Published lists the versions of its folder that published this text
	// (servers from 0.48.0).
	Published []int `json:"published,omitempty"`
}

// changeAuthor names who made a change: an account name, the app (Metatrash
// Sync, an agent such as Claude, a space key) and whether it was a person or
// an agent.
type changeAuthor struct {
	Name string `json:"name,omitempty"`
	App  string `json:"app,omitempty"`
	Kind string `json:"kind"`
}

// String is the author as history prints it: "dave (Claude, agent)".
func (a *changeAuthor) String() string {
	if a == nil {
		return ""
	}
	detail := a.Kind
	if a.App != "" {
		detail = a.App + ", " + a.Kind
	}
	if a.Name == "" {
		return "(" + detail + ")"
	}
	return a.Name + " (" + detail + ")"
}

// history lists up to limit changes to the file with this ID, skipping the
// offset newest. more reports that older entries remain.
func (c *client) history(ctx context.Context, id string, limit, offset int) (entries []historyEntry, more bool, err error) {
	var out struct {
		Entries    []historyEntry `json:"entries"`
		NextCursor *string        `json:"nextCursor"`
	}
	query := url.Values{"id": {id}, "limit": {strconv.Itoa(limit)}}
	if offset > 0 {
		query.Set("offset", strconv.Itoa(offset))
	}
	if err := c.do(ctx, "GET", "history", query, nil, &out); err != nil {
		if offset > 0 && errorCode(err) == "invalid_request" {
			return nil, false, fmt.Errorf("%w; paging with -offset needs metatrash 0.41.0 or later on the server", err)
		}
		return nil, false, err
	}
	return out.Entries, out.NextCursor != nil, nil
}
