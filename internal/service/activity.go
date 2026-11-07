package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"sync"
	"time"
)

// Activity (0.95.9): what is happening in a space beyond its files, so the
// site's Files list (and later Slate Viewer and mtsync) can show it instead
// of leaving people to wait for files to move:
//
//   - conversion: Docling's work on an upload or a stored PDF, Word,
//     PowerPoint or Excel file (queued, converting, failed, retrying), then
//     pairing (moving the original into assets/, kept and why, converted);
//   - agent: folder action jobs (queued, running, and for a while after,
//     done, failed or skipped);
//   - presence: who else has the space open on the site, and which file.
//
//	GET /api/v1/spaces/<space>/activity            (account REST)
//	GET /api/v1/site/spaces/<space>/activity?viewing=<path>  (the site)
//
// A signed-in site page that asks says it is present, viewing the file
// named (or the space, with viewing empty); it drops out a minute after it
// stops asking. Conversions and presence are kept in memory, so a restart
// forgets them, and with several Metatrash processes each answers for its
// own; agent jobs come from the account database.

// ActivityItem is one thing happening in a space.
type ActivityItem struct {
	// Kind is conversion, agent or presence.
	Kind string `json:"kind"`
	// Path is the file it concerns; for an agent job with no file (a
	// scheduled one) and for presence in no file, it is empty and Folder
	// says where.
	Path   string `json:"path,omitempty"`
	Folder string `json:"folder,omitempty"`
	// State: conversion queued, converting, pairing, failed, retrying, kept
	// or converted; agent queued, running, done, failed or skipped;
	// presence viewing.
	State string `json:"state"`
	// Message says it for a person.
	Message string `json:"message"`
	// Since is when the state began (RFC 3339).
	Since string `json:"since,omitempty"`
	// Ahead counts the conversions before a queued file.
	Ahead int `json:"ahead,omitempty"`
	// RetryAt is when a failed or retrying conversion is tried again.
	RetryAt string `json:"retryAt,omitempty"`
	// From is the original's path now, on a converted page.
	From string `json:"from,omitempty"`
	// Who is the person present; Action the folder action's name.
	Who    string `json:"who,omitempty"`
	Action string `json:"action,omitempty"`
}

// ActivityResult answers activity.
type ActivityResult struct {
	Space string `json:"space"`
	// Docling is "" when the server converts nothing, "ready", or
	// "unreachable" when its converter service hasn't answered yet.
	Docling string         `json:"docling,omitempty"`
	Items   []ActivityItem `json:"items"`
}

const (
	// How long results stay listed.
	activityResultFor = 15 * time.Minute
	activityKeptFor   = time.Hour
	// activityNotesMax bounds the conversion notes kept for one space.
	activityNotesMax = 200
	// presenceFor is how long a page counts as present after it last asked.
	presenceFor = time.Minute
	// presenceMax bounds the people listed for one space.
	presenceMax = 50
)

// pathNote is pairing's word on one path of a space.
type pathNote struct {
	state, message, from string
	at, until            time.Time
}

// doclingFailure is why a conversion failed and when it is tried again.
type doclingFailure struct {
	why     string
	soon    bool
	at, due time.Time
}

// note records pairing's word on path in space. Callers hold q.mu.
func (q *doclingQueue) note(space, p string, n pathNote) {
	notes := q.notes[space]
	if notes == nil {
		notes = map[string]pathNote{}
		q.notes[space] = notes
	}
	now := time.Now()
	for k, old := range notes {
		if !old.until.IsZero() && now.After(old.until) {
			delete(notes, k)
		}
	}
	if _, ok := notes[p]; !ok && len(notes) >= activityNotesMax {
		return
	}
	if n.at.IsZero() {
		n.at = now
	}
	notes[p] = n
}

