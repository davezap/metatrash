package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Reviews (0.50.0). Every filed copy of a form (submissions.go) waits for a
// review: status submitted, then approved or declined. A quiz is reviewed
// by the server when it can mark every answer (quiz.go); free-text answers
// wait for a reviewer's tick. A reviewer is a person with write access to
// the space a copy is filed in, and reviews it there:
//
// POST .../reviews  {"path", "status"?, "note"?, "marks"?}
//
// path is the copy in this space. A form takes status approved or declined;
// a quiz takes marks ({"field": true|false}) for at least every answer still
// to tick, and its status follows from the score. The receipt (My
// Submissions) and the copy are updated; the copy is filled in again from
// its template with the review in its front matter. Only the OAuth REST
// resource offers it, and only a person (Metatrash Sync).

const maxReviewNote = 2000

type reviewRequest struct {
	Path   string           `json:"path"`
	Status string           `json:"status,omitempty"`
	Note   string           `json:"note,omitempty"`
	Marks  map[string]*bool `json:"marks,omitempty"`
}

// reviewedRow is a receipt as a review needs it.
type reviewedRow struct {
	id, spaceID, userID, blob, templatePath, revision, targetFileID string
	quiz                                                            bool
	answers, marks                                                  sql.NullString
}

func (db *accountDatabase) reviewedRow(ctx context.Context, id, targetSpaceID string) (reviewedRow, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var r reviewedRow
	err := db.db.QueryRowContext(ctx, "SELECT submission_id, space_id, user_id, template_blob, template_path, template_revision, target_file_id, quiz, answers, marks FROM metatrash_form_submissions WHERE submission_id = ? AND target_space_id = ?", id, targetSpaceID).
		Scan(&r.id, &r.spaceID, &r.userID, &r.blob, &r.templatePath, &r.revision, &r.targetFileID, &r.quiz, &r.answers, &r.marks)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, fmt.Errorf("cannot read submissions")
	}
	return r, true, nil
}

func (db *accountDatabase) recordReview(ctx context.Context, id, status string, score *int, marks map[string]*bool, reviewer string, at int64, note, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var m any
	if marks != nil {
		b, err := json.Marshal(marks)
		if err != nil {
			return fmt.Errorf("cannot record review")
		}
		m = string(b)
	}
	_, err := db.db.ExecContext(ctx, "UPDATE metatrash_form_submissions SET status = ?, score = ?, marks = ?, reviewed_by = ?, reviewed_at = ?, note = ?, text = ? WHERE submission_id = ?", status, score, m, reviewer, at, note, text, id)
	if err != nil {
		return fmt.Errorf("cannot record review")
	}
	return nil
}

