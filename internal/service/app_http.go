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
// (npm run build:web) is copied into web/app. Since 0.87.0 it is the page at
// a space's own addresses (the old /app/ ones redirect there):
//
//	/spaces/public/[path]           the public space
//	/spaces/{owner}/{slug}/[path]   an owned space
//	/docs/[path]                    the docs space
//	/app/assets/{name}              the build's files (hashed names)
//
// The page opens the space at the file in the path, and the UI reads and
// changes files through the site API with the person's own access. The same
// addresses still answer ?raw, ?format=md, Accept: text/markdown, ?export
// and llms.txt as before, for programs and agents.
//
// With a docs space readable on the web, the home page (/) is the same page
// on the docs space's about.md (introduction.md before 0.95.5; 0.75.0).

//go:embed all:web/app
var appFiles embed.FS

//go:embed web/app-start.js
var appStartJS []byte

// appBuild is the entry script and its stylesheets, from the build's manifest.
type appBuild struct {
	Entry string
	CSS   []string
}

// appManifest is the build's .vite/manifest.json, by source file.
type appManifest map[string]struct {
	File           string   `json:"file"`
	CSS            []string `json:"css"`
	Imports        []string `json:"imports"`
	IsEntry        bool     `json:"isEntry"`
	DynamicImports []string `json:"dynamicImports"`
	Assets         []string `json:"assets"`
}

var appManifestOnce = sync.OnceValue(func() appManifest {
	data, err := appFiles.ReadFile("web/app/.vite/manifest.json")
	if err != nil {
		return nil
	}
	var manifest appManifest
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	return manifest
})

// entry is the build of one entry script: its file and every stylesheet it
// needs, its own and those of the chunks it shares with the other entry
// (one application entry plus shared chunks).
func (m appManifest) entry(src string) *appBuild {
	chunk, ok := m[src]
	if !ok || !chunk.IsEntry || chunk.File == "" {
		return nil
	}
	build := &appBuild{Entry: chunk.File}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		c := m[key]
		for _, imported := range c.Imports {
			walk(imported)
		}
		for _, css := range c.CSS {
			if !seen["css:"+css] {
				seen["css:"+css] = true
				build.CSS = append(build.CSS, css)
			}
		}
	}
	walk(src)
	return build
}

// appBuildOnce is the app (src/main.ts in the Viewer's website build).
var appBuildOnce = sync.OnceValue(func() *appBuild {
	return appManifestOnce().entry("src/main.ts")
})

