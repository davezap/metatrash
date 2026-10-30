package service

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A space made readable on the web: anyone can browse it read-only in the
// website explorer; agent access and other spaces do not change.
func TestWebReadableSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	stranger := w.user("stranger")
	ownerSession, strangerSession := w.session(w.owner), w.session(stranger)
	page := "/spaces/" + w.ownedName + "/README.md"
	writePrivateReadme(w)

	// Private: signed out → sign-in page; a signed-in stranger → not found.
	if res := oauthCall(w.h, "GET", page, nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("signed out, private: %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := oauthCall(w.h, "GET", page, nil, strangerSession); res.Code != 404 {
		t.Fatalf("stranger, private: %d", res.Code)
	}
	// A link from another site or a PDF arrives without the Strict session
	// cookie: the page reloads itself from this site instead of going to
	// sign in. Signed out after that reload, it is the sign-in page.
	crossSite := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "metatrash.com"
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, req)
		return res
	}
	if res := crossSite(page); res.Code != 200 || !strings.Contains(res.Body.String(), `<meta http-equiv="refresh" content="0">`) {
		t.Fatalf("cross-site, private: %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := crossSite("/spaces/" + w.owner.Username + "/no-such-space/"); res.Code != 200 {
		t.Fatalf("cross-site, missing: %d", res.Code)
	}
	// Signed out, a space that does not exist looks the same as a private one.
	if res := oauthCall(w.h, "GET", "/spaces/"+w.owner.Username+"/no-such-space/", nil); res.Code != 303 {
		t.Fatalf("signed out, missing: %d", res.Code)
	}
	account := oauthCall(w.h, "GET", "/account", nil, ownerSession)
	if !strings.Contains(account.Body.String(), "Make readable on the web") || !strings.Contains(account.Body.String(), "Warning:") {
		t.Fatal("account page lacks the web toggle and its warning")
	}

	// Only the owner may change it, with the form's token.
	ownerCSRF := w.s.accounts.mac("space-visibility:" + ownerSession.Value)
	strangerCSRF := w.s.accounts.mac("space-visibility:" + strangerSession.Value)
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {strangerCSRF}, "space": {w.ownedID}, "visibility": {"web"}}, strangerSession); res.Code != 404 {
		t.Fatalf("stranger toggles: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {strangerCSRF}, "space": {w.ownedID}, "visibility": {"web"}}, ownerSession); res.Code != 403 {
		t.Fatalf("wrong token: %d", res.Code)
	}
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.ownedID}, "visibility": {"public"}}, ownerSession); res.Code != 400 {
		t.Fatalf("bad value: %d", res.Code)
	}
	// A member of a space cannot make it public either: only its owner.
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.joinID}, "visibility": {"web"}}, ownerSession); res.Code != 404 {
		t.Fatalf("member toggles: %d", res.Code)
	}
	res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.ownedID}, "visibility": {"web"}}, ownerSession)
	if res.Code != 303 || res.Header().Get("Location") != "/account" {
		t.Fatalf("make web: %d %s", res.Code, res.Body)
	}
	notice := responseCookie(res, w.h.noticeCookieName())
	account = oauthCall(w.h, "GET", "/account", nil, ownerSession, notice)
	if body := account.Body.String(); !strings.Contains(body, accountNotices["space-web"]) || !strings.Contains(body, `name="visibility" value="private"`) {
		t.Fatal("account page after making the space web-readable")
	}

	// Web-readable: anyone may read, read-only, still not indexed.
	for _, viewer := range []string{"anonymous", "stranger", "owner"} {
		var res = oauthCall(w.h, "GET", page, nil)
		if viewer == "stranger" {
			res = oauthCall(w.h, "GET", page, nil, strangerSession)
		} else if viewer == "owner" {
			res = oauthCall(w.h, "GET", page, nil, ownerSession)
		}
		body := res.Body.String()
		if res.Code != 200 || !strings.Contains(body, "Readable on the web") || !strings.Contains(body, "# Private space") || res.Header().Get("X-Robots-Tag") != "" || strings.Contains(body, `name="robots"`) {
			t.Fatalf("%s, web: %d", viewer, res.Code)
		}
		if viewer != "owner" && !strings.Contains(body, "shared read-only by its owner") {
			t.Fatalf("%s sees the member wording", viewer)
		}
	}
	// ?raw serves a picture itself, sandboxed, for the document's images;
	// anything else, or another query, is not found.
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "pics/dot.svg", "text": `<svg xmlns="http://www.w3.org/2000/svg"/>`, "ifInState": state}, "")
	svg := oauthCall(w.h, "GET", "/spaces/"+w.ownedName+"/pics/dot.svg?raw", nil)
	if svg.Code != 200 || svg.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(svg.Header().Get("Content-Security-Policy"), "sandbox") || svg.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(svg.Body.String(), "<svg") {
		t.Fatalf("raw svg: %d %v", svg.Code, svg.Header())
	}
	for _, path := range []string{"README.md?raw", "pics/dot.svg?raw=1", "pics/dot.svg?x"} {
		if res := oauthCall(w.h, "GET", "/spaces/"+w.ownedName+"/"+path, nil); res.Code != 404 {
			t.Fatalf("%s: %d", path, res.Code)
		}
	}
	if body := oauthCall(w.h, "GET", page, nil).Body.String(); !strings.Contains(body, `data-raw-images="true"`) {
		t.Fatal("page lacks data-raw-images")
	}

	// The other space (the member's) stays private.
	if res := oauthCall(w.h, "GET", "/spaces/"+w.joinName+"/README.md", nil, strangerSession); res.Code != 404 {
		t.Fatalf("other space: %d", res.Code)
	}
	// Agents: a stranger's app still cannot reach the space.
	token = w.connect(stranger, map[string]string{}, "")
	w.tool(token, "read", map[string]any{"space": w.ownedName, "path": "README.md"}, "not_found")
	// Writes through the website do not exist.
	if res := oauthCall(w.h, "POST", page, url.Values{"text": {"x"}}); res.Code != 405 {
		t.Fatalf("POST to the explorer: %d", res.Code)
	}

	// Private again.
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.ownedID}, "visibility": {"private"}}, ownerSession); res.Code != 303 {
		t.Fatalf("make private: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", page, nil); res.Code != 303 {
		t.Fatalf("signed out after private: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", page, nil, ownerSession); res.Code != 200 || strings.Contains(res.Body.String(), "Readable on the web") || res.Header().Get("X-Robots-Tag") != "noindex, nofollow" || !strings.Contains(res.Body.String(), `name="robots" content="noindex, nofollow"`) {
		t.Fatalf("owner after private: %d", res.Code)
	}
}

// writePrivateReadme gives the owner's space a README.md: since 0.45.0 a new
// space starts with only its AGENTS.md and .metatrash.json.
func writePrivateReadme(w *oauthWorld) {
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "README.md", "text": "# Private space\n", "ifInState": state}, "")
}

