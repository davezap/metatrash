package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
// POST .../submissions          {"path", "answers", "revision"?, "blob"?, "today"?, "now"?}: send a form
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
	bad := fmt.Errorf("account schema v14 required (form submissions and reviews); follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_form_submissions' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return bad
	}
	var columns sql.NullString
	var nonUnique, prefixes int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_form_submissions' AND index_name = 'PRIMARY'").Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != "submission_id" || nonUnique != 0 || prefixes != 0 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT submission_id, space_id, user_id, form, title, template_path, template_file_id, template_revision, template_blob, target_space_id, target_path, target_file_id, submitted_at, text, quiz, status, score, answers, marks, reviewed_by, reviewed_at, note FROM metatrash_form_submissions LIMIT 0")
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
	// Acknowledged is the published version of the template's folder that
	// sending it confirmed reading, when the folder asks people to read it.
	// Only in the answer to sending.
	Acknowledged int `json:"acknowledged,omitempty"`
	// Status is submitted (waiting for review), approved or declined
	// (0.50.0), needs-info (the reviewer's note asks for more; send the form
	// again with resends) or resent (it was sent again) (0.95.10); Note and
	// ReviewedAt come from the reviewer.
	Status string `json:"status"`
	// Resends is the needs-info copy this one replaced; only in the answer
	// to sending (0.95.10).
	Resends    string `json:"resends,omitempty"`
	Note       string `json:"note,omitempty"`
	ReviewedAt string `json:"reviewedAt,omitempty"`
	// Quiz copies have a score once marked and a tick or cross per marked
	// answer (null while a reviewer still has to tick it); never the key.
	Quiz     bool             `json:"quiz,omitempty"`
	PassMark int              `json:"passMark,omitempty"`
	Score    *int             `json:"score,omitempty"`
	Marks    map[string]*bool `json:"marks,omitempty"`
	// Answers are the answers as sent, given with Text, so a failed quiz
	// can be fixed and sent again.
	Answers map[string]any `json:"answers,omitempty"`
}

type receiptsResult struct {
	Space       string    `json:"space"`
	Submissions []Receipt `json:"submissions"`
}

type receiptRow struct {
	Receipt
	spaceID, userID, fileID, blob, targetSpaceID, targetPath, targetFileID string
	at                                                                     int64
}

func (db *accountDatabase) recordSubmission(ctx context.Context, row receiptRow) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	answers, marks, err := receiptJSON(row.Answers, row.Marks)
	if err != nil {
		return err
	}
	_, err = db.db.ExecContext(ctx, "INSERT INTO metatrash_form_submissions (submission_id, space_id, user_id, form, title, template_path, template_file_id, template_revision, template_blob, target_space_id, target_path, target_file_id, submitted_at, text, quiz, status, score, answers, marks) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		row.ID, row.spaceID, row.userID, row.Form, row.Title, row.Template, row.fileID, row.Revision, row.blob, row.targetSpaceID, row.targetPath, row.targetFileID, row.at, row.Text, row.Quiz, row.Status, row.Score, answers, marks)
	if err != nil {
		return fmt.Errorf("cannot record submission")
	}
	return nil
}

// receiptJSON encodes answers and marks for the database; marks stay NULL
// for forms that are not quizzes.
func receiptJSON(answers map[string]any, marks map[string]*bool) (string, any, error) {
	a, err := marshalPlain(answers)
	if err != nil {
		return "", nil, fmt.Errorf("cannot record submission")
	}
	if marks == nil {
		return string(a), nil, nil
	}
	m, err := json.Marshal(marks)
	if err != nil {
		return "", nil, fmt.Errorf("cannot record submission")
	}
	return string(a), string(m), nil
}

// submissions reads a person's receipts for one space, newest first, or the
// one with id (and its text).
func (db *accountDatabase) submissions(ctx context.Context, spaceID, userID, id string) ([]Receipt, error) {
	rows, err := db.submissionRows(ctx, spaceID, userID, id)
	if err != nil {
		return nil, err
	}
	out := make([]Receipt, len(rows))
	for i, r := range rows {
		out[i] = r.Receipt
	}
	return out, nil
}

