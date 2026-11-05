package main

import (
	"strings"
	"testing"
)

func TestPlatformPathProblems(t *testing.T) {
	paths := []string{"Café (draft) 📄.md", "what?.md", "notes:2026.txt", "NUL.txt", "COM¹.log", "METATR~1/state.json", "trailing.", "last ", "Report.pdf", "report.pdf", "Foo/a.md", "foo/b.md", "safe.md"}
	windows := pathProblems(paths, true, true)
	for _, name := range paths {
		want := name != paths[0] && name != "safe.md"
		if (windows[name] != "") != want {
			t.Errorf("Windows %q: %q, want problem=%v", name, windows[name], want)
		}
	}
	if problems := pathProblems(paths, false, false); len(problems) != 0 {
		t.Fatalf("case-sensitive storage-compatible disk: %v", problems)
	}
	if problems := pathProblems([]string{"safe.md", "../escape.md", ".mtsync/state.json", `dir\file.md`}, false, false); len(problems) != 3 {
		t.Fatalf("unsafe paths: %v", problems)
	}
}

// A case collision must leave both server files and the local file untouched,
// while an independent file still makes progress. This runs on any host OS.
func TestSyncSkipsCaseCollisions(t *testing.T) {
	server, _ := testSpace(t)
	h := newHarness(t, server, "writer", t.TempDir())
	h.sync()
	h.agentWrite("Report.md", "server upper")
	h.agentWrite("report.md", "server lower")
	h.agentWrite("safe.md", "safe")
	h.put("Report.md", "local edit")
	h.s.insensitive = true
	err := h.s.cycle(h.ctx)
	if err == nil || !strings.Contains(err.Error(), "cannot sync") {
		t.Fatalf("collision: %v", err)
	}
	if got, _ := h.localText("Report.md"); got != "local edit" {
		t.Fatalf("local overwritten: %q", got)
	}
	if h.remoteText("Report.md") != "server upper" || h.remoteText("report.md") != "server lower" {
		t.Fatal("server collision files changed")
	}
	if got, _ := h.localText("safe.md"); got != "safe" {
		t.Fatalf("unrelated download blocked: %q", got)
	}
	if _, ok := h.s.st.Files["Report.md"]; ok {
		t.Fatal("skipped file was recorded as agreed")
	}
	// Removing the ambiguity permits a later pass to recover without a reset.
	state, _ := h.remote()
	if _, err := h.agent.remove(h.ctx, "report.md", state); err != nil {
		t.Fatal(err)
	}
	h.sync()
	if h.remoteText("Report.md") != "server upper" {
		t.Fatal("server original changed during recovery")
	}
	copies := conflictCopies(t, h.dir)
	if len(copies) != 1 {
		t.Fatalf("expected preserved local edit, got %v", copies)
	}
	if got, _ := h.localText(copies[0]); got != "local edit" {
		t.Fatalf("lost local edit: %q", got)
	}
}
