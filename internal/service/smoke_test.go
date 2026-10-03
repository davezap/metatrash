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

// One small end-to-end check; run manually with go test ./internal/service -run TestSmoke.
func TestSmoke(t *testing.T) {
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
	configPath := filepath.Join(dir, "spaces.json")
	if err = os.WriteFile(configPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	digest := func(key string) string { sum := sha256.Sum256([]byte(key)); return hex.EncodeToString(sum[:]) }
	b, _ = json.Marshal(map[string]Keys{"private": {ReadHashes: []string{digest("read-secret")}, WriteHashes: []string{digest("write-secret")}}})
	keyPath := filepath.Join(dir, "keys.json")
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
	call := func(method, url, key string, body any, status int) []byte {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, url, bytes.NewReader(payload))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, url, w.Code, status, w.Body.String())
		}
		return w.Body.Bytes()
	}
	decode := func(b []byte, dst any) {
		t.Helper()
		if err := json.Unmarshal(b, dst); err != nil {
			t.Fatal(err)
		}
	}
	var list ListResult
	decode(call("GET", "/api/v1/spaces/public/files", "", nil, 200), &list)
	initial := list.State
	var write WriteResult
	decode(call("PUT", "/api/v1/spaces/public/file?path=inbox/a.txt", "", map[string]any{"text": "hello\n", "ifInState": initial, "createOnly": true}, 201), &write)
	call("PUT", "/api/v1/spaces/public/file?path=inbox/a.txt", "", map[string]any{"text": "stale", "ifInState": initial}, 409)
	call("PUT", "/api/v1/spaces/public/file?path=readme.MD", "", map[string]any{"text": "overwrite", "ifInState": write.NewState}, 403)
	call("POST", "/api/v1/spaces/public/move", "", map[string]any{"from": "README.md", "to": "elsewhere.txt", "ifInState": write.NewState}, 403)
	var moved Mutation
	decode(call("POST", "/api/v1/spaces/public/move", "", map[string]any{"from": "inbox/a.txt", "to": "archive/a.txt", "ifInState": write.NewState}, 200), &moved)
	if moved.File.ID != write.File.ID {
		t.Fatal("move changed identity")
	}
	call("GET", "/api/v1/spaces/public/file?path=inbox/a.txt", "", nil, 404)
	var read ReadResult
	decode(call("GET", "/api/v1/spaces/public/file?path=archive/a.txt", "", nil, 200), &read)
	if read.File.Text != "hello\n" || read.State != moved.NewState {
		t.Fatal("move changed text or returned wrong state")
	}
	call("GET", "/api/v1/spaces/public/file?path=inbox/a.txt&revision="+write.NewState, "", nil, 200)
	var history HistoryResult
	decode(call("GET", "/api/v1/spaces/public/history?id="+write.File.ID, "", nil, 200), &history)
	if len(history.Entries) != 2 || history.Entries[0].Operation != "move" {
		t.Fatal("history did not follow the move")
	}
	call("DELETE", "/api/v1/spaces/public/file?path=README.md", "", map[string]any{"ifInState": moved.NewState}, 403)
	call("DELETE", "/api/v1/spaces/public/file?path=.metatrash.json", "", map[string]any{"ifInState": moved.NewState}, 403)
	call("DELETE", "/api/v1/spaces/public/file?path=archive/a.txt", "", map[string]any{"ifInState": moved.NewState, "text": "x"}, 400)
	var deleted Mutation
	decode(call("DELETE", "/api/v1/spaces/public/file?path=archive/a.txt", "", map[string]any{"ifInState": moved.NewState}, 200), &deleted)
	if deleted.File.ID != write.File.ID || deleted.OldState != moved.NewState {
		t.Fatal("delete returned the wrong file or state")
	}
	call("GET", "/api/v1/spaces/public/file?path=archive/a.txt", "", nil, 404)
	call("GET", "/api/v1/spaces/public/file?path=.metatrash.json", "", nil, 200)
	call("GET", "/api/v1/spaces/private/files", "", nil, 401)
	call("GET", "/api/v1/spaces/private/files", "wrong", nil, 401)
	decode(call("GET", "/api/v1/spaces/private/files", "read-secret", nil, 200), &list)
	call("PUT", "/api/v1/spaces/private/file?path=hello.txt", "read-secret", map[string]any{"text": "private", "ifInState": list.State}, 403)
	call("PUT", "/api/v1/spaces/private/file?path=hello.txt", "write-secret", map[string]any{"text": "private", "ifInState": list.State}, 201)
	if validUnicodeJSON([]byte(`{"text":"\ud800"}`)) {
		t.Fatal("accepted lone surrogate")
	}
}
