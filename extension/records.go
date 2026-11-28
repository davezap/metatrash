package extension

import "context"

// Record pointers (Core 0.105.0). A record pointer is a file whose text
// names a record kept by an extension, such as one section of a Company
// workflow record, instead of holding content of its own:
//
//	metatrash-virtual 1
//	record <record ID>
//	section <section ID>
//	step <step ID>
//	org <organisation>
//	form <form name>
//	from <sender>
//	type text/markdown
//
// Only extension code writes one (FileChange.Record, or RecordWriter). Each
// time a record pointer is read, Core asks Hooks.RecordView what that reader
// may see, and shows that Markdown in place of the pointer: on the space's
// pages (also ?raw and ?format=md, never cached), REST reads, MCP read and
// folder-action jobs. The search index keeps only the file's name,
// dashboards skip it, and export, history, revisions, GitHub folders and MT
// Sync carry only the pointer. Writes, edits and appends over a pointer are
// refused (record_pointer); moving it within its space keeps it working,
// and its text written anywhere else is refused.

// Record is what a record pointer names. Record is required; the others
// are optional. Each is one line of at most 200 bytes, without control
// characters.
type Record struct {
	Record  string `json:"record"`
	Section string `json:"section,omitempty"`
	Step    string `json:"step,omitempty"`
	Org     string `json:"org,omitempty"`
	Form    string `json:"form,omitempty"`
	From    string `json:"from,omitempty"`
}

// Routes in RecordViewRequest.Route: where the view is going.
const (
	RoutePage   = "page"   // the space's web page, ?raw and ?format=md
	RouteAPI    = "api"    // REST and the website's own API (Slate Viewer)
	RouteMCP    = "mcp"    // the MCP read tool (apps and space keys)
	RouteRunner = "runner" // a folder action's job; UserID is its payer
)

// RecordViewRequest is one read of a record pointer.
type RecordViewRequest struct {
	// UserID is the reader: the signed-in account, the account an app
	// acts for, or a job's payer. Empty for a space key or a visitor who
	// is not signed in.
	UserID string
	// SpaceID and Path are where the pointer is.
	SpaceID, Path string
	Record        Record
	Route         string
}

// RecordView is what one reader may see of a record. Hidden (no access)
// shows a blank file with Note, or a default note. Files are the names of
// the attachments the view may link through the record route
// (/api/v1/records/{record}/sections/{section}/files/{name}); RecordFile is
// still asked for each one fetched. Core never caches an answer.
type RecordView struct {
	Markdown string
	Files    []string
	Hidden   bool
	Note     string
}

// RecordFileRequest is a fetch of one record attachment.
type RecordFileRequest struct {
	UserID                string
	Record, Section, Name string
}

// RecordFile is where a record attachment is kept: a virtual file at Path in
// the space SpaceID. Core streams its content only when the file there is
// virtual, never cached. An error refuses, shown as SpaceGate's are; return
// a *server.Error 404 for an attachment the reader may not have, so a
// guessed or forged section reads as missing.
type RecordFile struct {
	SpaceID, Path string
}

// RecordWriter is implemented by the Host a module is given (Core 0.105.0):
// a module type-asserts its Host to it to write record pointers outside a
// tool call. OpenSpace gives a space's files by ID as the service, with
// changes recorded as made for actorID (an account ID, or "" for the
// service itself). Folder locks don't apply; WriteGate is still asked.
type RecordWriter interface {
	OpenSpace(ctx context.Context, spaceID, actorID string) (SpaceFiles, error)
}
