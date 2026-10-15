package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncedBuffer is a log the service and the test can share.
type syncedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncedBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncedBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServiceSyncsRegisteredFolders(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	serviceTick, workerRetry = 50*time.Millisecond, 100*time.Millisecond
	server, _ := testSpace(t)
	folderClient = func(e folderEntry, _ func(context.Context, bool) (string, error)) *client {
		key := func(context.Context, bool) (string, error) { return "writer", nil }
		return newClient(e.Server, e.Space, key, false)
	}
	agent := newClient(server, "private", func(context.Context, bool) (string, error) { return "writer", nil }, false)
	write := func(space *client, path, text string) {
		t.Helper()
		state, err := space.head(context.Background())
		if err == nil {
			_, err = space.write(context.Background(), path, text, state)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }

	logs := &syncedBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runService(ctx, 50*time.Millisecond, logs) }()
	eventually(t, 5*time.Second, "service running", serviceRunning)

	// A second service on the same computer is refused.
	if err := runService(ctx, time.Second, &syncedBuffer{}); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second service: %v", err)
	}

	// Registering folders starts them without a restart.
	a, b := filepath.Join(t.TempDir(), "a"), filepath.Join(t.TempDir(), "b")
	if err := saveRegistry(registry{Folders: []folderEntry{{Space: "private", Folder: a, Server: server}, {Space: "public", Folder: b, Server: server}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "both folders downloaded", func() bool {
		return exists(filepath.Join(a, "README.md")) && exists(filepath.Join(b, "README.md"))
	})
	write(agent, "hello.txt", "hi\n")
	eventually(t, 5*time.Second, "agent write downloaded", func() bool { return exists(filepath.Join(a, "hello.txt")) })
	eventually(t, 5*time.Second, "status written", func() bool {
		st := readStatus().Folders[a]
		return st.Space == "private" && !st.LastSync.IsZero() && st.Error == ""
	})
	var list bytes.Buffer
	if err := listFolders(&list); err != nil || !strings.Contains(list.String(), "in sync") {
		t.Fatalf("list: %v %s", err, list.String())
	}

	// A folder the service holds cannot be synced by another mtsync.
	s, _ := openSyncer(agent, a, server, "private", t.Logf)
	if _, err := s.lock(); err == nil {
		t.Fatal("folder lock not held")
	}

	// Removing a folder stops it; its files stay.
	if err := removeFolder(a, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "worker stopped", func() bool { return strings.Contains(logs.String(), "stopped syncing "+a) })
	write(agent, "later.txt", "not synced\n")
	time.Sleep(300 * time.Millisecond)
	if exists(filepath.Join(a, "later.txt")) || !exists(filepath.Join(a, "hello.txt")) {
		t.Fatal("removed folder still synced, or its files removed")
	}

	// mtsync stop ends the service and frees its lock.
	if err := requestStop(context.Background(), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("service did not stop")
	}
	if serviceRunning() || exists(filepath.Join(b, stateDir, "lock")) {
		t.Fatal("locks left after stop")
	}
}

func TestRegistryChecks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	root := t.TempDir()
	for _, c := range []struct {
		folder, parent string
		want           bool
	}{
		{filepath.Join(root, "a"), root, true},
		{root, root, true},
		{filepath.Join(root, "a", "b"), filepath.Join(root, "a"), true},
		{filepath.Join(root, "ab"), filepath.Join(root, "a"), false},
		{root, filepath.Join(root, "a"), false},
	} {
		if got := within(c.folder, c.parent); got != c.want {
			t.Errorf("within(%s, %s) = %v", c.folder, c.parent, got)
		}
	}
	// An overlapping folder is refused before anything is contacted.
	if err := saveRegistry(registry{Folders: []folderEntry{{Space: "x/y", Folder: filepath.Join(root, "a"), Server: defaultServer}}}); err != nil {
		t.Fatal(err)
	}
	if err := addFolder(context.Background(), defaultServer, "x/z", filepath.Join(root, "a", "inner"), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("nested folder: %v", err)
	}
	// Adding the same folder and space again is a no-op; another space is refused.
	var out bytes.Buffer
	if err := addFolder(context.Background(), defaultServer, "x/y", filepath.Join(root, "a"), &out); err != nil || !strings.Contains(out.String(), "nothing to add") {
		t.Fatalf("same folder again: %v %q", err, out.String())
	}
	if err := addFolder(context.Background(), defaultServer, "x/z", filepath.Join(root, "a"), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "mtsync remove") {
		t.Fatalf("same folder, other space: %v", err)
	}
	if err := removeFolder("x/y", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := removeFolder("x/y", &bytes.Buffer{}); err == nil {
		t.Fatal("removed twice")
	}
}

func TestLogTail(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	l, err := openLog("mtsync.log")
	if err != nil {
		t.Fatal(err)
	}
	l.maxBytes = 25
	for _, line := range []string{"one\n", "two\n", "three\n", "four\n", "five\n", "six\n", "seven\n"} {
		_, _ = l.Write([]byte(line))
	}
	var out bytes.Buffer
	if err := printLogTail(&out, 2); err != nil || !strings.HasSuffix(out.String(), "six\nseven\n") || strings.Contains(out.String(), "five") {
		t.Fatalf("tail: %q %v", out.String(), err)
	}
	path, _ := configFile("mtsync.log.1")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("log not rotated")
	}
}

func TestInstallFiles(t *testing.T) {
	if unit := systemdUnit("/home/dave/bin/mtsync"); !strings.Contains(unit, `ExecStart="/home/dave/bin/mtsync" run`) || !strings.Contains(unit, "WantedBy=default.target") {
		t.Fatal(unit)
	}
	if plist := launchAgent("/Users/dave/bin/mt&sync"); !strings.Contains(plist, "<string>/Users/dave/bin/mt&amp;sync</string><string>run</string>") {
		t.Fatal(plist)
	}
	if cmd := windowsCommand(`C:\Tools\mtsync.exe`); cmd != `"C:\Tools\mtsync.exe" start` {
		t.Fatal(cmd)
	}
}
