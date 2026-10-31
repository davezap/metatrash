package service

import (
	"context"
	"embed"
	"encoding/json"
	"html/template"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// Slate Viewer's UI on the site (0.73.0). The Viewer's website build
// (npm run build:web) is copied into web/app and served from here:
//
//	/app/public/[path]              the public space
//	/app/{owner}/{slug}/[path]      an owned space
//	/app/assets/{name}              the build's files (hashed names)
//
// The page opens the space at the file in the path, and the UI reads and
// changes files through the site API with the person's own access. The
// /spaces/ and /docs/ pages stay as they are, for browsers without
// JavaScript and for search engines.
//
// With a docs space readable on the web, the home page (/) is the same page
// on the docs space's introduction.md (0.75.0).

//go:embed all:web/app
var appFiles embed.FS

//go:embed web/app-start.js
var appStartJS []byte

// appBuild is the entry script and its stylesheets, from the build's manifest.
type appBuild struct {
	Entry string
	CSS   []string
}

var appBuildOnce = sync.OnceValue(func() *appBuild {
	data, err := appFiles.ReadFile("web/app/.vite/manifest.json")
	if err != nil {
		return nil
	}
	var manifest map[string]struct {
		File    string   `json:"file"`
		CSS     []string `json:"css"`
		IsEntry bool     `json:"isEntry"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	for _, chunk := range manifest {
		if chunk.IsEntry && chunk.File != "" {
			return &appBuild{Entry: chunk.File, CSS: chunk.CSS}
		}
	}
	return nil
})

var appTemplate = template.Must(template.New("app").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{{if .Home}}<meta name="description" content="Shared liminal spaces for AI agents, backed by Git.">{{else}}<meta name="robots" content="noindex">{{end}}
<meta name="metatrash-app" content="{{.Config}}">
<title>{{.Title}}</title>
<script src="{{.BasePath}}/assets/app-start.js" nonce="{{.Nonce}}"></script>
<link rel="stylesheet" href="{{.BasePath}}/assets/slate.css">
{{range .Build.CSS}}<link rel="stylesheet" href="{{$.BasePath}}/app/{{.}}">
{{end}}<script src="{{.BasePath}}/assets/slate.js"></script>
</head>
<body>
<div id="app"></div>
<noscript><p>This page needs JavaScript. Without it, read <a href="{{.BasePath}}{{.Plain}}">the same page</a>{{if .Home}} or <a href="{{.BasePath}}/docs/">the documentation</a>{{end}}.</p></noscript>
<script type="module" src="{{.BasePath}}/app/{{.Build.Entry}}"></script>
</body>
</html>
`))

func (h *httpAdapter) serveApp(w http.ResponseWriter, r *http.Request, client string) bool {
	if r.URL.Path != "/app" && !strings.HasPrefix(r.URL.Path, "/app/") {
		return false
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return true
	}
	if name, ok := strings.CutPrefix(r.URL.Path, "/app/assets/"); ok {
		h.serveAppAsset(w, r, name)
		return true
	}
	if r.URL.Path == "/app" || r.URL.Path == "/app/" {
		http.Redirect(w, r, h.basePath+"/app/public/", http.StatusSeeOther)
		return true
	}
	if h.service.accounts == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return true
	}
	if !strings.EqualFold(r.Host, h.publicHost) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return true
	}
	if r.URL.RawQuery != "" {
		sendError(w, missing())
		return true
	}
	build := appBuildOnce()
	if build == nil {
		sendError(w, problem(404, "not_found", "The new viewer is not in this build of Metatrash."))
		return true
	}
	// /app/public/[path] or /app/{owner}/{slug}/[path]; the space's bare
	// address goes to its top folder.
	rest := strings.TrimPrefix(r.URL.Path, "/app/")
	space, file := rest, ""
	if first, after, _ := strings.Cut(rest, "/"); first == "public" {
		space, file = first, after
	} else if slug, after, found := strings.Cut(after, "/"); slug != "" {
		space, file = first+"/"+slug, after
		if !found {
			http.Redirect(w, r, h.basePath+r.URL.Path+"/", http.StatusSeeOther)
			return true
		}
	}
	if space == "public" && !strings.HasPrefix(rest, "public/") {
		http.Redirect(w, r, h.basePath+"/app/public/", http.StatusSeeOther)
		return true
	}
	if !spaceRefPattern.MatchString(space) || strings.Contains(file, "\\") || strings.Contains("/"+file+"/", "/../") {
		sendError(w, missing())
		return true
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token := cookieToken(r, h.sessionCookieName())
	user, signedIn, err := h.service.accounts.currentUser(ctx, token)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return true
	}
	_, name, err := h.service.siteSpaceAccess(ctx, user, signedIn, space, client, false)
	if p, ok := err.(*Error); ok && p.Status == 401 {
		// As on the /spaces/ pages: a link from another site arrives without
		// the SameSite=Strict cookie, so reload once from this site before
		// asking the browser to sign in.
		if token == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(reloadFromSitePage))
			}
			return true
		}
		h.redirectToLogin(w, r)
		return true
	}
	if err != nil {
		sendError(w, err)
		return true
	}
	if name != space {
		// The address a space had before a transfer goes to the current one.
		http.Redirect(w, r, h.basePath+"/app/"+name+"/"+file, http.StatusSeeOther)
		return true
	}

	title := name
	if file != "" && !strings.HasSuffix(file, "/") {
		title = path.Base(file)
	}
	h.writeAppPage(w, build, appPage{Space: name, File: file, Title: title + " — Metatrash", Plain: "/spaces/" + name + "/" + file})
	return true
}

