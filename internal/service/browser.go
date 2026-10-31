package service

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"metatrash.com/metatrash"
)

//go:embed web/page.html
var browserHTML string

//go:embed web/style.css
var browserCSS []byte

//go:embed web/activity.js
var activityJS []byte

//go:embed web/copy.js
var copyJS []byte

//go:embed web/login.js
var loginJS []byte

//go:embed web/passkey.js
var passkeyJS []byte

//go:embed web/slate.js
var slateJS []byte

//go:embed web/slate.css
var slateCSS []byte

// Mermaid, which Slate loads on its own the first time a document has a
// ```mermaid block: an entry file and the chunks it imports.
//
//go:embed web/mermaid
var mermaidFiles embed.FS

//go:embed web/document.js
var documentJS []byte

//go:embed web/account.js
var accountJS []byte

// Only developer-owned, embedded assets belong here. Never serve space storage.
func (h *httpAdapter) serveAsset(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/assets/") {
		return false
	}
	var data []byte
	var kind string
	switch r.URL.Path {
	case "/assets/style.css":
		data, kind = browserCSS, "text/css; charset=utf-8"
	case "/assets/slate.js":
		data, kind = slateJS, "text/javascript; charset=utf-8"
	case "/assets/slate.css":
		data, kind = slateCSS, "text/css; charset=utf-8"
	case "/assets/document.js":
		data, kind = documentJS, "text/javascript; charset=utf-8"
	case "/assets/activity.js":
		data, kind = activityJS, "text/javascript; charset=utf-8"
	case "/assets/copy.js":
		data, kind = copyJS, "text/javascript; charset=utf-8"
	case "/assets/login.js":
		data, kind = loginJS, "text/javascript; charset=utf-8"
	case "/assets/passkey.js":
		data, kind = passkeyJS, "text/javascript; charset=utf-8"
	case "/assets/account.js":
		data, kind = accountJS, "text/javascript; charset=utf-8"
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

var browserTemplate = template.Must(template.New("page").Funcs(template.FuncMap{"version": func() string { return metatrash.Version }}).Parse(browserHTML))

type browserNode struct {
	Name, URL string
	Service   string // set on folders with a service attached, e.g. "GitHub owner/repo"
	Count     int
	Selected  bool
	Open      bool // a folder holding the selected file
	Children  []*browserNode
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

type browserPage struct {
	Private bool
	// Web marks a private space its owner made readable on the web; Member
	// is set when the viewer is its owner or a member.
	Web, Member bool
	// Docs marks the site's documentation space under /docs/: shown without
	// the space's own name and header.
	Docs bool
	// Legal is set when a docs space is configured: the footer then links to
	// About, Privacy and Terms under /docs/.
	// NoIndex asks search engines to skip the page (see indexable).
	NoIndex          bool
	Legal            bool
	SpaceName        string
	SpaceRef         string // owner/slug
	BasePath, MCPURL string
	AccountMCPURL    string // sign-in endpoint; empty when OAuth is off
	AccountsEnabled  bool
	Home             bool
	About            bool
	Markdown         bool
	// RawImages: path?raw serves a space's image files (owned spaces), so
	// document.js shows a document's relative pictures.
	RawImages         bool
	State, Path, Text string
	Tree              []*browserNode
	Recent            []recentFile
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

func fileTree(files map[string]record, selected string, spaceRoot ...string) []*browserNode {
	root := &browserNode{}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		parent := root
		parts := strings.Split(path, "/")
		for i, name := range parts {
			var node *browserNode
			for _, child := range parent.Children {
				if child.Name == name {
					node = child
					break
				}
			}
			if node == nil {
				node = &browserNode{Name: name}
				parent.Children = append(parent.Children, node)
			}
			if i == len(parts)-1 {
				node.URL = fileURL(path)
				if len(spaceRoot) > 0 {
					node.URL = spaceRoot[0] + strings.TrimPrefix(node.URL, "/spaces/public/")
				}
				node.Selected = path == selected
			}
			node.Count++
			if i < len(parts)-1 && strings.HasPrefix(selected, strings.Join(parts[:i+1], "/")+"/") {
				node.Open = true
			}
			parent = node
		}
	}
	return root.Children
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

func (h *httpAdapter) serveBrowser(w http.ResponseWriter, r *http.Request, client string) bool {
	path := r.URL.Path
	if path != "/" && path != "/about" && path != "/spaces/public" && !strings.HasPrefix(path, "/spaces/public/") {
		return false
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if path == "/" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; script-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	} else {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src https: data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	if path == "/" && h.serveAppHome(w, r, client) {
		return true
	}
	if path == "/about" {
		if r.URL.RawQuery != "" {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
		// With a docs space, the About page is its about.md. Temporary, so
		// removing the setting brings the built-in page back.
		if h.docsSpace() != "" {
			http.Redirect(w, r, h.basePath+"/docs/about.md", http.StatusFound)
			return true
		}
		var body bytes.Buffer
		if err := browserTemplate.Execute(&body, browserPage{BasePath: h.basePath, About: true, AccountsEnabled: h.service.accounts != nil, Legal: h.docsSpace() != ""}); err != nil {
			sendError(w, err)
			return true
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(body.Bytes())
		}
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
	asked := r.URL.RawQuery == markdownQuery && path != "/"
	for key, values := range q {
		if !asked && (path != "/spaces/public/" || key != "path" || len(values) != 1) {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
	}
	page := browserPage{BasePath: h.basePath, MCPURL: h.publicOrigin + h.basePath + "/mcp", Home: path == "/", AccountsEnabled: h.service.accounts != nil, Legal: h.docsSpace() != ""}
	if h.oauth != nil {
		page.AccountMCPURL = h.oauth.mcpResource
	}
	defaulted := false
	if !page.Home {
		page.Path = strings.TrimPrefix(path, "/spaces/public/")
		if page.Path == "" {
			page.Path = q.Get("path")
		}
		defaulted = page.Path == ""
		if defaulted {
			page.Path = "README.md"
		}
		if !validPath(strings.TrimSuffix(page.Path, "/")) {
			sendError(w, invalid("Invalid path."))
			return true
		}
		if q.Has("path") {
			http.Redirect(w, r, h.basePath+fileURL(page.Path), http.StatusPermanentRedirect)
			return true
		}
		if !indexable(page.Path) {
			page.NoIndex = true
			w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	repo := h.service.repos["public"]
	state, files, err := repo.snapshot(ctx, "")
	if err != nil {
		sendError(w, err)
		return true
	}
	page.State = state
	if page.Home {
		page.Recent, err = repo.recent(ctx, state, files)
		page.Recent = h.publicRecent(page.Recent)
	} else {
		if defaulted {
			page.Path = defaultPage(files)
		}
		markdown := asked || r.URL.RawQuery == "" && prefersMarkdown(r.Header.Get("Accept"))
		if folder, ok := strings.CutSuffix(page.Path, "/"); ok {
			serveFolder(w, r, repo, files, folder, "Metatrash public space", h.publicOrigin, h.basePath+"/spaces/public/", markdown)
			return true
		}
		if folder, name := splitFolder(page.Path); name == "llms.txt" {
			h.serveLLMS(w, r, repo, files, page.Path, folder, "Metatrash public space", h.basePath+"/spaces/public/")
			return true
		}
		file, exists := files[page.Path]
		if !exists {
			sendError(w, missing())
			return true
		}
		var content []byte
		content, err = repo.blob(ctx, file.Blob)
		if err == nil && markdown {
			if serveSource(w, r, page.Path, content) {
				return true
			}
			if asked {
				sendError(w, problem(404, "not_found", "This file is not text, so it has no Markdown."))
				return true
			}
		}
		page.Text = string(content)
		lowerPath := strings.ToLower(page.Path)
		page.Markdown = strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".markdown")
		page.Tree = fileTree(files, page.Path)
		h.publicTree(page.Tree)
	}
	if err != nil {
		sendError(w, err)
		return true
	}
	var body bytes.Buffer
	if err := browserTemplate.Execute(&body, page); err != nil {
		sendError(w, err)
		return true
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !page.Home {
		markdownAlternate(w, h.basePath+fileURL(page.Path))
	}
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
	return true
}

func (h *httpAdapter) publicRecent(files []recentFile) []recentFile {
	for i := range files {
		files[i].URL = h.basePath + files[i].URL
	}
	return files
}

func (h *httpAdapter) publicTree(nodes []*browserNode) {
	for _, node := range nodes {
		if node.URL != "" {
			node.URL = h.basePath + node.URL
		}
		h.publicTree(node.Children)
	}
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
