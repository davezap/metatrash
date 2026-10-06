package service

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type accountSpace struct {
	ID, Name, Slug, URL string
	Ready               bool
	// Web is set when anyone may read the space in the website's explorer.
	Web bool
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
		item := accountSpace{ID: space.ID, Name: space.Name, Slug: space.Slug, Ready: space.State == "ready" && h.service.ownedRepository(space.ID) != nil, Web: space.Visibility == spaceWeb}
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
	if r.URL.Path != "/docs" && !strings.HasPrefix(r.URL.Path, "/docs/") {
		return false
	}
	ref := h.docsSpace()
	if ref == "" {
		return false
	}
	owner, slug, _ := strings.Cut(ref, "/")
	path, hasPath := strings.CutPrefix(r.URL.Path, "/docs/")
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

// serveSpacePage renders one file of an owned space in the read-only explorer.
// root is the URL of the space's top folder; hasPath is false for the bare
// space URL without its trailing slash. docs selects the /docs/ rules above.
func (h *httpAdapter) serveSpacePage(w http.ResponseWriter, r *http.Request, client, owner, slug, path string, hasPath bool, root string, docs bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	// noindex until the page itself is known to be indexable (errors,
	// redirects and members-only pages keep it).
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
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
	if r.URL.RawQuery != "" {
		sendError(w, missing())
		return
	}
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
	space, err := scanOwnedSpace(h.service.ownedDB.db.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+", (owner_user_id = ? OR EXISTS (SELECT 1 FROM metatrash_memberships m WHERE m.space_id = metatrash_spaces.space_id AND m.user_id = ? AND m.status = 'active')) FROM metatrash_spaces WHERE owner_user_id = (SELECT user_id FROM metatrash_users WHERE username = ?) AND slug = ? AND provisioning_state = 'ready'", viewer, viewer, owner, slug), &member)
	if err != nil && err != sql.ErrNoRows {
		sendError(w, problem(503, "unavailable", "Your space is temporarily unavailable."))
		return
	}
	if err == sql.ErrNoRows || (!member && space.Visibility != spaceWeb) {
		// Signed out: always the sign-in page, so a private space's existence
		// is not revealed. The docs pages never ask anyone to sign in.
		if !signedIn && !docs {
			http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
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
	if !hasPath {
		http.Redirect(w, r, root, http.StatusTemporaryRedirect)
		return
	}
	if path == "" {
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
	file, exists := files[path]
	if !exists {
		sendError(w, missing())
		return
	}
	content, err := repo.blob(ctx, file.Blob)
	if err != nil {
		sendError(w, err)
		return
	}
	// Docs pages are all indexable; a web-readable space only in its top folder.
	noIndex := !docs && (space.Visibility != spaceWeb || !indexable(path))
	lowerPath := strings.ToLower(path)
	page := browserPage{NoIndex: noIndex, BasePath: h.basePath, AccountsEnabled: true, Private: true, Docs: docs, Legal: h.docsSpace() != "", Web: space.Visibility == spaceWeb, Member: member, SpaceName: space.Name, SpaceRef: owner + "/" + space.Slug, Path: path, State: state, Text: string(content), Markdown: strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".markdown"), Tree: fileTree(files, path, root)}
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
	if r.Method != http.MethodHead {
		_, _ = w.Write(body.Bytes())
	}
}