// submissionRow is a receipt with what the account-wide list needs: the
// template's space (space, its owner/slug in spaceName) and the template
// file and text it was filled in from.
type submissionRow struct {
	Receipt
	spaceID, spaceName, fileID, blob string
}

// submissionRows reads receipts as submissions does; with no spaceID, from
// every space (0.95.22).
func (db *accountDatabase) submissionRows(ctx context.Context, spaceID, userID, id string) ([]submissionRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	text, answers := "''", "NULL"
	query := " WHERE f.user_id = ?"
	args := []any{userID}
	if spaceID != "" {
		query += " AND f.space_id = ?"
		args = append(args, spaceID)
	}
	if id != "" {
		text, answers = "f.text", "f.answers"
		query += " AND f.submission_id = ?"
		args = append(args, id)
	}
	rows, err := db.db.QueryContext(ctx, "SELECT f.submission_id, f.form, f.title, f.template_path, f.template_revision, COALESCE(u.username, ''), s.slug, f.target_path, f.submitted_at, "+text+", f.quiz, f.status, f.score, f.marks, COALESCE(f.reviewed_at, 0), COALESCE(f.note, ''), "+answers+", f.space_id, COALESCE(tu.username, ''), COALESCE(ts.slug, ''), f.template_file_id, f.template_blob FROM metatrash_form_submissions f JOIN metatrash_spaces s ON s.space_id = f.target_space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id LEFT JOIN metatrash_spaces ts ON ts.space_id = f.space_id LEFT JOIN metatrash_users tu ON tu.user_id = ts.owner_user_id"+query+fmt.Sprintf(" ORDER BY f.submitted_at DESC, f.submission_id LIMIT %d", maxSubmissionRows), args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read submissions")
	}
	defer rows.Close()
	out := []submissionRow{}
	for rows.Next() {
		var row submissionRow
		r := &row.Receipt
		var owner, slug, path, templateOwner, templateSlug string
		var at, reviewedAt int64
		var marks, answers sql.NullString
		if err := rows.Scan(&r.ID, &r.Form, &r.Title, &r.Template, &r.Revision, &owner, &slug, &path, &at, &r.Text, &r.Quiz, &r.Status, &r.Score, &marks, &reviewedAt, &r.Note, &answers, &row.spaceID, &templateOwner, &templateSlug, &row.fileID, &row.blob); err != nil {
			return nil, fmt.Errorf("cannot read submissions")
		}
		if templateOwner != "" && templateSlug != "" {
			row.spaceName = templateOwner + "/" + templateSlug
		}
		r.Target = owner + "/" + slug + "/" + path
		r.SubmittedAt = time.Unix(at, 0).UTC().Format(time.RFC3339)
		if reviewedAt > 0 {
			r.ReviewedAt = time.Unix(reviewedAt, 0).UTC().Format(time.RFC3339)
		}
		if marks.Valid {
			_ = json.Unmarshal([]byte(marks.String), &r.Marks)
		}
		if answers.Valid {
			_ = json.Unmarshal([]byte(answers.String), &r.Answers)
		}
		if r.Quiz {
			if keys, _ := splitFrontMatter(r.Text); keys != nil {
				r.PassMark = intAttr(keys["passMark"])
			}
		}
		out = append(out, row)
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
	name, _, err := s.personDetails(ctx, userID)
	return name, err
}

// personDetails is the person's name, as personName, and email address.
func (s *Service) personDetails(ctx context.Context, userID string) (string, string, error) {
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var username, email string
	if err := s.ownedDB.db.QueryRowContext(lookup, "SELECT COALESCE(username, ''), email FROM metatrash_users WHERE user_id = ?", userID).Scan(&username, &email); err != nil {
		return "", "", fmt.Errorf("cannot read the account")
	}
	if username != "" {
		return username, email, nil
	}
	slug, err := deriveSpaceSlug(emailName(email))
	if err != nil {
		return "person", email, nil
	}
	return slug, email, nil
}

type submitRequest struct {
	Path string `json:"path"`
	// Version is the published version of the form's folder the answers
	// were given to (Slate Viewer shows published forms); 0 when not known.
	Version  int    `json:"version,omitempty"`
	Revision string `json:"revision,omitempty"`
	// Blob is the Git blob hash of the template text the answers were given
	// to (mtsync sends its synced copy's); like revision, a template that
	// changed since is refused.
	Blob    string                     `json:"blob,omitempty"`
	Answers map[string]json.RawMessage `json:"answers"`
	// Today is the person's local date, for date limits given as today; the
	// server's UTC date is used when it is missing or more than a day away.
	Today string `json:"today,omitempty"`
	// Now is the person's local time, HH:MM, for answers left out whose
	// default is @now (0.95.19); the space's time zone is used without it.
	Now string `json:"now,omitempty"`
	// Resends is the receipt ID of the person's copy of this form that a
	// reviewer marked needs-info (0.95.10). The new copy names it, and it
	// becomes resent.
	Resends string `json:"resends,omitempty"`
}

// resendRow is a needs-info copy being sent again.
type resendRow struct {
	targetSpaceID, targetFileID string
}

// claimResend marks the person's needs-info copy id of the form fileID as
// resent, so it is sent again only once. found is false when there is no
// such copy, and claimed false when it is not needs-info (status says what
// it is).
func (db *accountDatabase) claimResend(ctx context.Context, id, spaceID, userID, fileID string) (row resendRow, status string, found, claimed bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var quiz bool
	err = db.db.QueryRowContext(ctx, "SELECT target_space_id, target_file_id, status, quiz FROM metatrash_form_submissions WHERE submission_id = ? AND space_id = ? AND user_id = ? AND template_file_id = ?", id, spaceID, userID, fileID).
		Scan(&row.targetSpaceID, &row.targetFileID, &status, &quiz)
	if err == sql.ErrNoRows || err == nil && quiz {
		return row, "", false, false, nil
	}
	if err != nil {
		return row, "", false, false, fmt.Errorf("cannot read submissions")
	}
	if status != "needs-info" {
		return row, status, true, false, nil
	}
	res, err := db.db.ExecContext(ctx, "UPDATE metatrash_form_submissions SET status = 'resent' WHERE submission_id = ? AND status = 'needs-info'", id)
	if err != nil {
		return row, status, true, false, fmt.Errorf("cannot record submission")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		// A reviewer or another send got there first.
		return row, "changed since", true, false, nil
	}
	return row, "resent", true, true, nil
}

// releaseResend puts a claimed copy back to needs-info when sending it
// again failed.
func (db *accountDatabase) releaseResend(ctx context.Context, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_form_submissions SET status = 'needs-info' WHERE submission_id = ? AND status = 'resent'", id); err != nil {
		log.Printf("form submission %s stays resent after sending it again failed: %v", id, err)
	}
}

