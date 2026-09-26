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
)

//go:embed web/page.html
var browserHTML string

//go:embed web/style.css
var browserCSS []byte

var browserTemplate = template.Must(template.New("page").Parse(browserHTML))

type browserNode struct {
	Name, URL string
	Selected  bool
	Children  []*browserNode
}

type recentFile struct {
	Path, URL, Timestamp, Date string
}

type browserPage struct {
	Home              bool
	State, Path, Text string
	Tree              []*browserNode
	Recent            []recentFile
}

func fileURL(path string) string {
	return "/spaces/public/?" + url.Values{"path": {path}}.Encode()
}

func fileTree(files map[string]record, selected string) []*browserNode {
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
				node.Selected = path == selected
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
	if path != "/" && path != "/spaces/public" && path != "/spaces/public/" && path != "/assets/style.css" {
		return false
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	if path == "/assets/style.css" {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		if r.Method != http.MethodHead {
			_, _ = w.Write(browserCSS)
		}
		return true
	}
	if path == "/spaces/public" {
		location := "/spaces/public/"
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
		if path == "/" || key != "path" || len(values) != 1 {
			sendError(w, invalid("Unknown or duplicate query parameter."))
			return true
		}
	}
	page := browserPage{Home: path == "/"}
	if !page.Home {
		page.Path = q.Get("path")
		if page.Path == "" {
			page.Path = "README.md"
		}
		if !validPath(page.Path) {
			sendError(w, invalid("Invalid path."))
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
	} else {
		file, exists := files[page.Path]
		if !exists {
			sendError(w, missing())
			return true
		}
		var content []byte
		content, err = repo.blob(ctx, file.Blob)
		page.Text = string(content)
		page.Tree = fileTree(files, page.Path)
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
