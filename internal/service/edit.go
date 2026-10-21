package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
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
	if in.Query == "" || len(in.Query) > maxSearchQuery || strings.ContainsAny(in.Query, "\n\x00") {
		return nil, invalid(fmt.Sprintf("query takes 1 to %d characters on one line.", maxSearchQuery))
	}
	if in.Prefix != "" && (!strings.HasSuffix(in.Prefix, "/") || !validPath(strings.TrimSuffix(in.Prefix, "/")) || strings.Count(in.Prefix, "/") > 7) {
		return nil, invalid("Invalid folder prefix.")
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
	var match func(string) bool
	if in.Regex {
		pattern := in.Query
		if in.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, invalid("query is not a valid regular expression (RE2 syntax): " + err.Error())
		}
		match = re.MatchString
	} else if in.IgnoreCase {
		query := strings.ToLower(in.Query)
		match = func(line string) bool { return strings.Contains(strings.ToLower(line), query) }
	} else {
		match = func(line string) bool { return strings.Contains(line, in.Query) }
	}
	state, files, err := r.snapshot(ctx, in.Revision)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for p := range files {
		if strings.HasPrefix(p, in.Prefix) {
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
		lines := splitLines(string(texts[files[p].Blob]))
		for i, line := range lines {
			line = trimLineEnd(line)
			if !match(line) {
				continue
			}
			if len(result.Matches) == in.Limit {
				result.Truncated = true
				return result, nil
			}
			m := SearchMatch{Path: p, ID: files[p].ID, Line: i + 1, Text: clipLine(line)}
			for j := max(0, i-in.Context); j < i; j++ {
				m.Before = append(m.Before, clipLine(trimLineEnd(lines[j])))
			}
			for j := i + 1; j < len(lines) && j <= i+in.Context; j++ {
				m.After = append(m.After, clipLine(trimLineEnd(lines[j])))
			}
			result.Matches = append(result.Matches, m)
		}
	}
	return result, nil
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
