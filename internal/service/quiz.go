package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Quizzes (0.50.0). A quiz is a form template (forms.go) with quiz: true in
// a folder whose acknowledge service asks people to read its files. It
// covers one of them: that file counts as read while the person has an
// approved copy of the quiz as published now (policies in
// acknowledgements.go), instead of I Have Read This.
//
//	---
//	form: wfh-quiz
//	title: Working from home quiz
//	submitTo: dave/hr-review/quizzes/
//	quiz: true
//	covers: wfh-policy.md
//	passMark: 80
//	retryAfter: 1h
//	unmarked: name
//	---
//
// The answer key is a filled-in copy of the quiz in the review space, in its
// submitTo folder, named <space slug>-<form>-answers.md: a reviewer sends the
// quiz and copies their copy to that name (Slate Viewer's Use This as
// Answers). It only counts for the quiz text it was filled in from
// (templateBlob), because everyone in a space can read the quiz, never the
// key. The server marks each answer the key has an answer for; free text
// waits for a reviewer's tick (reviews.go).

const (
	defaultPassMark   = 100
	defaultRetryAfter = time.Hour
	maxRetryAfter     = 365 * 24 * time.Hour
)

var retryPattern = regexp.MustCompile(`^(\d{1,6})([mhd])$`)

// freeText are the field types a reviewer marks; the server marks the rest.
var freeText = map[string]bool{"text": true, "textarea": true, "email": true, "phone": true}

// readQuizKeys reads quiz, covers, passMark and retryAfter.
func (t *formTemplate) readQuizKeys(keys map[string]string) error {
	switch keys["quiz"] {
	case "", "false":
	case "true":
		t.Quiz = true
	default:
		return formError("quiz is true or false.")
	}
	t.PassMark, t.RetryAfter = defaultPassMark, defaultRetryAfter
	if !t.Quiz {
		for _, key := range []string{"covers", "passMark", "retryAfter", "unmarked"} {
			if _, ok := keys[key]; ok {
				return formError(key + " is only for quizzes; add quiz: true.")
			}
		}
		return nil
	}
	t.Covers = keys["covers"]
	if t.Covers == "" || strings.HasPrefix(t.Covers, "/") || strings.Contains(t.Covers, "\\") || !strings.HasSuffix(strings.ToLower(t.Covers), ".md") || !validPath(path.Clean("x/"+t.Covers)) {
		return formError("a quiz needs covers: the policy file it tests, next to the quiz or below it, such as covers: wfh-policy.md.")
	}
	if strings.HasPrefix(path.Clean(t.Covers), "..") {
		return formError("covers must name a file in the quiz's folder or below it.")
	}
	if v, ok := keys["passMark"]; ok {
		n, err := strconv.Atoi(strings.TrimSuffix(v, "%"))
		if err != nil || n < 1 || n > 100 {
			return formError("passMark is a percentage from 1 to 100, such as passMark: 80.")
		}
		t.PassMark = n
	}
	t.Unmarked = map[string]bool{}
	for _, name := range strings.Split(keys["unmarked"], ",") {
		if name = strings.TrimSpace(name); name != "" {
			t.Unmarked[name] = true
		}
	}
	if v, ok := keys["retryAfter"]; ok {
		d, err := parseRetryAfter(v)
		if err != nil {
			return err
		}
		t.RetryAfter = d
	}
	return nil
}

