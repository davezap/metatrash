package service

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// The site API (0.72.0) is the REST resource again for the signed-in
// browser, so the website can run Slate Viewer's UI: the session cookie
// instead of an OAuth token, the same handlers behind it.
//
//	GET  /api/v1/site/session                           who is signed in, the CSRF token, their spaces
//	*    /api/v1/site/spaces/public/{resource}          the public space
//	*    /api/v1/site/spaces/{owner}/{slug}/{resource}  an owned space
//
// {resource} is file, files, history, search, edit, move, append, export,
// content (GET), acknowledgements, submissions or reviews, as on
// /api/v1/account. GitHub pull/push/pending, uploads and configure stay with
// the OAuth resource for now.
//
// A person signed in on the site has their own access: the owner can change
// a space, a member as their permission allows (the one mtsync uses), and
// anyone, signed in or not, may read a space its owner made readable on the
// web. Writes need a signed-in session, a same-origin Origin header and the
// session's CSRF token in X-Metatrash-CSRF. The cookie is SameSite=Strict
// and writes must be application/json, so these are a second and third lock.

const siteCSRFHeader = "X-Metatrash-CSRF"

// siteAppName is the app recorded on changes made through the website.
const siteAppName = "Metatrash website"

type siteRequestKey struct{}

// siteRequest is true for a request through the site API, which may use the
// resources kept in the account database, as the OAuth resource may.
func siteRequest(ctx context.Context) bool {
	v, _ := ctx.Value(siteRequestKey{}).(bool)
	return v
}

// siteResources are the REST resources the site API offers, by method.
var siteResources = map[string]string{
	"file": "GET, PUT, DELETE", "files": "GET", "history": "GET", "search": "GET", "export": "GET",
	"edit": "POST", "move": "POST", "append": "POST",
	"content": "GET", "acknowledgements": "GET, POST", "submissions": "GET, POST", "reviews": "POST",
}

