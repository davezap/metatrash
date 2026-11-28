package service

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	pathpkg "path"
	"strings"
	"time"
	"unicode/utf8"

	"metatrash.com/metatrash/extension"
)

// Record pointers (0.105.0; extension/records.go): a file whose text names
// a record an extension keeps. The index marks it (File.Record), as it marks
// virtual files, so the same text from anywhere else is never one. Reads
// show what Hooks.RecordView lets the reader see; everything that copies
// files (export, history, GitHub folders, MT Sync) carries only the pointer.

type RecordInfo = extension.Record

const maxRecordField = 200

// recordPointer is the text Git holds for a record pointer.
func recordPointer(rec RecordInfo) string {
	var b strings.Builder
	b.WriteString(virtualHeader + "\n")
	for _, kv := range [][2]string{{"record", rec.Record}, {"section", rec.Section}, {"step", rec.Step}, {"org", rec.Org}, {"form", rec.Form}, {"from", rec.From}} {
		if kv[1] != "" {
			b.WriteString(kv[0] + " " + kv[1] + "\n")
		}
	}
	b.WriteString("type text/markdown\n")
	return b.String()
}

// checkRecord refuses a Record an extension can't write.
func checkRecord(rec RecordInfo) error {
	if rec.Record == "" {
		return invalid("A record pointer needs its record.")
	}
	for _, v := range []string{rec.Record, rec.Section, rec.Step, rec.Org, rec.Form, rec.From} {
		if len(v) > maxRecordField || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
			return invalid("A record pointer's values are one line of at most 200 bytes, without leading or trailing spaces.")
		}
		for _, c := range v {
			if c < 32 || c == 127 || c == ' ' || c == ' ' {
				return invalid("A record pointer's values have no control characters.")
			}
		}
	}
	return nil
}

// looksRecord reports text that would read as a record pointer.
func looksRecord(text string) bool {
	return looksVirtual(text) && strings.Contains(text, "\nrecord ")
}

func recordRefused(path string) *Error {
	return problem(409, "record_pointer", path+" is a record pointer: what it shows comes from the record, so it cannot be written, edited or appended to. Move or delete it, or write the text to another path.")
}

func recordPointerRefused() *Error {
	return problem(400, "record_pointer", "That text is a record pointer, which only Metatrash writes, and it works only in the space it was made in.")
}

// The route a read is for (extension.Route*), set by the entry points that
// aren't REST: the space's pages, MCP and folder-action jobs.
type recordRouteKey struct{}

func withRecordRoute(ctx context.Context, route string) context.Context {
	return context.WithValue(ctx, recordRouteKey{}, route)
}

// recordRouteOf is the route ctx reads for, or "sync" for Metatrash Sync,
// which gets only the pointer.
func recordRouteOf(ctx context.Context) string {
	if route, ok := ctx.Value(recordRouteKey{}).(string); ok {
		return route
	}
	if id, ok := ctx.Value(mcpIdentityKey{}).(mcpIdentity); ok && id.oauth != nil && strings.HasSuffix(id.oauth.ClientID, syncClientPath) {
		return "sync"
	}
	return extension.RouteAPI
}

// recordHooks is the module answering for record pointers, if any.
func (s *Service) recordHooks() *extension.Hooks {
	for i := range s.extensions {
		if s.extensions[i].RecordView != nil {
			return &s.extensions[i]
		}
	}
	return nil
}

const (
	recordHiddenNote      = "This record isn't shared with you, or no longer is."
	recordUnavailableNote = "This record can't be shown just now. Try again shortly."
	recordNoExtensionNote = "This server has nothing that shows records, so this file is blank."
)

// recordView is what ctx's reader may see of the record pointer at path:
// the Markdown, the attachments it may link, and a note when it is blank.
func (s *Service) recordView(ctx context.Context, r *repository, path string, rec RecordInfo) extension.RecordView {
	h := s.recordHooks()
	if h == nil {
		return extension.RecordView{Hidden: true, Note: recordNoExtensionNote}
	}
	req := extension.RecordViewRequest{UserID: actorOf(ctx), SpaceID: r.id, Path: path, Record: rec, Route: recordRouteOf(ctx)}
	view, err := h.RecordView(ctx, req)
	if err != nil {
		log.Printf("record view: %s %s: %v", r.id, path, err)
		return extension.RecordView{Hidden: true, Note: recordUnavailableNote}
	}
	if !view.Hidden && !utf8.ValidString(view.Markdown) {
		log.Printf("record view: %s %s: not UTF-8", r.id, path)
		return extension.RecordView{Hidden: true, Note: recordUnavailableNote}
	}
	if view.Hidden {
		view.Markdown, view.Files = "", nil
		if view.Note == "" {
			view.Note = recordHiddenNote
		}
	}
	return view
}

