package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// Slate Viewer's UI on the site (0.73.0), at the space's own addresses since
// 0.87.0: the page with the space and file in its meta config, for whoever
// may read the space.
func TestAppPagesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	stranger := w.user("stranger")
	ownerSession, strangerSession := w.session(w.owner), w.session(stranger)
	get := func(path string, headers ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://metatrash.com"+path, nil)
		r.RemoteAddr = "192.0.2.13:1"
		for i := 0; i+1 < len(headers); i += 2 {
			r.Header.Set(headers[i], headers[i+1])
		}
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	withSession := func(path string, session string) *httptest.ResponseRecorder {
		return get(path, "Cookie", w.h.sessionCookieName()+"="+session)
	}
	build := appBuildOnce()
	if build == nil {
		t.Skip("no website build in web/app")
	}
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "notes/a.md", "text": "# A\n", "ifInState": state}, "")
	page := "/spaces/" + w.ownedName + "/notes/a.md"

	// The owner gets the app, set to open the file, keeping the address.
	res := withSession(page, ownerSession.Value)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, `&#34;file&#34;:&#34;/spaces/`+w.ownedName+`/notes/a.md&#34;`) || !strings.Contains(body, `&#34;root&#34;:&#34;/spaces/`+w.ownedName+`&#34;`) || !strings.Contains(body, `&#34;addressRoot&#34;:&#34;/spaces/`+w.ownedName+`&#34;`) {
		t.Fatalf("owner page: %d %s", res.Code, body)
	}
	if !strings.Contains(body, `src="/app/`+build.Entry+`"`) || !strings.Contains(res.Header().Get("Content-Security-Policy"), "script-src 'self'") || strings.Contains(body, "<script>") {
		t.Fatalf("page: %s", body)
	}
	if !strings.Contains(body, `href="`+page+`?format=md"`) || !strings.Contains(body, `name="robots" content="noindex, nofollow"`) {
		t.Fatalf("no-script link or noindex: %s", body)
	}
	// The space's top folder opens on its first page; the bare address gets the slash.
	if res := withSession("/spaces/"+w.ownedName+"/", ownerSession.Value); res.Code != 200 || !strings.Contains(res.Body.String(), "metatrash-app") {
		t.Fatalf("top folder: %d %s", res.Code, res.Body)
	}
	if res := withSession("/spaces/"+w.ownedName, ownerSession.Value); res.Code != 307 || res.Header().Get("Location") != "/spaces/"+w.ownedName+"/" {
		t.Fatalf("bare address: %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := withSession("/spaces/"+w.ownedName+"/notes/missing.md", ownerSession.Value); res.Code != 404 {
		t.Fatalf("missing file: %d", res.Code)
	}

	// A stranger finds nothing; signed out is asked to sign in, and comes back.
	if res := withSession("/spaces/"+w.ownedName+"/", strangerSession.Value); res.Code != 404 {
		t.Fatalf("stranger: %d", res.Code)
	}
	res = get(page)
	if res.Code != 303 || res.Header().Get("Location") != "/login" || !strings.Contains(res.Header().Get("Set-Cookie"), w.h.returnCookieName()) {
		t.Fatalf("signed out: %d %v", res.Code, res.Header())
	}
	if res := get("/spaces/"+w.ownedName+"/", "Sec-Fetch-Site", "cross-site"); res.Code != 200 || !strings.Contains(res.Body.String(), "Opening") {
		t.Fatalf("cross-site link: %d", res.Code)
	}

	// The public space is open to anyone, and its top folder may be indexed.
	if res := get("/spaces/public/AGENTS.md"); res.Code != 200 || !strings.Contains(res.Body.String(), "&#34;root&#34;:&#34;/spaces/public&#34;") || strings.Contains(res.Body.String(), `name="robots"`) {
		t.Fatalf("public: %d %s", res.Code, res.Body)
	}
	for _, path := range []string{"/spaces", "/spaces/"} {
		if res := get(path); res.Code != 303 || res.Header().Get("Location") != "/spaces/public/" {
			t.Fatalf("%s: %d %s", path, res.Code, res.Header().Get("Location"))
		}
	}
	if _, err := w.db.db.ExecContext(context.Background(), "UPDATE metatrash_spaces SET visibility = 'web' WHERE space_id = ?", w.ownedID); err != nil {
		t.Fatal(err)
	}
	if res := get(page); res.Code != 200 || !strings.Contains(res.Body.String(), "metatrash-app") {
		t.Fatalf("web space signed out: %d", res.Code)
	}
	// Programs keep the file itself.
	if res := get(page, "Accept", "text/markdown"); res.Code != 200 || res.Body.String() != "# A\n" {
		t.Fatalf("markdown: %d %q", res.Code, res.Body)
	}
	if res := get(page + "?format=md"); res.Code != 200 || res.Body.String() != "# A\n" {
		t.Fatalf("?format=md: %d %q", res.Code, res.Body)
	}

	// The build's files, cached for good; nothing else from the folder.
	res = get("/app/" + build.Entry)
	if res.Code != 200 || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(res.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("entry: %d %v", res.Code, res.Header())
	}
	for _, path := range []string{"/app/assets/../README.md", "/app/assets/nothing.js"} {
		if res := get(path); res.Code != 404 {
			t.Fatalf("%s: %d", path, res.Code)
		}
	}
	if res := get("/assets/app-start.js"); res.Code != 200 {
		t.Fatalf("start script: %d", res.Code)
	}
	for _, old := range []string{"/assets/style.css", "/assets/document.js", "/assets/activity.js", "/assets/copy.js"} {
		if res := get(old); res.Code != 404 {
			t.Fatalf("old page asset %s: %d", old, res.Code)
		}
	}
	if res := get("/spaces/"+w.ownedName+"/?x", "Cookie", w.h.sessionCookieName()+"="+ownerSession.Value); res.Code != 404 {
		t.Fatalf("query: %d", res.Code)
	}
}

