package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Every step of a job sends its tool definitions again, so the runner sends
// the model shorter ones than MCP apps get: a short description per tool
// and per argument, without the arguments a pinned job rarely needs and
// without the length and pattern limits. The runner still checks every call
// against the full MCP schema, so a refused call says what was wrong.

// runnerToolText is each tool's description for a job.
var runnerToolText = map[string]string{
	"read":       "Read a file: its text, blob and the state. Long files: startLine and lineCount. ifBlob returns unchanged: true if the file still has that blob.",
	"search":     "Find lines in the folder's files, like grep: path, line number and text. Plain text, or RE2 with regex.",
	"write":      "Create a file or replace its whole text; needs ifInState from a read, list or search. To change part of a file use edit; to add to one, append.",
	"edit":       "Change part of a file: each oldText must match the current text exactly once (or set replaceAll). No ifInState needed.",
	"append":     "Add text at the end of a file (creating it), or before line.",
	"list":       "List the folder's files and the state. since: only what changed since that state.",
	"move":       "Move or rename a file, keeping its history; the destination must be absent. Needs ifInState.",
	"delete":     "Delete a file. Needs ifInState.",
	"history":    "A file's changes by file ID, newest first. Read an old text with read's path and revision.",
	"web_search": "Search the web: titles, URLs and snippets.",
	"web_fetch":  "Download a URL already in this conversation into a web/… reference: path (the text, Markdown for a page) and original. Read and search it like a file; move it into the folder to keep it.",
}

// runnerArgText describes the arguments a job is offered; any other
// argument of the MCP tool is left out (the full schema still accepts it).
var runnerArgText = map[string]map[string]string{
	"read":       {"path": "", "startLine": "First line, from 1.", "lineCount": "", "ifBlob": "Blob from an earlier read.", "revision": "A state or revision from history."},
	"search":     {"query": "", "prefix": "Subfolder ending in /; empty for the whole folder.", "regex": "", "ignoreCase": "", "context": "Lines around each match.", "limit": "", "path": "A web/… reference to search instead of the folder."},
	"write":      {"path": "", "text": "", "ifInState": "State from a read, list or search.", "createOnly": "Fail if the file exists."},
	"edit":       {"path": "", "edits": "", "ifInState": "Optional: refuse if the file changed since."},
	"append":     {"path": "", "text": "", "line": "Insert before this line (1 = top); leave out for the end."},
	"list":       {"prefix": "Subfolder ending in /; empty for the whole folder.", "since": "An earlier state: list only what changed since.", "limit": "", "cursor": "nextCursor from the previous page."},
	"move":       {"from": "", "to": "", "ifInState": "State from a read, list or search."},
	"delete":     {"path": "", "ifInState": "State from a read, list or search."},
	"history":    {"id": "File ID from read or list.", "limit": "", "cursor": "nextCursor from the previous page."},
	"web_search": {"query": "", "count": "Results, 1 to 20 (default 5)."},
	"web_fetch":  {"url": "A full http or https address."},
}

// runnerToolNames are the tools an action may list in "tools".
var runnerToolNames = []string{"read", "search", "write", "edit", "append", "list", "move", "delete", "history", "web_search", "web_fetch"}

// slimSchemaKeys are left out of what the model sees; the full schema still
// enforces them.
var slimSchemaKeys = map[string]bool{"pattern": true, "minLength": true, "maxLength": true, "minItems": true, "maxItems": true, "minimum": true, "maximum": true, "description": true}

// runnerToolDef is the definition a job sends the model for an MCP tool or
// a web tool: the short description and the slim schema.
func runnerToolDef(name string, input json.RawMessage) (json.RawMessage, error) {
	text, ok := runnerToolText[name]
	if !ok {
		return nil, fmt.Errorf("tool %s has no runner description", name)
	}
	var schema map[string]any
	if err := json.Unmarshal(input, &schema); err != nil {
		return nil, fmt.Errorf("tool %s: %w", name, err)
	}
	args := runnerArgText[name]
	if properties, ok := schema["properties"].(map[string]any); ok {
		for arg, raw := range properties {
			about, offered := args[arg]
			if !offered {
				delete(properties, arg)
				continue
			}
			p, _ := slim(raw).(map[string]any)
			if about != "" && p != nil {
				p["description"] = about
			}
			properties[arg] = p
		}
	}
	if required, ok := schema["required"].([]any); ok {
		kept := []any{}
		for _, r := range required {
			if _, offered := args[fmt.Sprint(r)]; offered {
				kept = append(kept, r)
			}
		}
		schema["required"] = kept
	}
	return json.Marshal(map[string]any{"name": name, "description": text, "input_schema": schema})
}

// slim drops the constraint keys from a schema, at every level.
func slim(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, inner := range t {
			if slimSchemaKeys[k] {
				// "description" can also be a property's name; keep those.
				if _, isSchema := inner.(map[string]any); !isSchema {
					delete(t, k)
					continue
				}
			}
			t[k] = slim(inner)
		}
	case []any:
		for i := range t {
			t[i] = slim(t[i])
		}
	}
	return v
}

// toolsFor narrows a job's tools to those the action lists (all when it
// lists none).
func toolsFor(tools []json.RawMessage, allowed []string) []json.RawMessage {
	if len(allowed) == 0 {
		return tools
	}
	keep := map[string]bool{}
	for _, name := range allowed {
		keep[name] = true
	}
	out := []json.RawMessage{}
	for _, raw := range tools {
		var t struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &t) == nil && keep[t.Name] {
			out = append(out, raw)
		}
	}
	return out
}

// checkActionTools checks an action's "tools" list.
func checkActionTools(tools []string) string {
	if tools == nil {
		return ""
	}
	if len(tools) == 0 {
		return "tools lists the tools the agent may use; leave it out for all of them."
	}
	seen := map[string]bool{}
	for _, name := range tools {
		known := false
		for _, n := range runnerToolNames {
			known = known || n == name
		}
		if !known {
			return fmt.Sprintf("tools: %q is not a tool; the tools are %s.", name, strings.Join(runnerToolNames, ", "))
		}
		if seen[name] {
			return fmt.Sprintf("tools: %s is listed twice.", name)
		}
		seen[name] = true
	}
	return ""
}
