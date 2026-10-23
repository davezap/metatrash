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
// mtsync submission prints one copy.

type receipt struct {
	ID          string `json:"id"`
	Form        string `json:"form"`
	Title       string `json:"title"`
	Template    string `json:"template"`
	Revision    string `json:"revision"`
	Target      string `json:"target"`
	SubmittedAt string `json:"submittedAt"`
	Text        string `json:"text,omitempty"`
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

// submit sends answers to the form at path; blob is the template text's hash.
func (c *client) submit(ctx context.Context, path, blob string, answers json.RawMessage) (receipt, error) {
	var out receipt
	if !c.account {
		return out, fmt.Errorf("forms need a signed-in space (mtsync login), not a space key")
	}
	body := map[string]any{"path": c.prefix + path, "answers": answers, "blob": blob, "today": time.Now().Format("2006-01-02")}
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

// submitForm sends the answers in answersFile to the form file. With asJSON
// the receipt is printed as JSON, and so is a refusal (with each field's
// problem), for Slate Viewer.
func submitForm(ctx context.Context, file, answersFile string, asJSON bool, stdin io.Reader, out io.Writer) error {
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
	r, err := c.submit(ctx, rel, gitBlobHash(text), answers)
	if err != nil {
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
	if asJSON {
		return printJSON(out, r)
	}
	fmt.Fprintf(out, "Sent %s at %s. Filed as %s.\n", r.Title, localTime(r.SubmittedAt), r.Target)
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
	}
	fmt.Fprintln(out, "Print one: mtsync submission PLACE ID")
	return nil
}
