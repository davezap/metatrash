package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

// Dashboard requests (0.99.0):
//
// POST .../query    a card's query (QueryInput); a read
// GET  .../notices?paths=a.md,b.md
//                   whether the caller has done each file a dashboard shows
//                   as a card: acknowledged a notice (a file with no form
//                   fields), or sent a form, for the file's text now
// POST .../notices  {"path", "blob"}: the caller acknowledges a notice
//
// Notices are kept with acknowledgements (one row per person, file and
// text), so a changed notice asks again. Only the account and site
// resources offer notices; query works wherever reading does.

const maxNoticePaths = 50

func readJSONBody(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return problem(415, "invalid_request", "Use application/json.")
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return problem(413, "payload_too_large", "Request exceeds the byte limit.")
		}
		return invalid("Could not read body.")
	}
	if !validUnicodeJSON(b) || noDuplicateKeys(b) != nil || strictJSON(b, v) != nil {
		return errBadBody
	}
	return nil
}

var errBadBody = invalid("Invalid body.")

// requestUsername is the signed-in person's username, for @me; empty for a
// space key or a visitor.
func (h *httpAdapter) requestUsername(ctx context.Context) string {
	identity, _ := ctx.Value(mcpIdentityKey{}).(mcpIdentity)
	if identity.oauth == nil || h.service.ownedDB == nil {
		return ""
	}
	who, _, err := h.service.personDetails(ctx, identity.oauth.UserID)
	if err != nil {
		return ""
	}
	return who
}

func (h *httpAdapter) serveQuery(w http.ResponseWriter, r *http.Request, repo *repository, space string) {
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	var in QueryInput
	if err := readJSONBody(w, r, 8192, &in); err != nil {
		if err == errBadBody {
			err = invalid(`Send {"source": "jobs/", "where": "state!=done", "group": "type", ...}: a dashboard card's query (docs/dashboards.md).`)
		}
		sendError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	value, err := h.service.query(ctx, repo, space, h.requestUsername(ctx), in)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, value)
}

// NoticeStatus says whether the caller has done one file a dashboard shows.
type NoticeStatus struct {
	Path string `json:"path"`
	Blob string `json:"blob,omitempty"`
	// Kind is notice (no form fields: acknowledged) or form (sent).
	Kind string `json:"kind"`
	Done bool   `json:"done"`
	At   string `json:"at,omitempty"`
}

type noticesResult struct {
	Space   string         `json:"space"`
	Notices []NoticeStatus `json:"notices"`
}

func (h *httpAdapter) serveNotices(w http.ResponseWriter, r *http.Request, repo *repository, space string) {
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	if repo.id == "" || h.service.ownedDB == nil {
		sendError(w, problem(404, "not_found", "Notices are kept only for your own and shared spaces."))
		return
	}
	// A visitor to a space shared on the web (signed out) sees every notice
	// and form as not done yet (0.99.2); acknowledging needs signing in.
	userID := ""
	if identity.oauth != nil {
		userID = identity.oauth.UserID
	} else if r.Method != http.MethodGet {
		sendError(w, problem(401, "unauthorized", "Sign in to acknowledge this notice."))
		return
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		for k := range q {
			if k != "paths" {
				sendError(w, invalid("Unknown query parameter; the only one is paths."))
				return
			}
		}
		var paths []string
		for _, p := range strings.Split(q.Get("paths"), ",") {
			if p = strings.TrimSpace(p); p != "" {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 || len(paths) > maxNoticePaths {
			sendError(w, invalid(fmt.Sprintf("paths lists 1 to %d files, separated by commas.", maxNoticePaths)))
			return
		}
		value, err := h.service.noticeStatus(r.Context(), repo, space, userID, paths)
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, 200, value)
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	if !personWrite(r.Context()) {
		sendError(w, problem(403, "forbidden", "Only a person can acknowledge a notice; agents cannot."))
		return
	}
	var body struct {
		Path string `json:"path"`
		Blob string `json:"blob"`
	}
	if err := readJSONBody(w, r, 4096, &body); err != nil || body.Path == "" || !hashPattern.MatchString(body.Blob) {
		if err == nil || err == errBadBody {
			err = invalid(`Send {"path": "...", "blob": "..."}: the notice and the blob of the text you saw.`)
		}
		sendError(w, err)
		return
	}
	value, err := h.service.acknowledgeNotice(r.Context(), repo, space, userID, body.Path, body.Blob)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, value)
}

// noticeKind reads a file as a dashboard shows it: a form (it has fields)
// or a notice (no fields: the button acknowledges it).
func noticeKind(text string) string {
	_, body := splitFrontMatter(text)
	fields := 0
	scanFields(body, func(int, int, int, string, string) { fields++ })
	if fields > 0 {
		return "form"
	}
	return "notice"
}

// acknowledgeNotice records that the person acknowledged path's text now.
// A file its folder's acknowledge service tracks is confirmed as a policy
// instead; a form is sent.
func (s *Service) acknowledgeNotice(ctx context.Context, r *repository, space, userID, path, blob string) (NoticeStatus, error) {
	if !validPath(path) || !strings.HasSuffix(strings.ToLower(path), ".md") {
		return NoticeStatus{}, invalid("A notice is a Markdown file in this space.")
	}
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return NoticeStatus{}, err
	}
	f, ok := files[path]
	if !ok || f.Virtual != nil {
		return NoticeStatus{}, missing()
	}
	if f.Blob != blob {
		return NoticeStatus{}, problem(409, "conflict", path+" changed since you saw it; read it again, then acknowledge it.")
	}
	b, err := r.blob(ctx, f.Blob)
	if err != nil {
		return NoticeStatus{}, err
	}
	if noticeKind(string(b)) == "form" {
		return NoticeStatus{}, problem(409, "form_required", path+" is a form: send it instead of acknowledging it.")
	}
	folders, err := r.publishedFolders(ctx, state, files)
	if err != nil {
		return NoticeStatus{}, err
	}
	for _, folder := range folders {
		for _, pf := range folder.Files {
			if pf.Path == path && pf.Tracked {
				return NoticeStatus{}, problem(409, "policy", path+" is a file its folder asks people to read: confirm it with I Have Read This (acknowledgements).")
			}
		}
	}
	now := s.now().UTC()
	// Version 1: a notice has no published versions; its text (blob) is what
	// was acknowledged.
	if err := s.ownedDB.recordAcknowledgement(ctx, r.id, ackRow{UserID: userID, FileID: f.ID, Blob: f.Blob, Path: path, Version: 1, At: now.Unix()}); err != nil {
		return NoticeStatus{}, err
	}
	return NoticeStatus{Path: path, Blob: f.Blob, Kind: "notice", Done: true, At: now.Format(time.RFC3339)}, nil
}