// parseRetryAfter reads 0, or a number of minutes, hours or days (30m, 1h, 2d).
func parseRetryAfter(v string) (time.Duration, error) {
	bad := formError("retryAfter is how long to wait before trying again: 0, or minutes, hours or days such as 30m, 1h or 2d.")
	if v == "0" {
		return 0, nil
	}
	m := retryPattern.FindStringSubmatch(v)
	if m == nil {
		return 0, bad
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
	d := time.Duration(n) * unit
	if d > maxRetryAfter {
		return 0, bad
	}
	return d, nil
}

// coveredPath is the path of the file a quiz at quizPath covers.
func (t formTemplate) coveredPath(quizPath string) string {
	return path.Join(path.Dir(quizPath), t.Covers)
}

// filedCopy is what a filed copy's front matter says.
type filedCopy struct {
	keys    map[string]string
	answers map[string]any
	marks   map[string]*bool
}

// parseFiledCopy reads a filed copy (forms.go render): its top-level keys,
// and the answers and marks blocks, whose values are JSON.
func parseFiledCopy(text string) (filedCopy, bool) {
	keys, _ := splitFrontMatter(text)
	if keys == nil || keys["form"] == "" || keys["submittedAt"] == "" {
		return filedCopy{}, false
	}
	c := filedCopy{keys: keys, answers: map[string]any{}, marks: map[string]*bool{}}
	lines := strings.Split(strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff"), "\n")
	block := ""
	for _, line := range lines[1:] {
		if t := strings.TrimRight(line, " \t"); t == "---" || t == "..." {
			break
		}
		if !strings.HasPrefix(line, " ") {
			block = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ":"))
			if !strings.HasSuffix(strings.TrimSpace(line), ":") {
				block = ""
			}
			continue
		}
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !fieldNamePattern.MatchString(name) {
			continue
		}
		value = strings.TrimSpace(value)
		switch block {
		case "answers":
			var v any
			if json.Unmarshal([]byte(value), &v) != nil {
				v = value
			}
			switch a := v.(type) {
			case string:
				c.answers[name] = a
			case []any:
				list := []string{}
				for _, item := range a {
					if s, ok := item.(string); ok {
						list = append(list, s)
					}
				}
				c.answers[name] = list
			}
		case "marks":
			var m *bool
			if json.Unmarshal([]byte(value), &m) == nil {
				c.marks[name] = m
			}
		}
	}
	return c, true
}

// quizKeyName is the answer key's file name for a quiz in the space slug.
func quizKeyName(slug, form string) string {
	return slug + "-" + form + "-answers.md"
}

// mark compares answers with the key. Every field the key answers is marked:
// true or false, or nil for free text, which a reviewer ticks. Fields the
// key leaves blank, fields the quiz lists as unmarked (such as a name) and
// signatures are not marked.
func (t formTemplate) mark(answers formAnswers, key map[string]any) map[string]*bool {
	marks := map[string]*bool{}
	for _, f := range t.Fields {
		want, ok := key[f.Name]
		if !ok || f.Type == "sign" || t.Unmarked[f.Name] || isBlankAnswer(want) {
			continue
		}
		if freeText[f.Type] {
			marks[f.Name] = nil
			continue
		}
		right := sameAnswer(f, answers[f.Name], want)
		marks[f.Name] = &right
	}
	return marks
}

func isBlankAnswer(v any) bool {
	switch a := v.(type) {
	case string:
		return strings.TrimSpace(a) == ""
	case []string:
		return len(a) == 0
	}
	return true
}

// sameAnswer reports whether a given answer matches the key's.
func sameAnswer(f formField, got, want any) bool {
	if f.Multi {
		g, _ := got.([]string)
		w, _ := want.([]string)
		if len(g) != len(w) {
			return false
		}
		set := map[string]bool{}
		for _, s := range w {
			set[s] = true
		}
		for _, s := range g {
			if !set[s] {
				return false
			}
		}
		return true
	}
	g, _ := got.(string)
	w, _ := want.(string)
	if f.Type == "number" {
		a, err1 := strconv.ParseFloat(g, 64)
		b, err2 := strconv.ParseFloat(strings.TrimSpace(w), 64)
		return err1 == nil && err2 == nil && math.Abs(a-b) < 1e-9
	}
	return g == strings.TrimSpace(w)
}

// quizScore is the percentage of marks that are right, once none is
// waiting for a reviewer.
func quizScore(marks map[string]*bool) (int, bool) {
	if len(marks) == 0 {
		return 100, true
	}
	right := 0
	for _, m := range marks {
		if m == nil {
			return 0, false
		}
		if *m {
			right++
		}
	}
	return right * 100 / len(marks), true
}

// quizStatus is a copy's status from its marks: submitted while a reviewer
// has to tick answers, otherwise approved at or over the pass mark.
func (t formTemplate) quizStatus(marks map[string]*bool) (string, *int) {
	score, done := quizScore(marks)
	if !done {
		return "submitted", nil
	}
	if score >= t.PassMark {
		return "approved", &score
	}
	return "declined", &score
}

