package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Form submissions (0.49.0). A person fills in a form template (forms.go) in
// Slate Viewer, or with mtsync submit, and sends the answers here. The server
// reads the template again, checks every answer, and files the filled-in copy
// in the folder its submitTo names, as a new file written for the person:
// they need no access to that space, which is usually a private review
// space only reviewers belong to. The account database keeps a receipt with
// the copy, so people can see what they sent.
//
// POST .../submissions          {"path", "answers", "revision"?, "blob"?, "today"?}: send a form
// GET  .../submissions          the caller's submissions of this space's forms
// GET  .../submissions?id=ID    one of them, with the copy that was filed
//
// Only the OAuth REST resource offers them, and only a person (Metatrash Sync)
// can submit. Anyone who can read the template can send it. submitTo must
// name a space with the same owner as the template's space, so a template
// can never send answers to someone else.

// Submissions per person per hour, across all spaces.
const (
	submissionsPerHour = 60
	maxSubmissionRows  = 200
)

var receiptIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (db *accountDatabase) checkSubmissionSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v13 required (form submissions); follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_form_submissions' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return bad
	}
	var columns sql.NullString
	var nonUnique, prefixes int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_form_submissions' AND index_name = 'PRIMARY'").Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != "submission_id" || nonUnique != 0 || prefixes != 0 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT submission_id, space_id, user_id, form, title, template_path, template_file_id, template_revision, target_space_id, target_path, submitted_at, text FROM metatrash_form_submissions LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	return nil
}

// Receipt is one form a person sent.
type Receipt struct {
	ID    string `json:"id"`
	Form  string `json:"form"`
	Title string `json:"title"`
	// Template is the template's path in the space; Revision the space's
	// state it was checked against.
	Template string `json:"template"`
	Revision string `json:"revision"`
	// Target is where the copy was filed: owner/slug/path.
	Target      string `json:"target"`
	SubmittedAt string `json:"submittedAt"`
	// Text is the filed copy, given for one receipt and after sending.
	Text string `json:"text,omitempty"`
}

type receiptsResult struct {
	Space       string    `json:"space"`
	Submissions []Receipt `json:"submissions"`
}

type receiptRow struct {
	Receipt
	spaceID, userID, fileID, targetSpaceID, targetPath string
	at                                                 int64
}

func (db *accountDatabase) recordSubmission(ctx context.Context, row receiptRow) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := db.db.ExecContext(ctx, "INSERT INTO metatrash_form_submissions (submission_id, space_id, user_id, form, title, template_path, template_file_id, template_revision, target_space_id, target_path, submitted_at, text) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		row.ID, row.spaceID, row.userID, row.Form, row.Title, row.Template, row.fileID, row.Revision, row.targetSpaceID, row.targetPath, row.at, row.Text)
	if err != nil {
		return fmt.Errorf("cannot record submission")
	}
	return nil
}

// submissions reads a person's receipts for one space, newest first, or the
// one with id (and its text).
func (db *accountDatabase) submissions(ctx context.Context, spaceID, userID, id string) ([]Receipt, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	text := "''"
	query := " WHERE f.space_id = ? AND f.user_id = ?"
	args := []any{spaceID, userID}
	if id != "" {
		text = "f.text"
		query += " AND f.submission_id = ?"
		args = append(args, id)
	}
	rows, err := db.db.QueryContext(ctx, "SELECT f.submission_id, f.form, f.title, f.template_path, f.template_revision, COALESCE(u.username, ''), s.slug, f.target_path, f.submitted_at, "+text+" FROM metatrash_form_submissions f JOIN metatrash_spaces s ON s.space_id = f.target_space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id"+query+fmt.Sprintf(" ORDER BY f.submitted_at DESC, f.submission_id LIMIT %d", maxSubmissionRows), args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read submissions")
	}
	defer rows.Close()
	out := []Receipt{}
	for rows.Next() {
		var r Receipt
		var owner, slug, path string
		var at int64
		if err := rows.Scan(&r.ID, &r.Form, &r.Title, &r.Template, &r.Revision, &owner, &slug, &path, &at, &r.Text); err != nil {
			return nil, fmt.Errorf("cannot read submissions")
		}
		r.Target = owner + "/" + slug + "/" + path
		r.SubmittedAt = time.Unix(at, 0).UTC().Format(time.RFC3339)
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read submissions")
	}
	return out, nil
}

// submitTarget is where a template sends its answers.
type submitTarget struct {
	repo           *repository
	spaceID, space string
	folder         string
}

