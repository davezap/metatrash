package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

// Forms (metatrash 0.49.0). A form template is a Markdown file whose front
// matter has a form key. mtsync submit sends a person's answers to one; the
// server checks them and files the filled-in copy where the template says.
// mtsync submissions lists what the person sent from a space's forms, and
// mtsync submission prints one copy. Since 0.50.0 every copy has a review
// status (quizzes are marked by the server), and mtsync review lets a
// reviewer approve, decline or tick a copy filed in their space.

type receipt struct {
	ID          string `json:"id"`
	Form        string `json:"form"`
	Title       string `json:"title"`
	Template    string `json:"template"`
	Revision    string `json:"revision"`
	Target      string `json:"target"`
	SubmittedAt string `json:"submittedAt"`
	Text        string `json:"text,omitempty"`
	// Review (metatrash 0.50.0): submitted, approved or declined.
	Status     string           `json:"status,omitempty"`
	Note       string           `json:"note,omitempty"`
	ReviewedAt string           `json:"reviewedAt,omitempty"`
	Quiz       bool             `json:"quiz,omitempty"`
	PassMark   int              `json:"passMark,omitempty"`
	Score      *int             `json:"score,omitempty"`
	Marks      map[string]*bool `json:"marks,omitempty"`
	Answers    map[string]any   `json:"answers,omitempty"`
}

type receiptsAnswer struct {
	Space       string    `json:"space"`
	Submissions []receipt `json:"submissions"`
}

// gitBlobHash is the Git blob hash of text, as the server names file texts.
func gitBlobHash(text []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(text))
	h.Write(text)
	return hex.EncodeToString(h.Sum(nil))
}

// submit sends answers to the form at path; blob is the template text's
// hash, or version the published version of its folder they answer.
func (c *client) submit(ctx context.Context, path, blob string, version int, answers json.RawMessage) (receipt, error) {
	var out receipt
	if !c.account {
		return out, fmt.Errorf("forms need a signed-in space (mtsync login), not a space key")
	}
	body := map[string]any{"path": c.prefix + path, "answers": answers, "today": time.Now().Format("2006-01-02")}
	if version > 0 {
		body["version"] = version
	} else {
		body["blob"] = blob
	}
	err := c.do(ctx, "POST", "submissions", nil, body, &out)
	out.Template = strings.TrimPrefix(out.Template, c.prefix)
	return out, err
}

// receipts lists the person's submissions of the space's forms, or the one
// with id and its text.
func (c *client) receipts(ctx context.Context, id string) (receiptsAnswer, error) {
	var out receiptsAnswer
	if !c.account {
		return out, fmt.Errorf("forms need a signed-in space (mtsync login), not a space key")
	}
	if id != "" {
		var one receipt
		err := c.do(ctx, "GET", "submissions", url.Values{"id": {id}}, nil, &one)
		one.Template = strings.TrimPrefix(one.Template, c.prefix)
		out.Submissions = []receipt{one}
		return out, err
	}
	err := c.do(ctx, "GET", "submissions", nil, nil, &out)
	for i := range out.Submissions {
		out.Submissions[i].Template = strings.TrimPrefix(out.Submissions[i].Template, c.prefix)
	}
	return out, err
}

// review records a review of the filed copy at path (metatrash 0.50.0).
func (c *client) review(ctx context.Context, path string, review json.RawMessage) (receipt, error) {
	var out receipt
	if !c.account {
		return out, fmt.Errorf("reviews need a signed-in space (mtsync login), not a space key")
	}
	var body map[string]any
	if err := json.Unmarshal(review, &body); err != nil {
		return out, err
	}
	body["path"] = c.prefix + path
	err := c.do(ctx, "POST", "reviews", nil, body, &out)
	out.Template = strings.TrimPrefix(out.Template, c.prefix)
	return out, err
}

// readAnswers reads the answers JSON object from a file, or stdin for "-".
func readAnswers(name string, stdin io.Reader) (json.RawMessage, error) {
	var b []byte
	var err error
	if name == "-" {
		b, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
	} else {
		b, err = os.ReadFile(name)
	}
	if err != nil {
		return nil, err
	}
	var check map[string]any
	if err := json.Unmarshal(b, &check); err != nil || check == nil {
		return nil, fmt.Errorf("answers must be a JSON object of field name to answer, such as {\"full_name\": \"Pat\"}")
	}
	return b, nil
}

