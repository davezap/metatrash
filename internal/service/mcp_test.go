package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"metatrash.com/metatrash"
)

// Owner-run transport smoke check; no live data or external HTTP requests.
func TestMCPSmoke(t *testing.T) {
	dir := t.TempDir()
	b, err := os.ReadFile("../../config/spaces.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Spaces["private"] = SpaceConfig{Visibility: "private", Overrides: json.RawMessage(`{}`)}
	b, _ = json.Marshal(cfg)
	configPath, keyPath := filepath.Join(dir, "spaces.json"), filepath.Join(dir, "keys.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	digest := func(key string) string { sum := sha256.Sum256([]byte(key)); return hex.EncodeToString(sum[:]) }
	b, _ = json.Marshal(map[string]Keys{"private": {ReadHashes: []string{digest("reader")}, WriteHashes: []string{digest("writer")}}})
	if err = os.WriteFile(keyPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), configPath, keyPath, filepath.Join(dir, "data"), metatrash.SpaceREADME)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	rpc := func(method, key string, params any, status int) map[string]any {
		t.Helper()
		body := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if method != "notifications/initialized" {
			body["id"] = 1
		}
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://metatrash.com/mcp", bytes.NewReader(b))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		r.Header.Set("MCP-Protocol-Version", "2025-11-25")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: HTTP %d: %s", method, w.Code, w.Body.String())
		}
		if status == 202 {
			return nil
		}
		var envelope map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope["error"] != nil {
			t.Fatalf("RPC error: %v", envelope["error"])
		}
		return envelope["result"].(map[string]any)
	}
	init := rpc("initialize", "", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "smoke", "version": "1.0.0"}}, 200)
	if init["protocolVersion"] != "2025-11-25" {
		t.Fatal(init)
	}
	rpc("notifications/initialized", "", map[string]any{}, 202)
	list := rpc("tools/list", "", map[string]any{}, 200)
	if len(list["tools"].([]any)) != 5 {
		t.Fatal(list)
	}
	call := func(name, key string, args map[string]any, code string) map[string]any {
		t.Helper()
		result := rpc("tools/call", key, map[string]any{"name": name, "arguments": args}, 200)
		text := result["content"].([]any)[0].(map[string]any)["text"].(string)
		var value map[string]any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatal(err)
		}
		if code != "" {
			if result["isError"] != true || value["error"].(map[string]any)["code"] != code {
				t.Fatalf("wanted %s: %v", code, result)
			}
		} else if result["isError"] == true || result["structuredContent"] == nil {
			t.Fatal(result)
		}
		return value
	}
	state := call("list", "", map[string]any{"space": "public"}, "")["state"]
	created := call("write", "", map[string]any{"space": "public", "path": "smoke.txt", "text": "hello\n", "ifInState": state, "createOnly": true}, "")
	id := created["file"].(map[string]any)["id"]
	call("write", "", map[string]any{"space": "public", "path": "smoke.txt", "text": "stale", "ifInState": state}, "state_mismatch")
	moved := call("move", "", map[string]any{"space": "public", "from": "smoke.txt", "to": "moved.txt", "ifInState": created["newState"]}, "")
	if moved["file"].(map[string]any)["id"] != id {
		t.Fatal("move changed identity")
	}
	read := call("read", "", map[string]any{"space": "public", "path": "moved.txt"}, "")
	if read["file"].(map[string]any)["text"] != "hello\n" {
		t.Fatal(read)
	}
	call("history", "", map[string]any{"space": "public", "id": id}, "")
	call("read", "", map[string]any{"space": "public", "path": "moved.txt", "text": "extra"}, "invalid_request")
	// Credentials must be rechecked on every request, never retained from a session.
	privateState := call("list", "reader", map[string]any{"space": "private"}, "")["state"]
	call("list", "", map[string]any{"space": "private"}, "unauthorized")
	call("write", "reader", map[string]any{"space": "private", "path": "test.txt", "text": "secret", "ifInState": privateState}, "forbidden")
	call("write", "writer", map[string]any{"space": "private", "path": "test.txt", "text": "secret", "ifInState": privateState}, "")
}
