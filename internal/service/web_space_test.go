package service

import (
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

	// Private: signed out → sign-in page; a signed-in stranger → not found.
	if res := oauthCall(w.h, "GET", page, nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("signed out, private: %d %s", res.Code, res.Header().Get("Location"))
	}
	if res := oauthCall(w.h, "GET", page, nil, strangerSession); res.Code != 404 {
		t.Fatalf("stranger, private: %d", res.Code)
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
	if res.Code != 303 || res.Header().Get("Location") != "/account#my-spaces" {
		t.Fatalf("make web: %d %s", res.Code, res.Body)
	}
	notice := responseCookie(res, w.h.noticeCookieName())
	account = oauthCall(w.h, "GET", "/account", nil, ownerSession, notice)
	if body := account.Body.String(); !strings.Contains(body, accountNotices["space-web"]) || !strings.Contains(body, "Make private again") {
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
		if res.Code != 200 || !strings.Contains(body, "Readable on the web") || !strings.Contains(body, "# Private space") || res.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
			t.Fatalf("%s, web: %d", viewer, res.Code)
		}
		if viewer != "owner" && !strings.Contains(body, "shared read-only by its owner") {
			t.Fatalf("%s sees the member wording", viewer)
		}
	}
	// The other space (the member's) stays private.
	if res := oauthCall(w.h, "GET", "/spaces/"+w.joinName+"/README.md", nil, strangerSession); res.Code != 404 {
		t.Fatalf("other space: %d", res.Code)
	}
	// Agents: a stranger's app still cannot reach the space.
	token := w.connect(stranger, map[string]string{}, "")
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
	if res := oauthCall(w.h, "GET", page, nil, ownerSession); res.Code != 200 || strings.Contains(res.Body.String(), "Readable on the web") {
		t.Fatalf("owner after private: %d", res.Code)
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
