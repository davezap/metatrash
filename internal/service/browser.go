package service

import (
	"bytes"
	"context"
	_ "embed"
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

//go:embed web/slate.js
var slateJS []byte

//go:embed web/slate.css
var slateCSS []byte

//go:embed web/document.js
var documentJS []byte

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
	default:
		sendError(w, missing())
		return true
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
	Private           bool
	SpaceName         string
	BasePath, MCPURL  string
	AccountMCPURL     string // sign-in endpoint; empty when OAuth is off
	AccountsEnabled   bool
	Home              bool
	About             bool
	Markdown          bool
	State, Path, Text string
	Tree              []*browserNode
	Recent            []recentFile
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
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; img-src https: data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	if path == "/about" {
		if r.URL.RawQuery != "" {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
		var body bytes.Buffer
		if err := browserTemplate.Execute(&body, browserPage{BasePath: h.basePath, About: true, AccountsEnabled: h.service.accounts != nil}); err != nil {
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
	for key, values := range q {
		if path != "/spaces/public/" || key != "path" || len(values) != 1 {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
	}
	page := browserPage{BasePath: h.basePath, MCPURL: h.publicOrigin + h.basePath + "/mcp", Home: path == "/", AccountsEnabled: h.service.accounts != nil}
	if h.oauth != nil {
		page.AccountMCPURL = h.oauth.mcpResource
	}
	if !page.Home {
		page.Path = strings.TrimPrefix(path, "/spaces/public/")
		if page.Path == "" {
			page.Path = q.Get("path")
		}
		if page.Path == "" {
			page.Path = "README.md"
		}
		if !validPath(page.Path) {
			sendError(w, invalid("Invalid path."))
			return true
		}
		if q.Has("path") {
			http.Redirect(w, r, h.basePath+fileURL(page.Path), http.StatusPermanentRedirect)
			return true
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
		file, exists := files[page.Path]
		if !exists {
			sendError(w, missing())
			return true
		}
		var content []byte
		content, err = repo.blob(ctx, file.Blob)
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
