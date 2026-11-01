package service

import (
	"net/http/httptest"
	"testing"
)

// Opening a /spaces/... or /docs/... link from outside the site goes to the
// same file in the app (0.83.0); the plain page stays for moving between
// plain pages, queries, Markdown, and programs and search engines.
func TestOpenInApp(t *testing.T) {
	w := newOAuthWorld(t)
	if appBuildOnce() == nil {
		t.Skip("no website build in web/app")
	}
	w.s.accounts.config.DocsSpace = w.ownedName
	browser := []string{"Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document", "Accept", "text/html,application/xhtml+xml,*/*;q=0.8"}
	get := func(path string, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://metatrash.com"+path, nil)
		r.RemoteAddr = "192.0.2.15:1"
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	opens := func(path, site, want string) {
		t.Helper()
		res := get(path, append(browser, "Sec-Fetch-Site", site)...)
		if res.Code != 302 || res.Header().Get("Location") != want {
			t.Fatalf("%s (%s): %d %q", path, site, res.Code, res.Header().Get("Location"))
		}
	}
	for _, site := range []string{"none", "cross-site", "same-site"} {
		opens("/spaces/"+w.ownedName+"/notes/a%20b.md", site, "/app/"+w.ownedName+"/notes/a%20b.md")
	}
	opens("/spaces/"+w.ownedName+"/", "none", "/app/"+w.ownedName+"/")
	opens("/spaces/"+w.ownedName, "none", "/app/"+w.ownedName+"/")
	opens("/spaces/public/README.md", "cross-site", "/app/public/README.md")
	opens("/docs/guides/start.md", "none", "/app/"+w.ownedName+"/guides/start.md")
	opens("/docs/", "none", "/app/"+w.ownedName+"/")
	opens("/docs", "none", "/app/"+w.ownedName+"/")

	// Everything else keeps the plain page (or whatever it answered before).
	path := "/spaces/" + w.ownedName + "/notes/a.md"
	for name, headers := range map[string][]string{
		"same-origin":  append(append([]string{}, browser...), "Sec-Fetch-Site", "same-origin"),
		"program":      nil,
		"markdown":     {"Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document", "Sec-Fetch-Site", "none", "Accept", "text/markdown"},
		"image":        {"Sec-Fetch-Mode", "no-cors", "Sec-Fetch-Dest", "image", "Sec-Fetch-Site", "cross-site"},
		"fetch":        {"Sec-Fetch-Mode", "cors", "Sec-Fetch-Dest", "empty", "Sec-Fetch-Site", "same-origin"},
		"no site sent": browser,
	} {
		if res := get(path, headers...); res.Code == 302 {
			t.Fatalf("%s opened the app: %q", name, res.Header().Get("Location"))
		}
	}
	for _, query := range []string{"?raw", "?export", "?format=md"} {
		if res := get(path+query, append(browser, "Sec-Fetch-Site", "none")...); res.Code == 302 {
			t.Fatalf("%s opened the app", query)
		}
	}
	if res := get("/llms.txt", append(browser, "Sec-Fetch-Site", "none")...); res.Code == 302 {
		t.Fatal("llms.txt opened the app")
	}
	if res := get("/spaces/public/?path=README.md", append(browser, "Sec-Fetch-Site", "none")...); res.Code == 302 {
		t.Fatal("public space query opened the app")
	}
	if res := httptest.NewRecorder(); true {
		r := httptest.NewRequest("HEAD", "https://metatrash.com"+path, nil)
		for i := 0; i+1 < len(browser); i += 2 {
			r.Header.Set(browser[i], browser[i+1])
		}
		r.Header.Set("Sec-Fetch-Site", "none")
		w.h.ServeHTTP(res, r)
		if res.Code == 302 {
			t.Fatal("HEAD opened the app")
		}
	}
}