// resentText records the old copy's text after its file was marked resent.
func (db *accountDatabase) resentText(ctx context.Context, id, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_form_submissions SET text = ? WHERE submission_id = ?", text, id); err != nil {
		return fmt.Errorf("cannot record submission")
	}
	return nil
}

// markResent is a filed copy's text with its status changed from needs-info
// to resent and the new copy's receipt named as resentAs, or "" when the
// text is not a needs-info copy.
func markResent(text, newID string) string {
	keys, _ := splitFrontMatter(text)
	if keys == nil || keys["status"] != "needs-info" {
		return ""
	}
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines[1:] {
		trimmed := strings.TrimRight(line, " \t\r\n")
		if trimmed == "---" || trimmed == "..." {
			break
		}
		if trimmed == "status: needs-info" {
			end := line[len(strings.TrimRight(line, "\r\n")):]
			if end == "" {
				end = "\n"
			}
			lines[i+1] = "status: resent" + end + "resentAs: " + newID + end
			return strings.Join(lines, "")
		}
	}
	return ""
}

// formNow is the sender's local time for @now defaults: the one they sent,
// or the time in the space's zone (UTC when it has none).
func (s *Service) formNow(ctx context.Context, spaceID, sent string, now time.Time) string {
	if timePattern.MatchString(sent) {
		return sent
	}
	if name := s.dbSpaceZone(ctx, spaceID); name != "" {
		if zone, err := time.LoadLocation(name); err == nil {
			return now.In(zone).Format("15:04")
		}
	}
	return now.UTC().Format("15:04")
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
	// In a published folder the form that counts is its published text,
	// which is what people are shown (0.50.0).
	folders, err := repo.publishedFolders(ctx, state, files)
	if err != nil {
		return Receipt{}, err
	}
	var folder *policyFolder
	var published policyFile
	for i := range folders {
		for _, pf := range folders[i].Files {
			if pf.Path == in.Path {
				folder, published = &folders[i], pf
			}
		}
	}
	blob, revision := f.Blob, state
	if folder != nil {
		blob, revision = published.Blob, folder.Revision
		if in.Version != 0 && in.Version != folder.Version {
			return Receipt{}, &Error{Status: 409, Code: "template_changed", Message: fmt.Sprintf("%s is now at version %d, not %d. Check your answers against the new version, then send it again.", folder.Folder, folder.Version, in.Version), CurrentState: state}
		}
		if in.Blob != "" && in.Blob != blob {
			return Receipt{}, &Error{Status: 409, Code: "template_changed", Message: fmt.Sprintf("This form is published as version %d of %s, which is not the text you filled in. Send the published version.", folder.Version, folder.Folder), CurrentState: state}
		}
	} else {
		if in.Version != 0 {
			return Receipt{}, &Error{Status: 409, Code: "template_changed", Message: "This form is not in a published version of its folder any more.", CurrentState: state}
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
	}
	b, err := repo.blob(ctx, blob)
	if err != nil {
		return Receipt{}, err
	}
	t, err := parseFormTemplate(string(b))
	if err != nil {
		return Receipt{}, err
	}
	if err := repo.checkFormLock(ctx, files, in.Path, t.Quiz, writerOf(ctx)); err != nil {
		return Receipt{}, err
	}
	// A signature is the person's own: an agent never signs (0.95.12).
	if submittedVia(ctx) != "" {
		for _, f := range t.Fields {
			if f.Type == "sign" {
				return Receipt{}, problem(403, "person_must_sign", in.Path+" asks for a signature, and only a person can sign a form: send it in Slate Viewer or with mtsync submit.")
			}
		}
	}
	if t.Quiz {
		if folder == nil {
			return Receipt{}, problem(409, "not_published", "A quiz can be taken once its folder publishes it; publish a new version first.")
		}
		if published.Quiz == nil {
			return Receipt{}, formError("a quiz goes in a folder whose acknowledge service asks people to read its files (Ask everyone to confirm they've read the published files, in Slate Viewer), and that takes in the quiz.")
		}
		if t.ReadFirst {
			if err := s.readFirst(ctx, repo, userID, folders, published.Covers); err != nil {
				return Receipt{}, err
			}
		}
	}
	now := s.now().UTC()
	today := now.Format("2006-01-02")
	if d, err := time.Parse("2006-01-02", in.Today); err == nil && validDate(in.Today) && d.Sub(now.Truncate(24*time.Hour)).Abs() <= 24*time.Hour {
		today = in.Today
	}
	// Photos may be large; the other answers keep the old limit.
	answerBytes := 0
	for _, field := range t.Fields {
		if field.Type != "photo" {
			answerBytes += len(in.Answers[field.Name])
		}
	}
	if answerBytes > maxFormAnswerBytes {
		return Receipt{}, problem(413, "payload_too_large", "The answers are larger than a form allows.")
	}
	who, email, err := s.personDetails(ctx, userID)
	if err != nil {
		return Receipt{}, err
	}
	answers, problems := t.checkAnswersFor(in.Answers, formContext{Today: today, Now: s.formNow(ctx, repo.id, in.Now, now), Name: who, Email: email})
	if len(problems) == 0 {
		// Photos given as paths of pictures in this space (0.95.19).
		problems = s.readSpacePhotos(ctx, repo, files, t, answers)
	}
	if len(problems) > 0 {
		e := invalid("Some answers need fixing.")
		e.Code, e.Fields = "invalid_answers", problems
		return Receipt{}, e
	}
	target, err := s.resolveSubmitTo(ctx, t.SubmitTo, s.repositoryOwner(repo))
	if err != nil {
		return Receipt{}, err
	}
	_, slug, _ := strings.Cut(space, "/")
	sub := submission{Template: space + "/" + in.Path, Revision: revision, Blob: blob, By: who, Via: submittedVia(ctx), At: now, Status: "submitted"}
	if in.Resends != "" && t.Quiz {
		return Receipt{}, invalid("A quiz is never sent back for more information; send it again without resends.")
	}
	if t.Quiz {
		if err := s.quizAllowed(ctx, repo.id, userID, f.ID, blob, t, now); err != nil {
			return Receipt{}, err
		}
		key, err := s.quizKey(ctx, target, slug, t, blob)
		if err != nil {
			return Receipt{}, err
		}
		if key != nil {
			sub.Marks = t.mark(answers, key)
			sub.Status, sub.Score = t.quizStatus(sub.Marks)
		} else if reviewer, err := s.ownedDB.canWriteSpace(ctx, target.spaceID, userID); err != nil {
			return Receipt{}, err
		} else if !reviewer {
			// Only a reviewer may send it before there is a key, to make one.
			return Receipt{}, problem(409, "no_answer_key", "This quiz is not ready yet: it has no answers for this version. Ask whoever looks after it.")
		}
	}
	if sub.ID, err = randomHex(16); err != nil {
		return Receipt{}, err
	}
	sub.Resends = in.Resends
	photos, err := s.storeFormPhotos(ctx, target, t, answers, sub.ID)
	if err != nil {
		return Receipt{}, err
	}
	text := t.render(answers, sub)
	if int64(len(text)) > target.repo.limits.MaxFileBytes {
		return Receipt{}, problem(413, "payload_too_large", "The filled-in form is larger than a file can be.")
	}
	// Sending a needs-info copy again (0.95.10): claim it first, so it is
	// sent again once, and mark its file resent in the same commit as the
	// new copy when both are in the same space.
	var old *resentCopy
	if in.Resends != "" {
		row, status, found, claimed, err := s.ownedDB.claimResend(ctx, in.Resends, repo.id, userID, f.ID)
		if err != nil {
			return Receipt{}, err
		}
		if !found {
			return Receipt{}, invalid("resends names none of your copies of this form.")
		}
		if !claimed {
			return Receipt{}, problem(409, "not_needs_info", "That copy is not waiting for more information any more (it is "+status+"); send the form without resends.")
		}
		if row.targetSpaceID == target.spaceID {
			old = &resentCopy{fileID: row.targetFileID, newID: sub.ID}
		}
	}
	base := target.folder + slug + "-" + who + "-" + t.Form + "-" + now.Format("2006-01-02T1504Z")
	value, err := s.queued(ctx, target.repo, func() (any, error) {
		return fileSubmission(ctx, target.repo, base, t.Form, text, old, photos)
	})
	if err != nil {
		if in.Resends != "" {
			s.ownedDB.releaseResend(ctx, in.Resends)
		}
		return Receipt{}, err
	}
	filed := value.(filedFile)
	if old != nil && old.text != "" {
		if err := s.ownedDB.resentText(ctx, in.Resends, old.text); err != nil {
			log.Printf("form submission %s marked resent in its file but not its receipt text: %v", in.Resends, err)
		}
	}
	receipt := Receipt{ID: sub.ID, Form: t.Form, Title: t.Title, Template: in.Path, Revision: revision, Target: target.space + "/" + filed.path, SubmittedAt: now.Format(time.RFC3339), Text: text, Status: sub.Status, Quiz: t.Quiz, Score: sub.Score, Marks: sub.Marks, Answers: answers, Resends: in.Resends}
	if t.Quiz {
		receipt.PassMark = t.PassMark
	}
	if err := s.ownedDB.recordSubmission(ctx, receiptRow{Receipt: receipt, spaceID: repo.id, userID: userID, fileID: f.ID, blob: blob, targetSpaceID: target.spaceID, targetPath: filed.path, targetFileID: filed.id, at: now.Unix()}); err != nil {
		// The copy is filed, which is what counts; only the receipt is missing.
		log.Printf("form submission %s filed at %s without a receipt: %v", sub.ID, receipt.Target, err)
		receipt.ID = ""
	}
	// Sending a form shows it was read: in a folder that asks people to read
	// its files, it counts as confirming the published version. A quiz is
	// not read but passed (its receipt says how it went). An agent sending a
	// form confirms nothing: reading is the person's own (0.95.12).
	if !t.Quiz && sub.Via == "" {
		if version, err := s.acknowledgeSent(ctx, repo, state, files, in.Path, userID, now); err != nil {
			log.Printf("form submission %s filed without confirming %s as read: %v", receipt.ID, in.Path, err)
		} else {
			receipt.Acknowledged = version
		}
	}
	return receipt, nil
}

// readFirst refuses a quiz while the person has not confirmed reading every
// file it covers, as published now (readFirst: true, 0.51.0).
func (s *Service) readFirst(ctx context.Context, repo *repository, userID string, folders []policyFolder, covers []string) error {
	if len(covers) == 0 {
		return nil
	}
	rows, err := s.ownedDB.acknowledgements(ctx, repo.id, userID)
	if err != nil {
		return err
	}
	idx := indexAcks(rows)
	want := map[string]bool{}
	for _, c := range covers {
		want[c] = true
	}
	var unread []string
	for _, folder := range folders {
		for _, f := range folder.Files {
			if want[f.Path] {
				if ack := idx.status(userID, f); ack == nil || !ack.Current {
					unread = append(unread, f.Path)
				}
			}
		}
	}
	if len(unread) == 0 {
		return nil
	}
	sort.Strings(unread)
	return problem(409, "read_first", "Read what this quiz covers first, then take it: "+strings.Join(unread, ", ")+".")
}

// quizAllowed refuses a quiz the person passed, has waiting for a reviewer,
// or failed less than retryAfter ago, for this text of the quiz.
func (s *Service) quizAllowed(ctx context.Context, spaceID, userID, fileID, blob string, t formTemplate, now time.Time) error {
	rows, err := s.ownedDB.quizRows(ctx, spaceID, userID)
	if err != nil {
		return err
	}
	st := quizStateOf(rows, userID, fileID, blob, t.RetryAfter)
	switch {
	case st.Status == "passed":
		return problem(409, "quiz_passed", "You have passed this quiz already.")
	case st.Status == "waiting":
		return problem(409, "quiz_waiting", "Your last answers to this quiz are waiting for a reviewer.")
	case st.Status == "failed" && now.Before(st.retry):
		wait := int(math.Ceil(st.retry.Sub(now).Seconds()))
		return &Error{Status: 409, Code: "retry_later", Message: "You can try this quiz again from " + st.RetryAt + ".", RetryAfterSeconds: wait}
	}
	return nil
}

// canWriteSpace reports whether the person owns the space or is an active
// member with read-write access: a reviewer, for a review space.
func (db *accountDatabase) canWriteSpace(ctx context.Context, spaceID, userID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var n int
	err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces s LEFT JOIN metatrash_memberships m ON m.space_id = s.space_id AND m.user_id = ? WHERE s.space_id = ? AND (s.owner_user_id = ? OR m.status = 'active' AND m.agent_permission = 'read_write')", userID, spaceID, userID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("cannot read space members")
	}
	return n > 0, nil
}