// siteSpace is one space in the session answer.
type siteSpace struct {
	Space  string `json:"space"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Access string `json:"access"`
}

type siteSession struct {
	SignedIn bool        `json:"signedIn"`
	Account  string      `json:"account,omitempty"`
	CSRF     string      `json:"csrf,omitempty"`
	Spaces   []siteSpace `json:"spaces"`
}

func (h *httpAdapter) siteCSRF(token string) string {
	return h.service.accounts.mac("site-api:" + token)
}

// accountName is how a person shows in history: their username, or their
// email address before the @ when they have none.
func accountName(user userAccount) string {
	if user.Username != "" {
		return user.Username
	}
	return emailName(user.Email)
}

func (h *httpAdapter) serveSiteAPI(w http.ResponseWriter, r *http.Request, client string) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	if h.service.accounts == nil || h.service.ownedDB == nil {
		sendError(w, missing())
		return
	}
	if !strings.EqualFold(r.Host, h.publicHost) {
		sendError(w, problem(403, "forbidden", "Use the configured site address."))
		return
	}
	// Only this site's own pages call it. A browser names the page's origin
	// on every write and on cross-origin reads; Sec-Fetch-Site says the same
	// where the browser sends it. "none" is the person opening the address
	// themselves (typed or bookmarked), which is fine for reading.
	origins := r.Header.Values("Origin")
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	if len(origins) > 1 || len(origins) == 1 && origins[0] != h.publicOrigin || !read && len(origins) == 0 {
		sendError(w, problem(403, "forbidden", "Unrecognized origin."))
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && !(read && site == "none") {
		sendError(w, problem(403, "forbidden", "Unrecognized origin."))
		return
	}
	token := cookieToken(r, h.sessionCookieName())
	lookup, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	user, signedIn, err := h.service.accounts.currentUser(lookup, token)
	cancel()
	if err != nil {
		sendError(w, problem(503, "unavailable", "Your account is temporarily unavailable."))
		return
	}
	limitKey := "site:" + client
	if signedIn {
		limitKey = "site:user:" + user.ID
	}
	if err := h.service.rates.reserve(allowance{limitKey, 600, 60}); err != nil {
		sendError(w, err)
		return
	}
	if !read {
		if !signedIn {
			sendError(w, problem(401, "unauthorized", "Sign in to change files."))
			return
		}
		if !hmac.Equal([]byte(r.Header.Get(siteCSRFHeader)), []byte(h.siteCSRF(token))) {
			sendError(w, problem(403, "forbidden", "The page is out of date. Reload it and try again."))
			return
		}
	}

	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/site/")
	if rest == "session" {
		if !read {
			w.Header().Set("Allow", "GET")
			sendError(w, problem(405, "invalid_request", "Use GET."))
			return
		}
		if r.URL.RawQuery != "" {
			sendError(w, invalid("Unknown query parameter."))
			return
		}
		value, err := h.siteSession(r.Context(), user, signedIn, token)
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, 200, value)
		return
	}
	parts := strings.Split(rest, "/")
	if parts[0] != "spaces" || len(parts) < 3 || len(parts) > 4 {
		sendError(w, missing())
		return
	}
	space, resource := parts[1], parts[2]
	if len(parts) == 4 {
		space, resource = parts[1]+"/"+parts[2], parts[3]
	}
	allow, ok := siteResources[resource]
	if !ok || !spaceRefPattern.MatchString(space) {
		sendError(w, missing())
		return
	}
	if !strings.Contains(allow, r.Method) {
		w.Header().Set("Allow", allow)
		sendError(w, problem(405, "invalid_request", "Unsupported method."))
		return
	}

	ctx := context.WithValue(r.Context(), siteRequestKey{}, true)
	if signedIn {
		// The handlers that keep records per person (acknowledgements,
		// submissions, reviews) read the user from the identity. There is
		// no grant: nothing on these routes looks one up.
		id := oauthIdentity{UserID: user.ID, ClientID: siteAppName, Scope: scopeReadWrite}
		ctx = context.WithValue(ctx, mcpIdentityKey{}, mcpIdentity{client: client, oauth: &id})
		ctx = context.WithValue(ctx, personWriteKey{}, true)
		author := changeAuthor{Name: accountName(user), App: siteAppName, Kind: "person"}
		ctx = withAuthor(ctx, func() changeAuthor { return author })
	}
	r = r.WithContext(ctx)
	h.serveRESTOperation(w, r, resource, "", func(write bool) (*repository, string, error) {
		return h.service.siteSpaceAccess(r.Context(), user, signedIn, space, client, write)
	})
}

// siteSession answers GET /api/v1/site/session.
func (h *httpAdapter) siteSession(ctx context.Context, user userAccount, signedIn bool, token string) (siteSession, error) {
	out := siteSession{Spaces: []siteSpace{}}
	// Anyone may change the public space, but the site asks them to sign in first.
	public := "read_only"
	if signedIn {
		public = "read_write"
	}
	names := make([]string, 0, len(h.service.config.Spaces))
	for name, sc := range h.service.config.Spaces {
		if sc.Visibility == "public" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out.Spaces = append(out.Spaces, siteSpace{Space: name, Name: name, Access: public})
	}
	if !signedIn {
		return out, nil
	}
	out.SignedIn, out.Account, out.CSRF = true, accountName(user), h.siteCSRF(token)
	spaces, err := h.service.ownedDB.accessibleSpaces(ctx, user)
	if err != nil {
		return siteSession{}, problem(503, "unavailable", "Your spaces are temporarily unavailable.")
	}
	for _, s := range spaces {
		if s.Owner == "" {
			continue
		}
		access := "read_only"
		if s.CanWrite {
			access = "read_write"
		}
		out.Spaces = append(out.Spaces, siteSpace{Space: s.Owner + "/" + s.Slug, Name: s.Name, Owner: s.Owner, Access: access})
	}
	return out, nil
}

// siteSpaceAccess is the access check for the site API: the public space by
// its own rules (reading only when signed out), an owned space by ownership,
// active membership and the member's permission, or by being readable on the
// web. Like agentSpaceAccess it runs before quotas and Git, and returns the
// canonical owner/slug.
func (s *Service) siteSpaceAccess(ctx context.Context, user userAccount, signedIn bool, space, client string, write bool) (*repository, string, error) {
	notFound := problem(404, "not_found", "Space not found.")
	if !signedIn {
		// Signed out, a private space and a missing one look the same.
		notFound = problem(401, "unauthorized", "Sign in to read this space: it is not shared on the web.")
	}
	if write && !signedIn {
		return nil, "", problem(401, "unauthorized", "Sign in to change files.")
	}
	if sc, configured := s.config.Spaces[space]; configured {
		if sc.Visibility != "public" {
			return nil, "", notFound
		}
		if err := s.Access(space, "", client, write); err != nil {
			return nil, "", err
		}
		return s.repos[space], space, nil
	}
	owner, slug, full := strings.Cut(space, "/")
	if !full || len(owner) > 32 || len(slug) > 48 || !usernamePattern.MatchString(owner) || !usernamePattern.MatchString(slug) {
		return nil, "", notFound
	}
	viewer := ""
	if signedIn {
		viewer = user.ID
	}
	const base = `SELECT s.space_id, COALESCE(u.username, ''), s.slug, s.owner_user_id, s.visibility, m.status, m.agent_permission FROM metatrash_spaces s JOIN metatrash_users u ON u.user_id = s.owner_user_id LEFT JOIN metatrash_memberships m ON m.space_id = s.space_id AND m.user_id = ? WHERE s.provisioning_state = 'ready' AND `
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var spaceID, username, currentSlug, ownerID, visibility string
	var status, permission sql.NullString
	scan := func(row *sql.Row) error {
		return row.Scan(&spaceID, &username, &currentSlug, &ownerID, &visibility, &status, &permission)
	}
	err := scan(s.ownedDB.db.QueryRowContext(lookup, base+`u.username = ? AND s.slug = ?`, viewer, owner, slug))
	if err == sql.ErrNoRows {
		// The address the space had before a transfer still names it.
		var aliasID string
		var found bool
		if aliasID, found, err = s.ownedDB.spaceByAlias(lookup, owner, slug); err == nil && !found {
			err = sql.ErrNoRows
		}
		if found {
			err = scan(s.ownedDB.db.QueryRowContext(lookup, base+`s.space_id = ?`, viewer, aliasID))
		}
	}
	if err == sql.ErrNoRows {
		return nil, "", notFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("site authorization unavailable")
	}
	access := ""
	switch {
	case signedIn && ownerID == user.ID:
		access = "read_write"
	case status.Valid && status.String == "suspended":
		return nil, "", problem(403, "forbidden", "Your access to this space is suspended by its owner.")
	case status.Valid && status.String == "active":
		access = "read_only"
		if permission.String == "read_write" {
			access = "read_write"
		}
	case visibility == spaceWeb:
		access = "read_only"
	default:
		return nil, "", notFound
	}
	repo := s.ownedRepository(spaceID)
	if repo == nil {
		return nil, "", notFound
	}
	if write && access != "read_write" {
		return nil, "", problem(403, "forbidden", "You can read this space but not change it.")
	}
	if err := s.reserveOwnedOperation(spaceID, client, write); err != nil {
		return nil, "", err
	}
	name := spaceID
	if username != "" {
		name = username + "/" + currentSlug
	}
	return repo, name, nil
}
