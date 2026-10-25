package service

import (
	"encoding/json"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

func toolNames(req agentRequest) []string {
	var names []string
	for _, tool := range req.Tools {
		var def struct{ Name string }
		json.Unmarshal(tool, &def)
		names = append(names, def.Name)
	}
	return names
}

func TestRunnerToolDefinitions(t *testing.T) {
	tools, schemas, inputs, err := agentTools(metatrash.ToolSchema)
	if err != nil {
		t.Fatal(err)
	}
	full, slimmed := 0, 0
	for name, input := range inputs {
		full += len(input)
		if _, ok := runnerToolText[name]; !ok {
			t.Errorf("%s has no runner description", name)
		}
	}
	for _, def := range tools {
		slimmed += len(def)
		text := string(def)
		for _, gone := range []string{`"pattern"`, `"maxLength"`, `"strict"`, `"space"`, ".metatrash.json is checked"} {
			if strings.Contains(text, gone) {
				t.Errorf("%s still has %s", text, gone)
			}
		}
	}
	all, _ := json.Marshal(tools)
	t.Logf("tool definitions: %d bytes (full input schemas alone %d)", len(all), full)
	if len(all) > 4500 {
		t.Errorf("the runner's tool definitions are %d bytes", len(all))
	}
	// Calls are still checked against the full schema.
	if err := schemas["read"].Validate(map[string]any{"path": strings.Repeat("a", 241)}); err == nil {
		t.Error("the full read schema accepted a 241-character path")
	}
	w := newRunnerWorld(t)
	w.runner.settings.search, w.runner.settings.downloads = &fakeSearch{}, &memoryObjects{objects: map[string][]byte{}}
	if err := w.runner.setWebTools(true, true); err != nil {
		t.Fatal(err)
	}
	withWeb, _ := json.Marshal(w.runner.webTools)
	t.Logf("with the web tools: %d bytes", len(withWeb))
	if len(withWeb) > 5500 || strings.Contains(string(withWeb), `"pattern"`) || !strings.Contains(string(withWeb), "A web/… reference to search instead of the folder.") {
		t.Errorf("web tools %s", withWeb)
	}
	if err := w.runner.webSchemas["search"].Validate(map[string]any{"query": "x", "path": "web/nothex.md"}); err == nil {
		t.Error("the full search schema accepted a bad reference")
	}
}

func TestRunnerActionTools(t *testing.T) {
	w, search, _, _ := webWorld(t)
	search.results = []webResult{{Title: "News", URL: "https://example.org/news"}}
	w.write("hr/.metatrash.json", `{"actions":[{"name":"news","on":"inbound/*.md","tools":["read","write","web_search"],"steps":5,"do":"Find the news and write news.md."}]}`)
	w.model.turns = []func(agentRequest) (agentResponse, error){
		func(req agentRequest) (agentResponse, error) {
			var system contentBlock
			json.Unmarshal(req.System[0], &system)
			if got := strings.Join(toolNames(req), ","); got != "read,write,web_search" || strings.Contains(system.Text, "web_fetch") || strings.Contains(system.Text, "## This folder's AGENTS.md") {
				t.Errorf("tools %s, system %s", got, system.Text)
			}
			return useTools(toolUse("t1", "delete", map[string]any{"path": "inbound/a.md", "ifInState": strings.Repeat("0", 40)}), toolUse("t2", "web_search", map[string]any{"query": "nz"}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastRaw(req)
			refused, _, isError := resultText(t, results[0])
			found, _, _ := resultText(t, results[1])
			if !isError || !strings.Contains(refused, "may use only read, write, web_search") || !strings.Contains(found, "example.org/news") {
				t.Errorf("delete %s / search %s", refused, found)
			}
			// Two calls made of five: three left, so the runner says to finish.
			var note contentBlock
			json.Unmarshal(results[len(results)-1], &note)
			if len(results) != 3 || note.Type != "text" || !strings.Contains(note.Text, "3 tool calls left") {
				t.Errorf("no warning: %s", results[len(results)-1])
			}
			return useTools(toolUse("t3", "read", map[string]any{"path": "inbound/a.md"}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			if n := len(lastRaw(req)); n != 1 {
				t.Errorf("warned twice: %d blocks", n)
			}
			text, _, _ := resultText(t, lastRaw(req)[0])
			var read struct{ State string }
			json.Unmarshal([]byte(text), &read)
			return useTools(toolUse("t4", "write", map[string]any{"path": "news.md", "text": "# News\n", "ifInState": read.State}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			if written, _, isError := resultText(t, lastRaw(req)[0]); isError {
				t.Errorf("write: %s", written)
			}
			return agentResponse{Content: []json.RawMessage{textBlock("Wrote news.md.")}, StopReason: "end_turn"}, nil
		},
	}
	w.write("hr/inbound/a.md", "question")
	jobs := w.wait(1)
	if jobs[0].Status != jobDone || len(w.model.turns) != 0 || w.read("hr/news.md") != "# News\n" {
		t.Fatalf("jobs %+v, %d turns left", jobs, len(w.model.turns))
	}
}

func TestActionToolsChecked(t *testing.T) {
	for tools, want := range map[string]string{
		`[]`:                   "leave it out",
		`["read","shell"]`:     `"shell" is not a tool`,
		`["read","read"]`:      "listed twice",
		`["read","web_fetch"]`: "",
	} {
		var c folderConfig
		err := json.Unmarshal([]byte(`{"actions":[{"name":"a","on":"*.md","do":"x","tools":`+tools+`}]}`), &c)
		if err == nil {
			err = checkActions(c.Actions)
		}
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v", tools, err)
		}
	}
}