// The home page (0.75.0): the app on the docs space's introduction.md, while
// the docs space is readable on the web; the public space otherwise.
func TestAppHomeAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	if appBuildOnce() == nil {
		t.Skip("no website build in web/app")
	}
	home := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://metatrash.com/", nil)
		r.RemoteAddr = "192.0.2.14:1"
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, r)
		return res
	}
	w.s.accounts.config.DocsSpace = w.ownedName
	// Private: the public space instead.
	if res := home(); res.Code != 302 || res.Header().Get("Location") != "/spaces/public/" {
		t.Fatalf("private docs space: %d", res.Code)
	}
	if _, err := w.db.db.ExecContext(context.Background(), "UPDATE metatrash_spaces SET visibility = 'web' WHERE space_id = ?", w.ownedID); err != nil {
		t.Fatal(err)
	}
	// No introduction.md yet: the space's first note.
	if res := home(); res.Code != 200 || !strings.Contains(res.Body.String(), "&#34;file&#34;:null") {
		t.Fatalf("no introduction: %d %s", res.Code, res.Body)
	}
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "introduction.md", "text": "# Welcome\n", "ifInState": state}, "")
	res := home()
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, "&#34;file&#34;:&#34;/spaces/"+w.ownedName+"/introduction.md&#34;") || !strings.Contains(body, "&#34;address&#34;:&#34;/&#34;") {
		t.Fatalf("home: %d %s", res.Code, body)
	}
	if strings.Contains(body, "noindex") || res.Header().Get("X-Robots-Tag") != "" || !strings.Contains(body, `href="/docs/introduction.md?format=md"`) || !strings.Contains(body, "&#34;addressRoot&#34;:&#34;/docs&#34;") {
		t.Fatalf("home should be indexed, with a link for no JavaScript: %v %s", res.Header(), body)
	}
}
