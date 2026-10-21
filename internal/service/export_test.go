package service

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metatrash.com/metatrash"
)

func TestExport(t *testing.T) {
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
	data := filepath.Join(dir, "data")
	s, err := Open(context.Background(), configPath, keyPath, data, metatrash.SpaceAGENTS)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h, err := s.Handler(metatrash.ToolSchema, nil)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, url, key string, body any, status int) *httptest.ResponseRecorder {
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
		return w
	}
	var list ListResult
	if err := json.Unmarshal(call("GET", "/api/v1/spaces/private/files", "read-secret", nil, 200).Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	state := list.State
	texts := map[string]string{"notes/a.md": "alpha\n", "notes/deep/b.txt": "beta", "empty.txt": ""}
	for _, p := range []string{"notes/a.md", "notes/deep/b.txt", "empty.txt"} {
		var write WriteResult
		if err := json.Unmarshal(call("PUT", "/api/v1/spaces/private/file?path="+p, "write-secret", map[string]any{"text": texts[p], "ifInState": state}, 201).Body.Bytes(), &write); err != nil {
			t.Fatal(err)
		}
		state = write.NewState
	}

	call("GET", "/api/v1/spaces/private/export", "", nil, 401)
	call("POST", "/api/v1/spaces/private/export", "write-secret", nil, 405)
	call("GET", "/api/v1/spaces/private/export?prefix=notes/", "read-secret", nil, 400)
	w := call("GET", "/api/v1/spaces/private/export", "read-secret", nil, 200)
	if w.Header().Get("Content-Type") != "application/zip" || w.Header().Get("X-Metatrash-State") != state || !strings.Contains(w.Header().Get("Content-Disposition"), `filename="private-`) {
		t.Fatalf("headers: %v", w.Header())
	}
	got := unzip(t, w.Body.Bytes())
	for p, text := range texts {
		if got[p] != text {
			t.Fatalf("%s: got %q, want %q", p, got[p], text)
		}
	}
	if !strings.Contains(got["AGENTS.md"], "Agent instructions") {
		t.Fatalf("AGENTS.md missing from export: %v", got)
	}
	var manifest ExportManifest
	if err := json.Unmarshal([]byte(got[exportManifestName]), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Space != "private" || manifest.State != state || len(manifest.Files) != len(got)-1 {
		t.Fatalf("manifest: %+v", manifest)
	}
	for _, f := range manifest.Files {
		if !idPattern.MatchString(f.ID) || int64(len(got[f.Path])) != f.Bytes {
			t.Fatalf("manifest file: %+v", f)
		}
	}

	// The console export reads the same repository from disk.
	consoleState, b, err := ExportRepository(context.Background(), filepath.Join(data, "repos", "private.git"), "private")
	if err != nil || consoleState != state {
		t.Fatalf("console export: %v %s", err, consoleState)
	}
	if fromDisk := unzip(t, b); len(fromDisk) != len(got) || fromDisk["notes/a.md"] != texts["notes/a.md"] {
		t.Fatalf("console export differs: %v", fromDisk)
	}
	if _, _, err := ExportRepository(context.Background(), filepath.Join(data, "repos", "nope.git"), "nope"); err == nil {
		t.Fatal("missing repository exported")
	}

	// Each export reads the whole space, so they have their own allowance.
	for i := 1; i < exportsPerClient; i++ {
		call("GET", "/api/v1/spaces/private/export", "read-secret", nil, 200)
	}
	call("GET", "/api/v1/spaces/private/export", "read-secret", nil, 429)
	call("GET", "/api/v1/spaces/public/export", "", nil, 200)
}

func unzip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		text, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(text)
	}
	return out
}

// The console finds an owned space by owner/slug or ID and exports its
// repository from the data directory.
func TestConsoleExportOwnedSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(`{"databaseConfigFile":"`+os.Getenv("METATRASH_TEST_DB_CONFIG")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdminDB(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, ref := range []string{w.ownedName, strings.ToUpper(w.ownedName) + "/", w.ownedID} {
		if space, err := admin.FindSpace(ctx, ref); err != nil || space.ID != w.ownedID || space.Owner+"/"+space.Slug != w.ownedName {
			t.Fatalf("FindSpace(%q): %+v %v", ref, space, err)
		}
	}
	for _, ref := range []string{"public", "nobody/nothing", strings.Repeat("0", 32), w.owner.Username + "/" + w.joinSlug} {
		if _, err := admin.FindSpace(ctx, ref); err != ErrNoSpace {
			t.Fatalf("FindSpace(%q): %v", ref, err)
		}
	}
	gitDir, err := SpaceRepositoryPath(integrationDataDir, true, w.ownedID)
	if err != nil {
		t.Fatal(err)
	}
	state, b, err := ExportRepository(ctx, gitDir, w.ownedName)
	if err != nil {
		t.Fatal(err)
	}
	files := unzip(t, b)
	var manifest ExportManifest
	if err := json.Unmarshal([]byte(files[exportManifestName]), &manifest); err != nil || manifest.Space != w.ownedName || manifest.State != state || files["AGENTS.md"] == "" {
		t.Fatalf("owned export: %v %+v %v", err, manifest, files)
	}
	if _, err := SpaceRepositoryPath(integrationDataDir, true, "../x"); err == nil {
		t.Fatal("bad space ID accepted")
	}
	if _, err := SpaceRepositoryPath(integrationDataDir, false, "../x"); err == nil {
		t.Fatal("bad space name accepted")
	}
}
