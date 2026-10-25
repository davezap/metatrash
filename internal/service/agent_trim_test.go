package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrimOldResults(t *testing.T) {
	long := strings.Repeat("policy text ", 200)
	read, _ := json.Marshal(map[string]any{"space": "dave/hr", "state": strings.Repeat("a", 40), "file": map[string]any{"path": "leave.md", "blob": strings.Repeat("b", 40), "bytes": 2400, "text": long}})
	search, _ := json.Marshal(map[string]any{"query": "nz leave law", "results": []any{map[string]any{"title": "Leave", "url": "https://example.org/leave", "snippet": long}}})
	result := func(id, text string) json.RawMessage {
		return rawBlock(contentBlock{Type: "tool_result", ToolUseID: id, Content: text})
	}
	image := rawImageResult("t3", `{"path":"web/0123456789abcdef.png"}`, imageResult{media: "image/png", data: []byte(long)})
	assistant := agentMessage{Role: "assistant", Content: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"` + long + `"}`)}}
	messages := []agentMessage{
		{Role: "user", Content: []json.RawMessage{textBlock("## Instructions\n\n" + long)}},
		assistant, {Role: "user", Content: []json.RawMessage{result("t1", string(read)), result("t2", `{"ok":true}`)}},
		assistant, {Role: "user", Content: []json.RawMessage{image}},
		assistant, {Role: "user", Content: []json.RawMessage{result("t4", string(search))}},
		assistant, {Role: "user", Content: []json.RawMessage{result("t5", string(read))}},
	}
	before := make([]string, len(messages))
	for i, m := range messages {
		b, _ := json.Marshal(m)
		before[i] = string(b)
	}
	trimOldResults(messages)
	after := func(i int) string {
		b, _ := json.Marshal(messages[i])
		return string(b)
	}
	// The instructions, the model's own turns and the newest two result turns
	// are untouched.
	for _, i := range []int{0, 1, 3, 5, 6, 7, 8} {
		if after(i) != before[i] {
			t.Errorf("message %d changed: %s", i, after(i))
		}
	}
	var b contentBlock
	json.Unmarshal(messages[2].Content[0], &b)
	var short trimmedResult
	if err := json.Unmarshal([]byte(b.Content), &short); err != nil || b.ToolUseID != "t1" || short.Trimmed == "" {
		t.Fatalf("read not shortened: %+v %v", b, err)
	}
	file, _ := short.Kept["file"].(map[string]any)
	if short.Kept["state"] != strings.Repeat("a", 40) || file["path"] != "leave.md" || file["blob"] != strings.Repeat("b", 40) || file["text"] != nil {
		t.Errorf("kept %+v", short.Kept)
	}
	if string(messages[2].Content[1]) != string(result("t2", `{"ok":true}`)) {
		t.Errorf("a small result changed: %s", messages[2].Content[1])
	}
	json.Unmarshal(messages[4].Content[0], &b)
	if b.ToolUseID != "t3" || !strings.Contains(b.Content, "with an image") || !strings.Contains(b.Content, "web/0123456789abcdef.png") || strings.Contains(after(4), `"image"`) {
		t.Errorf("image result %s", after(4))
	}
	// Shortening again changes nothing, and a later turn shortens the next.
	shortened := after(2)
	trimOldResults(messages)
	if after(2) != shortened {
		t.Error("a shortened result was shortened again")
	}
	messages = append(messages, assistant, agentMessage{Role: "user", Content: []json.RawMessage{result("t6", `{"ok":true}`)}})
	trimOldResults(messages)
	json.Unmarshal(messages[6].Content[0], &b)
	if !strings.Contains(b.Content, `"trimmed"`) || !strings.Contains(b.Content, `"results":"(1 items)"`) || !strings.Contains(b.Content, "nz leave law") {
		t.Errorf("search result %s", b.Content)
	}
	if after(8) != before[8] {
		t.Error("the second newest result turn changed")
	}
}
