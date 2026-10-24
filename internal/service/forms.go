package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Forms (0.49.0). A form template is a Markdown file whose front matter has
// a form key. Its fields are {{type name=… attr…}} tokens in the text; a
// choice or select token takes its options from the list right below it.
// Slate's fill mode draws the inputs and Slate Viewer sends the answers to
// the submissions resource (submissions.go), which checks them here, renders
// the filled-in copy and files it where the template's submitTo says.
//
//	---
//	form: leave-request
//	title: Leave request
//	submitTo: dave/hr-review/leave/
//	---
//	Full name: {{text name=full_name required max=80}}
//	Type of leave: {{choice name=type required}}
//	- ( ) Annual
//	- ( ) Sick
//
// Tokens inside code (fenced blocks and `spans`) and HTML comments are text,
// not fields. docs/forms.md describes every type and attribute; Slate's
// src/forms.js parses the same syntax and must stay in step with this file.

const (
	maxFormFields      = 200
	maxFormOptions     = 100
	maxFormOptionText  = 200
	maxFormLabel       = 200
	maxFormPattern     = 500
	maxFormTitle       = 200
	maxFormAnswerBytes = 64 << 10
	defaultTextMax     = 200
	limitTextMax       = 2000
	defaultTextareaMax = 5000
	limitTextareaMax   = 20000
)

var (
	formNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	fieldNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	fieldToken       = regexp.MustCompile(`\{\{\s*([A-Za-z]+)((?:[^{}"]|"(?:[^"\\]|\\.)*")*)\}\}`)
	fieldAttribute   = regexp.MustCompile(`^\s*([A-Za-z][A-Za-z-]*)(?:=(?:"((?:[^"\\]|\\.)*)"|([^\s"]+)))?`)
	optionLine       = regexp.MustCompile(`^(\s*)([-*+])\s+(?:\[[ xX]\]\s+|\(\s?[xX]?\s?\)\s+)?(.*\S)\s*$`)
	fenceLine        = regexp.MustCompile("^ {0,3}(```+|~~~+)")
	datePattern      = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timePattern      = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)
	emailPattern     = regexp.MustCompile(`^[^\s@]{1,64}@[^\s@]+\.[^\s@]+$`)
	phonePattern     = regexp.MustCompile(`^\+?[0-9 ().-]{6,24}$`)
)

// fieldAttributes are the attributes each field type takes, beyond name,
// label, required and placeholder, which every type takes.
var fieldAttributes = map[string]map[string]bool{
	"text":     {"min": true, "max": true, "pattern": true},
	"textarea": {"min": true, "max": true, "rows": true},
	"number":   {"min": true, "max": true, "step": true},
	"date":     {"min": true, "max": true},
	"time":     {"min": true, "max": true},
	"email":    {},
	"phone":    {},
	"choice":   {"multi": true, "min": true, "max": true},
	"select":   {},
	"yesno":    {},
	"sign":     {},
}

type formField struct {
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Required bool     `json:"required,omitempty"`
	Multi    bool     `json:"multi,omitempty"`
	Options  []string `json:"options,omitempty"`
	attrs    map[string]string
	pattern  *regexp.Regexp
	// line is the field's line in the template body (0-based), and
	// optionLines the lines holding its options.
	line, firstOption, lastOption int
}

type formTemplate struct {
	Form, Title, SubmitTo string
	Fields                []formField
	body                  []string
	// A quiz (0.50.0) is passed at PassMark percent; after a fail the person
	// may send it again after RetryAfter. Covers lists the policies it tests
	// (0.51.0), which ReadFirst asks people to read before taking it. See
	// quiz.go.
	Quiz       bool
	Covers     []string
	ReadFirst  bool
	PassMark   int
	RetryAfter time.Duration
	// Unmarked names fields that are not questions, such as a name.
	Unmarked map[string]bool
}

// FieldError is one field's problem in a refused submission.
type FieldError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

func formError(message string) *Error {
	return invalid("This form template has a mistake: " + message + " See docs/forms.md.")
}

