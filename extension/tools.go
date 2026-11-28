package extension

import (
	"context"
	"encoding/json"
)

// Tool is an agent tool a module adds (Core 0.98.0). Core offers it on the
// account MCP endpoint (/mcp/account, Claude and ChatGPT through OAuth) and
// to folder-action jobs whose action lists it, or its Group, in "tools":
// module tools are always opt-in for jobs. Core checks every call's
// arguments against InputSchema before Call, and turns Call's answer into
// the tool result: a value is sent as JSON; an error that is a
// *server.Error is shown as it is, anything else is logged and shown as an
// internal error.
type Tool struct {
	// Name is ^[a-z][a-z0-9_]{1,31}$ and must not be one of Core's tools.
	Name string
	// Group, when set, is a name an action's "tools" may list to opt in to
	// every tool of the group at once (for example "compute"). Same
	// pattern as Name, and no tool may have it as its name.
	Group string
	// Description is what apps see on MCP.
	Description string
	// Short is the shorter description folder-action jobs see (every step
	// of a job sends the tool definitions again). Empty uses Description.
	Short string
	// InputSchema is the JSON Schema of the arguments, an object. A tool
	// that works on a space takes it as a "space" property and passes it
	// to ToolCall.Open; jobs never see that property (a job works in its
	// own space and folder).
	InputSchema json.RawMessage
	Call        func(ctx context.Context, call ToolCall) (any, error)
}

// Agent kinds in ToolCall.Agent.
const (
	AgentApp    = "app"    // an app connected through OAuth (/mcp/account)
	AgentFolder = "folder" // a folder action's job
)

// ToolCall is one call of a module tool.
type ToolCall struct {
	// UserID is the account the call acts for: the person who connected
	// the app, or the account paying for the job.
	UserID string
	// Agent is AgentApp or AgentFolder.
	Agent string
	// Job is a folder action job's ID (empty for apps), so a module can
	// tell calls of one job from another's.
	Job string
	// Args are the call's arguments, already checked against InputSchema.
	// A job's arguments never hold "space".
	Args json.RawMessage
	// Open gives the files of a space the caller may use, after Core's
	// access checks for this caller: for an app, space is a value from its
	// spaces tool or a space ID, and write asks for read_write access; for
	// a job, space must be "" or its own space's ID, and Files covers only
	// the job's folder.
	Open func(ctx context.Context, space string, write bool) (SpaceFiles, error)
}

// SpaceFiles is one space's files as one caller may use them. Paths are
// relative to Folder.
type SpaceFiles interface {
	// SpaceID is the space's ID; Name is how results name it (owner/slug,
	// or a configured space's name).
	SpaceID() string
	Name() string
	// Folder is the folder paths are relative to: "" for the whole space,
	// or a job's folder ending in "/".
	Folder() string
	// Read gives the current files at paths: a file's path, or a folder
	// ending in "/" (everything under it; "" is the whole of Folder). Each
	// file comes with its blob hash. Virtual files (uploads such as
	// pictures and PDFs) are listed with Virtual set and no Data; record
	// pointers with Record set and their pointer as Data. More
	// than maxFiles files or maxBytes of text refuses with
	// payload_too_large, and a path that names nothing with not_found.
	Read(ctx context.Context, paths []string, maxFiles int, maxBytes int64) (state string, files []SpaceFile, err error)
	// Apply makes changes as one commit by the caller, with summary as its
	// message, or refuses them all. Each change names the blob its file
	// must still have (Base), "" for a file that must not exist yet, so a
	// file changed since it was read refuses with conflict (409, every
	// such path in the message). Texts are UTF-8 without NUL. The folder's
	// locks apply as for the caller's own writes, and .metatrash.json
	// files and virtual files can't be changed this way (a record pointer
	// only by another Record, or Delete). Folder files for new folders are
	// added, and emptied folders lose theirs, as writes and deletes do. It
	// answers the new state, or the old one when nothing changed.
	Apply(ctx context.Context, changes []FileChange, summary string) (state string, err error)
}

// SpaceFile is a file Read gave.
type SpaceFile struct {
	Path    string
	Blob    string
	Data    []byte
	Virtual bool
	// Record is set on a record pointer (Core 0.105.0); Data is then its
	// pointer text.
	Record *Record
}

// FileChange is one change Apply makes.
type FileChange struct {
	Path string
	// Base is the blob the file must have now; "" when it must not exist.
	Base string
	// Text is the file's new text; ignored when Delete is set.
	Text   string
	Delete bool
	// Record writes a record pointer instead of Text (Core 0.105.0): a new
	// one, or a new version of one (another step, say). Only a record
	// pointer may replace one; Delete removes it.
	Record *Record
}

// ToolError is an error a tool's Call returns to show the agent what went
// wrong: Core puts its Code (such as "invalid_request", "not_found" or
// "conflict") and Message in the tool result, as with its own errors.
type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string { return e.Message }
