package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRerunAction(t *testing.T) {
	w := newRunnerWorld(t)
	ctx := context.Background()
	w.write("hr/.metatrash.json", tidyConfig)
	w.write("hr/inbound/a.md", "a\n")
	w.write("hr/inbound/b.md", "b\n")
	w.write("hr/notes.md", "not covered\n")
	w.wait(2)

	code := func(err error) string {
		if p, ok := err.(*Error); ok {
			return p.Code
		}
		return fmt.Sprint(err)
	}
	// Asking first: how many files, who pays; nothing is queued.
	res, err := w.s.rerunAction(ctx, w.repo, w.repo.owner, rerunRequest{Folder: "hr/", Action: "tidy-draft"})
	if err != nil || res.Files != 2 || res.Payer != "you" || res.Queued != 0 || strings.Join(res.Sample, ",") != "inbound/a.md,inbound/b.md" || len(w.store.list("")) != 2 {
		t.Fatalf("preview: %+v %v", res, err)
	}
	// Only whoever pays may: the owner here.
	if _, err := w.s.rerunAction(ctx, w.repo, aliceID, rerunRequest{Folder: "hr/", Action: "tidy-draft"}); code(err) != "forbidden" {
		t.Errorf("alice: %v", err)
	}
	if _, err := w.s.rerunAction(ctx, w.repo, w.repo.owner, rerunRequest{Folder: "hr/", Action: "nope"}); code(err) != "not_found" {
		t.Errorf("unknown action: %v", err)
	}
	if _, err := w.s.rerunAction(ctx, w.repo, w.repo.owner, rerunRequest{Folder: "hr", Action: "tidy-draft"}); code(err) != "invalid_request" {
		t.Errorf("folder without /: %v", err)
	}

	// Seventeen more jobs this hour: with a and b, 19 of the 20 allowed.
	for i := 0; i < 17; i++ {
		_ = w.store.add(ctx, agentJob{ID: fmt.Sprintf("%032d", i), SpaceID: w.repo.id, Folder: "hr/", Action: "tidy-draft", File: "hr/inbound/old.md", Status: jobDone, QueuedAt: time.Now()})
	}
	res, err = w.s.rerunAction(ctx, w.repo, w.repo.owner, rerunRequest{Folder: "hr/", Action: "tidy-draft", Confirm: true})
	if err != nil || res.Queued != 2 {
		t.Fatalf("confirm: %+v %v", res, err)
	}
	reruns := 0
	for _, j := range w.wait(21) {
		if j.Commit == rerunCommit {
			reruns++
			if j.Status != jobDone || j.Contributor != w.repo.owner {
				t.Errorf("re-run job %+v", j)
			}
		}
	}
	if reruns != 2 {
		t.Fatalf("%d re-run jobs", reruns)
	}
	w.model.mu.Lock()
	last := string(w.model.requests[len(w.model.requests)-1].Messages[0].Content[0])
	w.model.mu.Unlock()
	if !strings.Contains(last, "is being run again") {
		t.Errorf("prompt: %s", last)
	}
	// The re-run doesn't count toward the folder's hour: the next file runs.
	w.write("hr/inbound/c.md", "c\n")
	if j := w.job(22, "hr/inbound/c.md"); j.Status != jobDone {
		t.Errorf("next file after a re-run: %+v", j)
	}

	// A member's own action: only that member, on their agent.
	w.writeAs(aliceID, "ops/.metatrash.json", `{"actions":[{"name":"mine","on":"*.md","do":"Tidy.","member":"alice"}]}`)
	w.writeAs(w.repo.owner, "ops/x.md", "x\n")
	if _, err := w.s.rerunAction(ctx, w.repo, w.repo.owner, rerunRequest{Folder: "ops/", Action: "mine"}); code(err) != "forbidden" {
		t.Errorf("owner on alice's action: %v", err)
	}
	if res, err := w.s.rerunAction(ctx, w.repo, aliceID, rerunRequest{Folder: "ops/", Action: "mine"}); err != nil || res.Files != 1 || res.Payer != "you" {
		t.Errorf("alice on her action: %+v %v", res, err)
	}
	// A scheduled action has no files to run again.
	w.write("sched/.metatrash.json", `{"actions":[{"name":"daily","every":"day 06:00","do":"Summarise."}]}`)
	if _, err := w.s.rerunAction(ctx, w.repo, w.repo.owner, rerunRequest{Folder: "sched/", Action: "daily"}); code(err) != "invalid_request" {
		t.Errorf("scheduled: %v", err)
	}
}

func TestFolderSort(t *testing.T) {
	if _, err := parseFolderConfig(`{"sort":"z-a"}`); err != nil {
		t.Error(err)
	}
	if _, err := parseFolderConfig(`{"sort":"a-z"}`); err != nil {
		t.Error(err)
	}
	if _, err := parseFolderConfig(`{"sort":"newest"}`); err == nil || !strings.Contains(err.Error(), `sort is "a-z"`) {
		t.Errorf("bad sort: %v", err)
	}
	w := newRunnerWorld(t)
	w.write("notes/.metatrash.json", `{"sort":"z-a"}`)
	w.write("notes/a.md", "a\n")
	_, files, err := w.repo.snapshot(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if files["notes/.metatrash.json"].Sort != "z-a" || files["notes/a.md"].Sort != "" || files[".metatrash.json"].Sort != "" {
		t.Errorf("sort marks: %q %q %q", files["notes/.metatrash.json"].Sort, files["notes/a.md"].Sort, files[".metatrash.json"].Sort)
	}
	w.write("notes/.metatrash.json", `{}`)
	_, files, _ = w.repo.snapshot(context.Background(), "")
	if files["notes/.metatrash.json"].Sort != "" {
		t.Error("sort mark kept after it was removed")
	}
}