var appTemplate = template.Must(template.New("app").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
{{if .Home}}<meta name="description" content="{{.Description}}">{{end}}{{if .NoIndex}}<meta name="robots" content="noindex, nofollow">{{end}}
<meta name="metatrash-app" content="{{.Config}}">
<title>{{.Title}}</title>
<meta property="og:site_name" content="{{.SiteName}}">
<meta property="og:type" content="website">
<meta property="og:title" content="{{.Title}}">
<meta property="og:description" content="{{.Description}}">
{{if .ShareImage}}<meta property="og:image" content="{{.ShareImage}}">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta name="twitter:card" content="summary_large_image">
{{else}}<meta name="twitter:card" content="summary">
{{end}}<link rel="icon" href="{{.BasePath}}/favicon.ico" sizes="any">
<link rel="apple-touch-icon" href="{{.BasePath}}/apple-touch-icon.png">
{{if .Markdown}}<link rel="alternate" type="text/markdown" href="{{.BasePath}}{{.Markdown}}">
{{end}}
<script src="{{.BasePath}}/assets/app-start.js" nonce="{{.Nonce}}"></script>
<link rel="stylesheet" href="{{.BasePath}}/assets/slate.css">
{{range .Build.CSS}}<link rel="stylesheet" href="{{$.BasePath}}/app/{{.}}">
{{end}}<script src="{{.BasePath}}/assets/slate.js"></script>
</head>
<body>
<div id="app"></div>
<noscript><p>This page needs JavaScript.{{if .Markdown}} Without it, read <a href="{{.BasePath}}{{.Markdown}}">this page's text</a>.{{end}}{{if .Home}} The documentation's pages are listed in <a href="{{.BasePath}}/llms.txt">llms.txt</a>.{{end}}</p></noscript>
<script type="module" src="{{.BasePath}}/app/{{.Build.Entry}}"></script>
</body>
</html>
`))

// appCSP is the app page's Content-Security-Policy. The page holds Markdown other
// people wrote next to the account's settings, so it allows no inline script, no
// eval, no plugins or frames and no other site's scripts: a document can't run
// code even if Slate let markup through. TestAppCSP keeps it that way.
func appCSP(nonce string) string {
	return "default-src 'none'; style-src 'self' 'nonce-" + nonce + "'; style-src-attr 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' https: data: blob:; font-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
}

func (h *httpAdapter) serveApp(w http.ResponseWriter, r *http.Request, client string) bool {
	if r.URL.Path == "/spaces" || r.URL.Path == "/spaces/" {
		// The site's own address for Settings (/spaces/#settings/...) and for
		// "somewhere to start": the public space. The #fragment stays.
		http.Redirect(w, r, h.basePath+"/spaces/public/", http.StatusSeeOther)
		return true
	}
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
	// The app's old addresses (0.73.0 to 0.86.0), in bookmarks, emails and
	// printed PDFs: /app/{space}/{file} is now /spaces/{space}/{file}, and
	// the docs space's /docs/{file}. The #fragment (#settings/...) stays.
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.EscapedPath(), "/app"), "/")
	target := "/spaces/" + rest
	if docs := h.docsSpace(); docs != "" && (rest == docs || strings.HasPrefix(rest, docs+"/")) {
		target = "/docs/" + strings.TrimPrefix(strings.TrimPrefix(rest, docs), "/")
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, h.basePath+target, http.StatusMovedPermanently)
	return true
}

// appPage is what one page of the app opens.
type appPage struct {
	// Space is owner/slug or public, File the path in it ("" or a folder
	// for none). Address is the page's address for the space's top folder
	// (/spaces/{space}/ or /docs/); the app keeps the address bar under it.
	// Markdown links the file's own text (?format=md) for browsers without
	// JavaScript; empty when it has none.
	Space, File, Title, Address, Markdown string
	// Home is the home page: indexed, its address kept as /. NoIndex asks
	// search engines to skip the page (see indexable).
	Home, NoIndex bool
}

func (h *httpAdapter) writeAppPage(w http.ResponseWriter, build *appBuild, page appPage) {
	root := "/spaces/" + page.Space
	config := map[string]any{"root": root, "file": nil}
	if page.Address != "" {
		config["addressRoot"] = strings.TrimSuffix(page.Address, "/")
	}
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
	w.Header().Set("Content-Security-Policy", appCSP(nonce))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Link previews (0.95.28): the page's title, the site's description, and
	// the brand's picture when it has one. The title is the page's <title>
	// already, so a preview shows nothing the page doesn't.
	shareImage := ""
	if b := h.service.brand; b != nil && len(b.ShareImage) > 0 {
		shareImage = h.publicOrigin + h.basePath + "/assets/share.png"
	}
	_ = appTemplate.Execute(w, struct {
		Config, Title, BasePath, Nonce, Markdown, Description, SiteName, ShareImage string
		Home, NoIndex                                                               bool
		Build                                                                       *appBuild
	}{string(encoded), page.Title, h.basePath, nonce, page.Markdown, h.homeDescription(), h.service.brandName(), shareImage, page.Home, page.NoIndex, build})
}

// serveAppHome answers / with the app on the docs space's about.md (0.95.5;
// introduction.md, the name before, while a space still has it; else its
// first note), when there is a docs space readable on the web and a
// build of the app. Otherwise it leaves / to the built-in home page.
func (h *httpAdapter) serveAppHome(w http.ResponseWriter, r *http.Request, client string) bool {
	ref, build := h.docsSpace(), h.frontendEntry("src/main.ts")
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
	file := ""
	if _, files, err := repo.snapshot(ctx, ""); err == nil {
		for _, name := range homePages {
			if files[name].Blob != "" {
				file = name
				break
			}
		}
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	page := appPage{Space: name, File: file, Title: h.homeTitle(), Address: "/docs/", Home: true}
	if file != "" {
		page.Markdown = "/docs/" + file + "?format=md"
	}
	h.writeAppPage(w, build, page)
	return true
}

// homePages are the docs space's files the home page opens, the first one
// present: about.md, the site's About page (/about goes there too), and
// introduction.md, its name before 0.95.5, for docs spaces not renamed yet.
var homePages = []string{"about.md", "introduction.md"}

const siteHomeTitle = "Metatrash — where AI agents work together"
const siteHomeDescription = "Where AI agents work together: shared folders that carry their own instructions, rules and actions, backed by Git."

// homeTitle and homeDescription are the home page's, the brand's when it
// sets them (0.95.28).
func (h *httpAdapter) homeTitle() string {
	if b := h.service.brand; b != nil && b.HomeTitle != "" {
		return b.HomeTitle
	}
	return brandText(h.service.brandName(), siteHomeTitle)
}
func (h *httpAdapter) homeDescription() string {
	if b := h.service.brand; b != nil && b.Description != "" {
		return b.Description
	}
	return siteHomeDescription
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
	data, err := h.frontendAsset(name)
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