// Your account offers each space as a zip (0.65.0): the space's top folder
// with ?export, for its owner and members only, even when it is readable on
// the web.
func TestDownloadSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	writePrivateReadme(w)
	ownerSession, strangerSession := w.session(w.owner), w.session(w.user("stranger"))
	download := "/spaces/" + w.ownedName + "/?export"

	res := oauthCall(w.h, "GET", download, nil, ownerSession)
	if res.Code != 200 || res.Header().Get("Content-Type") != "application/zip" || !strings.HasPrefix(res.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("owner: %d %v", res.Code, res.Header())
	}
	body := res.Body.Bytes()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range archive.File {
		names[f.Name] = true
	}
	if !names["README.md"] || !names[exportManifestName] {
		t.Fatalf("zip holds %v", names)
	}
	// A member downloads the space they joined.
	if res := oauthCall(w.h, "GET", "/spaces/"+w.joinName+"/?export", nil, ownerSession); res.Code != 200 {
		t.Fatalf("member: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", download, nil, strangerSession); res.Code != 404 {
		t.Fatalf("stranger: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", download, nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("signed out: %d %s", res.Code, res.Header().Get("Location"))
	}
	// Only the top folder takes it.
	for _, path := range []string{"README.md?export", "?export=1"} {
		if res := oauthCall(w.h, "GET", "/spaces/"+w.ownedName+"/"+path, nil, ownerSession); res.Code != 404 {
			t.Fatalf("%s: %d", path, res.Code)
		}
	}
	if body := oauthCall(w.h, "GET", "/account", nil, ownerSession).Body.String(); !strings.Contains(body, `href="`+download+`"`) || !strings.Contains(body, `href="/spaces/`+w.joinName+`/?export"`) {
		t.Fatal("account page lacks the Download links")
	}

	// Readable on the web: visitors still cannot download it.
	csrf := w.s.accounts.mac("space-visibility:" + ownerSession.Value)
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {csrf}, "space": {w.ownedID}, "visibility": {"web"}}, ownerSession); res.Code != 303 {
		t.Fatalf("make web: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", download, nil, strangerSession); res.Code != 404 {
		t.Fatalf("stranger, web: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", download, nil); res.Code != 404 {
		t.Fatalf("signed out, web: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", download, nil, ownerSession); res.Code != 200 {
		t.Fatalf("owner, web: %d", res.Code)
	}
}

func TestFileTreeOpensSelectedFolders(t *testing.T) {
	files := map[string]record{"a/b/c.md": {}, "a/d.md": {}, "e/f.md": {}, "top.md": {}}
	tree := fileTree(files, "a/b/c.md")
	open := map[string]bool{}
	var walk func([]*browserNode, string)
	walk = func(nodes []*browserNode, prefix string) {
		for _, n := range nodes {
			if n.Open {
				open[prefix+n.Name] = true
			}
			walk(n.Children, prefix+n.Name+"/")
		}
	}
	walk(tree, "")
	if len(open) != 2 || !open["a"] || !open["a/b"] {
		t.Fatalf("open folders %v", open)
	}
}

// The docs space: one web-readable space shown under /docs/ as part of the
// site. Not readable on the web means not found for everyone, with no sign-in
// page; the footer links to the legal pages only once it is configured.
func TestDocsSpaceAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	ownerSession := w.session(w.owner)
	if res := oauthCall(w.h, "GET", "/docs/README.md", nil); res.Code == 200 {
		t.Fatal("/docs/ served with no docs space configured")
	}
	if body := oauthCall(w.h, "GET", "/", nil).Body.String(); strings.Contains(body, "/docs/legal/privacy.md") || !strings.Contains(body, `href="/about"`) {
		t.Fatal("legal links, or no built-in About link, without a docs space")
	}
	if res := oauthCall(w.h, "GET", "/about", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "A little room where agents can meet.") {
		t.Fatalf("built-in about page: %d", res.Code)
	}
	w.s.accounts.config.DocsSpace = w.ownedName
	for _, page := range []string{"/", "/login", "/spaces/public/"} {
		body := oauthCall(w.h, "GET", page, nil).Body.String()
		if !strings.Contains(body, `href="/docs/legal/privacy.md"`) || !strings.Contains(body, `href="/docs/legal/terms.md"`) || !strings.Contains(body, `href="/docs/about.md"`) || strings.Contains(body, `href="/about"`) {
			t.Fatalf("%s lacks the docs footer links", page)
		}
	}
	if res := oauthCall(w.h, "GET", "/about", nil); res.Code != 302 || res.Header().Get("Location") != "/docs/about.md" {
		t.Fatalf("/about with docs: %d %s", res.Code, res.Header().Get("Location"))
	}

	// Private: not found for everyone, the owner included; never the sign-in page.
	for _, cookies := range [][]*http.Cookie{nil, {ownerSession}} {
		if res := oauthCall(w.h, "GET", "/docs/README.md", nil, cookies...); res.Code != 404 {
			t.Fatalf("private docs: %d %s", res.Code, res.Header().Get("Location"))
		}
	}

	writePrivateReadme(w)
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "legal/terms.md", "text": "# Terms\n", "ifInState": state}, "")
	ownerCSRF := w.s.accounts.mac("space-visibility:" + ownerSession.Value)
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.ownedID}, "visibility": {"web"}}, ownerSession); res.Code != 303 {
		t.Fatalf("make web: %d", res.Code)
	}
	// Search engines: all of /docs/; under /spaces/ only the top folder.
	for page, index := range map[string]bool{"/docs/legal/terms.md": true, "/spaces/" + w.ownedName + "/README.md": true, "/spaces/" + w.ownedName + "/legal/terms.md": false} {
		res := oauthCall(w.h, "GET", page, nil)
		noindex := res.Header().Get("X-Robots-Tag") == "noindex, nofollow" && strings.Contains(res.Body.String(), `name="robots" content="noindex, nofollow"`)
		if res.Code != 200 || noindex == index || (index && (res.Header().Get("X-Robots-Tag") != "" || strings.Contains(res.Body.String(), `name="robots"`))) {
			t.Fatalf("%s: %d index=%v header=%q", page, res.Code, index, res.Header().Get("X-Robots-Tag"))
		}
	}
	res := oauthCall(w.h, "GET", "/docs/README.md", nil)
	body := res.Body.String()
	if res.Code != 200 || !strings.Contains(body, "# Private space") || !strings.Contains(body, "EXPLORER / DOCS") || res.Header().Get("X-Robots-Tag") != "" || strings.Contains(body, `name="robots"`) {
		t.Fatalf("docs page: %d", res.Code)
	}
	if strings.Contains(body, "space-bar") || strings.Contains(body, w.ownedName) || strings.Contains(body, "/spaces/"+w.owner.Username) {
		t.Fatal("docs page shows the space's own name or links")
	}
	if !strings.Contains(body, `href="/docs/README.md"`) {
		t.Fatal("tree links are not under /docs/")
	}
	if res := oauthCall(w.h, "GET", "/docs", nil); res.Code != 307 || res.Header().Get("Location") != "/docs/" {
		t.Fatalf("/docs: %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := oauthCall(w.h, "GET", "/docs/", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "# Private space") {
		t.Fatalf("/docs/: %d", res.Code)
	}
	for _, page := range []string{"/docs/no-such-file.md", "/docs/README.md?x=1", "/docs/../README.md"} {
		if res := oauthCall(w.h, "GET", page, nil); res.Code != 404 && res.Code != 400 {
			t.Fatalf("%s: %d", page, res.Code)
		}
	}
	if res := oauthCall(w.h, "POST", "/docs/README.md", url.Values{"text": {"x"}}); res.Code != 405 {
		t.Fatalf("POST to docs: %d", res.Code)
	}
	// The space's own address still works as before.
	if res := oauthCall(w.h, "GET", "/spaces/"+w.ownedName+"/README.md", nil); res.Code != 200 || !strings.Contains(res.Body.String(), "Readable on the web") {
		t.Fatalf("space page: %d", res.Code)
	}

	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.ownedID}, "visibility": {"private"}}, ownerSession); res.Code != 303 {
		t.Fatalf("make private: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/docs/README.md", nil); res.Code != 404 {
		t.Fatalf("docs after private: %d", res.Code)
	}
}