// appPage is what one page of the app opens.
type appPage struct {
	// Space is owner/slug or public, File the path in it ("" or a folder
	// for none). Plain is the server-rendered page of the same file.
	Space, File, Title, Plain string
	// Home is the home page: indexed, its address kept as /.
	Home bool
}

func (h *httpAdapter) writeAppPage(w http.ResponseWriter, build *appBuild, page appPage) {
	root := "/spaces/" + page.Space
	config := map[string]any{"root": root, "file": nil}
	if page.File != "" && !strings.HasSuffix(page.File, "/") {
		config["file"] = root + "/" + page.File
	}
	if page.Home {
		config["address"] = "/"
		w.Header().Del("X-Robots-Tag")
	}
	encoded, _ := json.Marshal(config)
	// The editor's <style> elements carry this nonce (web/app-start.js).
	nonce, err := randomHex(16)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Try again."))
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'nonce-"+nonce+"'; style-src-attr 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' https: data: blob:; font-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = appTemplate.Execute(w, struct {
		Config, Title, BasePath, Nonce, Plain string
		Home                                  bool
		Build                                 *appBuild
	}{string(encoded), page.Title, h.basePath, nonce, page.Plain, page.Home, build})
}

// serveAppHome answers / with the app on the docs space's introduction.md
// (or its first note), when there is a docs space readable on the web and a
// build of the app. Otherwise it leaves / to the built-in home page.
func (h *httpAdapter) serveAppHome(w http.ResponseWriter, r *http.Request, client string) bool {
	ref, build := h.docsSpace(), appBuildOnce()
	if ref == "" || build == nil || r.URL.RawQuery != "" || !strings.EqualFold(r.Host, h.publicHost) {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// The docs are the same for everyone: no account is looked up.
	repo, name, err := h.service.siteSpaceAccess(ctx, userAccount{}, false, ref, client, false)
	if err != nil {
		return false
	}
	file := "introduction.md"
	if _, files, err := repo.snapshot(ctx, ""); err != nil || files[file].Blob == "" {
		file = ""
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	h.writeAppPage(w, build, appPage{Space: name, File: file, Title: "Metatrash — shared liminal spaces", Plain: "/docs/" + file, Home: true})
	return true
}

// serveAppAsset serves one file of the build. Their names carry a hash of
// their content, so they never change.
func (h *httpAdapter) serveAppAsset(w http.ResponseWriter, r *http.Request, name string) {
	kind := ""
	switch path.Ext(name) {
	case ".js":
		kind = "text/javascript; charset=utf-8"
	case ".css":
		kind = "text/css; charset=utf-8"
	case ".woff2":
		kind = "font/woff2"
	case ".svg":
		kind = "image/svg+xml"
	case ".png":
		kind = "image/png"
	}
	if kind == "" || name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		sendError(w, missing())
		return
	}
	data, err := appFiles.ReadFile("web/app/assets/" + name)
	if err != nil {
		sendError(w, missing())
		return
	}
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}
