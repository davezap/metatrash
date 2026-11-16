package service

// Module tools (0.98.0): agent tools an extension adds (extension.Tool).
// The account MCP endpoint offers them to connected apps, and folder-action
// jobs get the ones their action lists by name or group (always opt-in).
// Core checks each call's arguments against the tool's schema and gives the
// module a SpaceFiles for the spaces the caller may use: Read takes a
// snapshot of files, and Apply makes a set of changes as one commit, held to
// the same rules as the caller's own writes (access, locks, write gates,
// folder files), refusing a file changed since it was read.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"metatrash.com/metatrash/extension"
	"metatrash.com/metatrash/internal/filenames"
)

var moduleToolName = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

// moduleTool is a registered module tool with its resolved schema.
type moduleTool struct {
	extension.Tool
	schema *jsonschema.Resolved
	// job is the definition jobs send the model; mcpTool what apps see.
	job     json.RawMessage
	mcpTool *mcp.Tool
}

// moduleToolNames maps every registered module tool name to its group, for
// the checks of an action's "tools", which run without a service. Names
// stay registered for the life of the process.
var moduleToolNames = struct {
	sync.RWMutex
	tools  map[string]string
	groups map[string]bool
}{tools: map[string]string{}, groups: map[string]bool{}}

func registeredModuleTool(name string) (group string, ok bool) {
	moduleToolNames.RLock()
	defer moduleToolNames.RUnlock()
	group, ok = moduleToolNames.tools[name]
	return group, ok
}

func registeredToolGroup(name string) bool {
	moduleToolNames.RLock()
	defer moduleToolNames.RUnlock()
	return moduleToolNames.groups[name]
}

// coreToolNames are names module tools may not take.
func coreToolNames() map[string]bool {
	names := map[string]bool{"spaces": true, "pull": true, "pending": true, "push": true, "submit": true, "configure": true}
	for _, n := range runnerToolNames {
		names[n] = true
	}
	return names
}

// registerModuleTools checks the extensions' tools and prepares them.
func (s *Service) registerModuleTools() error {
	reserved := coreToolNames()
	seen := map[string]bool{}
	groups := map[string]bool{}
	var tools []*moduleTool
	for _, hooks := range s.extensions {
		for _, t := range hooks.Tools {
			if !moduleToolName.MatchString(t.Name) || reserved[t.Name] || seen[t.Name] {
				return fmt.Errorf("extension %s: tool name %q is invalid or taken", hooks.Name, t.Name)
			}
			if t.Group != "" && (!moduleToolName.MatchString(t.Group) || reserved[t.Group]) {
				return fmt.Errorf("extension %s: tool %s: group %q is invalid", hooks.Name, t.Name, t.Group)
			}
			if t.Call == nil || strings.TrimSpace(t.Description) == "" {
				return fmt.Errorf("extension %s: tool %s needs Call and Description", hooks.Name, t.Name)
			}
			seen[t.Name] = true
			if t.Group != "" {
				groups[t.Group] = true
			}
			mt, err := prepareModuleTool(t)
			if err != nil {
				return fmt.Errorf("extension %s: tool %s: %w", hooks.Name, t.Name, err)
			}
			tools = append(tools, mt)
		}
	}
	for g := range groups {
		if seen[g] {
			return fmt.Errorf("tool group %q is also a tool's name", g)
		}
	}
	s.moduleTools = map[string]*moduleTool{}
	moduleToolNames.Lock()
	defer moduleToolNames.Unlock()
	for _, t := range tools {
		s.moduleTools[t.Name] = t
		moduleToolNames.tools[t.Name] = t.Group
		if t.Group != "" {
			moduleToolNames.groups[t.Group] = true
		}
	}
	return nil
}

