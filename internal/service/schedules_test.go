package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseEvery(t *testing.T) {
	zone, _ := time.LoadLocation("Pacific/Auckland")
	// Sunday 25 October 2026, 10:20 in Auckland.
	after := time.Date(2026, 10, 25, 10, 20, 0, 0, zone)
	for every, want := range map[string]time.Time{
		"hour":           time.Date(2026, 10, 25, 11, 0, 0, 0, zone),
		"hour :30":       time.Date(2026, 10, 25, 10, 30, 0, 0, zone),
		"Day 06:00":      time.Date(2026, 10, 26, 6, 0, 0, 0, zone),
		"day 23:59":      time.Date(2026, 10, 25, 23, 59, 0, 0, zone),
		"weekday 08:00":  time.Date(2026, 10, 26, 8, 0, 0, 0, zone),
		"sunday 10:20":   time.Date(2026, 11, 1, 10, 20, 0, 0, zone),
		"saturday 09:15": time.Date(2026, 10, 31, 9, 15, 0, 0, zone),
		"month 1 09:00":  time.Date(2026, 11, 1, 9, 0, 0, 0, zone),
		"month 25 10:21": time.Date(2026, 10, 25, 10, 21, 0, 0, zone),
	} {
		s, err := parseEvery(every)
		if err != nil {
			t.Errorf("%s: %v", every, err)
			continue
		}
		if got := s.next(after, zone); !got.Equal(want) {
			t.Errorf("%s: next %s, want %s", every, got, want)
		}
	}
	// Friday evening: the next weekday run is Monday.
	friday := time.Date(2026, 10, 30, 20, 0, 0, 0, zone)
	s, _ := parseEvery("weekday 08:00")
	if got := s.next(friday, zone); got.Weekday() != time.Monday || got.Day() != 2 {
		t.Errorf("weekday after Friday: %s", got)
	}
	// Exactly on time counts as passed.
	s, _ = parseEvery("hour :30")
	if got := s.next(time.Date(2026, 10, 25, 10, 30, 0, 0, zone), zone); got.Hour() != 11 {
		t.Errorf("on the minute: %s", got)
	}
	for _, bad := range []string{"", "daily", "day", "day 6:00am", "day 24:00", "day 06:60", "hour 30", "month 29 09:00", "month 0 09:00", "fortnight 08:00", "monday", "monday 08:00 extra", strings.Repeat("x", 50)} {
		if _, err := parseEvery(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestRunnerSchedule(t *testing.T) {
	w := newRunnerWorld(t)
	w.write("hr/.metatrash.json", `{"actions":[{"name":"digest","every":"day 06:00","do":"Write today's digest to digest.md."}]}`)
	row := w.waitSchedule(func(s actionSchedule) bool { return s.Action == "digest" })
	if row.Folder != "hr/" || row.Every != "day 06:00" || !row.NextRun.After(time.Now()) || row.NextRun.In(w.runner.settings.zone).Hour() != 6 {
		t.Fatalf("schedule %+v", row)
	}
	// The space's own time zone moves the next run.
	w.mu.Lock()
	w.zones[w.repo.id] = "Australia/Perth"
	w.mu.Unlock()
	w.s.spaceZoneChanged(w.repo.id)
	row = w.waitSchedule(func(s actionSchedule) bool { return s.Action == "digest" })
	perth, _ := time.LoadLocation("Australia/Perth")
	if row.NextRun.In(perth).Hour() != 6 {
		t.Fatalf("after the zone change: %s", row.NextRun.In(perth))
	}
	w.model.turns = []func(agentRequest) (agentResponse, error){
		func(req agentRequest) (agentResponse, error) {
			var b contentBlock
			if err := json.Unmarshal(req.Messages[0].Content[0], &b); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.Text, "This is a scheduled run (every day 06:00, Australia/Perth time") || !strings.Contains(b.Text, "No file arrived.") {
				t.Errorf("prompt: %s", b.Text)
			}
			return useTools(toolUse("t1", "write", map[string]any{"path": "digest.md", "text": "# Digest\n", "ifInState": strings.Repeat("0", 40)}))(req)
		},
		func(agentRequest) (agentResponse, error) {
			return agentResponse{Content: []json.RawMessage{textBlock("Wrote the digest.")}, StopReason: "end_turn"}, nil
		},
	}
	// Bring the run forward: it is due now.
	row.NextRun = time.Now().Add(-time.Minute)
	if err := w.store.setSchedule(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	w.runner.poke()
	jobs := w.wait(1)
	if len(jobs) != 1 || jobs[0].File != "" || jobs[0].Action != "digest" || jobs[0].Status != jobDone || len(w.model.turns) != 0 {
		t.Fatalf("jobs %+v", jobs)
	}
	log := w.read("hr/runner.md")
	if !strings.Contains(log, "· digest · done") || !strings.Contains(log, "- Run: scheduled (every day 06:00)") || strings.Contains(log, "- File:") {
		t.Errorf("runner.md:\n%s", log)
	}
	row = w.waitSchedule(func(s actionSchedule) bool { return s.Action == "digest" && !s.LastRun.IsZero() })
	if !row.NextRun.After(time.Now()) {
		t.Errorf("not moved on: %+v", row)
	}
	// Nothing more is due, so nothing more runs.
	w.runner.poke()
	time.Sleep(100 * time.Millisecond)
	if n := len(w.store.list("")); n != 1 {
		t.Errorf("%d jobs after the run", n)
	}
	// Removing the action removes its schedule.
	w.write("hr/.metatrash.json", `{}`)
	deadline := time.Now().Add(5 * time.Second)
	for len(w.store.scheduleList(w.repo.id, time.Time{})) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("schedule not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitSchedule waits for a schedule row that ok accepts.
func (w *runnerWorld) waitSchedule(ok func(actionSchedule) bool) actionSchedule {
	w.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, s := range w.store.scheduleList(w.repo.id, time.Time{}) {
			if ok(s) {
				return s
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.t.Fatalf("no such schedule: %+v", w.store.scheduleList(w.repo.id, time.Time{}))
	return actionSchedule{}
}