// readRecord fills result with the record's view, or with the pointer for
// a route that gets only that (MT Sync) and for earlier revisions.
func (s *Service) readRecord(ctx context.Context, r *repository, path string, rec RecordInfo, pointer []byte, revision string, result *ReadResult) []byte {
	if revision != "" || recordRouteOf(ctx) == "sync" {
		return pointer
	}
	view := s.recordView(ctx, r, path, rec)
	if view.Note != "" {
		result.Note = strings.TrimSpace(result.Note + " " + view.Note)
	}
	if len(view.Files) > 0 {
		result.RecordFiles = recordFileLinks(rec, view.Files)
	}
	return []byte(view.Markdown)
}

// recordFileLinks are the attachment routes a view may link, by name.
func recordFileLinks(rec RecordInfo, names []string) map[string]string {
	out := map[string]string{}
	for _, name := range names {
		if name == "" || strings.ContainsAny(name, "/\\") || len(name) > 240 {
			continue
		}
		out[name] = recordFilePath(rec.Record, rec.Section, name)
	}
	return out
}

func recordFilePath(record, section, name string) string {
	return "/api/v1/records/" + url.PathEscape(record) + "/sections/" + url.PathEscape(section) + "/files/" + url.PathEscape(name)
}

// serveRecordPage sends a record pointer from a space page: ?raw and
// ?format=md send the view as Markdown, never cached.
func (h *httpAdapter) serveRecordPage(w http.ResponseWriter, r *http.Request, repo *repository, path string, rec RecordInfo, userID string) {
	ctx := withRecordRoute(withActor(r.Context(), userID), extension.RoutePage)
	view := h.service.recordView(ctx, repo, path, rec)
	text := view.Markdown
	if view.Hidden {
		text = "> " + view.Note + "\n"
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Accept")
	sendText(w, r, "text/markdown; charset=utf-8", []byte(text))
}

// serveRecordFile handles GET /api/v1/records/{record}/sections/{section}/
// files/{name} (0.105.0): one attachment of a record section, when the
// extension says the signed-in reader may have it. It is streamed from the
// space the extension names, only when the file there is virtual.
func (h *httpAdapter) serveRecordFile(w http.ResponseWriter, r *http.Request, userID, record, section, name string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Unsupported method."))
		return
	}
	hooks := h.service.recordHooks()
	if hooks == nil || hooks.RecordFile == nil || userID == "" || record == "" || name == "" {
		sendError(w, missing())
		return
	}
	for _, v := range []string{record, section, name} {
		if len(v) > maxRecordField || !utf8.ValidString(v) || strings.ContainsAny(v, "/\\\x00") {
			sendError(w, missing())
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	found, err := hooks.RecordFile(ctx, extension.RecordFileRequest{UserID: userID, Record: record, Section: section, Name: name})
	if err != nil {
		var p *Error
		if errors.As(err, &p) {
			sendError(w, p)
		} else {
			log.Printf("record file: %s/%s/%s: %v", record, section, name, err)
			sendError(w, errBillingUnavailable)
		}
		return
	}
	repo := h.service.ownedRepository(found.SpaceID)
	if repo == nil || !validPath(found.Path) {
		sendError(w, missing())
		return
	}
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		sendError(w, err)
		return
	}
	f, ok := files[found.Path]
	if !ok || f.Virtual == nil {
		sendError(w, missing())
		return
	}
	v := *f.Virtual
	cancel()
	h.streamVirtual(w, r, repo, pathpkg.Base(found.Path), v, v.Type, v.image())
}

// serveRecordRoute handles /api/v1/records/... for the person signed in to
// this site: pages show record attachments from it.
func (h *httpAdapter) serveRecordRoute(w http.ResponseWriter, r *http.Request, client string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/records/"), "/")
	if h.service.accounts == nil || len(parts) != 5 || parts[1] != "sections" || parts[3] != "files" {
		sendError(w, missing())
		return
	}
	if !strings.EqualFold(r.Host, h.publicHost) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		sendError(w, problem(403, "forbidden", "Unrecognized origin."))
		return
	}
	lookup, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	user, signedIn, err := h.service.accounts.currentUser(lookup, cookieToken(r, h.sessionCookieName()))
	cancel()
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return
	}
	if !signedIn {
		sendError(w, problem(401, "unauthorized", "Sign in to see this file."))
		return
	}
	if err := h.service.rates.reserve(allowance{"records:user:" + user.ID, 600, 60}); err != nil {
		sendError(w, err)
		return
	}
	h.serveRecordFile(w, r, user.ID, parts[0], parts[2], parts[4])
}