// splitFrontMatter returns the front matter keys of text (a block between
// --- lines at the very start) and the lines after it. Values may be quoted;
// other YAML is not understood, and nested keys are ignored.
func splitFrontMatter(text string) (map[string]string, []string) {
	text = strings.TrimPrefix(strings.ReplaceAll(text, "\r\n", "\n"), "\ufeff")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return nil, lines
	}
	keys := map[string]string{}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		if t := strings.TrimRight(line, " \t"); t == "---" || t == "..." {
			return keys, lines[i+1:]
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"') {
			var s string
			if json.Unmarshal([]byte(value), &s) == nil {
				value = s
			}
		} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		}
		keys[strings.TrimSpace(key)] = value
	}
	return nil, lines
}

// isFormTemplate reports whether text's front matter makes it a form: it has
// a form key, and no submittedAt, which marks a filed copy.
func isFormTemplate(text string) bool {
	keys, _ := splitFrontMatter(text)
	return isFormKeys(keys)
}

func isFormKeys(keys map[string]string) bool {
	_, form := keys["form"]
	_, filed := keys["submittedAt"]
	return form && !filed
}

// scanFields calls fn for every field token in the body lines, with the
// line, the token's byte range and its parts. Code and comments are skipped.
func scanFields(lines []string, fn func(line, start, end int, kind, attrs string)) {
	fence := ""
	inComment := false
	for i, line := range lines {
		if fence == "" && !inComment {
			if m := fenceLine.FindStringSubmatch(line); m != nil {
				fence = m[1]
				continue
			}
		} else if fence != "" {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, fence[:3]) && strings.Trim(t, fence[:1]) == "" && len(t) >= len(fence) {
				fence = ""
			}
			continue
		}
		masked := maskLine(line, &inComment)
		for _, m := range fieldToken.FindAllStringSubmatchIndex(masked, -1) {
			fn(i, m[0], m[1], line[m[2]:m[3]], line[m[4]:m[5]])
		}
	}
}

// maskLine blanks out comments and code spans so tokens in them are not
// found; inComment carries an open <!-- comment to the next line.
func maskLine(line string, inComment *bool) string {
	b := []byte(line)
	blank := func(from, to int) {
		for k := from; k < to; k++ {
			b[k] = ' '
		}
	}
	i := 0
	for i < len(b) {
		if *inComment {
			end := strings.Index(line[i:], "-->")
			if end < 0 {
				blank(i, len(b))
				return string(b)
			}
			blank(i, i+end+3)
			i += end + 3
			*inComment = false
			continue
		}
		switch {
		case strings.HasPrefix(line[i:], "<!--"):
			*inComment = true
		case b[i] == '`':
			run := 1
			for i+run < len(b) && b[i+run] == '`' {
				run++
			}
			closing := strings.Index(line[i+run:], strings.Repeat("`", run))
			if closing < 0 {
				i += run
				continue
			}
			blank(i, i+run+closing+run)
			i += run + closing + run
		default:
			i++
		}
	}
	return string(b)
}

// parseAttributes reads a token's attributes: name=value, name="quoted
// value" (with \" and \\) or a bare flag.
func parseAttributes(text string) (map[string]string, error) {
	attrs := map[string]string{}
	for strings.TrimSpace(text) != "" {
		m := fieldAttribute.FindStringSubmatchIndex(text)
		if m == nil {
			return nil, fmt.Errorf("cannot read %q", strings.TrimSpace(text))
		}
		key := text[m[2]:m[3]]
		value := "true"
		if m[4] >= 0 {
			value = strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(text[m[4]:m[5]])
		} else if m[6] >= 0 {
			value = text[m[6]:m[7]]
		}
		if _, dup := attrs[key]; dup {
			return nil, fmt.Errorf("%s is given twice", key)
		}
		attrs[key] = value
		text = text[m[1]:]
		if text != "" && text[0] != ' ' && text[0] != '\t' {
			return nil, fmt.Errorf("put a space between attributes")
		}
	}
	return attrs, nil
}

