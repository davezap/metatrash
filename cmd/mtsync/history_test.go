package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHistoryAndShow(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	server, _ := testSpace(t)
	folderClient = func(e folderEntry, _ func(context.Context, bool) (string, error)) *client {
		return newClient(e.Server, e.Space, func(context.Context, bool) (string, error) { return "writer", nil }, false)
	}
	ctx := context.Background()
	dir := t.TempDir()
	h := newHarness(t, server, "writer", dir)
	if err := saveRegistry(registry{Folders: []folderEntry{{Space: "private", Folder: dir, Server: server}}}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one\n", "two\n", "three\n"} {
		h.agentWrite("notes/plan.md", text)
	}
	h.sync()
	file := filepath.Join(dir, "notes", "plan.md")

	page := func(limit, offset int) historyJSON {
		t.Helper()
		var out bytes.Buffer
		if err := showHistory(ctx, file, limit, offset, true, &out); err != nil {
			t.Fatal(err)
		}
		var answer historyJSON
		if err := json.Unmarshal(out.Bytes(), &answer); err != nil {
			t.Fatalf("%v: %s", err, out.String())
		}
		return answer
	}
	first := page(2, 0)
	if len(first.Entries) != 2 || first.NextOffset == nil || *first.NextOffset != 2 || first.Space != "private" || first.ID == "" {
		t.Fatalf("first page: %+v", first)
	}
	if by := first.Entries[0].By; by == nil || by.String() != "(space key, agent)" {
		t.Fatalf("newest entry by %+v", by)
	}
	if first.Entries[0].Operation != "write" || first.Entries[0].File != file {
		t.Fatalf("newest entry: %+v", first.Entries[0])
	}
	last := page(2, 2)
	if len(last.Entries) != 1 || last.NextOffset != nil || last.Entries[0].Operation != "create" {
		t.Fatalf("last page: %+v", last)
	}
	if past := page(2, 10); len(past.Entries) != 0 || past.NextOffset != nil {
		t.Fatalf("past the end: %+v", past)
	}

	// show prints each revision's text exactly.
	for i, want := range []string{"three\n", "two\n"} {
		var out bytes.Buffer
		if err := showRevision(ctx, file, first.Entries[i].Revision, &out); err != nil || out.String() != want {
			t.Fatalf("show %d: %v %q", i, err, out.String())
		}
	}

	// After a rename, history follows the file and show takes the old name.
	if err := os.Rename(file, filepath.Join(dir, "notes", "plan-v2.md")); err != nil {
		t.Fatal(err)
	}
	h.sync()
	renamed := filepath.Join(dir, "notes", "plan-v2.md")
	var out bytes.Buffer
	if err := showHistory(ctx, renamed, 10, 0, true, &out); err != nil {
		t.Fatal(err)
	}
	var all historyJSON
	_ = json.Unmarshal(out.Bytes(), &all)
	if len(all.Entries) != 4 || all.Entries[0].Operation != "move" || all.Entries[1].File != file {
		t.Fatalf("after rename: %+v", all)
	}
	out.Reset()
	if err := showRevision(ctx, all.Entries[1].File, all.Entries[1].Revision, &out); err != nil || out.String() != "three\n" {
		t.Fatalf("show old name: %v %q", err, out.String())
	}

	// The short revisions the plain listing prints work too, and the current
	// name finds a revision from before the rename.
	for _, rev := range []string{all.Entries[1].Revision[:12], all.Entries[1].Revision[:7], strings.ToUpper(all.Entries[1].Revision[:12]), all.Entries[1].Revision} {
		out.Reset()
		if err := showRevision(ctx, renamed, rev, &out); err != nil || out.String() != "three\n" {
			t.Fatalf("show %s by current name: %v %q", rev, err, out.String())
		}
	}
	out.Reset()
	if err := showRevision(ctx, renamed, all.Entries[0].Revision[:12], &out); err != nil || out.String() != "three\n" {
		t.Fatalf("show the move: %v %q", err, out.String())
	}

	// The plain listing names the next page.
	out.Reset()
	if err := showHistory(ctx, renamed, 1, 0, false, &out); err != nil || !strings.Contains(out.String(), "move") || !strings.Contains(out.String(), "-offset 1 history") {
		t.Fatalf("text listing: %v\n%s", err, out.String())
	}

	// Refusals.
	h.put("draft.md", "not uploaded\n")
	if err := showHistory(ctx, filepath.Join(dir, "draft.md"), 10, 0, false, &out); err == nil || !strings.Contains(err.Error(), "not in private yet") {
		t.Fatalf("unsynced file: %v", err)
	}
	if err := showHistory(ctx, filepath.Join(t.TempDir(), "x.md"), 10, 0, false, &out); err == nil || !strings.Contains(err.Error(), "not in a folder mtsync syncs") {
		t.Fatalf("outside folders: %v", err)
	}
	if err := showHistory(ctx, renamed, 0, 0, false, &out); err == nil {
		t.Fatal("limit 0 accepted")
	}
	if err := showRevision(ctx, renamed, "abc", &out); err == nil || !strings.Contains(err.Error(), "not a revision") {
		t.Fatalf("short revision: %v", err)
	}
	if err := showRevision(ctx, file, first.Entries[0].Revision[:12], &out); err == nil || !strings.Contains(err.Error(), "current name") {
		t.Fatalf("short revision with the old name: %v", err)
	}
	if err := showRevision(ctx, renamed, "0000000", &out); err == nil || !strings.Contains(err.Error(), "not in the last") {
		t.Fatalf("short revision not in history: %v", err)
	}
	if err := showRevision(ctx, file, strings.Repeat("0", 40), &out); err == nil || !strings.Contains(err.Error(), "did not exist") {
		t.Fatalf("path not there at revision: %v", err)
	}
}
