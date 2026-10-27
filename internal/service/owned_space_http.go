package service

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"strconv"
	"strings"
	"time"
)

type accountSpace struct {
	ID, Name, Slug, URL string
	Ready               bool
	// Web is set when anyone may read the space in the website's explorer.
	Web bool
	// Timezone is the space's own time zone; "" uses the server's.
	Timezone string
}

func (h *httpAdapter) sessionCookieName() string {
	if h.basePath == "" {
		return sessionCookie
	}
	return sessionCookie + "-" + secretDigest(h.basePath)[:16]
}

func (h *httpAdapter) ownedSpaceURL(username, slug string) string {
	// Both segments have already passed the persisted ASCII validation rules.
	return h.basePath + "/spaces/" + username + "/" + slug + "/"
}

func (h *httpAdapter) accountSpaces(ctx context.Context, user userAccount) ([]accountSpace, error) {
	if h.service.ownedDB == nil {
		return nil, fmt.Errorf("owned spaces unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	zones, err := h.service.ownedDB.spaceTimezones(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	rows, err := h.service.ownedDB.db.QueryContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE owner_user_id = ? ORDER BY created_at, space_id", user.ID)
	if err != nil {
		return nil, fmt.Errorf("cannot list owned spaces")
	}
	defer rows.Close()
	spaces := []accountSpace{}
	for rows.Next() {
		space, err := scanOwnedSpace(rows)
		if err != nil {
			return nil, err
		}
		item := accountSpace{ID: space.ID, Name: space.Name, Slug: space.Slug, Ready: space.State == "ready" && h.service.ownedRepository(space.ID) != nil, Web: space.Visibility == spaceWeb, Timezone: zones[space.ID]}
		if item.Ready && user.Username != "" {
			item.URL = h.ownedSpaceURL(user.Username, space.Slug)
		}
		spaces = append(spaces, item)
	}
	return spaces, rows.Err()
}

// docsSpace is the owner/slug of the space shown under /docs/, or "" when
// none is configured (or accounts are off).
func (h *httpAdapter) docsSpace() string {
	if h.service.accounts == nil || h.service.ownedDB == nil {
		return ""
	}
	return h.service.accounts.config.DocsSpace
}

// serveDocs shows the configured docs space under /docs/ as part of the site,
// without the space's own name and header. It is the same read-only explorer,
// but only while the space is readable on the web: otherwise, and for anything
// missing, /docs/ is simply not found, signed in or not, members included.
func (h *httpAdapter) serveDocs(w http.ResponseWriter, r *http.Request, client string) bool {
	// /llms.txt, where AI tools look first, is the docs' llms.txt.
	if r.URL.Path != "/docs" && !strings.HasPrefix(r.URL.Path, "/docs/") && r.URL.Path != "/llms.txt" {
		return false
	}
	ref := h.docsSpace()
	if ref == "" {
		return false
	}
	owner, slug, _ := strings.Cut(ref, "/")
	path, hasPath := strings.CutPrefix(r.URL.Path, "/docs/")
	if r.URL.Path == "/llms.txt" {
		path, hasPath = "llms.txt", true
	}
	h.serveSpacePage(w, r, client, owner, slug, path, hasPath, h.basePath+"/docs/", true)
	return true
}

// Every private document request resolves the current human account and checks
// its immutable ID against current ownership or active membership before Git reads.
// A space its owner made readable on the web ("web") is also shown, read-only,
// to anyone, signed in or not. There is no bearer-key or public Dispatch path
// into this handler, and agents' access does not depend on it.
func (h *httpAdapter) serveOwnedBrowser(w http.ResponseWriter, r *http.Request, client string) bool {
	if !strings.HasPrefix(r.URL.Path, "/spaces/") || r.URL.Path == "/spaces/public" || strings.HasPrefix(r.URL.Path, "/spaces/public/") {
		return false
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/spaces/"), "/", 3)
	if len(parts) < 2 {
		parts = append(parts, "")
	}
	path := ""
	if len(parts) == 3 {
		path = parts[2]
	}
	h.serveSpacePage(w, r, client, parts[0], parts[1], path, len(parts) == 3, h.ownedSpaceURL(parts[0], parts[1]), false)
	return true
}

// reloadFromSitePage reloads the address it was served at, from this site.
const reloadFromSitePage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="robots" content="noindex"><meta http-equiv="refresh" content="0"><title>Opening… — Metatrash</title></head><body><p>Opening… <a href="">Continue</a></p></body></html>`

// serveSpacePage renders one file of an owned space in the read-only explorer.
// root is the URL of the space's top folder; hasPath is false for the bare
// space URL without its trailing slash. docs selects the /docs/ rules above.
func (h *httpAdapter) serveSpacePage(w http.ResponseWriter, r *http.Request, client, owner, slug, path string, hasPath bool, root string, docs bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// noindex until the page itself is known to be indexable (errors,
	// redirects and members-only pages keep it).
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' https: data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
		return
	}
	if h.service.accounts == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	if !strings.EqualFold(r.Host, h.publicHost) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return
	}
	// ?raw serves an image file itself, for the pictures in a document, and
	// ?format=md (or Accept: text/markdown) a file's text as it is.
	raw := r.URL.RawQuery == "raw"
	asked := r.URL.RawQuery == markdownQuery
	if r.URL.RawQuery != "" && !raw && !asked {
		sendError(w, missing())
		return
	}
	markdown := asked || r.URL.RawQuery == "" && prefersMarkdown(r.Header.Get("Accept"))
	if len(slug) > 48 || !usernamePattern.MatchString(slug) || len(owner) > 32 || !usernamePattern.MatchString(owner) {
		sendError(w, missing())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	// The docs pages are the same for everyone, so they never look at the
	// visitor's account.
	var user userAccount
	var signedIn bool
	if !docs {
		var err error
		user, signedIn, err = h.service.accounts.currentUser(ctx, cookieToken(r, h.sessionCookieName()))
		if err != nil {
			sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
			return
		}
	}
	viewer := ""
	if signedIn {
		viewer = user.ID
	}
	var member bool
	const columns = "SELECT " + ownedSpaceColumns + ", (owner_user_id = ? OR EXISTS (SELECT 1 FROM metatrash_memberships m WHERE m.space_id = metatrash_spaces.space_id AND m.user_id = ? AND m.status = 'active')) FROM metatrash_spaces WHERE "
	space, err := scanOwnedSpace(h.service.ownedDB.db.QueryRowContext(ctx, columns+"owner_user_id = (SELECT user_id FROM metatrash_users WHERE username = ?) AND slug = ? AND provisioning_state = 'ready'", viewer, viewer, owner, slug), &member)
	moved := false
	if err == sql.ErrNoRows {
		// The address the space had before a transfer: the docs pages carry
		// on from it, and website links go to the current address, but only
		// for someone who may see the space there.
		id, found, aliasErr := h.service.ownedDB.spaceByAlias(ctx, owner, slug)
		switch {
		case aliasErr != nil:
			err = aliasErr
		case found:
			space, err = scanOwnedSpace(h.service.ownedDB.db.QueryRowContext(ctx, columns+"space_id = ? AND provisioning_state = 'ready'", viewer, viewer, id), &member)
			moved = err == nil
		}
	}
	if err != nil && err != sql.ErrNoRows {
		sendError(w, problem(503, "unavailable", "Your space is temporarily unavailable."))
		return
	}
	if err == sql.ErrNoRows || (!member && space.Visibility != spaceWeb) {
		// Signed out: always the sign-in page, so a private space's existence
		// is not revealed. The docs pages never ask anyone to sign in.
		if !signedIn && !docs {
			// The session cookie is SameSite=Strict, so a link from another
			// site or a PDF arrives without it even when this browser is
			// signed in. Reload once from this site, which sends it, before
			// sending the browser to sign in. The reload is same-origin, so
			// it cannot loop, and it keeps the link's #fragment.
			if cookieToken(r, h.sessionCookieName()) == "" && r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(reloadFromSitePage))
				}
				return
			}
			h.redirectToLogin(w, r)
			return
		}
		sendError(w, missing())
		return
	}
	repo := h.service.ownedRepository(space.ID)
	if repo == nil {
		sendError(w, missing())
		return
	}
	if moved && !docs {
		var current string
		if err := h.service.ownedDB.db.QueryRowContext(ctx, "SELECT username FROM metatrash_users WHERE user_id = ? AND username IS NOT NULL", space.OwnerID).Scan(&current); err != nil {
			sendError(w, problem(503, "unavailable", "Your space is temporarily unavailable."))
			return
		}
		target := h.ownedSpaceURL(current, space.Slug)
		if hasPath {
			target += (&url.URL{Path: path}).EscapedPath()
		}
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		// Permanent: the old address stays reserved for this space, and
		// search engines move a web-readable space's pages to the new one.
		// (Cache-Control: no-store, set above, still applies: the space can
		// move again, or come back to this address.)
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	if !hasPath {
		target := root
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
		return
	}
	defaulted := path == ""
	if defaulted {
		path = "README.md"
	}
	if !validPath(path) {
		sendError(w, invalid("Invalid path."))
		return
	}
	if err := h.service.reserveOwnedOperation(space.ID, client, false); err != nil {
		sendError(w, err)
		return
	}
	state, files, err := repo.snapshot(ctx, "")
	if err != nil {
		sendError(w, err)
		return
	}
	if defaulted {
		path = defaultPage(files)
	}
	if path == "llms.txt" && !raw {
		h.serveLLMS(w, r, repo, files, space.Name, docs, root)
		return
	}
	file, exists := files[path]
	if !exists {
		sendError(w, missing())
		return
	}
	if raw {
		h.serveRawImage(w, r, repo, path, file.Blob)
		return
	}
	content, err := repo.blob(ctx, file.Blob)
	if err != nil {
		sendError(w, err)
		return
	}
	if markdown && serveSource(w, r, path, content) {
		return
	}
	if asked {
		sendError(w, problem(404, "not_found", "This file is not text, so it has no Markdown."))
		return
	}
	// Docs pages are all indexable; a web-readable space only in its top folder.
	noIndex := !docs && (space.Visibility != spaceWeb || !indexable(path))
	lowerPath := strings.ToLower(path)
	page := browserPage{NoIndex: noIndex, BasePath: h.basePath, AccountsEnabled: true, Private: true, Docs: docs, Legal: h.docsSpace() != "", Web: space.Visibility == spaceWeb, Member: member, SpaceName: space.Name, SpaceRef: owner + "/" + space.Slug, Path: path, State: state, Text: string(content), Markdown: strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".markdown"), RawImages: true, Tree: fileTree(files, path, root)}
	if !docs {
		configs, err := repo.folderConfigs(ctx, files)
		if err != nil {
			sendError(w, err)
			return
		}
		markServiceFolders(page.Tree, "", configs)
	}
	var body bytes.Buffer
	if err := browserTemplate.Execute(&body, page); err != nil {
		sendError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if !noIndex {
		w.Header().Del("X-Robots-Tag")
	}
	markdownAlternate(w, root+(&url.URL{Path: path}).EscapedPath())
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}

// serveLLMS sends a space's llms.txt: its own file if it has one, else the
// list of its Markdown pages. The caller has checked the visitor may read
// the space.
func (h *httpAdapter) serveLLMS(w http.ResponseWriter, r *http.Request, repo *repository, files map[string]record, name string, docs bool, root string) {
	if file, ok := files["llms.txt"]; ok {
		content, err := repo.blob(r.Context(), file.Blob)
		if err != nil {
			sendError(w, err)
			return
		}
		if !serveSource(w, r, "llms.txt", content) {
			sendError(w, missing())
		}
		return
	}
	if docs {
		name = "Metatrash docs"
	}
	sendText(w, r, "text/plain; charset=utf-8", llmsText(name, h.publicOrigin+root, files))
}

// rawImageTypes are the files ?raw serves, by extension: pictures only, so a
// space's HTML or scripts never run on this site.
var rawImageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon",
	".svg": "image/svg+xml",
}

// serveRawImage sends an image file of a space as itself (path?raw), so a
// document's pictures show on its page as they do in Slate Viewer. The
// caller has checked the visitor may read the space.
func (h *httpAdapter) serveRawImage(w http.ResponseWriter, r *http.Request, repo *repository, path, blob string) {
	kind, ok := rawImageTypes[strings.ToLower(pathpkg.Ext(path))]
	if !ok {
		sendError(w, missing())
		return
	}
	content, err := repo.blob(r.Context(), blob)
	if err != nil {
		sendError(w, err)
		return
	}
	// An SVG opened on its own runs nothing: no scripts, no other origin.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", kind)
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(content)
	}
}
