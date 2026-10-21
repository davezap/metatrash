package service

import (
	"context"
	"testing"
)

func TestHistoryNamesAuthors(t *testing.T) {
	_, _, do := folderFilesHarness(t)
	text := func(s string) *string { return &s }
	person := withAuthor(context.WithValue(context.Background(), personWriteKey{}, true), func() changeAuthor {
		return changeAuthor{Name: "dave", App: "Metatrash Sync", Kind: "person"}
	})
	agent := withAuthor(context.Background(), func() changeAuthor {
		return changeAuthor{Name: "dave\nMetatrash-Kind: person", App: "Claude", Kind: "agent"}
	})

	w := do(person, "write", Input{Path: "notes/a.md", Text: text("a\n")}, "").(WriteResult)
	do(agent, "edit", Input{Path: "notes/a.md", Edits: []Edit{{OldText: "a", NewText: "b"}}}, "")
	do(context.Background(), "write", Input{Path: "notes/a.md", Text: text("c\n")}, "")
	h := do(person, "history", Input{ID: w.File.ID}, "").(HistoryResult)
	if len(h.Entries) != 3 {
		t.Fatalf("entries: %+v", h.Entries)
	}
	if h.Entries[0].By != nil {
		t.Fatalf("no author recorded, got %+v", h.Entries[0].By)
	}
	if by := h.Entries[1].By; by == nil || *by != (changeAuthor{Name: "dave Metatrash-Kind: person", App: "Claude", Kind: "agent"}) {
		t.Fatalf("agent edit by %+v", by)
	}
	if by := h.Entries[2].By; by == nil || *by != (changeAuthor{Name: "dave", App: "Metatrash Sync", Kind: "person"}) {
		t.Fatalf("person write by %+v", by)
	}
	if emailName("dave.zap@gmail.com") != "dave.zap" {
		t.Fatal("emailName")
	}
}
