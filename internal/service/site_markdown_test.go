package service

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPrefersMarkdown(t *testing.T) {
	for accept, want := range map[string]bool{
		"": false,
		"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8": false,
		"text/markdown": true,
		"text/markdown,text/html;q=0.9,*/*;q=0.7": true,
		"text/html, text/markdown":                true,
		"text/html,text/markdown;q=0.9":           false,
		"text/markdown;q=0":                       false,
		"text/x-markdown":                         true,
		"*/*":                                     false,
		"text/markdown;q=bad":                     false,
	} {
		if got := prefersMarkdown(accept); got != want {
			t.Errorf("%q: got %v", accept, got)
		}
	}
}

func TestLLMSText(t *testing.T) {
	files := map[string]record{"README.md": {}, "AGENTS.md": {}, ".metatrash.json": {}, "guides/AGENTS.md": {}, "guides/b.md": {}, "guides/a b.md": {}, "about.md": {}, "data.json": {}}
	got := string(llmsText("Docs", "https://metatrash.com/docs/", files))
	want := `# Docs

> The pages of Docs as Markdown. Each link gives a page's Markdown; the same address without ?format=md is the page itself.

## Pages

- [README.md](https://metatrash.com/docs/README.md?format=md)
- [about.md](https://metatrash.com/docs/about.md?format=md)

## guides

- [a b.md](https://metatrash.com/docs/guides/a%20b.md?format=md)
- [b.md](https://metatrash.com/docs/guides/b.md?format=md)
`
	if got != want {
		t.Fatalf("got:\n%s", got)
	}
	if got := string(llmsText("Empty", "https://x/", map[string]record{"AGENTS.md": {}})); !strings.HasSuffix(got, "There are no Markdown pages yet.\n") {
		t.Fatalf("empty: %s", got)
	}
}

