package service

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Guided forms (Core 0.95.19): a field's default answer and questions asked
// only when an earlier answer calls for them.
//
//	Reported by: {{text name=reporter default=@name}}
//	Damaged? {{yesno name=damaged required}}
//
//	What is damaged? {{textarea name=details required show-if="damaged=Yes"}}
//
// default= fills the answer in before the person starts; it may be text or
// one of @name, @email, @today and @now. An answer left out of a submission
// takes the default too, so mtsync and agents need only send what differs.
//
// show-if= names an earlier field and what its answer must be. A field with
// it is shown with the block it is in (its paragraph, or its own line in a
// list or table, with a choice's options), and every field in that block
// shares the condition. A hidden question is not asked: its answer is not
// required, is ignored when sent, and the block is left out of the filed
// copy. Slate's src/forms.js reads the same syntax.

// formContext is what defaults are filled in from: the sender's name and
// address, and their local date and time.
type formContext struct {
	Today, Now, Name, Email string
}

// defaultTokens are the @ defaults and the field types that take each.
var defaultTokens = map[string]map[string]bool{
	"@name":  {"text": true, "textarea": true},
	"@email": {"text": true, "textarea": true, "email": true},
	"@today": {"text": true, "textarea": true, "date": true},
	"@now":   {"text": true, "textarea": true, "time": true},
}

// resolveDefault is a field's default answer for the sender: a list for a
// choice with multi, otherwise text.
func (f formField) resolveDefault(c formContext) any {
	d := f.attrs["default"]
	if f.Multi {
		picked := []string{}
		if d == "" {
			return picked
		}
		for _, v := range strings.Split(d, "|") {
			picked = append(picked, strings.TrimSpace(v))
		}
		return picked
	}
	switch d {
	case "@name":
		return c.Name
	case "@email":
		return c.Email
	case "@today":
		return c.Today
	case "@now":
		if f.Type == "time" {
			return c.Now
		}
		return strings.TrimSpace(c.Today + " " + c.Now)
	}
	return d
}

// checkDefault checks a field's default= when the template is read. The
// filled-in default is checked again like any answer when the form is sent.
func checkDefault(f formField) error {
	d, ok := f.attrs["default"]
	if !ok {
		return nil
	}
	if f.Type == "sign" {
		return fmt.Errorf("can't have a default: only the person signs")
	}
	if f.Type == "photo" {
		return fmt.Errorf("can't have a default: photos are taken or chosen when the form is filled in")
	}
	if strings.HasPrefix(d, "@") {
		types, known := defaultTokens[d]
		if !known {
			return fmt.Errorf("default %s is not one of @name, @email, @today or @now", d)
		}
		if !types[f.Type] {
			return fmt.Errorf("can't take default %s", d)
		}
		return nil
	}
	if d == "" {
		return fmt.Errorf("default needs a value")
	}
	bad := func(what string) error { return fmt.Errorf("default must be %s", what) }
	switch f.Type {
	case "choice", "select":
		values := []string{d}
		if f.Multi {
			values = strings.Split(d, "|")
		}
		for _, v := range values {
			if !contains(f.Options, strings.TrimSpace(v)) {
				return fmt.Errorf("default %q is not one of its options", strings.TrimSpace(v))
			}
		}
	case "yesno":
		if d != "Yes" && d != "No" {
			return bad("Yes or No")
		}
	case "number":
		if n, err := strconv.ParseFloat(d, 64); err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return bad("a number")
		}
	case "date":
		if !validDate(d) {
			return bad("a date such as 2026-01-31, or @today")
		}
	case "time":
		if !timePattern.MatchString(d) {
			return bad("a time such as 09:30, or @now")
		}
	case "email":
		if !emailPattern.MatchString(d) {
			return bad("an email address, or @email")
		}
	default:
		if utf8.RuneCountInString(d) > limitTextareaMax {
			return bad("shorter")
		}
	}
	return nil
}

// showIf is a field's condition: the earlier field it names, how its answer
// is compared (answered, unanswered, =, !=, >, >=, < or <=) and the values.
type showIf struct {
	Field, Op string
	Values    []string
}

var showIfPattern = regexp.MustCompile(`^\s*(!?)\s*([A-Za-z][A-Za-z0-9_]{0,63})\s*(?:(!=|>=|<=|=|>|<)(.*))?$`)

