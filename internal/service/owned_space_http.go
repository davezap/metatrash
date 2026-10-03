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
		item := accountSpace{ID: space.ID, Name: space.Name, Slug: space.Slug, Ready: space.State == "ready" && h.service.ownedRepository(space.ID) != nil}
		if item.Ready && user.Username != "" {
			item.URL = h.ownedSpaceURL(user.Username, space.Slug)
		}
		spaces = append(spaces, item)
	}
	return spaces, rows.Err()
}

// Every private document request resolves the current human account and checks
// its immutable ID against current ownership or active membership before Git reads.
// There is no bearer-key or public Dispatch path into this handler.
func (h *httpAdapter) serveOwnedBrowser(w http.ResponseWriter, r *http.Request, client string) bool {
	if !strings.HasPrefix(r.URL.Path, "/spaces/") || r.URL.Path == "/spaces/public" || strings.HasPrefix(r.URL.Path, "/spaces/public/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; style-src-attr 'unsafe-inline'; script-src 'self'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		sendError(w, problem(405, "invalid_request", "Use GET or HEAD."))
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
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/spaces/"), "/", 3)
	if len(parts) < 2 || r.URL.RawQuery != "" {
		sendError(w, missing())
		return true
	}
	if len(parts[1]) > 48 || !usernamePattern.MatchString(parts[1]) {
		sendError(w, missing())
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	user, signedIn, err := h.service.accounts.currentUser(ctx, cookieToken(r, h.sessionCookieName()))
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return true
	}
	if !signedIn {
		http.Redirect(w, r, h.basePath+"/login", http.StatusSeeOther)
		return true
	}
	if len(parts[0]) > 32 || !usernamePattern.MatchString(parts[0]) {
		sendError(w, missing())
		return true
	}
	space, err := scanOwnedSpace(h.service.ownedDB.db.QueryRowContext(ctx, "SELECT "+ownedSpaceColumns+" FROM metatrash_spaces WHERE owner_user_id = (SELECT user_id FROM metatrash_users WHERE username = ?) AND slug = ? AND provisioning_state = 'ready' AND (owner_user_id = ? OR EXISTS (SELECT 1 FROM metatrash_memberships m WHERE m.space_id = metatrash_spaces.space_id AND m.user_id = ? AND m.status = 'active'))", parts[0], parts[1], user.ID, user.ID))
	if err == sql.ErrNoRows {
		sendError(w, missing())
		return true
	}
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your space is temporarily unavailable."))
		return true
	}
	repo := h.service.ownedRepository(space.ID)
	if repo == nil {
		sendError(w, missing())
		return true
	}
	root := h.ownedSpaceURL(parts[0], space.Slug)
	if len(parts) == 2 {
		http.Redirect(w, r, root, http.StatusTemporaryRedirect)
		return true
	}
	path := parts[2]
	if path == "" {
		path = "README.md"
	}
	if !validPath(path) {
		sendError(w, invalid("Invalid path."))
		return true
	}
	if err := h.service.reserveOwnedOperation(space.ID, client, false); err != nil {
		sendError(w, err)
		return true
	}
	state, files, err := repo.snapshot(ctx, "")
	if err != nil {
		sendError(w, err)
		return true
	}
	file, exists := files[path]
	if !exists {
		sendError(w, missing())
		return true
	}
	content, err := repo.blob(ctx, file.Blob)
	if err != nil {
		sendError(w, err)
		return true
	}
	configs, err := repo.folderConfigs(ctx, files)
	if err != nil {
		sendError(w, err)
		return true
	}
	lowerPath := strings.ToLower(path)
	page := browserPage{BasePath: h.basePath, AccountsEnabled: true, Private: true, SpaceName: space.Name, Path: path, State: state, Text: string(content), Markdown: strings.HasSuffix(lowerPath, ".md") || strings.HasSuffix(lowerPath, ".markdown"), Tree: fileTree(files, path, root)}
	markServiceFolders(page.Tree, "", configs)
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