// resolveSubmitTo finds submitTo's space, which must be ready and have the
// same owner as the template's space.
func (s *Service) resolveSubmitTo(ctx context.Context, submitTo, ownerID string) (submitTarget, error) {
	refused := formError("submitTo must be owner/space/folder/ naming a space with the same owner as this one, such as owner/hr-review/leave/.")
	parts := strings.SplitN(submitTo, "/", 3)
	if len(parts) < 2 || !usernamePattern.MatchString(parts[0]) || !usernamePattern.MatchString(parts[1]) {
		return submitTarget{}, refused
	}
	folder := ""
	if len(parts) == 3 && parts[2] != "" {
		folder = parts[2]
		if !strings.HasSuffix(folder, "/") || !validPath(strings.TrimSuffix(folder, "/")) || strings.Count(folder, "/") > 6 || hasDotName(folder) {
			return submitTarget{}, refused
		}
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var id, owner string
	err := s.ownedDB.db.QueryRowContext(lookup, `SELECT s.space_id, s.owner_user_id FROM metatrash_spaces s JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE u.username = ? AND s.slug = ? AND s.provisioning_state = 'ready'`, parts[0], parts[1]).Scan(&id, &owner)
	if err == sql.ErrNoRows || err == nil && owner != ownerID {
		return submitTarget{}, refused
	}
	if err != nil {
		return submitTarget{}, fmt.Errorf("cannot find the submitTo space")
	}
	repo := s.ownedRepository(id)
	if repo == nil {
		return submitTarget{}, refused
	}
	return submitTarget{repo: repo, spaceID: id, space: parts[0] + "/" + parts[1], folder: folder}, nil
}

// personName is the name a submission is filed under: the username, or the
// email address before the @, reduced to a file-safe word.
func (s *Service) personName(ctx context.Context, userID string) (string, error) {
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var username, email string
	if err := s.ownedDB.db.QueryRowContext(lookup, "SELECT COALESCE(username, ''), email FROM metatrash_users WHERE user_id = ?", userID).Scan(&username, &email); err != nil {
		return "", fmt.Errorf("cannot read the account")
	}
	if username != "" {
		return username, nil
	}
	slug, err := deriveSpaceSlug(emailName(email))
	if err != nil {
		return "person", nil
	}
	return slug, nil
}

type submitRequest struct {
	Path     string `json:"path"`
	Revision string `json:"revision,omitempty"`
	// Blob is the Git blob hash of the template text the answers were given
	// to (mtsync sends its synced copy's); like revision, a template that
	// changed since is refused.
	Blob    string                     `json:"blob,omitempty"`
	Answers map[string]json.RawMessage `json:"answers"`
	// Today is the person's local date, for date limits given as today; the
	// server's UTC date is used when it is missing or more than a day away.
	Today string `json:"today,omitempty"`
}

// submitForm checks a submission of the form at in.Path and files it.
func (s *Service) submitForm(ctx context.Context, repo *repository, space, userID string, in submitRequest) (Receipt, error) {
	if !validPath(in.Path) || !strings.HasSuffix(strings.ToLower(in.Path), ".md") {
		return Receipt{}, invalid("path must be a form template (.md) in this space.")
	}
	state, files, err := repo.snapshot(ctx, "")
	if err != nil {
		return Receipt{}, err
	}
	f, ok := files[in.Path]
	if !ok {
		return Receipt{}, missing()
	}
	if in.Revision != "" && in.Revision != state {
		// The person filled in the template as it was then; refuse if it
		// changed since, so they can check the changed fields.
		_, old, err := repo.snapshot(ctx, in.Revision)
		if err != nil {
			return Receipt{}, invalid("revision is not an earlier state of this space.")
		}
		if was, ok := old[in.Path]; !ok || was.ID != f.ID || was.Blob != f.Blob {
			return Receipt{}, &Error{Status: 409, Code: "template_changed", Message: "The form changed after you started filling it in. Check your answers against the new version, then send it again.", CurrentState: state}
		}
	}
	if in.Blob != "" && in.Blob != f.Blob {
		return Receipt{}, &Error{Status: 409, Code: "template_changed", Message: "The form changed after you started filling it in, or your copy is not synced yet. Check your answers against the new version, then send it again.", CurrentState: state}
	}
	b, err := repo.blob(ctx, f.Blob)
	if err != nil {
		return Receipt{}, err
	}
	t, err := parseFormTemplate(string(b))
	if err != nil {
		return Receipt{}, err
	}
	now := s.now().UTC()
	today := now.Format("2006-01-02")
	if d, err := time.Parse("2006-01-02", in.Today); err == nil && validDate(in.Today) && d.Sub(now.Truncate(24*time.Hour)).Abs() <= 24*time.Hour {
		today = in.Today
	}
	answers, problems := t.checkAnswers(in.Answers, today)
	if len(problems) > 0 {
		e := invalid("Some answers need fixing.")
		e.Code, e.Fields = "invalid_answers", problems
		return Receipt{}, e
	}
	target, err := s.resolveSubmitTo(ctx, t.SubmitTo, s.repositoryOwner(repo))
	if err != nil {
		return Receipt{}, err
	}
	who, err := s.personName(ctx, userID)
	if err != nil {
		return Receipt{}, err
	}
	_, slug, _ := strings.Cut(space, "/")
	text := t.render(answers, submission{Template: space + "/" + in.Path, Revision: state, By: who, At: now})
	if int64(len(text)) > target.repo.limits.MaxFileBytes {
		return Receipt{}, problem(413, "payload_too_large", "The filled-in form is larger than a file can be.")
	}
	base := target.folder + slug + "-" + who + "-" + t.Form + "-" + now.Format("2006-01-02T1504Z")
	value, err := s.queued(ctx, func() (any, error) {
		return fileSubmission(ctx, target.repo, base, t.Form, text)
	})
	if err != nil {
		return Receipt{}, err
	}
	path := value.(string)
	id, err := randomHex(16)
	if err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{ID: id, Form: t.Form, Title: t.Title, Template: in.Path, Revision: state, Target: target.space + "/" + path, SubmittedAt: now.Format(time.RFC3339), Text: text}
	if err := s.ownedDB.recordSubmission(ctx, receiptRow{Receipt: receipt, spaceID: repo.id, userID: userID, fileID: f.ID, targetSpaceID: target.spaceID, targetPath: path, at: now.Unix()}); err != nil {
		// The copy is filed, which is what counts; only the receipt is missing.
		log.Printf("form submission %s filed at %s without a receipt: %v", id, receipt.Target, err)
		receipt.ID = ""
	}
	return receipt, nil
}

// fileSubmission writes the copy as a new file named base.md, or base-2.md
// and so on when that name is taken. Runs on the write queue.
func fileSubmission(ctx context.Context, r *repository, base, form, text string) (string, error) {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return "", err
	}
	path := ""
	for n := 1; n <= 50 && path == ""; n++ {
		candidate := base + ".md"
		if n > 1 {
			candidate = base + "-" + strconv.Itoa(n) + ".md"
		}
		if _, taken := files[candidate]; !taken {
			path = candidate
		}
	}
	if path == "" || !validPath(path) {
		return "", problem(409, "conflict", "Too many submissions of this form this minute; try again shortly.")
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return "", err
	}
	if githubFolderAround(github, fileFolder(path)) != "" {
		return "", formError("submitTo is inside a GitHub folder; send forms to an ordinary folder.")
	}
	readOnly, err := r.readOnlyRules(ctx, files, nil)
	if err != nil {
		return "", err
	}
	if level := fileLock(readOnly, path); blocks(level, true) {
		return "", readOnlyFile(path, level)
	}
	for p := range files {
		if strings.HasPrefix(path, p+"/") {
			return "", formError("submitTo names a folder where " + p + " is a file.")
		}
	}
	id, err := randomHex(16)
	if err != nil {
		return "", err
	}
	files[path] = record{File: File{ID: id, Path: path, Bytes: int64(len(text)), Deletable: true}}
	texts := map[string]string{path: text}
	added, err := addFolderFiles(files, texts, github, r.rootAgents, path)
	if err != nil {
		return "", err
	}
	changes := append([]commitChange{{id, "create", path}}, added...)
	if _, err := r.commitFiles(ctx, head, files, texts, nil, changes, "Form submission: "+form); err != nil {
		return "", err
	}
	return path, nil
}