// parseFormTemplate reads a form template. Mistakes are reported so the
// template's author can fix them; a file without a form key is not a form.
func parseFormTemplate(text string) (formTemplate, error) {
	keys, body := splitFrontMatter(text)
	t := formTemplate{Form: keys["form"], Title: keys["title"], SubmitTo: keys["submitTo"], body: body}
	if !isFormKeys(keys) {
		return t, problem(400, "not_a_form", "This file is not a form template: its front matter has no form key, or it is a filled-in copy (submittedAt).")
	}
	if !formNamePattern.MatchString(t.Form) {
		return t, formError("form must be a short name of lowercase letters, digits and hyphens, such as leave-request.")
	}
	if t.Title == "" {
		t.Title = t.Form
	}
	if utf8.RuneCountInString(t.Title) > maxFormTitle || strings.ContainsFunc(t.Title, unicode.IsControl) {
		return t, formError(fmt.Sprintf("title is limited to %d characters.", maxFormTitle))
	}
	if t.SubmitTo == "" {
		return t, formError("submitTo is missing: name the space and folder that receive the answers, such as owner/hr-review/leave/.")
	}
	if err := t.readQuizKeys(keys); err != nil {
		return t, err
	}
	names := map[string]bool{}
	optionsUsed := map[int]bool{}
	var problemText string
	scanFields(body, func(line, start, end int, kind, attrText string) {
		if problemText != "" {
			return
		}
		fail := func(format string, args ...any) {
			problemText = fmt.Sprintf("line %d: ", line+1) + fmt.Sprintf(format, args...)
		}
		kind = strings.ToLower(kind)
		allowed, known := fieldAttributes[kind]
		if !known {
			fail("{{%s}} is not a field type; use text, textarea, number, date, time, email, phone, choice, select, yesno or sign.", kind)
			return
		}
		attrs, err := parseAttributes(attrText)
		if err != nil {
			fail("{{%s}}: %v.", kind, err)
			return
		}
		f := formField{Type: kind, Name: attrs["name"], Label: attrs["label"], attrs: attrs, line: line, firstOption: -1, lastOption: -1}
		for key, value := range attrs {
			switch key {
			case "name", "label", "placeholder":
			case "required":
				f.Required = value == "true"
				if value != "true" {
					fail("required takes no value.")
					return
				}
			default:
				if !allowed[key] {
					fail("{{%s}} does not take %s.", kind, key)
					return
				}
			}
		}
		if !fieldNamePattern.MatchString(f.Name) {
			fail("every field needs a name of letters, digits and _ starting with a letter, such as name=full_name.")
			return
		}
		if names[f.Name] {
			fail("two fields are named %s.", f.Name)
			return
		}
		names[f.Name] = true
		if utf8.RuneCountInString(f.Label) > maxFormLabel || utf8.RuneCountInString(attrs["placeholder"]) > maxFormLabel {
			fail("label and placeholder are limited to %d characters.", maxFormLabel)
			return
		}
		if err := checkFieldAttributes(&f); err != nil {
			fail("%s %v.", f.Name, err)
			return
		}
		if kind == "choice" || kind == "select" {
			if optionsUsed[line] {
				fail("only one choice or select field can start on a line, because its options are the list below it.")
				return
			}
			optionsUsed[line] = true
			f.firstOption, f.lastOption, f.Options = fieldOptions(body, line)
			if len(f.Options) == 0 {
				fail("%s needs its options as a list right below it, such as - ( ) Yes.", f.Name)
				return
			}
			if len(f.Options) > maxFormOptions {
				fail("%s has more than %d options.", f.Name, maxFormOptions)
				return
			}
			seen := map[string]bool{}
			for _, o := range f.Options {
				if seen[o] || utf8.RuneCountInString(o) > maxFormOptionText {
					fail("%s: each option must be different and at most %d characters.", f.Name, maxFormOptionText)
					return
				}
				seen[o] = true
			}
			if f.Multi {
				if lo, hi := intAttr(f.attrs["min"]), intAttr(f.attrs["max"]); hi > len(f.Options) || lo > len(f.Options) {
					fail("%s asks for more picks than it has options.", f.Name)
					return
				}
			}
		}
		if len(t.Fields) == maxFormFields {
			fail("a form can have at most %d fields.", maxFormFields)
			return
		}
		t.Fields = append(t.Fields, f)
	})
	if problemText != "" {
		return t, formError(problemText)
	}
	if len(t.Fields) == 0 {
		return t, formError("it has no fields; add one such as {{text name=full_name required}}.")
	}
	for name := range t.Unmarked {
		if !names[name] {
			return t, formError("unmarked names " + name + ", which is not a field of this quiz.")
		}
	}
	return t, nil
}

