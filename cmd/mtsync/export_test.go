package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportSpace(t *testing.T) {
	server, _ := testSpace(t)
	ctx := context.Background()
	agent := newClient(server, "private", func(context.Context, bool) (string, error) { return "writer", nil }, false)
	state, err := agent.head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.write(ctx, "notes/plan.md", "the plan\n", state); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MTSYNC_TEST_KEY", "reader")
	dir := t.TempDir()
	file := filepath.Join(dir, "backup.zip")
	var out bytes.Buffer
	if err := exportSpace(ctx, server, "private", file, "MTSYNC_TEST_KEY", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "to "+file) {
		t.Fatalf("output: %s", out.String())
	}
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if got := strings.Join(names, " "); !strings.Contains(got, "notes/plan.md") || !strings.Contains(got, "AGENTS.md") || !strings.Contains(got, ".metatrash-export.json") {
		t.Fatalf("zip holds %s", got)
	}

	// Default name: space-date-state.zip in the current folder.
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := exportSpace(ctx, server, "private", "", "MTSYNC_TEST_KEY", &out); err != nil {
		t.Fatal(err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "private-*-*.zip")); len(matches) != 1 {
		t.Fatalf("default file not written: %v (%s)", matches, out.String())
	}

	if err := exportSpace(ctx, server, "private/notes", file, "MTSYNC_TEST_KEY", &out); err == nil || !strings.Contains(err.Error(), "whole space") {
		t.Fatalf("folder export: %v", err)
	}
	t.Setenv("MTSYNC_TEST_KEY", "wrong")
	if err := exportSpace(ctx, server, "private", file, "MTSYNC_TEST_KEY", &out); errorCode(err) != "unauthorized" {
		t.Fatalf("wrong key: %v", err)
	}
}
