package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
}

func writeStatus(t *testing.T, status statusBoard) {
	t.Helper()
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	path, err := configFile("sync-status.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestListFoldersJSON(t *testing.T) {
	isolateConfig(t)

	var out struct {
		ServiceRunning bool `json:"serviceRunning"`
		Folders        []struct {
			Folder   string `json:"folder"`
			State    string `json:"state"`
			LastSync string `json:"lastSync"`
			Error    string `json:"error"`
		} `json:"folders"`
	}
	var buf bytes.Buffer
	if err := listFolders(true, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ServiceRunning || out.Folders == nil || len(out.Folders) != 0 {
		t.Fatalf("empty folders = %+v, want serviceRunning false and empty array", out)
	}

	dir := t.TempDir()
	if err := saveRegistry(registry{Folders: []folderEntry{{Space: "dave/notes", Folder: dir, Server: defaultServer}}}); err != nil {
		t.Fatal(err)
	}
	writeStatus(t, statusBoard{Folders: map[string]folderStatus{dir: {Space: "dave/notes", LastSync: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)}}})
	lock, err := configFile("mtsync-service.lock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}

	buf.Reset()
	if err := listFolders(true, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Folders) != 1 || out.Folders[0].State != "in-sync" || out.Folders[0].LastSync != "2026-10-29T08:00:00Z" {
		t.Fatalf("folder = %+v, want in-sync at 2026-10-29T08:00:00Z", out.Folders)
	}
	buf.Reset()
	if err := listFolders(false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "in sync, checked") {
		t.Fatalf("text output %q does not contain in sync, checked", buf.String())
	}

	writeStatus(t, statusBoard{Folders: map[string]folderStatus{dir: {Space: "dave/notes", Error: "boom"}}})
	buf.Reset()
	if err := listFolders(true, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Folders) != 1 || out.Folders[0].State != "error" || out.Folders[0].Error != "boom" {
		t.Fatalf("folder = %+v, want error boom", out.Folders)
	}

	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := listFolders(true, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Folders) != 1 || out.Folders[0].State != "stopped" {
		t.Fatalf("folder = %+v, want stopped", out.Folders)
	}

	buf.Reset()
	if err := listFolders(false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "service not running") {
		t.Fatalf("text output %q does not contain service not running", buf.String())
	}
	if err := saveRegistry(registry{}); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := listFolders(false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No folders registered.") {
		t.Fatalf("text output %q does not contain empty-list message", buf.String())
	}
}

func TestListSpacesJSON(t *testing.T) {
	isolateConfig(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/account/spaces" {
			t.Errorf("path = %q, want /api/v1/account/spaces", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"spaces":[{"space":"dave/notes","name":"Notes","access":"read_only"}]}`))
	}))
	defer server.Close()

	var out struct {
		Server string `json:"server"`
		Spaces []struct {
			Space  string `json:"space"`
			Name   string `json:"name"`
			Access string `json:"access"`
		} `json:"spaces"`
	}
	var buf bytes.Buffer
	token := func(context.Context, bool) (string, error) { return "t", nil }
	if err := listSpaces(context.Background(), server.URL, token, true, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Server != server.URL || len(out.Spaces) != 1 || out.Spaces[0].Access != "read_only" {
		t.Fatalf("spaces output = %+v", out)
	}

	buf.Reset()
	if err := listSpaces(context.Background(), server.URL, token, false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "read only") || !strings.Contains(buf.String(), "Notes") {
		t.Fatalf("text output %q", buf.String())
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"spaces":[]}`))
	}))
	defer empty.Close()
	buf.Reset()
	if err := listSpaces(context.Background(), empty.URL, token, true, &buf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Spaces == nil || len(out.Spaces) != 0 {
		t.Fatalf("spaces = %+v, want empty array", out.Spaces)
	}
}
