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
	Status       int    `json:"status"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	CurrentState string `json:"currentState,omitempty"`
	// Fields lists each refused answer of a form (submit).
	Fields []fieldProblem `json:"fields,omitempty"`
}

// fieldProblem is one refused answer.
type fieldProblem struct {
	Name    string `json:"name"`
	Message string `json:"message"`
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
	// Virtual is set on a virtual file (metatrash 0.60.0): its content is
	// kept outside Git, Blob is its pointer's, and the content moves through
	// .../content (download and upload).
	Virtual *virtualInfo `json:"virtual,omitempty"`
}

// virtualInfo describes a virtual file's content, as the server lists it.
type virtualInfo struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Type   string `json:"type"`
	// TextSHA256 is set when the server holds text for the file (its
	// Markdown for a PDF or Word file); it equals SHA256 for text content.
	TextSHA256 string `json:"textSha256,omitempty"`
	Source     string `json:"source,omitempty"`
	Refresh    string `json:"refresh,omitempty"`
}

// maxVirtualBytes is the most one virtual file holds (metatrash 0.60.0).
const maxVirtualBytes = 100 << 20

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
// up to limit bytes.
func (c *client) send(ctx context.Context, method, resource string, query url.Values, payload []byte, limit int64) ([]byte, http.Header, error) {
	contentType := ""
	if payload != nil {
		contentType = "application/json"
	}
	resp, err := c.sendRaw(ctx, c.http, method, resource, query, payload, contentType)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > limit {
		return nil, nil, fmt.Errorf("answer is larger than %d bytes", limit)
	}
	return b, resp.Header, nil
}

// sendRaw sends one request and returns a successful answer, whose body the
// caller reads and closes. It refreshes the token once on 401 and waits out
// 429 and 503 responses as Retry-After asks (not a server that keeps no
// virtual files, which says so with 503).
func (c *client) sendRaw(ctx context.Context, hc *http.Client, method, resource string, query url.Values, payload []byte, contentType string) (*http.Response, error) {
	target := c.base
	if resource != "" {
		target += "/" + resource
	}
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if c.token != nil {
			token, err := c.token(ctx, false)
			if err != nil {
				return nil, err
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 300 {
			return resp, nil
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
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
				return nil, err
			}
			continue
		}
		if (resp.StatusCode == 429 || resp.StatusCode == 503 && e.Code != "virtual_unavailable") && attempt < 20 {
			wait, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			if wait < 1 {
				wait = 1
			}
			if err := c.sleep(ctx, time.Duration(wait)*time.Second); err != nil {
				return nil, err
			}
			continue
		}
		return nil, &e
	}
}

// transfers is the HTTP client for downloads and uploads of whole files,
// which may take longer than the minute other requests get.
func (c *client) transfers() *http.Client {
	t := *c.http
	t.Timeout = 30 * time.Minute
	return &t
}

// export saves the whole space at its current state as a zip to w, as it
// arrives (a space with virtual files can be larger than memory should
// hold).
func (c *client) export(ctx context.Context, w io.Writer) (string, int64, error) {
	resp, err := c.sendRaw(ctx, c.transfers(), "GET", "export", nil, nil, "")
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	state := resp.Header.Get("X-Metatrash-State")
	head := make([]byte, 4)
	n, _ := io.ReadFull(resp.Body, head)
	if len(state) < 12 || n < 4 || string(head) != "PK\x03\x04" {
		return "", 0, fmt.Errorf("the server did not send a zip; it may be older than 0.38.0")
	}
	if _, err := w.Write(head); err != nil {
		return "", 0, err
	}
	copied, err := io.Copy(w, resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("the export was cut off: %w", err)
	}
	return state, copied + 4, nil
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
	text, _, err := c.readFile(ctx, path, revision)
	return text, err
}

// readFile reads a file's text, and says whether it is a virtual file: its
// text is then the server's text for it (a PDF's Markdown, say), and its
// content comes from download.
func (c *client) readFile(ctx context.Context, path, revision string) (string, *virtualInfo, error) {
	var out struct {
		File struct {
			Text    string       `json:"text"`
			Virtual *virtualInfo `json:"virtual"`
		} `json:"file"`
	}
	query := url.Values{"path": {c.prefix + path}}
	if revision != "" {
		query.Set("revision", revision)
	}
	err := c.do(ctx, "GET", "file", query, nil, &out)
	return out.File.Text, out.File.Virtual, err
}

// download writes a file's content at revision ("" for the latest) to w: a
// virtual file's bytes, or an ordinary file's text (metatrash 0.60.0).
func (c *client) download(ctx context.Context, path, revision string, w io.Writer) (int64, error) {
	query := url.Values{"path": {c.prefix + path}}
	if revision != "" {
		query.Set("revision", revision)
	}
	resp, err := c.sendRaw(ctx, c.transfers(), "GET", "content", query, nil, "")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	n, err := io.Copy(w, io.LimitReader(resp.Body, maxVirtualBytes+1))
	if err == nil && n > maxVirtualBytes {
		err = fmt.Errorf("%s is larger than %d MB", path, maxVirtualBytes>>20)
	}
	return n, err
}

// upload stores content as the virtual file at path (a new one, or a new
// version), as of state (metatrash 0.60.0). The server tells the type from
// the name and the bytes.
func (c *client) upload(ctx context.Context, path string, content []byte, state string) (mutation, error) {
	if content == nil {
		content = []byte{}
	}
	resp, err := c.sendRaw(ctx, c.transfers(), "PUT", "content", url.Values{"path": {c.prefix + path}, "ifInState": {state}}, content, "application/octet-stream")
	if err != nil {
		return mutation{}, err
	}
	defer resp.Body.Close()
	var out mutation
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return mutation{}, err
	}
	out.File.Path = strings.TrimPrefix(out.File.Path, c.prefix)
	return out, nil
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
