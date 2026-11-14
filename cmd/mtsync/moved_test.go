package main

import "testing"

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