func prepareModuleTool(t extension.Tool) (*moduleTool, error) {
	var schema map[string]any
	if err := json.Unmarshal(t.InputSchema, &schema); err != nil || schema["type"] != "object" {
		return nil, fmt.Errorf("InputSchema must be a JSON Schema object")
	}
	var js jsonschema.Schema
	if err := json.Unmarshal(t.InputSchema, &js); err != nil {
		return nil, err
	}
	resolved, err := js.Resolve(nil)
	if err != nil {
		return nil, err
	}
	var tool mcp.Tool
	b, _ := json.Marshal(map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
	if err := json.Unmarshal(b, &tool); err != nil {
		return nil, err
	}
	// Jobs work in their own space: drop the space argument.
	if properties, ok := schema["properties"].(map[string]any); ok {
		delete(properties, "space")
	}
	if required, ok := schema["required"].([]any); ok {
		kept := []any{}
		for _, r := range required {
			if r != "space" {
				kept = append(kept, r)
			}
		}
		schema["required"] = kept
	}
	short := t.Short
	if short == "" {
		short = t.Description
	}
	job, err := json.Marshal(map[string]any{"name": t.Name, "description": short, "input_schema": schema})
	if err != nil {
		return nil, err
	}
	return &moduleTool{Tool: t, schema: resolved, job: job, mcpTool: &tool}, nil
}

// moduleToolsFor lists the definitions of the module tools an action opts
// in to, by name or group.
func (s *Service) moduleToolsFor(action folderAction) []json.RawMessage {
	var names []string
	for name, t := range s.moduleTools {
		if action.listsModuleTool(t.Name, t.Group) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []json.RawMessage
	for _, name := range names {
		out = append(out, s.moduleTools[name].job)
	}
	return out
}

func (a folderAction) listsModuleTool(name, group string) bool {
	for _, n := range a.Tools {
		if n == name || group != "" && n == group {
			return true
		}
	}
	return false
}

// callModuleTool runs a module tool and answers the result as JSON text,
// and whether it is an error.
func callModuleTool(ctx context.Context, t *moduleTool, call extension.ToolCall) (string, bool) {
	fail := func(err error) (string, bool) {
		var p *Error
		var te *extension.ToolError
		if !errors.As(err, &p) && errors.As(err, &te) {
			p = problem(400, te.Code, te.Message)
		}
		if p == nil {
			log.Printf("tool %s: %v", t.Name, err)
			p = problem(500, "internal_error", "Service operation failed.")
		}
		b, _ := json.Marshal(struct {
			Error *Error `json:"error"`
		}{p})
		return string(b), true
	}
	raw := call.Args
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage("{}")
	}
	if len(raw) > 4<<20 {
		return fail(problem(413, "payload_too_large", "Arguments are too large."))
	}
	var instance any
	if err := json.Unmarshal(raw, &instance); err != nil {
		return fail(invalid("Invalid arguments."))
	}
	if err := t.schema.Validate(instance); err != nil {
		return fail(invalid("Arguments do not match the tool input schema: " + err.Error()))
	}
	call.Args = raw
	value, err := t.Call(ctx, call)
	if err != nil {
		return fail(err)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fail(err)
	}
	if len(b) > maxToolResultBytes {
		return fail(problem(413, "result_too_large", fmt.Sprintf("The result is over %d bytes.", maxToolResultBytes)))
	}
	return string(b), false
}

// addModuleMCPTools offers the module tools on the account MCP endpoint.
func (h *httpAdapter) addModuleMCPTools(server *mcp.Server) {
	names := make([]string, 0, len(h.service.moduleTools))
	for name := range h.service.moduleTools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := h.service.moduleTools[name]
		server.AddTool(t.mcpTool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			identity, ok := ctx.Value(mcpIdentityKey{}).(mcpIdentity)
			if !ok || identity.oauth == nil {
				return mcpFailure(problem(401, "unauthorized", "Missing transport credentials.")), nil
			}
			id := *identity.oauth
			if err := h.service.rates.reserve(allowance{"oauth:tool:" + id.GrantID, 240, 60}); err != nil {
				return mcpFailure(err), nil
			}
			call := extension.ToolCall{UserID: id.UserID, Agent: extension.AgentApp, Args: req.Params.Arguments,
				Open: func(ctx context.Context, space string, write bool) (extension.SpaceFiles, error) {
					repo, name, err := h.service.agentSpaceAccess(ctx, id, space, identity.client, write)
					if err != nil {
						return nil, err
					}
					return &spaceFiles{s: h.service, r: repo, name: name}, nil
				}}
			text, isError := callModuleTool(ctx, t, call)
			result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: isError}
			if !isError {
				var value any
				if json.Unmarshal([]byte(text), &value) == nil {
					if _, isObject := value.(map[string]any); isObject {
						result.StructuredContent = value
					}
				}
			}
			return result, nil
		})
	}
}

