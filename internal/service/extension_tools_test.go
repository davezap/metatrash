package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/extension"
)

// demoTools is a module with two tools in group "demo": snapshot reads files
// and propose applies changes, the way Company's compute tools do.
func demoTools(calls *[]extension.ToolCall) []extension.Tool {
	return []extension.Tool{
		{Name: "demo_snapshot", Group: "demo", Description: "Read files.", Short: "Read.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"space":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}},"required":["paths"],"additionalProperties":false}`),
			Call: func(ctx context.Context, call extension.ToolCall) (any, error) {
				*calls = append(*calls, call)
				var args struct {
					Space string   `json:"space"`
					Paths []string `json:"paths"`
				}
				if err := json.Unmarshal(call.Args, &args); err != nil {
					return nil, err
				}
				files, err := call.Open(ctx, args.Space, false)
				if err != nil {
					return nil, err
				}
				if len(args.Paths) == 0 {
					return nil, &extension.ToolError{Code: "invalid_request", Message: "Name at least one path."}
				}
				state, got, err := files.Read(ctx, args.Paths, 10, 1<<20)
				if err != nil {
					return nil, err
				}
				out := map[string]any{"state": state, "folder": files.Folder()}
				var list []map[string]any
				for _, f := range got {
					list = append(list, map[string]any{"path": f.Path, "blob": f.Blob, "text": string(f.Data), "virtual": f.Virtual})
				}
				out["files"] = list
				return out, nil
			}},
		{Name: "demo_propose", Group: "demo", Description: "Apply changes.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"space":{"type":"string"},"changes":{"type":"array"}},"required":["changes"]}`),
			Call: func(ctx context.Context, call extension.ToolCall) (any, error) {
				*calls = append(*calls, call)
				var args struct {
					Space   string                 `json:"space"`
					Changes []extension.FileChange `json:"changes"`
				}
				if err := json.Unmarshal(call.Args, &args); err != nil {
					return nil, err
				}
				files, err := call.Open(ctx, args.Space, true)
				if err != nil {
					return nil, err
				}
				state, err := files.Apply(ctx, args.Changes, "Demo changes")
				if err != nil {
					return nil, err
				}
				return map[string]string{"state": state}, nil
			}},
	}
}

func TestModuleToolRegistration(t *testing.T) {
	s := &Service{}
	ok := extension.Tool{Name: "demo_ok", Group: "demox", Description: "x", InputSchema: json.RawMessage(`{"type":"object"}`),
		Call: func(context.Context, extension.ToolCall) (any, error) { return nil, nil }}
	for _, bad := range []func(extension.Tool) extension.Tool{
		func(t extension.Tool) extension.Tool { t.Name = "read"; return t },
		func(t extension.Tool) extension.Tool { t.Name = "Bad-Name"; return t },
		func(t extension.Tool) extension.Tool { t.Group = "write"; return t },
		func(t extension.Tool) extension.Tool { t.Call = nil; return t },
		func(t extension.Tool) extension.Tool { t.InputSchema = json.RawMessage(`{"type":"string"}`); return t },
	} {
		s.extensions = []extension.Hooks{{Name: "m", Tools: []extension.Tool{bad(ok)}}}
		if err := s.registerModuleTools(); err == nil {
			t.Errorf("registered %+v", bad(ok))
		}
	}
	s.extensions = []extension.Hooks{{Name: "m", Tools: []extension.Tool{ok}}, {Name: "n", Tools: []extension.Tool{ok}}}
	if err := s.registerModuleTools(); err == nil {
		t.Error("registered one name twice")
	}
	s.extensions = []extension.Hooks{{Name: "m", Tools: []extension.Tool{ok}}}
	if err := s.registerModuleTools(); err != nil {
		t.Fatal(err)
	}
	if why := checkActionTools([]string{"read", "demo_ok"}); why != "" {
		t.Error(why)
	}
	if why := checkActionTools([]string{"demox"}); why != "" {
		t.Error(why)
	}
	if why := checkActionTools([]string{"demo_nope"}); why == "" {
		t.Error("an unknown tool was accepted")
	}
	var def map[string]any
	_ = json.Unmarshal(s.moduleTools["demo_ok"].job, &def)
	if def["name"] != "demo_ok" || def["description"] != "x" {
		t.Errorf("job definition %v", def)
	}
}

