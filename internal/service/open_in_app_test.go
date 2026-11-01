package service

import (
	"net/http/httptest"
	"testing"
)

// The app's old addresses (/app/..., 0.73.0 to 0.86.0) go to the space's own
// address, and the docs space's to /docs/ (0.87.0).
func TestOldAppAddresses(t *testing.T) {
	w := newOAuthWorld(t)
	w.s.accounts.config.DocsSpace = w.joinName
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://metatrash.com"+path, nil)
		r.RemoteAddr = "192.0.2.15:1"
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	for path, want := range map[string]string{
		"/app":                  "/spaces/",
		"/app/":                 "/spaces/",
		"/app/public/README.md": "/spaces/public/README.md",
		"/app/" + w.ownedName + "/notes/a%20b.md": "/spaces/" + w.ownedName + "/notes/a%20b.md",
		"/app/" + w.ownedName + "/":               "/spaces/" + w.ownedName + "/",
		"/app/" + w.joinName + "/guides/x.md":     "/docs/guides/x.md",
		"/app/" + w.joinName + "/":                "/docs/",
		"/app/" + w.joinName:                      "/docs/",
	} {
		if res := get(path); res.Code != 301 || res.Header().Get("Location") != want {
			t.Fatalf("%s: %d %q, want %q", path, res.Code, res.Header().Get("Location"), want)
		}
	}
}