// noticeStatus answers whether the person has done each file: acknowledged
// a notice's text, or sent a form whose text is the one now.
func (s *Service) noticeStatus(ctx context.Context, r *repository, space, userID string, paths []string) (noticesResult, error) {
	_, files, err := r.snapshot(ctx, "")
	if err != nil {
		return noticesResult{}, err
	}
	// userID is empty for a visitor: nothing is done, nothing to look up.
	acked := map[string]int64{}
	if userID != "" {
		acks, err := s.ownedDB.acknowledgements(ctx, r.id, userID)
		if err != nil {
			return noticesResult{}, err
		}
		for _, a := range acks {
			acked[a.FileID+":"+a.Blob] = a.At
		}
	}
	var formIDs []string
	out := noticesResult{Space: space, Notices: []NoticeStatus{}}
	for _, p := range paths {
		f, ok := files[p]
		if !validPath(p) || !ok || f.Virtual != nil {
			out.Notices = append(out.Notices, NoticeStatus{Path: p, Kind: "missing"})
			continue
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return noticesResult{}, err
		}
		st := NoticeStatus{Path: p, Blob: f.Blob, Kind: noticeKind(string(b))}
		if st.Kind == "notice" {
			if at, ok := acked[f.ID+":"+f.Blob]; ok {
				st.Done, st.At = true, time.Unix(at, 0).UTC().Format(time.RFC3339)
			}
		} else {
			formIDs = append(formIDs, f.ID)
		}
		out.Notices = append(out.Notices, st)
	}
	if len(formIDs) > 0 && userID != "" {
		sent, err := s.ownedDB.sentTemplates(ctx, r.id, userID, formIDs)
		if err != nil {
			return noticesResult{}, err
		}
		for i, st := range out.Notices {
			if st.Kind != "form" {
				continue
			}
			if at, ok := sent[files[st.Path].ID+":"+st.Blob]; ok {
				out.Notices[i].Done, out.Notices[i].At = true, time.Unix(at, 0).UTC().Format(time.RFC3339)
			}
		}
	}
	return out, nil
}

// sentTemplates is when the person last sent each of these forms, by
// "fileID:templateBlob". A copy sent back and sent again counts once; one
// waiting for more information still counts as sent.
func (db *accountDatabase) sentTemplates(ctx context.Context, spaceID, userID string, fileIDs []string) (map[string]int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	marks := strings.TrimSuffix(strings.Repeat("?,", len(fileIDs)), ",")
	args := []any{spaceID, userID}
	for _, id := range fileIDs {
		args = append(args, id)
	}
	rows, err := db.db.QueryContext(ctx, "SELECT template_file_id, template_blob, MAX(submitted_at) FROM metatrash_form_submissions WHERE space_id = ? AND user_id = ? AND template_file_id IN ("+marks+") GROUP BY template_file_id, template_blob", args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read submissions")
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id, blob string
		var at int64
		if err := rows.Scan(&id, &blob, &at); err != nil {
			return nil, fmt.Errorf("cannot read submissions")
		}
		out[id+":"+blob] = at
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read submissions")
	}
	return out, nil
}
