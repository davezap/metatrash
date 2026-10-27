package service

import (
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Markdown for agents (0.59.1): every page of a space, and of /docs/, is a
// file already, so the site sends the file as it is when a client asks for
// Markdown, with ?format=md or an Accept header that prefers text/markdown
// to HTML. The access rules are the page's own. An llms.txt at the top of
// each space (and of /docs/) lists its Markdown pages, unless the space
// has an llms.txt of its own, which is then sent as it is.

// markdownQuery is the query string that asks a page for its Markdown.
const markdownQuery = "format=md"

// maxLLMSEntries caps the pages a generated llms.txt lists.
const maxLLMSEntries = 2000

// prefersMarkdown reports whether an Accept header ranks text/markdown at
// least as high as text/html. Browsers never list text/markdown, so they
// always get the page.
func prefersMarkdown(accept string) bool {
	markdown, html := 0.0, 0.0
	for _, part := range strings.Split(accept, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if v, ok := params["q"]; ok {
			if q, err = strconv.ParseFloat(v, 64); err != nil {
				continue
			}
		}
		switch media {
		case "text/markdown", "text/x-markdown":
			markdown = max(markdown, q)
		case "text/html", "application/xhtml+xml":
			html = max(html, q)
		}
	}
	return markdown > 0 && markdown >= html
}

func isMarkdownPath(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// serveSource sends a file's text as it is: text/markdown for Markdown
// files, text/plain for other text. It reports false, sending nothing, for
// a file that is not UTF-8 text.
func serveSource(w http.ResponseWriter, r *http.Request, path string, content []byte) bool {
	if !utf8.Valid(content) {
		return false
	}
	w.Header().Add("Vary", "Accept")
	kind := "text/plain; charset=utf-8"
	if isMarkdownPath(path) {
		kind = "text/markdown; charset=utf-8"
	}
	sendText(w, r, kind, content)
	return true
}

// sendText sends text that never runs as a page on this site. Search
// engines index the HTML pages, not these copies of them.
func sendText(w http.ResponseWriter, r *http.Request, kind string, content []byte) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
}

// markdownAlternate tells clients of an HTML page that its Markdown is at
// the same address with ?format=md.
func markdownAlternate(w http.ResponseWriter, pageURL string) {
	w.Header().Add("Vary", "Accept")
	w.Header().Set("Link", "<"+pageURL+"?"+markdownQuery+`>; rel="alternate"; type="text/markdown"`)
}

// llmsText lists the Markdown pages in folder ("" for the whole space), its
// own first and then one section per subfolder, linking each page's
// Markdown. root is the space's absolute address, ending in a slash.
func llmsText(title, root string, files map[string]record, folder string) []byte {
	prefix := ""
	if folder != "" {
		prefix = folder + "/"
	}
	folders := map[string][]string{}
	for path := range files {
		if strings.HasPrefix(path, prefix) && isMarkdownPath(path) && !isFolderFile(path) {
			folder, _ := splitFolder(path)
			folders[folder] = append(folders[folder], path)
		}
	}
	names := make([]string, 0, len(folders))
	for folder := range folders {
		names = append(names, folder)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("# " + title + "\n\n")
	b.WriteString("> The pages of " + title + " as Markdown. Each link gives a page's Markdown; the same address without ?format=md is the page itself.\n")
	listed := 0
	for _, name := range names {
		paths := folders[name]
		sort.Strings(paths)
		if name == folder {
			b.WriteString("\n## Pages\n\n")
		} else {
			b.WriteString("\n## " + name + "\n\n")
		}
		for _, path := range paths {
			if listed == maxLLMSEntries {
				b.WriteString("- …(more pages are left out)\n")
				return []byte(b.String())
			}
			_, name := splitFolder(path)
			b.WriteString("- [" + strings.ReplaceAll(name, "]", `\]`) + "](" + root + strings.TrimPrefix(fileURL(path), "/spaces/public/") + "?" + markdownQuery + ")\n")
			listed++
		}
	}
	if listed == 0 {
		b.WriteString("\nThere are no Markdown pages yet.\n")
	}
	return []byte(b.String())
}

// splitFolder splits a path into its folder ("" for the top) and name.
func splitFolder(path string) (string, string) {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i], path[i+1:]
	}
	return "", path
}

// serveFolder answers a folder's own address (path/to/folder/): for a
// client that asks for Markdown, the folder's README.md, else the list of
// its pages; for a browser, a redirect to the page the folder opens on.
// title names the space; root is its address under the site, ending in a
// slash, and origin the site's.
func serveFolder(w http.ResponseWriter, r *http.Request, repo *repository, files map[string]record, folder, title, origin, root string, markdown bool) {
	prefix := folder + "/"
	found := false
	for path := range files {
		if strings.HasPrefix(path, prefix) {
			found = true
			break
		}
	}
	if !found {
		sendError(w, missing())
		return
	}
	if !markdown {
		http.Redirect(w, r, root+(&url.URL{Path: defaultPageIn(files, folder)}).EscapedPath(), http.StatusFound)
		return
	}
	w.Header().Add("Vary", "Accept")
	if file, ok := files[prefix+"README.md"]; ok {
		content, err := repo.blob(r.Context(), file.Blob)
		if err != nil {
			sendError(w, err)
			return
		}
		if serveSource(w, r, prefix+"README.md", content) {
			return
		}
	}
	sendText(w, r, "text/markdown; charset=utf-8", llmsText(title+": "+prefix, origin+root, files, folder))
}