// forget drops pairing's word on path in space. Callers hold q.mu.
func (q *doclingQueue) forget(space, p string) {
	delete(q.notes[space], p)
	if len(q.notes[space]) == 0 {
		delete(q.notes, space)
	}
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// activity lists what is happening in space r. viewer, when set, is the
// person asking, recorded as present first.
func (s *Service) activity(ctx context.Context, r *repository, space string, viewer *presenceEntry) (ActivityResult, error) {
	result := ActivityResult{Space: space, Items: []ActivityItem{}}
	if !r.owned || r.id == "" {
		return result, nil
	}
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return result, err
	}
	if q := s.docling(); q != nil {
		result.Docling = "ready"
		if _, ident := q.identity(); ident == "" {
			result.Docling = "unreachable"
		}
		result.Items = append(result.Items, q.activity(r, files)...)
	}
	if a := s.agents.Load(); a != nil && a.store != nil {
		jobs, err := a.store.recent(ctx, r.id, time.Now().Add(-24*time.Hour))
		if err == nil {
			result.Items = append(result.Items, jobActivity(jobs, time.Now())...)
		}
	}
	if viewer != nil {
		s.presence.see(r.id, *viewer)
	}
	for _, p := range s.presence.list(r.id) {
		if viewer != nil && p.userID == viewer.userID {
			continue
		}
		item := ActivityItem{Kind: "presence", State: "viewing", Who: p.name, Since: stamp(p.since)}
		if _, ok := files[p.path]; ok && p.path != "" {
			item.Path, item.Message = p.path, p.name+" has this file open."
		} else {
			item.Message = p.name + " has this space open."
		}
		result.Items = append(result.Items, item)
	}
	sort.SliceStable(result.Items, func(i, j int) bool {
		a, b := result.Items[i], result.Items[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Kind < b.Kind
	})
	return result, nil
}

// activity lists the conversions and pairings of space r's files.
func (q *doclingQueue) activity(r *repository, files map[string]record) []ActivityItem {
	now := time.Now()
	var items []ActivityItem
	q.mu.Lock()
	defer q.mu.Unlock()
	listed := map[string]bool{}
	for p, n := range q.notes[r.id] {
		if !n.until.IsZero() && now.After(n.until) {
			delete(q.notes[r.id], p)
			continue
		}
		if _, ok := files[p]; !ok {
			continue // deleted or moved since
		}
		listed[p] = true
		items = append(items, ActivityItem{Kind: "conversion", Path: p, State: n.state, Message: n.message, Since: stamp(n.at), From: n.from})
	}
	for p, f := range files {
		if f.Virtual == nil || listed[p] || !q.wants(r, *f.Virtual) {
			continue
		}
		key := q.key(r, *f.Virtual)
		label := doclingFormats[f.Virtual.Type].label
		switch {
		case !q.running[key].IsZero():
			items = append(items, ActivityItem{Kind: "conversion", Path: p, State: "converting", Since: stamp(q.running[key]),
				Message: "Converting this " + label + " to Markdown with Docling."})
		case q.pending[key]:
			ahead := len(q.running)
			for other, at := range q.queuedAt {
				if other != key && q.pending[other] && q.running[other].IsZero() && at.Before(q.queuedAt[key]) {
					ahead++
				}
			}
			items = append(items, ActivityItem{Kind: "conversion", Path: p, State: "queued", Since: stamp(q.queuedAt[key]), Ahead: ahead,
				Message: "Waiting to be converted to Markdown."})
		default:
			fail, ok := q.failures[key]
			if !ok || now.After(fail.due) {
				continue
			}
			item := ActivityItem{Kind: "conversion", Path: p, State: "failed", Since: stamp(fail.at), RetryAt: stamp(fail.due),
				Message: "Docling couldn't convert this " + label + ": " + fail.why + ". Agents read its plain text instead."}
			if fail.soon {
				item.State = "retrying"
				item.Message = "The converter couldn't take this " + label + " (" + fail.why + "). It is tried again in a few minutes."
			}
			items = append(items, item)
		}
	}
	return items
}

// pairedNotes records pairing's outcome for each uploaded path: converted
// on the new page, or kept with why. Callers hold q.mu.
func (q *doclingQueue) pairedNotes(r *repository, outcomes []pairOutcome) {
	now := time.Now()
	for _, o := range outcomes {
		q.forget(r.id, o.path)
		switch {
		case o.page != "":
			q.note(r.id, o.page, pathNote{state: "converted", from: o.original, until: now.Add(activityResultFor),
				message: fmt.Sprintf("Made from %s with Docling. The original is in %s.", path.Base(o.path), path.Dir(o.original)+"/")})
		case o.why != "":
			q.note(r.id, o.path, pathNote{state: "kept", until: now.Add(activityKeptFor),
				message: "Converted to Markdown for agents, but left as it is: " + o.why + "."})
		}
	}
}

// jobActivity lists folder action jobs: waiting and running ones, and
// those that ended in the last activityResultFor.
func jobActivity(jobs []agentJob, now time.Time) []ActivityItem {
	var items []ActivityItem
	for _, j := range jobs {
		item := ActivityItem{Kind: "agent", Path: j.File, Folder: j.Folder, Action: j.Action, State: j.Status}
		what := fmt.Sprintf("Folder action %q", j.Action)
		switch j.Status {
		case jobQueued:
			item.Since, item.Message = stamp(j.QueuedAt), what+" is waiting to run."
		case jobRunning:
			item.Since, item.Message = stamp(j.StartedAt), what+" is running."
		case jobDone, jobFailed, jobSkipped:
			if j.FinishedAt.IsZero() || now.Sub(j.FinishedAt) > activityResultFor {
				continue
			}
			item.Since = stamp(j.FinishedAt)
			switch j.Status {
			case jobDone:
				item.Message = what + " finished. Its notes are in " + j.Folder + runnerLogName + "."
			case jobFailed:
				item.Message = what + " failed: " + j.Error
			default:
				item.Message = what + " didn't run: " + j.Error
			}
		default:
			continue
		}
		items = append(items, item)
	}
	return items
}

// presenceEntry is one person with a space open on the site.
type presenceEntry struct {
	userID, name, path string
	since, seen        time.Time
}

// presenceBoard holds who has which space open; the zero value is ready.
type presenceBoard struct {
	mu     sync.Mutex
	spaces map[string]map[string]presenceEntry
}

// see records p as present in space now.
func (b *presenceBoard) see(space string, p presenceEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if b.spaces == nil {
		b.spaces = map[string]map[string]presenceEntry{}
	}
	// Drop everyone gone, in every space, so the board stays small.
	for id, people := range b.spaces {
		for u, e := range people {
			if now.Sub(e.seen) > presenceFor {
				delete(people, u)
			}
		}
		if len(people) == 0 {
			delete(b.spaces, id)
		}
	}
	people := b.spaces[space]
	if people == nil {
		people = map[string]presenceEntry{}
		b.spaces[space] = people
	}
	old, ok := people[p.userID]
	if !ok && len(people) >= presenceMax {
		return
	}
	p.since, p.seen = now, now
	if ok && old.path == p.path {
		p.since = old.since
	}
	people[p.userID] = p
}

// list is who has space open now, longest first.
func (b *presenceBoard) list(space string) []presenceEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	var out []presenceEntry
	for _, e := range b.spaces[space] {
		if now.Sub(e.seen) <= presenceFor {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].since.Before(out[j].since) || out[i].since.Equal(out[j].since) && out[i].name < out[j].name
	})
	return out
}

// serveActivity answers GET .../activity, with ?viewing=<path> from a
// signed-in person on the site.
func (h *httpAdapter) serveActivity(w http.ResponseWriter, r *http.Request, repo *repository, space string) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) > 1 || len(q) == 1 && len(q["viewing"]) != 1 {
		sendError(w, invalid("Only ?viewing=<path> is accepted."))
		return
	}
	var viewer *presenceEntry
	if _, ok := q["viewing"]; ok && siteRequest(r.Context()) {
		viewing := q.Get("viewing")
		if viewing != "" && !validPath(viewing) {
			sendError(w, invalid("viewing must be a file's path, or empty."))
			return
		}
		identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
		if author, ok := authorOf(r.Context()); ok && author.Kind == "person" && identity.oauth != nil && identity.oauth.UserID != "" {
			viewer = &presenceEntry{userID: identity.oauth.UserID, name: author.Name, path: viewing}
		}
	}
	value, err := h.service.activity(r.Context(), repo, space, viewer)
	if err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sendJSON(w, 200, value)
}
