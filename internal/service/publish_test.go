package service

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestPublishedVersionSettings(t *testing.T) {
	for text, want := range map[string]string{
		`{"publishedVersion":0}`:                                                             "whole number",
		`{"publishedVersion":1.5}`:                                                           "wrong value type",
		`{"publishedVersion":"2"}`:                                                           "wrong value type",
		`{"services":[{"type":"acknowledge","repo":"a/b"}]}`:                                 "takes only files",
		`{"services":[{"type":"acknowledge","files":["a/b.md"]}]}`:                           "files entry",
		`{"services":[{"type":"github","repo":"a/b","files":["x.md"]}]}`:                     "takes no files",
		`{"services":[{"type":"acknowledge"},{"type":"acknowledge"}]}`:                       "only one acknowledge",
		`{"locked":{"publishedVersion":"agents","bogus":"agents"}}`:                          "publishedVersion",
		`{"services":[{"type":"acknowledge","files":["` + strings.Repeat("x", 241) + `"]}]}`: "files entry",
	} {
		if _, err := parseFolderConfig(text); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", text, err, want)
		}
	}
	for _, text := range []string{
		`{"publishedVersion":3,"locked":{"publishedVersion":"agents"}}`,
		`{"services":[{"type":"acknowledge"}],"publishedVersion":1}`,
		`{"services":[{"type":"acknowledge","files":["*.md","hr/"]},{"type":"github","repo":"a/b"}]}`,
	} {
		if _, err := parseFolderConfig(text); err != nil {
			t.Errorf("%s: %v", text, err)
		}
	}

	_, _, do := folderFilesHarness(t)
	agent := context.Background()
	person := context.WithValue(agent, personWriteKey{}, true)
	text := func(s string) *string { return &s }
	do(agent, "write", Input{Path: "policies/leave.md", Text: text("Leave v1\n")}, "")
	// acknowledge may sit on the root and nest; publishedVersion only goes up.
	do(agent, "write", Input{Path: ".metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}]}`)}, "")
	do(agent, "write", Input{Path: "policies/.metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}],"publishedVersion":2}`)}, "")
	do(agent, "write", Input{Path: "policies/.metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}],"publishedVersion":1}`)}, "invalid_request")
	do(agent, "write", Input{Path: "policies/.metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}],"publishedVersion":3}`)}, "")
	// A person can keep agents from publishing.
	do(person, "write", Input{Path: "policies/.metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}],"publishedVersion":3,"locked":{"publishedVersion":"agents"}}`)}, "")
	do(agent, "write", Input{Path: "policies/.metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}],"publishedVersion":4,"locked":{"publishedVersion":"agents"}}`)}, "locked_setting")
	do(person, "write", Input{Path: "policies/.metatrash.json", Text: text(`{"services":[{"type":"acknowledge"}],"publishedVersion":4,"locked":{"publishedVersion":"agents"}}`)}, "")
	// Not inherited: a subfolder publishes on its own.
	do(agent, "write", Input{Path: "policies/hr/a.md", Text: text("a\n")}, "")
	r := do(agent, "read", Input{Path: "policies/hr/.metatrash.json"}, "").(ReadResult)
	if _, ok := configKeys(string(r.Effective))["publishedVersion"]; ok {
		t.Fatalf("publishedVersion inherited: %s", r.Effective)
	}
}

func TestPublishedVersions(t *testing.T) {
	_, owned, do := folderFilesHarness(t)
	ctx := context.Background()
	person := withAuthor(context.WithValue(ctx, personWriteKey{}, true), func() changeAuthor {
		return changeAuthor{Name: "dave", App: "Metatrash Sync", Kind: "person"}
	})
	text := func(s string) *string { return &s }
	config := func(v string) *string {
		return text(`{"services":[{"type":"acknowledge"}],"publishedVersion":` + v + `}`)
	}
	leave := do(ctx, "write", Input{Path: "policies/leave.md", Text: text("Leave v1\n")}, "").(WriteResult)
	do(ctx, "write", Input{Path: "policies/travel.md", Text: text("Travel v1\n")}, "")
	do(ctx, "write", Input{Path: "policies/logo.png.txt", Text: text("not a policy\n")}, "")
	do(person, "write", Input{Path: "policies/.metatrash.json", Text: config("1")}, "")
	// A draft edit, then version 2 changes leave only.
	do(ctx, "write", Input{Path: "policies/leave.md", Text: text("Leave typo\n")}, "")
	do(ctx, "write", Input{Path: "policies/leave.md", Text: text("Leave v2\n")}, "")
	do(person, "write", Input{Path: "policies/.metatrash.json", Text: config("2")}, "")
	do(ctx, "write", Input{Path: "policies/leave.md", Text: text("Leave draft\n")}, "")
	do(ctx, "write", Input{Path: "policies/new.md", Text: text("Not yet published\n")}, "")

	state, files, err := owned.snapshot(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	points, err := owned.publishPoints(ctx, state, files, "policies/")
	if err != nil || len(points) != 2 || points[0].Version != 1 || points[1].Version != 2 || points[1].By == nil || points[1].By.Name != "dave" {
		t.Fatalf("points %+v %v", points, err)
	}
	folders, err := owned.publishedFolders(ctx, state, files)
	if err != nil || len(folders) != 1 {
		t.Fatalf("folders %+v %v", folders, err)
	}
	got := map[string]policyFile{}
	for _, f := range folders[0].Files {
		got[f.Path] = f
	}
	if len(got) != 3 || !got["policies/leave.md"].Draft || got["policies/travel.md"].Draft || !got["policies/leave.md"].Tracked || got["policies/logo.png.txt"].Tracked {
		t.Fatalf("files %+v", got)
	}
	if b, _ := owned.blob(ctx, got["policies/leave.md"].Blob); string(b) != "Leave v2\n" {
		t.Fatalf("published leave %q", b)
	}

	// History marks the text each version published.
	h := do(ctx, "history", Input{ID: leave.File.ID}, "").(HistoryResult)
	var marks [][]int
	for _, e := range h.Entries {
		marks = append(marks, e.Published)
	}
	// newest first: draft, v2, typo, v1
	if len(marks) != 4 || marks[0] != nil || !slices.Equal(marks[1], []int{2}) || marks[2] != nil || !slices.Equal(marks[3], []int{1}) {
		t.Fatalf("marks %v", marks)
	}
	travel := got["policies/travel.md"]
	h = do(ctx, "history", Input{ID: travel.ID}, "").(HistoryResult)
	if len(h.Entries) != 1 || !slices.Equal(h.Entries[0].Published, []int{1, 2}) {
		t.Fatalf("travel marks %+v", h.Entries)
	}

	// The tracked files rule: names, patterns and subfolders.
	svc := folderService{Type: "acknowledge", Files: []string{"leave.md", "hr/"}}
	for p, want := range map[string]bool{"policies/leave.md": true, "policies/travel.md": false, "policies/hr/x/y.txt": true, "policies/AGENTS.md": false, "other/leave.md": false} {
		if svc.tracked("policies/", p) != want {
			t.Errorf("tracked %s = %v", p, !want)
		}
	}
}
