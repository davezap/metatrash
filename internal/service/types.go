package service

import (
	"encoding/json"
	"strings"
)

type Error struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	CurrentState      string `json:"currentState,omitempty"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
	// Fields lists each refused answer of a form submission.
	Fields []FieldError `json:"fields,omitempty"`
	Status int          `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func problem(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}
func invalid(message string) *Error { return problem(400, "invalid_request", message) }
func missing() *Error               { return problem(404, "not_found", "File or revision not found.") }

type File struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Protected bool   `json:"protected"`
	// Protected and Deletable are derived on every snapshot and never stored:
	// a folder's AGENTS.md and .metatrash.json are protected (see
	// folder_files.go), which delete and move refuse.
	Deletable bool `json:"deletable"`
	// ReadOnly, also derived, is the lock a readOnly entry in its folder's
	// settings puts on the file: "agents" or "everyone" (see locks.go).
	ReadOnly string `json:"readOnly,omitempty"`
	// Blob is the Git blob hash of the text. Read, write, edit, append, move
	// and delete results carry it, and list with blobs=true or since=, so a
	// client can tell a changed file without reading it (read's ifBlob).
	Blob string `json:"blob,omitempty"`
	// Virtual is set on a virtual file (0.60.0, virtual.go): the text is
	// its pointer, and this describes the content kept outside Git.
	Virtual *VirtualInfo `json:"virtual,omitempty"`
}

type record struct {
	File
	Blob string `json:"blob"`
}

type ReadResult struct {
	Space string `json:"space"`
	State string `json:"state"`
	File  struct {
		File
		Text string `json:"text"`
	} `json:"file"`
	// Lines is set when the read asked for startLine or lineCount; Text then
	// holds only those lines.
	Lines *LineRange `json:"lines,omitempty"`
	// Effective is set when the file is a .metatrash.json that inherits: the
	// folder's settings with the inherited ones filled in.
	Effective json.RawMessage `json:"effective,omitempty"`
	// Note explains a virtual file's text: its Markdown, or why it has none.
	Note string `json:"note,omitempty"`
	// Unchanged is set when the read gave ifBlob and the file still has that
	// blob; Text is then empty, so an agent re-checking a file it holds pays
	// for no text.
	Unchanged bool `json:"unchanged,omitempty"`
}

type ListResult struct {
	Space      string  `json:"space"`
	State      string  `json:"state"`
	Files      []File  `json:"files"`
	NextCursor *string `json:"nextCursor"`
	// Removed lists paths gone since the state a REST list with since= named;
	// Files then holds only what was added or changed (see listChanges).
	Removed []string `json:"removed,omitempty"`
}

type Mutation struct {
	Space    string `json:"space"`
	OldState string `json:"oldState"`
	NewState string `json:"newState"`
	File     File   `json:"file"`
	// Hint reminds the agent of changes in a GitHub folder that have waited
	// unpushed for a while (see unpushedHint).
	Hint string `json:"hint,omitempty"`
	// Rebased is set when ifInState was older than the space's head and the
	// change was applied on top of the head, because the files it touches
	// had not changed (see unchangedSince). Other files may have.
	Rebased bool `json:"rebased,omitempty"`
}

type WriteResult struct {
	Mutation
	Created bool `json:"created"`
	Changed bool `json:"changed"`
}

type HistoryEntry struct {
	Revision  string `json:"revision"`
	Path      string `json:"path"`
	Operation string `json:"operation"`
	Timestamp string `json:"timestamp"`
	// By is who made the change (see authors.go); absent on older commits
	// and the service's own.
	By *changeAuthor `json:"by,omitempty"`
	// Published lists the versions of the file's publishing folder whose
	// text of the file this is (see publish.go).
	Published []int `json:"published,omitempty"`
}

type HistoryResult struct {
	Space      string         `json:"space"`
	State      string         `json:"state"`
	ID         string         `json:"id"`
	Entries    []HistoryEntry `json:"entries"`
	NextCursor *string        `json:"nextCursor"`
}

type Input struct {
	Path       string  `json:"-"`
	Revision   string  `json:"-"`
	Prefix     string  `json:"-"`
	ID         string  `json:"-"`
	Cursor     string  `json:"-"`
	Limit      int     `json:"-"`
	Offset     int     `json:"-"`
	Blobs      bool    `json:"-"`
	After      string  `json:"-"`
	Since      string  `json:"-"`
	Text       *string `json:"text,omitempty"`
	IfInState  string  `json:"ifInState,omitempty"`
	CreateOnly bool    `json:"createOnly,omitempty"`
	// Strict refuses a stale ifInState even when the touched files are
	// unchanged. REST write, move and delete default to it (see parseBody).
	Strict    bool   `json:"strict,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	StartLine int    `json:"-"`
	LineCount int    `json:"-"`
	// IfBlob makes a read answer unchanged without the text when the file
	// still has this blob.
	IfBlob string `json:"-"`
	// Line is where append puts its text: before that line (1 = top);
	// 0 adds it at the end.
	Line       int    `json:"-"`
	Edits      []Edit `json:"-"`
	Query      string `json:"-"`
	Regex      bool   `json:"-"`
	IgnoreCase bool   `json:"-"`
	Context    int    `json:"-"`
	// Virtual is set only by Metatrash itself (putVirtual): the write is a
	// virtual file's pointer, and this describes its content.
	Virtual *VirtualInfo `json:"-"`
}

// storedRecord is the index entry format in .metatrash/files.json.
type storedRecord struct {
	ID        string       `json:"id"`
	Path      string       `json:"path"`
	Bytes     int64        `json:"bytes"`
	Protected bool         `json:"protected"`
	Blob      string       `json:"blob"`
	Virtual   *VirtualInfo `json:"virtual,omitempty"`
}

// validPath checks path syntax only. ".", "..", ".git" (any case, trailing
// dots ignored as Windows does) are never names, and ".metatrash" at the root
// is the service's own index. Other names starting with a dot are allowed only
// inside a GitHub folder, which a write checks against the space (dotNameRule).
func validPath(path string) bool {
	if len(path) > 240 || !pathPattern.MatchString(path) {
		return false
	}
	for i, segment := range strings.Split(path, "/") {
		lower := strings.TrimRight(strings.ToLower(segment), ".")
		if segment == "." || segment == ".." || lower == ".git" || (i == 0 && lower == ".metatrash") {
			return false
		}
	}
	return true
}

// hasDotName reports a path segment starting with a dot, other than a final
// .metatrash.json (allowed in every folder).
func hasDotName(path string) bool {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ".") && !(i == len(segments)-1 && segment == folderConfigName) {
			return true
		}
	}
	return false
}
func storageLimit() error { return problem(507, "storage_limit", "Space storage limit reached.") }