// quizKey reads the answer key for the quiz text blob in target's folder,
// or nil when there is none for this text.
func (s *Service) quizKey(ctx context.Context, target submitTarget, slug string, t formTemplate, blob string) (map[string]any, error) {
	_, files, err := target.repo.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	f, ok := files[target.folder+quizKeyName(slug, t.Form)]
	if !ok {
		return nil, nil
	}
	b, err := target.repo.blob(ctx, f.Blob)
	if err != nil {
		return nil, err
	}
	c, ok := parseFiledCopy(string(b))
	if !ok || c.keys["form"] != t.Form || c.keys["templateBlob"] != blob {
		return nil, nil
	}
	if len(t.mark(formAnswers{}, c.answers)) == 0 {
		return nil, nil
	}
	return c.answers, nil
}

// quizMeta is what a published Markdown file's front matter says about
// quizzes: whether it is one, and the path of the file it covers.
type quizMeta struct {
	quiz       bool
	covers     string
	retryAfter time.Duration
}

// quizMetaCache keeps quizMeta by blob hash and quiz path; blobs never change.
var quizMetaCache sync.Map

func (r *repository) quizMeta(ctx context.Context, blob, quizPath string) quizMeta {
	key := blob + " " + quizPath
	if v, ok := quizMetaCache.Load(key); ok {
		return v.(quizMeta)
	}
	meta := quizMeta{}
	if b, err := r.blob(ctx, blob); err == nil {
		if keys, _ := splitFrontMatter(string(b)); isFormKeys(keys) && keys["quiz"] == "true" {
			if t, err := parseFormTemplate(string(b)); err == nil && t.Quiz {
				meta = quizMeta{quiz: true, covers: t.coveredPath(quizPath), retryAfter: t.RetryAfter}
			}
		}
	}
	quizMetaCache.Store(key, meta)
	return meta
}

// quizRow is one quiz copy a person sent, for policies and retry rules.
type quizRow struct {
	UserID, FileID, Blob, Status string
	Score                        *int
	SubmittedAt, ReviewedAt      int64
}

// quizRows reads a space's quiz copies (one person's when userID is set),
// oldest first.
func (db *accountDatabase) quizRows(ctx context.Context, spaceID, userID string) ([]quizRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query := "SELECT user_id, template_file_id, template_blob, status, score, submitted_at, COALESCE(reviewed_at, 0) FROM metatrash_form_submissions WHERE space_id = ? AND quiz = 1"
	args := []any{spaceID}
	if userID != "" {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	rows, err := db.db.QueryContext(ctx, query+fmt.Sprintf(" ORDER BY submitted_at, submission_id LIMIT %d", maxAcknowledgementRows), args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read quiz results")
	}
	defer rows.Close()
	var out []quizRow
	for rows.Next() {
		var q quizRow
		var score *int
		if err := rows.Scan(&q.UserID, &q.FileID, &q.Blob, &q.Status, &score, &q.SubmittedAt, &q.ReviewedAt); err != nil {
			return nil, fmt.Errorf("cannot read quiz results")
		}
		q.Score = score
		out = append(out, q)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read quiz results")
	}
	return out, nil
}

// quizState is a person's state for one quiz text.
type quizState struct {
	// Status is "passed", "waiting" (for a reviewer), "failed" or "new".
	Status string `json:"status"`
	Score  *int   `json:"score,omitempty"`
	// RetryAt is when a failed quiz can be sent again.
	RetryAt string `json:"retryAt,omitempty"`
	// Older is set when the person passed an earlier text of the quiz.
	Older bool `json:"older,omitempty"`
	retry time.Time
}

// quizStateOf is userID's state for the quiz file fileID at text blob.
func quizStateOf(rows []quizRow, userID, fileID, blob string, retryAfter time.Duration) quizState {
	st := quizState{Status: "new"}
	for _, q := range rows {
		if q.UserID != userID || q.FileID != fileID {
			continue
		}
		if q.Blob != blob {
			if q.Status == "approved" {
				st.Older = true
			}
			continue
		}
		switch q.Status {
		case "approved":
			return quizState{Status: "passed", Score: q.Score}
		case "submitted":
			st.Status, st.Score, st.RetryAt, st.retry = "waiting", nil, "", time.Time{}
		case "declined":
			at := q.SubmittedAt
			if q.ReviewedAt > at {
				at = q.ReviewedAt
			}
			st.Status, st.Score = "failed", q.Score
			st.retry = time.Unix(at, 0).Add(retryAfter).UTC()
			st.RetryAt = st.retry.Format(time.RFC3339)
		}
	}
	return st
}