// callJobModuleTool runs a module tool for a folder-action job, in the
// job's space and folder.
func (a *agentRunner) callJobModuleTool(ctx context.Context, t *moduleTool, r *repository, spaceID, jobID string, pin pinnedFolder, raw json.RawMessage) (string, bool) {
	job := runnerJobOf(ctx)
	if job == nil {
		return `{"error":{"code":"invalid_request","message":"Unknown tool."}}`, true
	}
	var args map[string]json.RawMessage
	if json.Unmarshal(raw, &args) == nil {
		if _, has := args["space"]; has {
			return `{"error":{"code":"invalid_request","message":"A job works in its own space: leave out space."}}`, true
		}
	}
	call := extension.ToolCall{UserID: job.payerID, Agent: extension.AgentFolder, Job: jobID, Args: raw,
		Open: func(ctx context.Context, space string, write bool) (extension.SpaceFiles, error) {
			if space != "" && space != spaceID {
				return nil, problem(403, "forbidden", "A job may use only its own space.")
			}
			return &spaceFiles{s: a.s, r: r, name: a.spaceAddress(ctx, spaceID), folder: string(pin), job: true}, nil
		}}
	text, isError := callModuleTool(ctx, t, call)
	return strings.ReplaceAll(text, string(pin), ""), isError
}

// spaceFiles is extension.SpaceFiles for one caller.
type spaceFiles struct {
	s      *Service
	r      *repository
	name   string
	folder string // "" or a folder ending in "/"
	job    bool
}

func (f *spaceFiles) SpaceID() string {
	if f.r.id != "" {
		return f.r.id
	}
	return f.name
}
func (f *spaceFiles) Name() string   { return f.name }
func (f *spaceFiles) Folder() string { return f.folder }