// acknowledgeSent records path as read at its folder's published version
// when an acknowledge service tracks it; it returns that version, or 0.
func (s *Service) acknowledgeSent(ctx context.Context, repo *repository, state string, files map[string]record, path, userID string, now time.Time) (int, error) {
	if s.ownedDB == nil {
		return 0, nil
	}
	folders, err := repo.publishedFolders(ctx, state, files)
	if err != nil {
		return 0, err
	}
	for _, folder := range folders {
		for _, f := range folder.Files {
			if f.Path != path || !f.Tracked {
				continue
			}
			if err := s.ownedDB.recordAcknowledgement(ctx, repo.id, ackRow{UserID: userID, FileID: f.ID, Blob: f.Blob, Path: path, Version: folder.Version, At: now.Unix()}); err != nil {
				return 0, err
			}
			return folder.Version, nil
		}
	}
	return 0, nil
}

// resentCopy is the needs-info copy a submission sends again: fileSubmission
// marks the file with fileID resent (as newID) and sets text to its new
// text, when it still is a needs-info copy the folder's locks let it change.
type resentCopy struct {
	fileID, newID string
	text          string
}

// filedFile is where fileSubmission filed a copy.
type filedFile struct {
	path, id string
}

// fileSubmission writes the copy as a new file named base.md, or base-2.md
// and so on when that name is taken, with its photos' pointers. Runs on the
// write queue.
func fileSubmission(ctx context.Context, r *repository, base, form, text string, old *resentCopy, photos []formPhoto) (filedFile, error) {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return filedFile{}, err
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
		return filedFile{}, problem(409, "conflict", "Too many submissions of this form this minute; try again shortly.")
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return filedFile{}, err
	}
	if githubFolderAround(github, fileFolder(path)) != "" {
		return filedFile{}, formError("submitTo is inside a GitHub folder; send forms to an ordinary folder.")
	}
	readOnly, err := r.readOnlyRules(ctx, files, nil)
	if err != nil {
		return filedFile{}, err
	}
	if level := fileLock(readOnly, path); blocks(level, writerPerson) {
		return filedFile{}, readOnlyFile(path, level)
	}
	for p := range files {
		if strings.HasPrefix(path, p+"/") {
			return filedFile{}, formError("submitTo names a folder where " + p + " is a file.")
		}
	}
	id, err := randomHex(16)
	if err != nil {
		return filedFile{}, err
	}
	files[path] = record{File: File{ID: id, Path: path, Bytes: int64(len(text)), Deletable: true}}
	texts := map[string]string{path: text}
	changes := []commitChange{{id, "create", path}}
	paths := []string{path}
	if len(photos) > 0 {
		var size int64
		for _, p := range photos {
			size += p.info.Size
		}
		if virtualSpaceBytes(files, "")+size > maxVirtualPerSpace {
			return filedFile{}, problem(507, "storage_limit", fmt.Sprintf("The photos would take the review space's virtual files over %d GB.", maxVirtualPerSpace>>30))
		}
	}
	for _, p := range photos {
		if _, taken := files[p.path]; taken || !validPath(p.path) {
			return filedFile{}, problem(409, "conflict", "A photo's file name is taken; send the form again.")
		}
		if level := fileLock(readOnly, p.path); blocks(level, writerPerson) {
			return filedFile{}, readOnlyFile(p.path, level)
		}
		for q := range files {
			if strings.HasPrefix(p.path, q+"/") {
				return filedFile{}, formError("submitTo names a folder where " + q + " is a file, so its photos have nowhere to go.")
			}
		}
		pid, err := randomHex(16)
		if err != nil {
			return filedFile{}, err
		}
		info := p.info
		pointer := info.pointer()
		files[p.path] = record{File: File{ID: pid, Path: p.path, Bytes: int64(len(pointer)), Deletable: true, Virtual: &info}}
		texts[p.path] = pointer
		changes = append(changes, commitChange{pid, "create", p.path})
		paths = append(paths, p.path)
	}
	added, err := addFolderFiles(files, texts, github, r.rootAgents, paths...)
	if err != nil {
		return filedFile{}, err
	}
	changes = append(changes, added...)
	message := "Form submission: " + form
	if old != nil {
		old.text = ""
		for p, rec := range files {
			if rec.ID != old.fileID || p == path {
				continue
			}
			if blocks(fileLock(readOnly, p), writerPerson) {
				break
			}
			b, err := r.blob(ctx, rec.Blob)
			if err != nil {
				return filedFile{}, err
			}
			if marked := markResent(string(b), old.newID); marked != "" {
				rec.Bytes = int64(len(marked))
				files[p] = rec
				texts[p] = marked
				changes = append(changes, commitChange{rec.ID, "write", p})
				old.text = marked
				message = "Form submission: " + form + " (sent again)"
			}
			break
		}
	}
	if _, err := r.commitFiles(ctx, head, files, texts, nil, changes, message); err != nil {
		if old != nil {
			old.text = ""
		}
		return filedFile{}, err
	}
	return filedFile{path, id}, nil
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
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxSubmitBodyBytes))
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
	if err := strictJSON(b, &body); err != nil || body.Path == "" || body.Answers == nil || body.Revision != "" && !hashPattern.MatchString(body.Revision) || body.Blob != "" && !hashPattern.MatchString(body.Blob) || body.Resends != "" && !receiptIDPattern.MatchString(body.Resends) {
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
