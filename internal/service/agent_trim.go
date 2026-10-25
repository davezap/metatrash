package service

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Every step of a job sends the whole conversation again, so long tool
// results (a file read, search matches, a fetched page's preview, an image)
// cost their tokens on every later step. Once the agent has moved on, the
// runner shortens them to their small fields: paths, states, blobs, titles,
// URLs and counts stay, long text and lists go. The agent can call the tool
// again for the rest, and URLs it has seen stay fetchable.
const (
	keepRecentResults = 2   // newest tool-result turns kept whole
	trimResultBytes   = 600 // results at or under this size are kept whole
	trimKeptString    = 200 // longest string field kept in a shortened result
	trimKeptFields    = 24
)

// trimmedResult is what a shortened tool result says.
type trimmedResult struct {
	Trimmed string         `json:"trimmed"`
	Kept    map[string]any `json:"kept,omitempty"`
}

// trimOldResults shortens tool results older than the newest
// keepRecentResults turns. It never touches the model's own messages, which
// some providers need back unchanged.
func trimOldResults(messages []agentMessage) {
	seen := 0
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != "user" || !hasToolResult(m) {
			continue
		}
		if seen++; seen <= keepRecentResults {
			continue
		}
		for k, raw := range m.Content {
			if len(raw) <= trimResultBytes {
				continue
			}
			var b struct {
				Type      string          `json:"type"`
				ToolUseID string          `json:"tool_use_id"`
				IsError   bool            `json:"is_error"`
				Content   json.RawMessage `json:"content"`
			}
			if json.Unmarshal(raw, &b) != nil || b.Type != "tool_result" {
				continue
			}
			text, images := toolResultText(b.Content)
			var short trimmedResult
			if json.Unmarshal([]byte(text), &short) == nil && short.Trimmed != "" {
				continue
			}
			m.Content[k] = rawBlock(contentBlock{Type: "tool_result", ToolUseID: b.ToolUseID, IsError: b.IsError, Content: shortenResult(text, images, len(raw))})
		}
	}
}

func hasToolResult(m agentMessage) bool {
	for _, raw := range m.Content {
		var b struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &b) == nil && b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// toolResultText is a tool result's text, whether its content is a string or a
// list of blocks, and how many images it held.
func toolResultText(content json.RawMessage) (string, int) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, 0
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(content, &blocks)
	images := 0
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text += b.Text
		case "image":
			images++
		}
	}
	return text, images
}

// shortenResult keeps a result's small fields: the top level of its JSON and
// one level of objects below it.
func shortenResult(text string, images, size int) string {
	out := trimmedResult{Trimmed: fmt.Sprintf("This earlier result (%d bytes) was shortened to save tokens; its small fields are in kept. Call the tool again if you need the rest.", size)}
	if images > 0 {
		out.Trimmed = fmt.Sprintf("This earlier result (%d bytes, with an image) was shortened to save tokens; its small fields are in kept. Read it again if you need to see the image.", size)
	}
	var obj map[string]any
	if json.Unmarshal([]byte(text), &obj) == nil {
		out.Kept = smallFields(obj, true)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func smallFields(obj map[string]any, nested bool) map[string]any {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kept := map[string]any{}
	for _, k := range keys {
		if len(kept) == trimKeptFields {
			break
		}
		switch v := obj[k].(type) {
		case string:
			if len(v) <= trimKeptString {
				kept[k] = v
			}
		case float64, bool:
			kept[k] = v
		case []any:
			kept[k] = fmt.Sprintf("(%d items)", len(v))
		case map[string]any:
			if nested {
				if inner := smallFields(v, false); len(inner) > 0 {
					kept[k] = inner
				}
			}
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}