func intAttr(value string) int {
	n, _ := strconv.Atoi(value)
	return n
}

// checkFieldAttributes checks the values of a field's attributes.
func checkFieldAttributes(f *formField) error {
	a := f.attrs
	whole := func(key string, lo, hi int) error {
		v, ok := a[key]
		if !ok {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return fmt.Errorf("%s must be a whole number from %d to %d", key, lo, hi)
		}
		return nil
	}
	switch f.Type {
	case "text", "textarea":
		limit := limitTextMax
		if f.Type == "textarea" {
			limit = limitTextareaMax
		}
		if err := whole("min", 0, limit); err != nil {
			return err
		}
		if err := whole("max", 1, limit); err != nil {
			return err
		}
		if err := whole("rows", 1, 40); err != nil {
			return err
		}
		if p, ok := a["pattern"]; ok {
			if len(p) > maxFormPattern {
				return fmt.Errorf("pattern is limited to %d characters", maxFormPattern)
			}
			re, err := regexp.Compile(`^(?:` + p + `)$`)
			if err != nil {
				return fmt.Errorf("pattern is not a valid regular expression")
			}
			f.pattern = re
		}
	case "number":
		for _, key := range []string{"min", "max", "step"} {
			if v, ok := a[key]; ok {
				n, err := strconv.ParseFloat(v, 64)
				if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || key == "step" && n <= 0 {
					return fmt.Errorf("%s must be a number", key)
				}
			}
		}
	case "date":
		for _, key := range []string{"min", "max"} {
			if v, ok := a[key]; ok && v != "today" && !validDate(v) {
				return fmt.Errorf("%s must be a date such as 2026-01-31, or today", key)
			}
		}
	case "time":
		for _, key := range []string{"min", "max"} {
			if v, ok := a[key]; ok && !timePattern.MatchString(v) {
				return fmt.Errorf("%s must be a time such as 09:30", key)
			}
		}
	case "choice":
		f.Multi = a["multi"] == "true"
		if v, ok := a["multi"]; ok && v != "true" {
			return fmt.Errorf("multi takes no value")
		}
		if !f.Multi && (a["min"] != "" || a["max"] != "") {
			return fmt.Errorf("min and max (how many to pick) need multi")
		}
		if err := whole("min", 0, maxFormOptions); err != nil {
			return err
		}
		if err := whole("max", 1, maxFormOptions); err != nil {
			return err
		}
	}
	return nil
}

func validDate(v string) bool {
	if !datePattern.MatchString(v) {
		return false
	}
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}

// fieldOptions reads the list right below line (one blank line may come
// first): its first and last line and each item's text without the bullet
// and any ( ) or [ ] box.
func fieldOptions(body []string, line int) (int, int, []string) {
	i := line + 1
	if i < len(body) && strings.TrimSpace(body[i]) == "" {
		i++
	}
	first := i
	var options []string
	for ; i < len(body); i++ {
		m := optionLine.FindStringSubmatch(body[i])
		if m == nil {
			break
		}
		options = append(options, strings.TrimSpace(m[3]))
	}
	if len(options) == 0 {
		return -1, -1, nil
	}
	return first, i - 1, options
}

// formAnswers holds checked answers: one string per field, or a list for a
// choice with multi.
type formAnswers map[string]any

