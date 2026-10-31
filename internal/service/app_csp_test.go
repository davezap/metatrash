package service

import (
	"strings"
	"testing"
)

// The app page shows Markdown other people wrote on the same page as the
// account's settings (0.79.0), so its CSP is the backstop if Slate ever lets
// markup through. This fails if the policy loosens.
func TestAppCSP(t *testing.T) {
	policy := appCSP("abc123")
	directives := map[string]string{}
	for _, part := range strings.Split(policy, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		if _, dup := directives[fields[0]]; dup {
			t.Fatalf("directive %s twice: %s", fields[0], policy)
		}
		directives[fields[0]] = strings.Join(fields[1:], " ")
	}
	want := map[string]string{
		"default-src":     "'none'",
		"script-src":      "'self'",
		"base-uri":        "'none'",
		"frame-ancestors": "'none'",
		"form-action":     "'self'",
		"connect-src":     "'self'",
	}
	for name, value := range want {
		if directives[name] != value {
			t.Errorf("%s is %q, want %q", name, directives[name], value)
		}
	}
	// Frames, plugins, workers and other scripts fall back to default-src 'none'.
	for _, name := range []string{"script-src-elem", "script-src-attr", "frame-src", "child-src", "object-src", "worker-src", "manifest-src"} {
		if v, ok := directives[name]; ok {
			t.Errorf("%s must not be set (it would override default-src 'none'): %q", name, v)
		}
	}
	for _, bad := range []string{"unsafe-eval", "wasm-unsafe-eval", "strict-dynamic", "unsafe-hashes", "*", "http:"} {
		for name, value := range directives {
			for _, token := range strings.Fields(value) {
				if strings.Trim(token, "'") == bad && !(name == "style-src-attr" && bad == "unsafe-inline") {
					t.Errorf("%s allows %s", name, token)
				}
			}
		}
	}
	if strings.Contains(directives["script-src"], "unsafe-inline") || strings.Contains(directives["style-src"], "unsafe-inline") {
		t.Errorf("inline script or style elements allowed: %s", policy)
	}
	if !strings.Contains(directives["style-src"], "'nonce-abc123'") {
		t.Errorf("style nonce missing: %s", directives["style-src"])
	}
}
