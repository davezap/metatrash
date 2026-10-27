package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Edit replaces one exact piece of a file's text. Edits let an agent change
// part of a file without sending the whole text back.
type Edit struct {
	OldText    string `json:"oldText"`
	NewText    string `json:"newText"`
	ReplaceAll bool   `json:"replaceAll,omitempty"`
}

// LineRange describes the lines a read with startLine or lineCount returned.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
	Total int `json:"total"`
}

type SearchMatch struct {
	Path   string   `json:"path"`
	ID     string   `json:"id"`
	Line   int      `json:"line"`
	Text   string   `json:"text"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

type SearchResult struct {
	Space   string        `json:"space"`
	State   string        `json:"state"`
	Matches []SearchMatch `json:"matches"`
	// Truncated is set when more lines matched than limit.
	Truncated bool `json:"truncated"`
}

const (
	maxEdits          = 50
	maxSearchQuery    = 500
	maxSearchContext  = 10
	maxSearchLimit    = 200
	maxSearchLineText = 400
	maxReadLines      = 5000
)

// splitLines cuts text after each "\n", keeping line endings, so the lines
// join back to the exact text. A last line without "\n" is still a line;
// empty text has none.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// lineRange cuts text to lineCount lines from startLine (1-based); zero
// values mean the first line and the rest of the file.
func lineRange(text string, startLine, lineCount int) (string, LineRange, error) {
	lines := splitLines(text)
	if startLine == 0 {
		startLine = 1
	}
	if startLine > len(lines) && !(startLine == 1 && len(lines) == 0) {
		return "", LineRange{}, invalid(fmt.Sprintf("startLine %d is past the end of the file, which has %d lines.", startLine, len(lines)))
	}
	end := len(lines)
	if lineCount > 0 && startLine-1+lineCount < end {
		end = startLine - 1 + lineCount
	}
	if len(lines) == 0 {
		return "", LineRange{Start: 1, End: 0, Total: 0}, nil
	}
	return strings.Join(lines[startLine-1:end], ""), LineRange{Start: startLine, End: end, Total: len(lines)}, nil
}

// checkEditInput validates an edit request before it is queued.
func checkEditInput(in Input) error {
	if !validPath(in.Path) {
		return invalid("Valid path is required.")
	}
	if in.IfInState != "" && !hashPattern.MatchString(in.IfInState) {
		return invalid("ifInState must be a commit hash from read, list or search.")
	}
	if len(in.Edits) == 0 || len(in.Edits) > maxEdits {
		return invalid(fmt.Sprintf("edits takes 1 to %d replacements.", maxEdits))
	}
	for i, e := range in.Edits {
		if e.OldText == "" {
			return invalid(fmt.Sprintf("edit %d: oldText is empty; to add text, include the text next to it in oldText and newText.", i+1))
		}
		if strings.ContainsRune(e.NewText, 0) {
			return invalid("Text must be UTF-8 without NUL.")
		}
	}
	return nil
}

// edit applies edits to the file's current text and writes the result. It
// runs on the write queue. ifInState is optional and checked per file (unless
// strict), and each oldText must still match exactly, so a change by someone
// else to the same lines is refused rather than overwritten.
func (s *Service) edit(ctx context.Context, r *repository, space string, in Input) (any, error) {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	f, ok := files[in.Path]
	if !ok {
		return nil, missing()
	}
	if f.Virtual != nil {
		return nil, virtualRefused(in.Path)
	}
	rebased := in.IfInState != "" && in.IfInState != head
	if rebased && (in.Strict || !r.unchangedSince(ctx, in.IfInState, files, in.Path)) {
		message := in.Path + " changed since that state; read it again before editing."
		if in.Strict {
			message = "Space changed; re-read before retrying."
		}
		return nil, &Error{Status: 409, Code: "state_mismatch", Message: message, CurrentState: head}
	}
	b, err := r.blob(ctx, f.Blob)
	if err != nil {
		return nil, err
	}
	text, err := applyEdits(in.Path, string(b), in.Edits)
	if err != nil {
		return nil, err
	}
	if int64(len(text)) > r.limits.MaxFileBytes {
		return nil, problem(413, "payload_too_large", "The edited text exceeds the file limit.")
	}
	in.Text, in.IfInState = &text, head
	value, err := s.mutate(ctx, r, space, "write", in)
	if v, ok := value.(WriteResult); ok && err == nil {
		v.Rebased = rebased
		return v, nil
	}
	return value, err
}

// checkAppendInput validates an append request before it is queued.
func checkAppendInput(in Input) error {
	if !validPath(in.Path) {
		return invalid("Valid path is required.")
	}
	if in.Text == nil || *in.Text == "" {
		return invalid("text is required.")
	}
	if !utf8.ValidString(*in.Text) || strings.ContainsRune(*in.Text, 0) {
		return invalid("Text must be UTF-8 without NUL.")
	}
	if in.Line < 0 {
		return invalid("line must be 1 or more; leave it out to add at the end.")
	}
	return nil
}

// appendText adds text to a file without the agent reading it: at the end,
// or before in.Line. Without a line it creates a missing file. It runs on
// the write queue against the head, so it needs no ifInState.
func (s *Service) appendText(ctx context.Context, r *repository, space string, in Input) (any, error) {
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return nil, err
	}
	text := ""
	if f, ok := files[in.Path]; ok {
		if f.Virtual != nil {
			return nil, virtualRefused(in.Path)
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return nil, err
		}
		text = string(b)
	} else if in.Line > 0 {
		return nil, problem(404, "not_found", fmt.Sprintf("%s does not exist, so it has no line %d; leave line out to create it.", in.Path, in.Line))
	}
	text, err = insertText(text, *in.Text, in.Line)
	if err != nil {
		return nil, err
	}
	if int64(len(text)) > r.limits.MaxFileBytes {
		return nil, problem(413, "payload_too_large", "The text would exceed the file limit.")
	}
	in.Text, in.IfInState, in.Strict, in.CreateOnly = &text, head, false, false
	return s.mutate(ctx, r, space, "write", in)
}

// insertText puts add into text before line (1-based), or at the end when
// line is 0 or one past the last line. Added text starts on a line of its
// own and, before an existing line, ends with a line break; in a file with
// CRLF endings, LF line breaks in add become CRLF.
func insertText(text, add string, line int) (string, error) {
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
		if !strings.Contains(add, "\r") {
			add = strings.ReplaceAll(add, "\n", "\r\n")
		}
	}
	lines := splitLines(text)
	if line > len(lines)+1 {
		has := fmt.Sprintf("%d lines", len(lines))
		if len(lines) == 1 {
			has = "1 line"
		}
		return "", invalid(fmt.Sprintf("line %d is past the end of the file, which has %s; leave line out to add at the end.", line, has))
	}
	if line == 0 || line == len(lines)+1 {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += newline
		}
		return text + add, nil
	}
	if !strings.HasSuffix(add, "\n") {
		add += newline
	}
	before := strings.Join(lines[:line-1], "")
	return before + add + text[len(before):], nil
}

// applyEdits applies each edit in turn to text. An oldText must match
// exactly once (or at least once with replaceAll). When a file uses CRLF
// line endings and the edit was written with LF, the edit is matched and
// applied with CRLF instead.
func applyEdits(path, text string, edits []Edit) (string, error) {
	for i, e := range edits {
		old, next := e.OldText, e.NewText
		n := strings.Count(text, old)
		if n == 0 && strings.Contains(text, "\r\n") && strings.Contains(old, "\n") && !strings.Contains(old, "\r") {
			crlf := strings.ReplaceAll(old, "\n", "\r\n")
			if m := strings.Count(text, crlf); m > 0 {
				old, n = crlf, m
				if !strings.Contains(next, "\r") {
					next = strings.ReplaceAll(next, "\n", "\r\n")
				}
			}
		}
		switch {
		case n == 0:
			return "", problem(409, "conflict", fmt.Sprintf("edit %d: oldText was not found in %s. It must match the current text exactly, including spaces and line breaks; read or search the file again.", i+1, path))
		case n > 1 && !e.ReplaceAll:
			return "", problem(409, "conflict", fmt.Sprintf("edit %d: oldText matches %d places in %s. Include more of the surrounding text so it matches once, or set replaceAll.", i+1, n, path))
		case e.ReplaceAll:
			text = strings.ReplaceAll(text, old, next)
		default:
			text = strings.Replace(text, old, next, 1)
		}
	}
	return text, nil
}

// search finds lines matching in.Query in the files under in.Prefix at the
// space's head (or in.Revision).
func (s *Service) search(ctx context.Context, r *repository, space string, in Input) (any, error) {
	if in.Prefix != "" && (!strings.HasSuffix(in.Prefix, "/") || !validPath(strings.TrimSuffix(in.Prefix, "/")) || strings.Count(in.Prefix, "/") > 7) {
		return nil, invalid("Invalid folder prefix.")
	}
	match, err := searchMatcher(&in)
	if err != nil {
		return nil, err
	}
	state, files, err := r.snapshot(ctx, in.Revision)
	if err != nil {
		return nil, err
	}
	// Virtual files are left out: their text is outside Git (read them).
	paths := []string{}
	for p, f := range files {
		if strings.HasPrefix(p, in.Prefix) && f.Virtual == nil {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	hashes := make([]string, 0, len(paths))
	for _, p := range paths {
		hashes = append(hashes, files[p].Blob)
	}
	texts, err := r.blobs(ctx, hashes)
	if err != nil {
		return nil, err
	}
	result := SearchResult{Space: space, State: state, Matches: []SearchMatch{}}
	for _, p := range paths {
		if !searchLines(&result, p, files[p].ID, string(texts[files[p].Blob]), match, in) {
			break
		}
	}
	return result, nil
}

// searchMatcher checks a search's query, context and limit (setting the
// default limit) and returns the line matcher it asks for.
func searchMatcher(in *Input) (func(string) bool, error) {
	if in.Query == "" || len(in.Query) > maxSearchQuery || strings.ContainsAny(in.Query, "\n\x00") {
		return nil, invalid(fmt.Sprintf("query takes 1 to %d characters on one line.", maxSearchQuery))
	}
	if in.Context < 0 || in.Context > maxSearchContext {
		return nil, invalid(fmt.Sprintf("context must be 0..%d.", maxSearchContext))
	}
	if in.Limit == 0 {
		in.Limit = 50
	}
	if in.Limit < 1 || in.Limit > maxSearchLimit {
		return nil, invalid(fmt.Sprintf("limit must be 1..%d.", maxSearchLimit))
	}
	if in.Regex {
		pattern := in.Query
		if in.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, invalid("query is not a valid regular expression (RE2 syntax): " + err.Error())
		}
		return re.MatchString, nil
	}
	if in.IgnoreCase {
		query := strings.ToLower(in.Query)
		return func(line string) bool { return strings.Contains(strings.ToLower(line), query) }, nil
	}
	return func(line string) bool { return strings.Contains(line, in.Query) }, nil
}

// searchLines adds one file's matching lines to result. It returns false once
// the limit is reached (result is then truncated).
func searchLines(result *SearchResult, path, id, text string, match func(string) bool, in Input) bool {
	lines := splitLines(text)
	for i, line := range lines {
		line = trimLineEnd(line)
		if !match(line) {
			continue
		}
		if len(result.Matches) == in.Limit {
			result.Truncated = true
			return false
		}
		m := SearchMatch{Path: path, ID: id, Line: i + 1, Text: clipLine(line)}
		for j := max(0, i-in.Context); j < i; j++ {
			m.Before = append(m.Before, clipLine(trimLineEnd(lines[j])))
		}
		for j := i + 1; j < len(lines) && j <= i+in.Context; j++ {
			m.After = append(m.After, clipLine(trimLineEnd(lines[j])))
		}
		result.Matches = append(result.Matches, m)
	}
	return true
}

func trimLineEnd(line string) string {
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
}

// clipLine shortens a long line for search results, at a UTF-8 boundary.
func clipLine(line string) string {
	if len(line) <= maxSearchLineText {
		return line
	}
	cut := maxSearchLineText
	for cut > 0 && line[cut]&0xC0 == 0x80 {
		cut--
	}
	return line[:cut] + "…"
}