// checkAnswers checks raw answers against the template's fields; today is
// the submitter's date for date limits given as today. Every problem is
// returned, one per field.
func (t formTemplate) checkAnswers(raw map[string]json.RawMessage, today string) (formAnswers, []FieldError) {
	var problems []FieldError
	out := formAnswers{}
	known := map[string]bool{}
	for _, f := range t.Fields {
		known[f.Name] = true
	}
	for name := range raw {
		if !known[name] {
			problems = append(problems, FieldError{name, "This form has no field of that name; the template may have changed."})
		}
	}
	for _, f := range t.Fields {
		fail := func(message string) { problems = append(problems, FieldError{f.Name, message}) }
		value, present := raw[f.Name]
		if f.Multi {
			var picked []string
			if present && string(value) != "null" && json.Unmarshal(value, &picked) != nil {
				fail("Send the picked options as a list.")
				continue
			}
			seen := map[string]bool{}
			for _, p := range picked {
				if !contains(f.Options, p) || seen[p] {
					fail(fmt.Sprintf("%q is not one of the options.", p))
					picked = nil
					break
				}
				seen[p] = true
			}
			if picked == nil && len(seen) > 0 {
				continue
			}
			lo, hi := intAttr(f.attrs["min"]), intAttr(f.attrs["max"])
			switch {
			case f.Required && len(picked) == 0:
				fail("Pick at least one.")
			case lo > 0 && len(picked) < lo && (len(picked) > 0 || f.Required):
				fail(fmt.Sprintf("Pick at least %d.", lo))
			case hi > 0 && len(picked) > hi:
				fail(fmt.Sprintf("Pick at most %d.", hi))
			default:
				// Keep the template's order.
				ordered := []string{}
				for _, o := range f.Options {
					if seen[o] {
						ordered = append(ordered, o)
					}
				}
				out[f.Name] = ordered
			}
			continue
		}
		var s string
		if present && string(value) != "null" && json.Unmarshal(value, &s) != nil {
			fail("Send the answer as text.")
			continue
		}
		s = strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
		if message := checkAnswer(f, s, today); message != "" {
			fail(message)
			continue
		}
		out[f.Name] = s
	}
	return out, problems
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// checkAnswer returns what is wrong with one answer, or "".
func checkAnswer(f formField, s, today string) string {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && !(f.Type == "textarea" && (r == '\n' || r == '\t'))
	}) {
		return "Use plain text on one line."
	}
	if s == "" {
		if f.Required {
			if f.Type == "sign" {
				return "Sign the form to send it."
			}
			return "This answer is required."
		}
		return ""
	}
	a := f.attrs
	switch f.Type {
	case "text", "textarea":
		n := utf8.RuneCountInString(s)
		limit := defaultTextMax
		if f.Type == "textarea" {
			limit = defaultTextareaMax
		}
		if a["max"] != "" {
			limit = intAttr(a["max"])
		}
		if n > limit {
			return fmt.Sprintf("Use at most %d characters.", limit)
		}
		if lo := intAttr(a["min"]); n < lo {
			return fmt.Sprintf("Use at least %d characters.", lo)
		}
		if f.pattern != nil && !f.pattern.MatchString(s) {
			return "This answer is not in the form asked for."
		}
	case "number":
		n, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) || len(s) > 40 {
			return "Enter a number."
		}
		if v, ok := a["min"]; ok {
			if lo, _ := strconv.ParseFloat(v, 64); n < lo {
				return "Enter " + v + " or more."
			}
		}
		if v, ok := a["max"]; ok {
			if hi, _ := strconv.ParseFloat(v, 64); n > hi {
				return "Enter " + v + " or less."
			}
		}
		if v, ok := a["step"]; ok {
			step, _ := strconv.ParseFloat(v, 64)
			base, _ := strconv.ParseFloat(a["min"], 64)
			if q := (n - base) / step; math.Abs(q-math.Round(q)) > 1e-9 {
				return "Enter a number in steps of " + v + "."
			}
		}
	case "date":
		if !validDate(s) {
			return "Enter a date such as 2026-01-31."
		}
		bound := func(v string) string {
			if v == "today" {
				return today
			}
			return v
		}
		if v, ok := a["min"]; ok && s < bound(v) {
			return "Enter " + bound(v) + " or later."
		}
		if v, ok := a["max"]; ok && s > bound(v) {
			return "Enter " + bound(v) + " or earlier."
		}
	case "time":
		if !timePattern.MatchString(s) {
			return "Enter a time such as 09:30."
		}
		if v, ok := a["min"]; ok && s < v {
			return "Enter " + v + " or later."
		}
		if v, ok := a["max"]; ok && s > v {
			return "Enter " + v + " or earlier."
		}
	case "email":
		if len(s) > 254 || !emailPattern.MatchString(s) {
			return "Enter an email address."
		}
	case "phone":
		digits := 0
		for _, r := range s {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if !phonePattern.MatchString(s) || digits < 6 || digits > 15 {
			return "Enter a phone number."
		}
	case "choice", "select":
		if !contains(f.Options, s) {
			return "Pick one of the options."
		}
	case "yesno":
		if s != "Yes" && s != "No" {
			return "Answer Yes or No."
		}
	case "sign":
		if s != "true" {
			return "Sign by sending true."
		}
	}
	return ""
}

