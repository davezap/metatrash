package service

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed web/login.js
var loginJS []byte

//go:embed web/passkey.js
var passkeyJS []byte

// The sign-in, app connection and provider return pages' own look
// (Slate Viewer's colours), and the script that picks the saved theme for them.
//
//go:embed web/signin.css
var signinCSS []byte

//go:embed web/theme.js
var themeJS []byte

// The site's icon (Slate Viewer's app icon): /favicon.ico holds 16-256 px
// sizes, /apple-touch-icon.png is the 256 px one for phone home screens.
//
//go:embed web/favicon.ico
var faviconICO []byte

//go:embed web/apple-touch-icon.png
var appleTouchIconPNG []byte

//go:embed web/slate.js
var slateJS []byte

//go:embed web/slate.css
var slateCSS []byte

// Mermaid, which Slate loads on its own the first time a document has a
// ```mermaid block: an entry file and the chunks it imports.
//
//go:embed web/mermaid
var mermaidFiles embed.FS

// Only developer-owned, embedded assets belong here. Never serve space storage.
func (h *httpAdapter) serveAsset(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/assets/") && r.URL.Path != "/favicon.ico" && r.URL.Path != "/apple-touch-icon.png" {
		return false
	}
	var data []byte
	var kind string
	switch r.URL.Path {
	case "/favicon.ico":
		data, kind = faviconICO, "image/x-icon"
		w.Header().Set("Cache-Control", "public, max-age=86400")
	case "/apple-touch-icon.png":
		data, kind = appleTouchIconPNG, "image/png"
		w.Header().Set("Cache-Control", "public, max-age=86400")
	case "/assets/slate.js":
		data, kind = h.editorAsset("slate.js", slateJS), "text/javascript; charset=utf-8"
	case "/assets/slate.css":
		data, kind = h.editorAsset("slate.css", slateCSS), "text/css; charset=utf-8"
	case "/assets/login.js":
		data, kind = loginJS, "text/javascript; charset=utf-8"
	case "/assets/passkey.js":
		data, kind = passkeyJS, "text/javascript; charset=utf-8"
	case "/assets/signin.css":
		data, kind = signinCSS, "text/css; charset=utf-8"
	case "/assets/theme.js":
		data, kind = themeJS, "text/javascript; charset=utf-8"
	case "/assets/app-start.js":
		data, kind = appStartJS, "text/javascript; charset=utf-8"
	default:
		name, ok := strings.CutPrefix(r.URL.Path, "/assets/mermaid/")
		if !ok || !strings.HasSuffix(name, ".js") || strings.Contains(name, "..") {
			sendError(w, missing())
			return true
		}
		file, err := mermaidFiles.ReadFile("web/mermaid/" + name)
		if err != nil {
			sendError(w, missing())
			return true
		}
		data, kind = file, "text/javascript; charset=utf-8"
		// Chunk names carry a hash of their content, so they never change.
		if strings.HasPrefix(name, "chunks/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	w.Header().Set("Content-Type", kind)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
	return true
}

type recentFile struct {
	Path      string `json:"path"`
	URL       string `json:"url"`
	Timestamp string `json:"timestamp"`
	Date      string `json:"date"`
}

func (h *httpAdapter) serveRecent(w http.ResponseWriter, r *http.Request, client string) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		sendError(w, problem(405, "invalid_request", "Use GET."))
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	if err := h.service.Access("public", "", client, false); err != nil {
		sendError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	repo := h.service.repos["public"]
	state, files, err := repo.snapshot(ctx, "")
	if err != nil {
		sendError(w, err)
		return
	}
	recent, err := repo.recent(ctx, state, files)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, struct {
		State string       `json:"state"`
		Files []recentFile `json:"files"`
	}{state, h.publicRecent(recent)})
}

// Search engines may index the site's pages, everything under /docs/, and in
// spaces anyone can read under /spaces/ (the public space and spaces readable
// on the web) only the files in the space's top folder. Nested files and
// members-only spaces are noindex.
func indexable(path string) bool {
	return !strings.Contains(path, "/")
}