// reviewCopy records a review of the copy at in.Path in repo.
func (s *Service) reviewCopy(ctx context.Context, repo *repository, userID string, in reviewRequest) (Receipt, error) {
	if !validPath(in.Path) {
		return Receipt{}, invalid("Invalid path.")
	}
	note := strings.TrimSpace(strings.ReplaceAll(in.Note, "\r\n", "\n"))
	if utf8.RuneCountInString(note) > maxReviewNote || !utf8.ValidString(note) || strings.ContainsFunc(note, func(r rune) bool { return unicode.IsControl(r) && r != '\n' }) {
		return Receipt{}, invalid(fmt.Sprintf("note is plain text of at most %d characters.", maxReviewNote))
	}
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		return Receipt{}, err
	}
	f, ok := files[in.Path]
	if !ok {
		return Receipt{}, missing()
	}
	b, err := repo.blob(ctx, f.Blob)
	if err != nil {
		return Receipt{}, err
	}
	notCopy := invalid(in.Path + " is not a copy of a form that can be reviewed here: copies filed from Metatrash 0.50.0 on name their submission in their front matter.")
	copied, ok := parseFiledCopy(string(b))
	if !ok || !receiptIDPattern.MatchString(copied.keys["submission"]) {
		return Receipt{}, notCopy
	}
	row, found, err := s.ownedDB.reviewedRow(ctx, copied.keys["submission"], repo.id)
	if err != nil {
		return Receipt{}, err
	}
	if !found || row.targetFileID != f.ID {
		return Receipt{}, notCopy
	}
	quizRepo := s.ownedRepository(row.spaceID)
	if quizRepo == nil {
		return Receipt{}, problem(409, "conflict", "The space this form came from is gone, so its copy cannot be filled in again.")
	}
	tb, err := quizRepo.blob(ctx, row.blob)
	if err != nil {
		return Receipt{}, problem(409, "conflict", "The form this copy came from cannot be read, so the copy cannot be filled in again.")
	}
	t, err := parseFormTemplate(string(tb))
	if err != nil {
		return Receipt{}, err
	}
	var answers formAnswers
	if !row.answers.Valid || json.Unmarshal([]byte(row.answers.String), &answers) != nil {
		return Receipt{}, notCopy
	}
	// Answers to choices with multi come back as lists of any.
	for _, field := range t.Fields {
		if list, ok := answers[field.Name].([]any); ok {
			picked := []string{}
			for _, item := range list {
				if s, ok := item.(string); ok {
					picked = append(picked, s)
				}
			}
			answers[field.Name] = picked
		}
	}
	at, err := time.Parse(time.RFC3339, copied.keys["submittedAt"])
	if err != nil {
		return Receipt{}, notCopy
	}
	reviewer, err := s.personName(ctx, userID)
	if err != nil {
		return Receipt{}, err
	}
	now := s.now().UTC()
	sub := submission{Template: copied.keys["template"], Revision: row.revision, Blob: row.blob, ID: row.id, By: copied.keys["submittedBy"], At: at, ReviewedBy: reviewer, ReviewedAt: now, Note: note}
	if row.quiz {
		if in.Status != "" {
			return Receipt{}, invalid("A quiz's status follows from its marks; send marks, not status.")
		}
		var marks map[string]*bool
		if !row.marks.Valid || json.Unmarshal([]byte(row.marks.String), &marks) != nil {
			return Receipt{}, problem(409, "no_answer_key", "This copy was not marked: it was sent before the quiz had answers. Mark it once there are answers, by sending the quiz again.")
		}
		for name, m := range in.Marks {
			if _, ok := marks[name]; !ok || m == nil {
				return Receipt{}, invalid("marks gives true or false for answers the key marks; " + name + " is not one.")
			}
			marks[name] = m
		}
		sub.Marks = marks
		sub.Status, sub.Score = t.quizStatus(marks)
		if sub.Score == nil {
			var left []string
			for _, field := range t.Fields {
				if m, ok := marks[field.Name]; ok && m == nil {
					left = append(left, field.Name)
				}
			}
			return Receipt{}, invalid("Tick or cross every free-text answer first: " + strings.Join(left, ", ") + ".")
		}
	} else {
		if in.Marks != nil {
			return Receipt{}, invalid("Only quizzes take marks.")
		}
		if in.Status != "approved" && in.Status != "declined" {
			return Receipt{}, invalid(`status is "approved" or "declined".`)
		}
		sub.Status = in.Status
	}
	text := t.render(answers, sub)
	if _, err := s.queued(ctx, func() (any, error) {
		head, files, err := repo.snapshot(ctx, "")
		if err != nil {
			return nil, err
		}
		cur, ok := files[in.Path]
		if !ok || cur.ID != f.ID {
			return nil, problem(409, "conflict", "The copy moved while it was reviewed; review it again.")
		}
		readOnly, err := repo.readOnlyRules(ctx, files, nil)
		if err != nil {
			return nil, err
		}
		if level := fileLock(readOnly, in.Path); blocks(level, true) {
			return nil, readOnlyFile(in.Path, level)
		}
		cur.Bytes = int64(len(text))
		files[in.Path] = cur
		return repo.commitFiles(ctx, head, files, map[string]string{in.Path: text}, nil, []commitChange{{cur.ID, "write", in.Path}}, "Form review: "+t.Form+" "+sub.Status)
	}); err != nil {
		return Receipt{}, err
	}
	if err := s.ownedDB.recordReview(ctx, row.id, sub.Status, sub.Score, sub.Marks, userID, now.Unix(), note, text); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{ID: row.id, Form: t.Form, Title: t.Title, Template: row.templatePath, Revision: row.revision, SubmittedAt: at.UTC().Format(time.RFC3339), Text: text, Status: sub.Status, Note: note, ReviewedAt: now.Format(time.RFC3339), Quiz: row.quiz, Score: sub.Score, Marks: sub.Marks}
	if row.quiz {
		receipt.PassMark = t.PassMark
	}
	return receipt, nil
}

// serveReviews handles the reviews resource after access was checked for
// writing.
func (h *httpAdapter) serveReviews(w http.ResponseWriter, r *http.Request, repo *repository) {
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	if identity.oauth == nil || repo.id == "" || h.service.ownedDB == nil {
		sendError(w, problem(404, "not_found", "Forms work only in your own and shared spaces."))
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	if !personWrite(r.Context()) {
		sendError(w, problem(403, "forbidden", "Only a person can review a form, in Slate Viewer or with mtsync review; agents cannot."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			sendError(w, problem(413, "payload_too_large", "Request exceeds the byte limit."))
		} else {
			sendError(w, invalid("Could not read body."))
		}
		return
	}
	var body reviewRequest
	if err := strictJSON(b, &body); err != nil || body.Path == "" {
		sendError(w, invalid(`Send {"path": "...", "status": "approved"} for a form, or {"path": "...", "marks": {"field": true}} for a quiz, with an optional "note".`))
		return
	}
	value, err := h.service.reviewCopy(r.Context(), repo, identity.oauth.UserID, body)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, value)
}
