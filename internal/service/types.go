package service

import "strings"

type Error struct {
	Code              string `json:"code"`
	Message           string `json:"message"`
	CurrentState      string `json:"currentState,omitempty"`
	RetryAfterSeconds int    `json:"retryAfterSeconds,omitempty"`
	Status            int    `json:"-"`
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
	// Deletable is derived from the path on every snapshot and never stored:
	// false for README.md and the root .metatrash.json.
	Deletable bool `json:"deletable"`
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
}

type ListResult struct {
	Space      string  `json:"space"`
	State      string  `json:"state"`
	Files      []File  `json:"files"`
	NextCursor *string `json:"nextCursor"`
}

type Mutation struct {
	Space    string `json:"space"`
	OldState string `json:"oldState"`
	NewState string `json:"newState"`
	File     File   `json:"file"`
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
	Text       *string `json:"text,omitempty"`
	IfInState  string  `json:"ifInState,omitempty"`
	CreateOnly bool    `json:"createOnly,omitempty"`
	From       string  `json:"from,omitempty"`
	To         string  `json:"to,omitempty"`
}

// storedRecord is the index entry format in .metatrash/files.json.
type storedRecord struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	Protected bool   `json:"protected"`
	Blob      string `json:"blob"`
}

// deletable reports whether delete accepts a file at path.
func deletable(path string, protected bool) bool {
	return !protected && !strings.EqualFold(path, "README.md") && path != folderConfigName
}

func validPath(path string) bool { return len(path) <= 240 && pathPattern.MatchString(path) }
func storageLimit() error        { return problem(507, "storage_limit", "Space storage limit reached.") }
