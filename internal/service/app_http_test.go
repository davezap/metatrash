package service

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// Slate Viewer's UI on the site (0.73.0): the page at /app/ with the space
// and file in its meta config, for whoever may read the space.
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

	// The owner gets the page, set to open the file.
	res := withSession("/app/"+w.ownedName+"/notes/a.md", ownerSession.Value)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, `content="{&#34;file&#34;:&#34;/spaces/`+w.ownedName+`/notes/a.md&#34;,&#34;root&#34;:&#34;/spaces/`+w.ownedName+`&#34;}"`) {
		t.Fatalf("owner page: %d %s", res.Code, body)
	}
	if !strings.Contains(body, `src="/app/`+build.Entry+`"`) || !strings.Contains(res.Header().Get("Content-Security-Policy"), "script-src 'self'") || strings.Contains(body, "<script>") {
		t.Fatalf("page: %s", body)
	}
	// The space's top folder opens with no file; its bare address gets the slash.
	if res := withSession("/app/"+w.ownedName+"/", ownerSession.Value); res.Code != 200 || !strings.Contains(res.Body.String(), "&#34;file&#34;:null") {
		t.Fatalf("top folder: %d %s", res.Code, res.Body)
	}
	if res := withSession("/app/"+w.ownedName, ownerSession.Value); res.Code != 303 || res.Header().Get("Location") != "/app/"+w.ownedName+"/" {
		t.Fatalf("bare address: %d %s", res.Code, res.Header().Get("Location"))
	}

	// A stranger finds nothing; signed out is asked to sign in, and comes back.
	if res := withSession("/app/"+w.ownedName+"/", strangerSession.Value); res.Code != 404 {
		t.Fatalf("stranger: %d", res.Code)
	}
	res = get("/app/" + w.ownedName + "/notes/a.md")
	if res.Code != 303 || res.Header().Get("Location") != "/login" || !strings.Contains(res.Header().Get("Set-Cookie"), w.h.returnCookieName()) {
		t.Fatalf("signed out: %d %v", res.Code, res.Header())
	}
	if res := get("/app/"+w.ownedName+"/", "Sec-Fetch-Site", "cross-site"); res.Code != 200 || !strings.Contains(res.Body.String(), "Opening") {
		t.Fatalf("cross-site link: %d", res.Code)
	}

	// The public space is open to anyone; readable on the web too.
	if res := get("/app/public/README.md"); res.Code != 200 || !strings.Contains(res.Body.String(), "&#34;root&#34;:&#34;/spaces/public&#34;") {
		t.Fatalf("public: %d %s", res.Code, res.Body)
	}
	if res := get("/app/public"); res.Code != 303 || res.Header().Get("Location") != "/app/public/" {
		t.Fatalf("bare public: %d", res.Code)
	}
	if _, err := w.db.db.ExecContext(context.Background(), "UPDATE metatrash_spaces SET visibility = 'web' WHERE space_id = ?", w.ownedID); err != nil {
		t.Fatal(err)
	}
	if res := get("/app/" + w.ownedName + "/"); res.Code != 200 {
		t.Fatalf("web space signed out: %d", res.Code)
	}

	// The build's files, cached for good; nothing else from the folder.
	res = get("/app/" + build.Entry)
	if res.Code != 200 || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(res.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("entry: %d %v", res.Code, res.Header())
	}
	for _, path := range []string{"/app/.vite/manifest.json", "/app/README.md", "/app/assets/../README.md", "/app/assets/nothing.js"} {
		if res := get(path); res.Code == 200 && !strings.Contains(res.Body.String(), "metatrash-app") {
			t.Fatalf("%s: %d", path, res.Code)
		}
	}
	if res := get("/assets/app-start.js"); res.Code != 200 {
		t.Fatalf("start script: %d", res.Code)
	}
	if res := get("/app/"+w.ownedName+"/?x", "Cookie", w.h.sessionCookieName()+"="+ownerSession.Value); res.Code != 404 {
		t.Fatalf("query: %d", res.Code)
	}
}