func (f *spaceFiles) Read(ctx context.Context, paths []string, maxFiles int, maxBytes int64) (string, []extension.SpaceFile, error) {
	state, files, err := f.r.snapshot(ctx, "")
	if err != nil {
		return "", nil, err
	}
	picked := map[string]record{}
	for _, p := range paths {
		if p != "" && !strings.HasSuffix(p, "/") {
			if !validPath(p) {
				return "", nil, invalid(fmt.Sprintf("Invalid path %q.", p))
			}
			rec, ok := files[f.folder+p]
			if !ok {
				return "", nil, problem(404, "not_found", p+" is not in the space.")
			}
			picked[f.folder+p] = rec
			continue
		}
		if p != "" && !validPath(strings.TrimSuffix(p, "/")) {
			return "", nil, invalid(fmt.Sprintf("Invalid folder %q.", p))
		}
		prefix, found := f.folder+p, false
		for path, rec := range files {
			if strings.HasPrefix(path, prefix) {
				picked[path], found = rec, true
			}
		}
		if !found {
			return "", nil, problem(404, "not_found", "Nothing is in "+p+".")
		}
	}
	if len(picked) > maxFiles {
		return "", nil, problem(413, "payload_too_large", fmt.Sprintf("That is %d files; at most %d.", len(picked), maxFiles))
	}
	var total int64
	var hashes []string
	for _, rec := range picked {
		if rec.Virtual == nil {
			total += rec.Bytes
			hashes = append(hashes, rec.Blob)
		}
	}
	if total > maxBytes {
		return "", nil, problem(413, "payload_too_large", fmt.Sprintf("Those files hold %d bytes; at most %d.", total, maxBytes))
	}
	blobs, err := f.r.blobs(ctx, hashes)
	if err != nil {
		return "", nil, err
	}
	out := make([]extension.SpaceFile, 0, len(picked))
	for path, rec := range picked {
		sf := extension.SpaceFile{Path: strings.TrimPrefix(path, f.folder), Blob: rec.Blob, Virtual: rec.Virtual != nil}
		if rec.Virtual == nil {
			b, ok := blobs[rec.Blob]
			if !ok {
				return "", nil, fmt.Errorf("blob %s missing", rec.Blob)
			}
			sf.Data = b
		}
		out = append(out, sf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return state, out, nil
}

func (f *spaceFiles) Apply(ctx context.Context, changes []extension.FileChange, summary string) (string, error) {
	if len(changes) == 0 {
		return "", invalid("No changes to apply.")
	}
	if len(changes) > f.r.limits.MaxFiles {
		return "", storageLimit()
	}
	summary = strings.Join(strings.Fields(summary), " ")
	if utf8.RuneCountInString(summary) > 200 {
		summary = string([]rune(summary)[:200])
	}
	seen := map[string]bool{}
	for _, c := range changes {
		if !validPath(c.Path) {
			return "", invalid(fmt.Sprintf("Invalid path %q.", c.Path))
		}
		if seen[c.Path] {
			return "", invalid(c.Path + " is changed twice.")
		}
		seen[c.Path] = true
		if c.Base != "" && !hashPattern.MatchString(c.Base) {
			return "", invalid(c.Path + ": base must be a blob hash.")
		}
		if isFolderConfig(c.Path) {
			return "", problem(403, "protected_file", c.Path+": folder settings can't be changed this way; use write.")
		}
		if f.job && c.Path == runnerLogName {
			return "", runnerOwnFile(pinnedFolder(f.folder), c.Path)
		}
		if c.Delete {
			continue
		}
		if !utf8.ValidString(c.Text) || strings.ContainsRune(c.Text, 0) {
			return "", invalid(c.Path + ": text must be UTF-8 without NUL.")
		}
		if int64(len(c.Text)) > f.r.limits.MaxFileBytes {
			return "", problem(413, "payload_too_large", c.Path+" is over the file limit.")
		}
	}
	value, err := f.s.queued(ctx, f.r, func() (any, error) { return f.apply(ctx, changes, summary) })
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (f *spaceFiles) apply(ctx context.Context, changes []extension.FileChange, summary string) (string, error) {
	r := f.r
	head, files, err := r.snapshot(ctx, "")
	if err != nil {
		return "", err
	}
	var conflicts []string
	for _, c := range changes {
		now, exists := files[f.folder+c.Path]
		if (c.Base == "") == exists || exists && now.Blob != c.Base {
			conflicts = append(conflicts, c.Path)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return "", &Error{Status: 409, Code: "conflict", CurrentState: head,
			Message: "Changed in the space since they were read: " + strings.Join(conflicts, ", ") + "."}
	}
	github, err := r.githubFolders(ctx, files, nil)
	if err != nil {
		return "", err
	}
	person := writerOf(ctx)
	readOnly, err := r.readOnlyRules(ctx, files, nil)
	if err != nil {
		return "", err
	}
	keep := func(p string) bool { return blocks(fileLock(readOnly, p), person) }
	texts := map[string]string{}
	var commit []commitChange
	var written, deleted []string
	for _, c := range changes {
		p := f.folder + c.Path
		if level := fileLock(readOnly, p); blocks(level, person) {
			return "", readOnlyFile(c.Path, level)
		}
		rec, exists := files[p]
		if exists && rec.Virtual != nil {
			return "", virtualRefused(c.Path)
		}
		if c.Delete {
			if !exists {
				return "", problem(404, "not_found", c.Path+" is not in the space.")
			}
			if rec.Protected {
				return "", folderFileKept(c.Path)
			}
			delete(files, p)
			commit = append(commit, commitChange{rec.ID, "delete", p})
			deleted = append(deleted, p)
			continue
		}
		operation := "write"
		if !exists {
			if looksVirtual(c.Text) {
				return "", pointerRefused()
			}
			if reason := filenames.NewName(p); reason != "" {
				return "", invalid("Invalid path " + c.Path + ": " + reason + ".")
			}
			id, err := randomHex(16)
			if err != nil {
				return "", err
			}
			rec = record{File: File{ID: id, Path: p}}
			operation = "create"
		} else if b, err := r.blob(ctx, rec.Blob); err != nil {
			return "", err
		} else if string(b) == c.Text {
			continue
		}
		rec.Bytes = int64(len(c.Text))
		files[p] = rec
		texts[p] = c.Text
		commit = append(commit, commitChange{rec.ID, operation, p})
		written = append(written, p)
	}
	if len(commit) == 0 {
		return head, nil
	}
	for _, dest := range written {
		for p := range files {
			if strings.HasPrefix(dest, p+"/") || strings.HasPrefix(p, dest+"/") {
				return "", invalid("File/directory path collision at " + strings.TrimPrefix(dest, f.folder) + ".")
			}
		}
	}
	readOnlyAfter, err := r.readOnlyRules(ctx, files, nil)
	if err != nil {
		return "", err
	}
	for _, p := range written {
		rec := files[p]
		rec.Protected = protectedFolderFile(github, p)
		rec.Deletable = !rec.Protected
		rec.ReadOnly = fileLock(readOnlyAfter, p)
		files[p] = rec
	}
	commit = append(commit, removeEmptyFolderFiles(files, github, keep, deleted...)...)
	added, err := addFolderFiles(files, texts, github, r.rootAgents, written...)
	if err != nil {
		return "", err
	}
	return r.commitFiles(ctx, head, files, texts, nil, append(commit, added...), summary)
}
