package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// runLoop starts loop in the background with logs collected safely.
func runLoop(t *testing.T, server, dir string, interval time.Duration) (*harness, func() []string) {
	t.Helper()
	h := newHarness(t, server, "writer", dir)
	var mu sync.Mutex
	logs := []string{}
	h.s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, strings.TrimSpace(fmt.Sprintf(format, args...)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = h.s.loop(ctx, interval, false)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), logs...)
	}
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %s", what, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// With a long interval, an agent's write still arrives at once: the server
// answers the waiting request when the space moves.
func TestLoopHearsChangesAtOnce(t *testing.T) {
	server, requests := testSpace(t)
	dir := t.TempDir()
	h, _ := runLoop(t, server, dir, time.Minute)
	eventually(t, 5*time.Second, "first download", func() bool { _, ok := h.localText("README.md"); return ok })
	h.agentWrite("news.txt", "fresh\n")
	eventually(t, 3*time.Second, "agent write downloaded", func() bool { text, _ := h.localText("news.txt"); return text == "fresh\n" })
	// Idle: only the waiting request is open, no polling.
	time.Sleep(300 * time.Millisecond)
	before := requests.Load()
	time.Sleep(time.Second)
	if n := requests.Load() - before; n > 0 {
		t.Fatalf("%d requests while idle", n)
	}
}

// Local saves are found by scanning the folder every interval.
func TestLoopUploadsLocalSaves(t *testing.T) {
	server, _ := testSpace(t)
	dir := t.TempDir()
	h, _ := runLoop(t, server, dir, 100*time.Millisecond)
	eventually(t, 5*time.Second, "first download", func() bool { _, ok := h.localText("README.md"); return ok })
	if err := os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("saved here\n"), 0644); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "local save uploaded", func() bool {
		text, err := h.agent.read(context.Background(), "mine.txt", "")
		return err == nil && text == "saved here\n"
	})
}

// A server from before 0.34.0 refuses after=; mtsync falls back to checking
// every interval.
func TestLoopFallsBackToPolling(t *testing.T) {
	server, _ := testSpaceServer(t, true)
	dir := t.TempDir()
	h, logs := runLoop(t, server, dir, 100*time.Millisecond)
	eventually(t, 5*time.Second, "first download", func() bool { _, ok := h.localText("README.md"); return ok })
	h.agentWrite("news.txt", "polled\n")
	eventually(t, 5*time.Second, "agent write downloaded", func() bool { text, _ := h.localText("news.txt"); return text == "polled\n" })
	found := false
	for _, line := range logs() {
		found = found || strings.Contains(line, "does not announce changes")
	}
	if !found {
		t.Fatalf("no fallback notice: %v", logs())
	}
}