// parseShowIf reads a show-if value: name (answered), !name (not answered),
// or name, an operator and values, where = and != take several values
// separated by |.
func parseShowIf(text string) (*showIf, error) {
	m := showIfPattern.FindStringSubmatch(text)
	if m == nil {
		return nil, fmt.Errorf(`show-if must be a field's name and what its answer must be, such as show-if="damaged=Yes"`)
	}
	c := &showIf{Field: m[2], Op: m[3]}
	switch {
	case m[3] == "" && m[1] == "!":
		c.Op = "unanswered"
	case m[3] == "":
		c.Op = "answered"
	case m[1] == "!":
		return nil, fmt.Errorf(`show-if takes ! only before a name on its own, such as show-if="!phone"`)
	default:
		for _, v := range strings.Split(m[4], "|") {
			v = strings.TrimSpace(v)
			if v == "" {
				return nil, fmt.Errorf("show-if needs a value after %s", m[3])
			}
			c.Values = append(c.Values, v)
		}
		if len(c.Values) > 1 && c.Op != "=" && c.Op != "!=" {
			return nil, fmt.Errorf("show-if takes several values (with |) only after = or !=")
		}
	}
	return c, nil
}

func (c *showIf) String() string {
	switch c.Op {
	case "answered":
		return c.Field
	case "unanswered":
		return "!" + c.Field
	}
	return c.Field + c.Op + strings.Join(c.Values, "|")
}

// checkShowIf checks a condition against the field it names.
func checkShowIf(c *showIf, on formField) error {
	if on.Type == "sign" {
		return fmt.Errorf("show-if can't depend on a signature")
	}
	if on.Type == "photo" {
		return fmt.Errorf("show-if can't depend on a photo")
	}
	if c.Op == "answered" || c.Op == "unanswered" {
		return nil
	}
	ordered := c.Op == ">" || c.Op == ">=" || c.Op == "<" || c.Op == "<="
	if ordered && on.Type != "number" && on.Type != "date" && on.Type != "time" {
		return fmt.Errorf("show-if compares with %s only numbers, dates and times; %s is %s", c.Op, on.Name, on.Type)
	}
	for _, v := range c.Values {
		switch on.Type {
		case "choice", "select":
			if !contains(on.Options, v) {
				return fmt.Errorf("show-if: %q is not one of %s's options", v, on.Name)
			}
		case "yesno":
			if v != "Yes" && v != "No" {
				return fmt.Errorf("show-if: %s is answered Yes or No", on.Name)
			}
		case "number":
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				return fmt.Errorf("show-if: %s is a number, and %q is not", on.Name, v)
			}
		case "date":
			if v != "today" && !validDate(v) {
				return fmt.Errorf("show-if: %s is a date, such as 2026-01-31 or today", on.Name)
			}
		case "time":
			if !timePattern.MatchString(v) {
				return fmt.Errorf("show-if: %s is a time, such as 09:30", on.Name)
			}
		}
	}
	return nil
}

// holds reports whether the condition is met by answer, the named field's
// answer (blank when that question was not asked).
func (c *showIf) holds(on formField, answer any, today string) bool {
	var given []string
	switch a := answer.(type) {
	case string:
		if s := strings.TrimSpace(a); s != "" {
			given = []string{s}
		}
	case []string:
		given = a
	}
	switch c.Op {
	case "answered":
		return len(given) > 0
	case "unanswered":
		return len(given) == 0
	case "=", "!=":
		match := false
		for _, g := range given {
			for _, v := range c.Values {
				if sameValue(on, g, v, today) {
					match = true
				}
			}
		}
		if c.Op == "=" {
			return match
		}
		return !match
	}
	if len(given) != 1 {
		return false
	}
	cmp, ok := compareValue(on, given[0], c.Values[0], today)
	if !ok {
		return false
	}
	switch c.Op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	}
	return cmp <= 0
}

func sameValue(on formField, got, want, today string) bool {
	if on.Type == "number" || on.Type == "date" {
		cmp, ok := compareValue(on, got, want, today)
		return ok && cmp == 0
	}
	return got == want
}

// compareValue orders a number, date or time answer against a value.
func compareValue(on formField, got, want, today string) (int, bool) {
	if on.Type == "number" {
		a, err1 := strconv.ParseFloat(got, 64)
		b, err2 := strconv.ParseFloat(want, 64)
		if err1 != nil || err2 != nil {
			return 0, false
		}
		switch {
		case a < b:
			return -1, true
		case a > b:
			return 1, true
		}
		return 0, true
	}
	if on.Type == "date" && want == "today" {
		want = today
	}
	return strings.Compare(got, want), true
}