// submission describes who sent a form, and its review, for the filed copy.
type submission struct {
	// Template is owner/slug/path; Revision the state it was checked
	// against and Blob the template text's blob; ID the receipt's.
	Template, Revision, Blob, ID, By string
	At                               time.Time
	// Status is submitted, approved or declined.
	Status string
	// Marks holds a quiz's marks by field: true, false, or nil while a
	// reviewer still has to tick it. Score is set once every mark is in.
	Marks map[string]*bool
	Score *int
	// Set by a review.
	ReviewedBy string
	ReviewedAt time.Time
	Note       string
}

// render is the filed copy: front matter with the answers, then the
// template's text with every field filled in. Comments are left out.
func (t formTemplate) render(answers formAnswers, sub submission) string {
	var out strings.Builder
	out.WriteString("---\n")
	line := func(key string, value any) {
		b, _ := marshalPlain(value)
		out.WriteString(key + ": " + string(b) + "\n")
	}
	out.WriteString("form: " + t.Form + "\n")
	line("title", t.Title)
	line("template", sub.Template)
	out.WriteString("templateRevision: " + sub.Revision + "\n")
	if sub.Blob != "" {
		out.WriteString("templateBlob: " + sub.Blob + "\n")
	}
	if sub.ID != "" {
		out.WriteString("submission: " + sub.ID + "\n")
	}
	line("submittedBy", sub.By)
	out.WriteString("submittedAt: " + sub.At.UTC().Format(time.RFC3339) + "\n")
	status := sub.Status
	if status == "" {
		status = "submitted"
	}
	out.WriteString("status: " + status + "\n")
	if t.Quiz {
		out.WriteString("quiz: true\n")
		out.WriteString("passMark: " + strconv.Itoa(t.PassMark) + "\n")
		if sub.Score != nil {
			out.WriteString("score: " + strconv.Itoa(*sub.Score) + "\n")
		}
		if sub.Marks != nil {
			out.WriteString("marks:\n")
			for _, f := range t.Fields {
				if m, ok := sub.Marks[f.Name]; ok {
					b, _ := json.Marshal(m)
					out.WriteString("  " + f.Name + ": " + string(b) + "\n")
				}
			}
		}
	}
	if sub.ReviewedBy != "" {
		line("reviewedBy", sub.ReviewedBy)
		out.WriteString("reviewedAt: " + sub.ReviewedAt.UTC().Format(time.RFC3339) + "\n")
		if sub.Note != "" {
			line("note", sub.Note)
		}
	}
	out.WriteString("answers:\n")
	for _, f := range t.Fields {
		value, ok := answers[f.Name]
		if !ok {
			value = ""
		}
		if f.Type == "sign" && value == "true" {
			value = sub.By + ", " + sub.At.UTC().Format(time.RFC3339)
		}
		b, _ := marshalPlain(value)
		out.WriteString("  " + f.Name + ": " + string(b) + "\n")
	}
	out.WriteString("---\n")

	byLine := map[int][]formField{}
	options := map[int]formField{}
	for _, f := range t.Fields {
		byLine[f.line] = append(byLine[f.line], f)
		for i := f.firstOption; i >= 0 && i <= f.lastOption; i++ {
			options[i] = f
		}
	}
	body := stripComments(t.body)
	blank := true
	for i, text := range body {
		if text == commentOnly || text == "" && blank {
			continue
		}
		blank = text == ""
		if f, ok := options[i]; ok {
			if m := optionLine.FindStringSubmatch(text); m != nil {
				box := "[ ]"
				chosen := strings.TrimSpace(m[3])
				switch v := answers[f.Name].(type) {
				case string:
					if v == chosen {
						box = "[x]"
					}
				case []string:
					if contains(v, chosen) {
						box = "[x]"
					}
				}
				out.WriteString(m[1] + m[2] + " " + box + " " + escapeInline(chosen) + "\n")
				continue
			}
		}
		if len(byLine[i]) == 0 {
			out.WriteString(text + "\n")
			continue
		}
		var after []string
		var b strings.Builder
		last := 0
		inComment := false
		for _, m := range fieldToken.FindAllStringSubmatchIndex(maskLine(text, &inComment), -1) {
			attrs, err := parseAttributes(text[m[4]:m[5]])
			var field *formField
			for k := range byLine[i] {
				if err == nil && byLine[i][k].Name == attrs["name"] {
					field = &byLine[i][k]
				}
			}
			if field == nil {
				continue
			}
			b.WriteString(text[last:m[0]])
			last = m[1]
			value, _ := answers[field.Name].(string)
			switch field.Type {
			case "choice", "select":
				// The list below shows the choice.
			case "textarea":
				after = append(after, quoteBlock(value))
			case "sign":
				if value == "true" {
					b.WriteString("**Signed by " + escapeInline(sub.By) + ", " + sub.At.UTC().Format("2006-01-02 15:04") + " UTC**")
				} else {
					b.WriteString("_(not signed)_")
				}
			default:
				if value == "" {
					b.WriteString("_(blank)_")
				} else {
					b.WriteString("**" + escapeInline(value) + "**")
				}
			}
			// A marked quiz answer is followed by a tick or a cross.
			if mark := sub.Marks[field.Name]; mark != nil {
				if s := b.String(); s != "" && !strings.HasSuffix(s, " ") {
					b.WriteString(" ")
				}
				if *mark {
					b.WriteString("✓")
				} else {
					b.WriteString("✗")
				}
			}
		}
		b.WriteString(text[last:])
		filled := strings.TrimRight(b.String(), " \t")
		if strings.TrimSpace(filled) != "" || len(after) == 0 {
			out.WriteString(filled + "\n")
		}
		for _, block := range after {
			out.WriteString(block)
		}
	}
	return strings.TrimRight(out.String(), "\n") + "\n"
}