func fileURL(path string) string {
	parts := strings.Split(path, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/spaces/public/" + strings.Join(parts, "/")
}

// Commit order defines "touched"; stable IDs deduplicate updates and moves.
// Read fixed-size batches so a frequently updated file cannot hide other files.
func (repo *repository) recent(ctx context.Context, state string, files map[string]record) ([]recentFile, error) {
	current := map[string]string{}
	for path, file := range files {
		current[file.ID] = path
	}
	seen := map[string]bool{}
	result := []recentFile{}
	for offset := 0; len(result) < 10 && len(seen) < len(current); offset += 100 {
		b, err := git(ctx, repo.path, "", nil, "log", "--first-parent", "--format=%cI%x09%s", "--max-count=100", "--skip="+strconv.Itoa(offset), state, "--")
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		for _, line := range lines {
			fields := strings.SplitN(line, "\t", 2)
			if len(fields) != 2 {
				continue
			}
			message := strings.SplitN(fields[1], " ", 3)
			if len(message) != 3 {
				continue
			}
			path, exists := current[message[0]]
			if !exists || seen[message[0]] {
				continue
			}
			stamp, err := time.Parse(time.RFC3339, fields[0])
			if err != nil {
				return nil, fmt.Errorf("invalid recent file timestamp: %w", err)
			}
			seen[message[0]] = true
			result = append(result, recentFile{Path: path, URL: fileURL(path), Timestamp: stamp.UTC().Format(time.RFC3339), Date: stamp.UTC().Format("02 Jan 2006, 15:04 UTC")})
			if len(result) == 10 {
				break
			}
		}
		if len(lines) < 100 {
			break
		}
	}
	return result, nil
}

// serveBrowser answers the site's home page, /about and the public space's
// addresses. Browsers get the site app (Slate Viewer's UI, app_http.go);
// programs keep ?format=md, Accept: text/markdown, folders' Markdown and
// llms.txt (0.87.0: the server-rendered pages are gone).
func (h *httpAdapter) serveBrowser(w http.ResponseWriter, r *http.Request, client string) bool {
	path := r.URL.Path
	if path != "/" && path != "/about" && path != "/spaces/public" && !strings.HasPrefix(path, "/spaces/public/") {
		return false
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	if path == "/" {
		if h.serveAppHome(w, r, client) {
			return true
		}
		// Without a docs space to show, the home page is the public space.
		http.Redirect(w, r, h.basePath+"/spaces/public/", http.StatusFound)
		return true
	}
	if path == "/about" {
		if r.URL.RawQuery != "" {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
		// With a docs space, the About page is its about.md.
		target := "/spaces/public/"
		if h.docsSpace() != "" {
			target = "/docs/about.md"
		}
		http.Redirect(w, r, h.basePath+target, http.StatusFound)
		return true
	}
	if path == "/spaces/public" {
		location := h.basePath + "/spaces/public/"
		if r.URL.RawQuery != "" {
			location += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, location, http.StatusPermanentRedirect)
		return true
	}
	if err := h.service.Access("public", "", client, false); err != nil {
		sendError(w, err)
		return true
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		sendError(w, invalid("Invalid query."))
		return true
	}
	asked := r.URL.RawQuery == markdownQuery
	for key, values := range q {
		if !asked && (path != "/spaces/public/" || key != "path" || len(values) != 1) {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
	}
	filePath := strings.TrimPrefix(path, "/spaces/public/")
	if filePath == "" {
		filePath = q.Get("path")
	}
	defaulted := filePath == ""
	if defaulted {
		filePath = "README.md"
	}
	if !validPath(strings.TrimSuffix(filePath, "/")) {
		sendError(w, invalid("Invalid path."))
		return true
	}
	if q.Has("path") {
		http.Redirect(w, r, h.basePath+fileURL(filePath), http.StatusPermanentRedirect)
		return true
	}
	noIndex := !indexable(filePath)
	if noIndex {
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	repo := h.service.repos["public"]
	_, files, err := repo.snapshot(ctx, "")
	if err != nil {
		sendError(w, err)
		return true
	}
	if defaulted {
		filePath = defaultPage(files)
	}
	markdown := asked || r.URL.RawQuery == "" && prefersMarkdown(r.Header.Get("Accept"))
	if folder, ok := strings.CutSuffix(filePath, "/"); ok {
		serveFolder(w, r, repo, files, folder, "Metatrash public space", h.publicOrigin, h.basePath+"/spaces/public/", markdown)
		return true
	}
	if folder, name := splitFolder(filePath); name == "llms.txt" {
		h.serveLLMS(w, r, repo, files, filePath, folder, "Metatrash public space", h.basePath+"/spaces/public/")
		return true
	}
	file, exists := files[filePath]
	if !exists {
		if redirectToFolder(w, r, files, filePath, h.basePath+"/spaces/public/") {
			return true
		}
		sendError(w, missing())
		return true
	}
	content, err := repo.blob(ctx, file.Blob)
	if err != nil {
		sendError(w, err)
		return true
	}
	if markdown {
		if serveSource(w, r, filePath, content) {
			return true
		}
		if asked {
			sendError(w, problem(404, "not_found", "This file is not text, so it has no Markdown."))
			return true
		}
	}
	build := h.frontendEntry("src/main.ts")
	if build == nil {
		sendError(w, problem(404, "not_found", "The site app is not in this build of Metatrash."))
		return true
	}
	markdownAlternate(w, h.basePath+fileURL(filePath))
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		return true
	}
	page := appPage{Space: "public", File: filePath, Title: pathpkg.Base(filePath) + " — Metatrash public space", Address: "/spaces/public/", NoIndex: noIndex}
	if utf8.Valid(content) {
		page.Markdown = fileURL(filePath) + "?" + markdownQuery
	}
	h.writeAppPage(w, build, page)
	return true
}

func (h *httpAdapter) publicRecent(files []recentFile) []recentFile {
	for i := range files {
		files[i].URL = h.basePath + files[i].URL
	}
	return files
}

// defaultPage is the file a space's address shows when it names none: its
// README.md, else its first top-level Markdown file, else the root AGENTS.md.
func defaultPage(files map[string]record) string {
	return defaultPageIn(files, "")
}

// defaultPageIn is the page a folder's address opens on, by the same rule
// within the folder (0.59.2).
func defaultPageIn(files map[string]record, folder string) string {
	prefix := ""
	if folder != "" {
		prefix = folder + "/"
	}
	if _, ok := files[prefix+"README.md"]; ok {
		return prefix + "README.md"
	}
	first := ""
	for p := range files {
		name, ok := strings.CutPrefix(p, prefix)
		if ok && !strings.Contains(name, "/") && !isFolderFile(p) && isMarkdownPath(p) && (first == "" || p < first) {
			first = p
		}
	}
	if first != "" {
		return first
	}
	if _, ok := files[prefix+agentsFileName]; ok || folder == "" {
		return prefix + agentsFileName
	}
	// A GitHub folder has no AGENTS.md of Metatrash's: its first file.
	for p := range files {
		if strings.HasPrefix(p, prefix) && (first == "" || p < first) {
			first = p
		}
	}
	return first
}
