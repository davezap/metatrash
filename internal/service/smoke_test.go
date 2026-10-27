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
	"strings"
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
	s, err := Open(context.Background(), configPath, keyPath, filepath.Join(dir, "data"), metatrash.SpaceAGENTS)
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
	call("DELETE", "/api/v1/spaces/public/file?path=inbox/AGENTS.md", "", map[string]any{"ifInState": write.NewState}, 403)
	call("POST", "/api/v1/spaces/public/move", "", map[string]any{"from": "AGENTS.md", "to": "elsewhere.txt", "ifInState": write.NewState}, 403)
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
	// offset pages without a cursor; past the end is an empty page.
	var older HistoryResult
	decode(call("GET", "/api/v1/spaces/public/history?limit=1&offset=1&id="+write.File.ID, "", nil, 200), &older)
	if len(older.Entries) != 1 || older.Entries[0].Operation != "create" || older.NextCursor != nil {
		t.Fatalf("history offset 1: %+v", older)
	}
	var newest HistoryResult
	decode(call("GET", "/api/v1/spaces/public/history?limit=1&offset=0&id="+write.File.ID, "", nil, 200), &newest)
	if len(newest.Entries) != 1 || newest.Entries[0].Operation != "move" || newest.NextCursor == nil {
		t.Fatalf("history offset 0: %+v", newest)
	}
	decode(call("GET", "/api/v1/spaces/public/history?offset=5&id="+write.File.ID, "", nil, 200), &older)
	if len(older.Entries) != 0 {
		t.Fatalf("history past the end: %+v", older)
	}
	call("GET", "/api/v1/spaces/public/history?offset=-1&id="+write.File.ID, "", nil, 400)
	call("GET", "/api/v1/spaces/public/history?offset=1&cursor="+*newest.NextCursor+"&id="+write.File.ID, "", nil, 400)
	call("GET", "/api/v1/spaces/public/files?offset=1", "", nil, 400)
	// Search engines: the public space's top folder only.
	for page, index := range map[string]bool{"/spaces/public/AGENTS.md": true, "/spaces/public/archive/a.txt": false} {
		r := httptest.NewRequest("GET", page, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		res := httptest.NewRecorder()
		h.ServeHTTP(res, r)
		tagged := res.Header().Get("X-Robots-Tag") == "noindex, nofollow" && strings.Contains(res.Body.String(), `name="robots" content="noindex, nofollow"`)
		clean := res.Header().Get("X-Robots-Tag") == "" && !strings.Contains(res.Body.String(), `name="robots"`)
		if res.Code != 200 || (index && !clean) || (!index && !tagged) {
			t.Fatalf("%s: %d header %q", page, res.Code, res.Header().Get("X-Robots-Tag"))
		}
	}
	// Markdown for agents (0.59.1): the file as it is, and llms.txt.
	for page, want := range map[string]string{"/spaces/public/archive/a.txt?format=md": "text/plain; charset=utf-8", "/spaces/public/AGENTS.md?format=md": "text/markdown; charset=utf-8", "/spaces/public/llms.txt": "text/plain; charset=utf-8", "/spaces/public/archive/llms.txt": "text/plain; charset=utf-8", "/spaces/public/archive/?format=md": "text/markdown; charset=utf-8"} {
		r := httptest.NewRequest("GET", page, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		res := httptest.NewRecorder()
		h.ServeHTTP(res, r)
		if res.Code != 200 || res.Header().Get("Content-Type") != want || strings.Contains(res.Body.String(), "<html") {
			t.Fatalf("%s: %d %s", page, res.Code, res.Header().Get("Content-Type"))
		}
	}
	r := httptest.NewRequest("GET", "/spaces/public/archive/a.txt", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Accept", "text/markdown")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, r)
	if res.Code != 200 || res.Body.String() != "hello\n" {
		t.Fatalf("Accept: text/markdown: %d %q", res.Code, res.Body)
	}
	// A folder's address opens its page (here its AGENTS.md).
	r = httptest.NewRequest("GET", "/spaces/public/archive/", nil)
	r.RemoteAddr = "127.0.0.1:12345"
	res = httptest.NewRecorder()
	h.ServeHTTP(res, r)
	if res.Code != 302 || res.Header().Get("Location") != "/spaces/public/archive/AGENTS.md" {
		t.Fatalf("folder: %d %s", res.Code, res.Header().Get("Location"))
	}
	call("DELETE", "/api/v1/spaces/public/file?path=AGENTS.md", "", map[string]any{"ifInState": moved.NewState}, 403)
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
	// blobs=true adds each file's Git blob hash (git hash-object of "private").
	decode(call("GET", "/api/v1/spaces/private/files?blobs=true", "read-secret", nil, 200), &list)
	blobs := map[string]string{}
	for _, f := range list.Files {
		blobs[f.Path] = f.Blob
	}
	if blobs["hello.txt"] != "c8c6761a582d9302073286f71d449f5e510da54d" || blobs["AGENTS.md"] == "" {
		t.Fatalf("list blobs: %v", blobs)
	}
	if b := call("GET", "/api/v1/spaces/private/files", "read-secret", nil, 200); bytes.Contains(b, []byte(`"blob"`)) {
		t.Fatal("blob sent without blobs=true")
	}
	call("GET", "/api/v1/spaces/private/files?blobs=1", "read-secret", nil, 400)
	if validUnicodeJSON([]byte(`{"text":"\ud800"}`)) {
		t.Fatal("accepted lone surrogate")
	}
}