// commentOnly marks a line that held only a comment, which the copy leaves
// out, so it has no gaps where the template's notes were.
const commentOnly = "\x00"

// stripComments removes <!-- --> comments outside code from the lines.
func stripComments(lines []string) []string {
	out := make([]string, len(lines))
	fence := ""
	inComment := false
	for i, line := range lines {
		if fence == "" && !inComment {
			if m := fenceLine.FindStringSubmatch(line); m != nil {
				fence = m[1]
				out[i] = line
				continue
			}
		} else if fence != "" {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, fence[:3]) && strings.Trim(t, fence[:1]) == "" && len(t) >= len(fence) {
				fence = ""
			}
			out[i] = line
			continue
		}
		var b strings.Builder
		rest := line
		for rest != "" {
			if inComment {
				end := strings.Index(rest, "-->")
				if end < 0 {
					rest = ""
					break
				}
				rest = rest[end+3:]
				inComment = false
				continue
			}
			start := strings.Index(rest, "<!--")
			if start < 0 {
				b.WriteString(rest)
				break
			}
			b.WriteString(rest[:start])
			rest = rest[start+4:]
			inComment = true
		}
		out[i] = b.String()
		if strings.TrimSpace(out[i]) == "" && strings.TrimSpace(line) != "" {
			out[i] = commentOnly
		}
	}
	return out
}

// quoteBlock shows a multi-line answer as a block quote of its own.
func quoteBlock(value string) string {
	if value == "" {
		return "> _(blank)_\n"
	}
	var b strings.Builder
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			b.WriteString(">\n")
			continue
		}
		b.WriteString("> " + escapeLineStart(escapeInline(line)) + "\n")
	}
	return b.String()
}

// escapeInline keeps an answer from becoming Markdown or a field token.
func escapeInline(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_[]<>|~{}#!", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

var listStart = regexp.MustCompile(`^(\d+)([.)])`)

// escapeLineStart keeps a quoted line from starting a list or a heading.
func escapeLineStart(s string) string {
	if s != "" && strings.ContainsRune("-+=", rune(s[0])) {
		return "\\" + s
	}
	if m := listStart.FindStringSubmatch(s); m != nil {
		return m[1] + "\\" + s[len(m[1]):]
	}
	return s
}

// marshalPlain encodes a front matter value as JSON, which YAML reads too,
// without escaping < > &.
func marshalPlain(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
