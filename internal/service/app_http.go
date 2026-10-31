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
// /spaces/ pages stay as they are.

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
<meta name="robots" content="noindex">
<meta name="metatrash-app" content="{{.Config}}">
<title>{{.Title}} — Metatrash</title>
<script src="{{.BasePath}}/assets/app-start.js" nonce="{{.Nonce}}"></script>
<link rel="stylesheet" href="{{.BasePath}}/assets/slate.css">
{{range .Build.CSS}}<link rel="stylesheet" href="{{$.BasePath}}/app/{{.}}">
{{end}}<script src="{{.BasePath}}/assets/slate.js"></script>
</head>
<body>
<div id="app"></div>
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

	root := "/spaces/" + name
	config := map[string]any{"root": root, "file": nil}
	title := name
	if file != "" && !strings.HasSuffix(file, "/") {
		config["file"] = root + "/" + file
		title = path.Base(file)
	}
	encoded, _ := json.Marshal(config)
	// The editor's <style> elements carry this nonce (web/app-start.js).
	nonce, err := randomHex(16)
	if err != nil {
		sendError(w, problem(503, "unavailable", "Try again."))
		return true
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self' 'nonce-"+nonce+"'; style-src-attr 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' https: data: blob:; font-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = appTemplate.Execute(w, struct {
		Config, Title, BasePath, Nonce string
		Build                          *appBuild
	}{string(encoded), title, h.basePath, nonce, build})
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
