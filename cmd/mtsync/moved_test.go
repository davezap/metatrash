package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The hosted service moved from metatrash.com to collumii.com (Core 0.95.29).
func TestCurrentServer(t *testing.T) {
	for in, want := range map[string]string{
		"https://metatrash.com":  "https://collumii.com",
		"https://metatrash.com/": "https://collumii.com",
		"https://collumii.com":   "https://collumii.com",
		"https://example.org":    "https://example.org",
		"http://127.0.0.1:8080":  "http://127.0.0.1:8080",
	} {
		if got := currentServer(in); got != want {
			t.Errorf("currentServer(%q) = %q, want %q", in, got, want)
		}
	}
	if defaultServer != "https://collumii.com" {
		t.Errorf("defaultServer = %q", defaultServer)
	}
}

// A folder whose state was saved against metatrash.com keeps syncing at
// collumii.com instead of being refused as "already syncs … on metatrash.com".
func TestOpenSyncerMovedServer(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, stateDir), 0700); err != nil {
		t.Fatal(err)
	}
	state := `{"server":"https://metatrash.com","space":"dave-zap/notes","state":"","files":{}}`
	if err := os.WriteFile(filepath.Join(root, stateDir, "state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openSyncer(nil, root, "https://collumii.com", "dave-zap/notes", t.Logf)
	if err != nil {
		t.Fatalf("openSyncer: %v", err)
	}
	if s.st.Server != "https://collumii.com" {
		t.Errorf("state server = %q, want https://collumii.com", s.st.Server)
	}
	if _, err := openSyncer(nil, root, "https://collumii.com", "dave-zap/other", t.Logf); err == nil {
		t.Error("a folder syncing another space was accepted")
	}
}