// serveSubmissions handles the submissions resource after access was checked
// for reading.
func (h *httpAdapter) serveSubmissions(w http.ResponseWriter, r *http.Request, repo *repository, space string) {
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	if identity.oauth == nil || repo.id == "" || h.service.ownedDB == nil {
		sendError(w, problem(404, "not_found", "Forms work only in your own and shared spaces."))
		return
	}
	userID := identity.oauth.UserID
	if r.Method == http.MethodGet {
		id := ""
		if r.URL.RawQuery != "" {
			q, err := url.ParseQuery(r.URL.RawQuery)
			if err != nil || len(q) != 1 || len(q["id"]) != 1 || !receiptIDPattern.MatchString(q["id"][0]) {
				sendError(w, invalid("Unknown query parameter; the only one is id=ID."))
				return
			}
			id = q["id"][0]
		}
		rows, err := h.service.ownedDB.submissions(r.Context(), repo.id, userID, id)
		if err != nil {
			sendError(w, err)
			return
		}
		if id != "" {
			if len(rows) == 0 {
				sendError(w, missing())
				return
			}
			sendJSON(w, 200, rows[0])
			return
		}
		sendJSON(w, 200, receiptsResult{Space: space, Submissions: rows})
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	if !personWrite(r.Context()) {
		sendError(w, problem(403, "forbidden", "Only a person can send a form, in Slate Viewer or with mtsync submit; agents cannot."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFormAnswerBytes+4096))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			sendError(w, problem(413, "payload_too_large", "The answers are larger than a form allows."))
		} else {
			sendError(w, invalid("Could not read body."))
		}
		return
	}
	var body submitRequest
	if err := strictJSON(b, &body); err != nil || body.Path == "" || body.Answers == nil || body.Revision != "" && !hashPattern.MatchString(body.Revision) || body.Blob != "" && !hashPattern.MatchString(body.Blob) {
		sendError(w, invalid(`Send {"path": "...", "answers": {"field": "answer", ...}, "revision": "..."}: the form, your answers by field name, and the space state you filled it in from.`))
		return
	}
	if err := h.service.rates.reserve(allowance{"submit:" + userID, submissionsPerHour, 3600}); err != nil {
		sendError(w, err)
		return
	}
	value, err := h.service.submitForm(r.Context(), repo, space, userID, body)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 201, value)
}