// submitForm sends the answers in answersFile to the form file; with version
// set, to that published version of it, as Slate Viewer shows it. With
// asJSON the receipt is printed as JSON, and so is a refusal (with each
// field's problem), for Slate Viewer.
func submitForm(ctx context.Context, file, answersFile string, version int, asJSON bool, stdin io.Reader, out io.Writer) error {
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	text, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	answers, err := readAnswers(answersFile, stdin)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	r, err := c.submit(ctx, rel, gitBlobHash(text), version, answers)
	if err != nil {
		return printRefusal(err, asJSON, out)
	}
	if asJSON {
		return printJSON(out, r)
	}
	fmt.Fprintf(out, "Sent %s at %s. Filed as %s.\n", r.Title, localTime(r.SubmittedAt), r.Target)
	if r.Quiz {
		fmt.Fprintln(out, reviewLine(r))
	}
	return nil
}

// printRefusal prints a refused request's field problems (or the whole
// refusal as JSON) and returns err.
func printRefusal(err error, asJSON bool, out io.Writer) error {
	var refused *apiError
	if asJSON && errors.As(err, &refused) {
		_ = printJSON(out, map[string]any{"error": refused})
	} else if errors.As(err, &refused) {
		for _, f := range refused.Fields {
			fmt.Fprintf(out, "  %s: %s\n", f.Name, f.Message)
		}
	}
	return err
}

// reviewLine says where a copy's review stands.
func reviewLine(r receipt) string {
	status := map[string]string{"submitted": "Waiting for review", "approved": "Approved", "declined": "Declined"}[r.Status]
	if r.Quiz {
		status = map[string]string{"submitted": "Waiting for a reviewer", "approved": "Passed", "declined": "Not passed"}[r.Status]
		if r.Score != nil {
			status += fmt.Sprintf(" (%d%%)", *r.Score)
		}
	}
	if status == "" {
		status = r.Status
	}
	if r.Note != "" {
		status += ": " + r.Note
	}
	return status
}

// reviewCopy sends the review in reviewFile ({"status", "note", "marks"})
// for the filed copy file, in a space the person reviews.
func reviewCopy(ctx context.Context, file, reviewFile string, asJSON bool, stdin io.Reader, out io.Writer) error {
	e, rel, err := syncedFile(file)
	if err != nil {
		return err
	}
	review, err := readAnswers(reviewFile, stdin)
	if err != nil {
		return fmt.Errorf("the review must be a JSON object such as {\"status\": \"approved\", \"note\": \"...\"}, or {\"marks\": {\"field\": true}} for a quiz")
	}
	c := folderClient(e, tokenSource(e.Server))
	r, err := c.review(ctx, rel, review)
	if err != nil {
		return printRefusal(err, asJSON, out)
	}
	if asJSON {
		return printJSON(out, r)
	}
	fmt.Fprintf(out, "%s: %s\n", r.Title, reviewLine(r))
	return nil
}

// showSubmissions lists what the person sent from the forms of the space
// synced at place, or prints one copy when id is set.
func showSubmissions(ctx context.Context, place, id string, asJSON bool, out io.Writer) error {
	e, err := syncedPlace(place)
	if err != nil {
		return err
	}
	c := folderClient(e, tokenSource(e.Server))
	answer, err := c.receipts(ctx, id)
	if err != nil {
		return err
	}
	if id != "" {
		if asJSON {
			return printJSON(out, answer.Submissions[0])
		}
		_, err := io.WriteString(out, answer.Submissions[0].Text)
		return err
	}
	if asJSON {
		return printJSON(out, answer)
	}
	if len(answer.Submissions) == 0 {
		fmt.Fprintln(out, "You have not sent any forms from this space.")
		return nil
	}
	for _, r := range answer.Submissions {
		fmt.Fprintf(out, "%s  %-30s %s  %s\n", localTime(r.SubmittedAt), r.Title, r.ID, r.Target)
		if r.Status != "" {
			fmt.Fprintf(out, "    %s\n", reviewLine(r))
		}
	}
	fmt.Fprintln(out, "Print one: mtsync submission PLACE ID")
	return nil
}