// Markdown for agents: ?format=md or Accept: text/markdown sends a page's
// file as it is, under the page's own access rules, and llms.txt lists the
// pages.
func TestMarkdownPagesAgainstDatabase(t *testing.T) {
	w := newOAuthWorld(t)
	stranger := w.user("stranger")
	ownerSession, strangerSession := w.session(w.owner), w.session(stranger)
	writePrivateReadme(w)
	token := w.connect(w.owner, map[string]string{w.ownedID: "read_write"}, "")
	state := w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	state = w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "legal/terms.md", "text": "# Terms\n", "ifInState": state}, "")["newState"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "data.json", "text": "{}\n", "ifInState": state}, "")
	space := "/spaces/" + w.ownedName + "/"
	accept := func(target, accept string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "metatrash.com"
		req.Header.Set("Accept", accept)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		res := httptest.NewRecorder()
		w.h.ServeHTTP(res, req)
		return res
	}
	isMarkdown := func(res *httptest.ResponseRecorder, text string) bool {
		return res.Code == 200 && res.Header().Get("Content-Type") == "text/markdown; charset=utf-8" && res.Body.String() == text && strings.Contains(res.Header().Get("Content-Security-Policy"), "sandbox") && res.Header().Get("X-Robots-Tag") == "noindex, nofollow"
	}

	// Private: the same rules as the page.
	if res := oauthCall(w.h, "GET", space+"README.md?format=md", nil); res.Code != 303 || res.Header().Get("Location") != "/login" {
		t.Fatalf("signed out, private: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", space+"README.md?format=md", nil, strangerSession); res.Code != 404 {
		t.Fatalf("stranger, private: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", space+"README.md?format=md", nil, ownerSession); !isMarkdown(res, "# Private space\n") {
		t.Fatalf("owner, ?format=md: %d %v %q", res.Code, res.Header(), res.Body)
	}
	if res := accept(space+"legal/terms.md", "text/markdown, text/html;q=0.9", ownerSession); !isMarkdown(res, "# Terms\n") {
		t.Fatalf("owner, Accept: %d %v", res.Code, res.Header())
	}
	if res := accept(space+"legal/terms.md", "text/html,*/*;q=0.8", ownerSession); res.Code != 200 || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("browser Accept: %d %s", res.Code, res.Header().Get("Content-Type"))
	}
	if res := oauthCall(w.h, "GET", space+"llms.txt", nil, strangerSession); res.Code != 404 {
		t.Fatalf("stranger, private llms.txt: %d", res.Code)
	}

	ownerCSRF := w.s.accounts.mac("space-visibility:" + ownerSession.Value)
	if res := oauthCall(w.h, "POST", "/account/spaces/visibility", url.Values{"csrf": {ownerCSRF}, "space": {w.ownedID}, "visibility": {"web"}}, ownerSession); res.Code != 303 {
		t.Fatalf("make web: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", space+"README.md?format=md", nil); !isMarkdown(res, "# Private space\n") {
		t.Fatalf("anonymous, web: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", space+"?format=md", nil); !isMarkdown(res, "# Private space\n") {
		t.Fatalf("space top, ?format=md: %d", res.Code)
	}
	if res := oauthCall(w.h, "GET", "/spaces/"+w.ownedName+"?format=md", nil); res.Code != 307 || res.Header().Get("Location") != space+"?format=md" {
		t.Fatalf("bare space address: %d %s", res.Code, res.Header().Get("Location"))
	}
	// Other text is sent as plain text; other queries are not found.
	if res := oauthCall(w.h, "GET", space+"data.json?format=md", nil); res.Code != 200 || res.Header().Get("Content-Type") != "text/plain; charset=utf-8" || res.Body.String() != "{}\n" {
		t.Fatalf("data.json: %d %s", res.Code, res.Header().Get("Content-Type"))
	}
	for _, page := range []string{"README.md?format=html", "README.md?format=md&x=1", "nothing.md?format=md"} {
		if res := oauthCall(w.h, "GET", space+page, nil); res.Code != 404 {
			t.Fatalf("%s: %d", page, res.Code)
		}
	}
	// The page tells clients where its Markdown is.
	res := oauthCall(w.h, "GET", space+"legal/terms.md", nil)
	if res.Header().Get("Link") != "<"+space+`legal/terms.md?format=md>; rel="alternate"; type="text/markdown"` || res.Header().Get("Vary") != "Accept" || !strings.Contains(res.Body.String(), `<link rel="alternate" type="text/markdown" href="?format=md">`) {
		t.Fatalf("page alternate: %v", res.Header())
	}
	res = oauthCall(w.h, "GET", space+"llms.txt", nil)
	body := res.Body.String()
	if res.Code != 200 || res.Header().Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(body, "- [README.md](https://metatrash.com"+space+"README.md?format=md)") || !strings.Contains(body, "## legal\n\n- [terms.md](https://metatrash.com"+space+"legal/terms.md?format=md)") || strings.Contains(body, "AGENTS.md") || strings.Contains(body, "data.json") {
		t.Fatalf("llms.txt: %d\n%s", res.Code, body)
	}

	// Docs: /docs/llms.txt, and /llms.txt at the site's root.
	w.s.accounts.config.DocsSpace = w.ownedName
	if res := oauthCall(w.h, "GET", "/docs/legal/terms.md?format=md", nil); !isMarkdown(res, "# Terms\n") {
		t.Fatalf("docs ?format=md: %d", res.Code)
	}
	for _, page := range []string{"/docs/llms.txt", "/llms.txt"} {
		body := oauthCall(w.h, "GET", page, nil).Body.String()
		if !strings.HasPrefix(body, "# Metatrash docs\n") || !strings.Contains(body, "(https://metatrash.com/docs/legal/terms.md?format=md)") {
			t.Fatalf("%s:\n%s", page, body)
		}
	}
	// A space's own llms.txt is sent as it is.
	state = w.tool(token, "list", map[string]any{"space": w.ownedName}, "")["state"]
	w.tool(token, "write", map[string]any{"space": w.ownedName, "path": "llms.txt", "text": "# Mine\n", "ifInState": state}, "")
	if res := oauthCall(w.h, "GET", "/llms.txt", nil); res.Code != 200 || res.Body.String() != "# Mine\n" || res.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("own llms.txt: %d %q", res.Code, res.Body)
	}
	w.s.accounts.config.DocsSpace = ""
	if res := oauthCall(w.h, "GET", "/llms.txt", nil); res.Code != 404 {
		t.Fatalf("/llms.txt without docs: %d", res.Code)
	}
}