func TestModuleToolsInJobs(t *testing.T) {
	w := newRunnerWorld(t)
	var calls []extension.ToolCall
	w.s.extensions = append(w.s.extensions, extension.Hooks{Name: "demo", Tools: demoTools(&calls)})
	if err := w.s.registerModuleTools(); err != nil {
		t.Fatal(err)
	}
	w.write("dev/locked.md", "locked\n")
	w.write("dev/.metatrash.json", `{"actions":[{"name":"build","on":"inbound/*.md","do":"Build it.","tools":["read","demo"]},{"name":"plain","on":"plain/*.md","do":"Read it."}],"readOnly":{"locked.md":"agents"}}`)
	w.write("dev/src/main.go", "package main\n")
	w.write("dev/src/old.txt", "old\n")
	w.write("other/secret.md", "secret\n")
	var mainBlob, oldBlob, lockedBlob string
	w.model.turns = []func(agentRequest) (agentResponse, error){
		func(req agentRequest) (agentResponse, error) {
			names := []string{}
			for _, raw := range req.Tools {
				var d struct {
					Name        string         `json:"name"`
					InputSchema map[string]any `json:"input_schema"`
				}
				_ = json.Unmarshal(raw, &d)
				names = append(names, d.Name)
				if strings.Contains(string(raw), `"space"`) {
					t.Errorf("job tool offers space: %s", raw)
				}
			}
			if strings.Join(names, ",") != "read,demo_propose,demo_snapshot" {
				t.Errorf("tools %v", names)
			}
			return useTools(
				toolUse("t1", "demo_snapshot", map[string]any{"paths": []string{"src/", "locked.md"}}),
				toolUse("t2", "demo_snapshot", map[string]any{"paths": []string{"../other/secret.md"}}),
				toolUse("t3", "demo_snapshot", map[string]any{"paths": []string{"src/"}, "space": "elsewhere"}),
			)(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastResults(t, req)
			if results[0].IsError || !results[1].IsError || !results[2].IsError {
				t.Fatalf("results %+v", results)
			}
			var snap struct {
				Folder string `json:"folder"`
				Files  []struct{ Path, Blob, Text string }
			}
			if err := json.Unmarshal([]byte(results[0].Content), &snap); err != nil {
				t.Fatal(err)
			}
			blobs, texts := map[string]string{}, map[string]string{}
			for _, f := range snap.Files {
				blobs[f.Path], texts[f.Path] = f.Blob, f.Text
			}
			// src/ also holds its folder files.
			if snap.Folder != "" || len(snap.Files) != 5 || texts["src/main.go"] != "package main\n" || blobs["src/AGENTS.md"] == "" {
				t.Errorf("snapshot %s", results[0].Content)
			}
			lockedBlob, mainBlob, oldBlob = blobs["locked.md"], blobs["src/main.go"], blobs["src/old.txt"]
			return useTools(
				// A locked file refuses the whole set.
				toolUse("t4", "demo_propose", map[string]any{"changes": []map[string]any{{"Path": "src/main.go", "Base": mainBlob, "Text": "package main\n\nfunc main() {}\n"}, {"Path": "locked.md", "Base": lockedBlob, "Text": "changed"}}}),
				toolUse("t5", "demo_propose", map[string]any{"changes": []map[string]any{{"Path": ".metatrash.json", "Base": "", "Text": "{}"}}}),
				toolUse("t6", "demo_propose", map[string]any{"changes": []map[string]any{
					{"Path": "src/main.go", "Base": mainBlob, "Text": "package main\n\nfunc main() {}\n"},
					{"Path": "src/old.txt", "Base": oldBlob, "Delete": true},
					{"Path": "build/out.txt", "Base": "", "Text": "ok\n"},
				}}),
				// The same set again: main.go changed since its base.
				toolUse("t7", "demo_propose", map[string]any{"changes": []map[string]any{{"Path": "src/main.go", "Base": mainBlob, "Text": "x"}}}),
			)(req)
		},
		func(req agentRequest) (agentResponse, error) {
			results := lastResults(t, req)
			for i, want := range []string{"read_only_file", "protected_file", "", "conflict"} {
				if got := results[i].Content; (want == "") == results[i].IsError || want != "" && !strings.Contains(got, `"`+want+`"`) {
					t.Errorf("result %d: %s, want %q", i, got, want)
				}
			}
			if !strings.Contains(results[3].Content, "src/main.go") || strings.Contains(results[3].Content, "dev/") {
				t.Errorf("conflict: %s", results[3].Content)
			}
			return agentResponse{Content: []json.RawMessage{textBlock("Built.")}, StopReason: "end_turn"}, nil
		},
	}
	w.write("dev/inbound/go.md", "build\n")
	jobs := w.wait(1)
	if jobs[0].Status != jobDone {
		t.Fatalf("jobs %+v", jobs)
	}
	if w.read("dev/src/main.go") != "package main\n\nfunc main() {}\n" || w.exists("dev/src/old.txt") || w.read("dev/build/out.txt") != "ok\n" || w.read("dev/locked.md") != "locked\n" {
		t.Error("the changes did not land as expected")
	}
	if !w.exists("dev/build/AGENTS.md") || !w.exists("dev/build/.metatrash.json") {
		t.Error("the new folder has no folder files")
	}
	if len(calls) == 0 || calls[0].Agent != extension.AgentFolder || calls[0].UserID != w.repo.owner || calls[0].Job == "" {
		t.Errorf("call %+v", calls)
	}
	// One commit by the job's agent, with the summary.
	_, files, _ := w.repo.snapshot(context.Background(), "")
	history := w.do(context.Background(), "history", Input{ID: files["dev/build/out.txt"].ID}, "").(HistoryResult)
	if by := history.Entries[0].By; by == nil || by.Kind != "agent" || by.Name != "dave" {
		t.Errorf("by %+v", history.Entries[0].By)
	}
	mainHistory := w.do(context.Background(), "history", Input{ID: files["dev/src/main.go"].ID}, "").(HistoryResult)
	if history.Entries[0].Revision != mainHistory.Entries[0].Revision {
		t.Error("the changes were not one commit")
	}

	// An action that doesn't list the tools neither sees nor runs them.
	w.model.turns = []func(agentRequest) (agentResponse, error){
		func(req agentRequest) (agentResponse, error) {
			for _, raw := range req.Tools {
				if strings.Contains(string(raw), "demo_") {
					t.Errorf("plain action offered %s", raw)
				}
			}
			return useTools(toolUse("t1", "demo_snapshot", map[string]any{"paths": []string{"src/"}}))(req)
		},
		func(req agentRequest) (agentResponse, error) {
			if r := lastResults(t, req); !r[0].IsError {
				t.Errorf("plain action ran a module tool: %s", r[0].Content)
			}
			return agentResponse{Content: []json.RawMessage{textBlock("Done.")}, StopReason: "end_turn"}, nil
		},
	}
	w.write("dev/plain/a.md", "a\n")
	if jobs := w.wait(2); jobs[0].Status != jobDone || jobs[1].Status != jobDone {
		t.Fatalf("jobs %+v", jobs)
	}
}

// TestModuleToolsOnMCPAgainstDatabase: connected apps see module tools on
// /mcp/account only, and Open holds them to the connection's access.
func TestModuleToolsOnMCPAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	var calls []extension.ToolCall
	w.s.extensions = append(w.s.extensions, extension.Hooks{Name: "demo", Tools: demoTools(&calls)})
	if err := w.s.registerModuleTools(); err != nil {
		t.Fatal(err)
	}
	var err error
	if w.h.mcpAccount, err = w.h.mcpHandler(metatrash.ToolSchema, true); err != nil {
		t.Fatal(err)
	}
	if w.h.mcp, err = w.h.mcpHandler(metatrash.ToolSchema, false); err != nil {
		t.Fatal(err)
	}
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write", w.joinID: "read_only"}, "")
	listed := func(path string) bool {
		res := w.mcp(token, "tools/list", map[string]any{}, path)
		b, _ := json.Marshal(res.result)
		return strings.Contains(string(b), `"demo_snapshot"`) && strings.Contains(string(b), `"demo_propose"`)
	}
	if !listed("") {
		t.Fatal("the account endpoint does not list the module tools")
	}
	if listed("/mcp") {
		t.Fatal("the anonymous endpoint lists the module tools")
	}
	snap := w.tool(token, "demo_snapshot", map[string]any{"space": w.ownedName, "paths": []string{"AGENTS.md"}}, "")
	files := snap["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "AGENTS.md" || snap["folder"] != "" {
		t.Fatalf("snapshot %v", snap)
	}
	blob := files[0].(map[string]any)["blob"].(string)
	if len(calls) != 1 || calls[0].Agent != extension.AgentApp || calls[0].UserID != w.owner.ID {
		t.Fatalf("calls %+v", calls)
	}
	w.tool(token, "demo_snapshot", map[string]any{"space": w.joinName, "paths": []string{""}}, "")
	w.tool(token, "demo_snapshot", map[string]any{"space": w.ownedName, "paths": []string{"nope.md"}}, "not_found")
	w.tool(token, "demo_snapshot", map[string]any{"space": w.ownedName}, "invalid_request")
	if res := w.tool(token, "demo_snapshot", map[string]any{"space": w.ownedName, "paths": []string{}}, "invalid_request"); !strings.Contains(fmt.Sprint(res), "Name at least one path.") {
		t.Errorf("tool error: %v", res)
	}
	w.tool(token, "demo_propose", map[string]any{"space": w.joinName, "changes": []map[string]any{{"Path": "x.md", "Text": "x"}}}, "insufficient_scope")
	res := w.tool(token, "demo_propose", map[string]any{"space": w.ownedName, "changes": []map[string]any{
		{"Path": "AGENTS.md", "Base": blob, "Text": "# Notes\n"}, {"Path": "out/result.txt", "Text": "42\n"}}}, "")
	if res["state"] == "" {
		t.Fatalf("propose %v", res)
	}
	read := w.tool(token, "read", map[string]any{"space": w.ownedName, "path": "out/result.txt"}, "")
	if read["file"].(map[string]any)["text"] != "42\n" {
		t.Fatalf("read %v", read)
	}
	w.tool(token, "demo_propose", map[string]any{"space": w.ownedName, "changes": []map[string]any{{"Path": "AGENTS.md", "Base": blob, "Text": "again"}}}, "conflict")
}