var (
	headingLine  = regexp.MustCompile(`^ {0,3}#{1,6}(\s|$)`)
	listItemLine = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s`)
)

// fieldBlock is the lines a field's show-if hides: its paragraph (up to a
// blank line, heading, list, table or code), or its own line when it is in a
// list item or table row, and a choice's options.
func fieldBlock(body []string, f formField) (int, int) {
	own := func(line string) bool {
		t := strings.TrimSpace(line)
		return strings.HasPrefix(t, "|") || listItemLine.MatchString(line)
	}
	stops := func(line string) bool {
		return strings.TrimSpace(line) == "" || headingLine.MatchString(line) || fenceLine.MatchString(line) || own(line)
	}
	start, end := f.line, f.line
	if !own(body[f.line]) {
		for start > 0 && !stops(body[start-1]) {
			start--
		}
		for end+1 < len(body) && !stops(body[end+1]) {
			end++
		}
	}
	if f.lastOption > end {
		end = f.lastOption
	}
	return start, end
}

// readShowIf reads every field's default and show-if once the fields are
// known: a show-if names a field asked before its block, and the fields in
// one block share its condition.
func (t *formTemplate) readShowIf() error {
	index := map[string]int{}
	for i, f := range t.Fields {
		index[f.Name] = i
	}
	type block struct {
		start, end int
		cond       *showIf
		by         string
	}
	var blocks []*block
	at := make([]*block, len(t.Fields))
	for i := range t.Fields {
		f := &t.Fields[i]
		if err := checkDefault(*f); err != nil {
			return formError(fmt.Sprintf("line %d: %s %v.", f.line+1, f.Name, err))
		}
		start, end := fieldBlock(t.body, *f)
		var b *block
		for _, other := range blocks {
			if start <= other.end && end >= other.start {
				b = other
			}
		}
		if b == nil {
			b = &block{start: start, end: end}
			blocks = append(blocks, b)
		}
		b.start, b.end = min(b.start, start), max(b.end, end)
		at[i] = b
		text, ok := f.attrs["show-if"]
		if !ok {
			continue
		}
		c, err := parseShowIf(text)
		if err != nil {
			return formError(fmt.Sprintf("line %d: %s %v.", f.line+1, f.Name, err))
		}
		if b.cond != nil && b.cond.String() != c.String() {
			return formError(fmt.Sprintf("line %d: %s and %s are in the same paragraph with different show-if; fields in one paragraph are shown together, so give it once, or put a blank line between them.", f.line+1, b.by, f.Name))
		}
		b.cond, b.by = c, f.Name
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		b := at[i]
		if b.cond == nil {
			continue
		}
		j, ok := index[b.cond.Field]
		if !ok {
			return formError(fmt.Sprintf("line %d: show-if names %s, which is not a field of this form.", f.line+1, b.cond.Field))
		}
		if j >= i || at[j] == b {
			return formError(fmt.Sprintf("line %d: show-if names %s, which must be asked before this paragraph.", f.line+1, b.cond.Field))
		}
		if err := checkShowIf(b.cond, t.Fields[j]); err != nil {
			return formError(fmt.Sprintf("line %d: %v.", f.line+1, err))
		}
		f.showIf, f.hideFrom, f.hideTo = b.cond, b.start, b.end
		f.ShowIf = b.cond.String()
	}
	return nil
}

// asked reports which fields the person is asked, given their answers: a
// field with show-if is asked when the field it names was asked and its
// answer meets the condition.
func (t formTemplate) asked(answers formAnswers, today string) map[string]bool {
	asked := map[string]bool{}
	byName := map[string]formField{}
	for _, f := range t.Fields {
		byName[f.Name] = f
		if f.showIf == nil {
			asked[f.Name] = true
			continue
		}
		on := byName[f.showIf.Field]
		var answer any
		if asked[on.Name] {
			answer = answers[on.Name]
		}
		asked[f.Name] = f.showIf.holds(on, answer, today)
	}
	return asked
}

// hiddenLines are the body lines of the blocks not asked.
func (t formTemplate) hiddenLines(asked map[string]bool) map[int]bool {
	hidden := map[int]bool{}
	for _, f := range t.Fields {
		if f.showIf != nil && !asked[f.Name] {
			for i := f.hideFrom; i <= f.hideTo; i++ {
				hidden[i] = true
			}
		}
	}
	return hidden
}
