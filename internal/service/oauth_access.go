package service

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// spaceRefPattern is the shape of a space argument on the OAuth endpoints:
// a configured name ("public"), a 32-hex ID, or owner/slug. Bare slugs pass
// this check only so that agentSpaceAccess can explain the owner/slug form.
var spaceRefPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}(/[a-z0-9][a-z0-9-]{0,47})?$`)

// connectedSpace is one space an OAuth connection can currently use. Space is
// the owner/slug name agents pass back ("public" for the public space); ID is
// the immutable space ID, which is also accepted.
type connectedSpace struct {
	Space  string `json:"space"`
	ID     string `json:"id,omitempty"`
	Name   string `json:"name"`
	Owner  string `json:"owner"`
	Access string `json:"access"`
}

type connectedSpaces struct {
	Spaces []connectedSpace `json:"spaces"`
}

// effectiveAccess combines consent, current ownership or membership, the
// owner's member permission and the token scope. Empty means no access.
func effectiveAccess(id oauthIdentity, consent, ownerID string, status, memberPermission sql.NullString) string {
	access := consent
	if ownerID != id.UserID {
		if !status.Valid || status.String != "active" {
			return ""
		}
		if memberPermission.String != "read_write" {
			access = "read_only"
		}
	}
	if !id.canWrite() {
		access = "read_only"
	}
	if access != "read_only" && access != "read_write" {
		return ""
	}
	return access
}

// agentSpaceAccess is the single access check for OAuth agent operations. It
// runs once per operation, before quotas and Git: the public space follows its
// anonymous rules; owned spaces need consent ∩ current ownership or active
// membership ∩ the member permission ∩ token scope. Configured key-protected
// spaces are never reachable with OAuth (D3). Space references select a
// repository; they never grant authority.
//
// An owned space is named owner/slug (as in /spaces/{owner}/{slug}/), by the
// owner/slug it had before a transfer, or by its ID. Only spaces connected to
// this grant are matched, so unconnected spaces stay indistinguishable from
// missing ones. It returns the canonical name (owner/slug, or the configured
// name) used in results and cursors.
func (s *Service) agentSpaceAccess(ctx context.Context, id oauthIdentity, space, client string, write bool) (*repository, string, error) {
	notConnected := problem(404, "not_found", "Space not found or not connected to this app. Call spaces to list the spaces this connection can use.")
	if sc, configured := s.config.Spaces[space]; configured {
		if sc.Visibility != "public" {
			return nil, "", notConnected
		}
		if err := s.Access(space, "", client, write); err != nil {
			return nil, "", err
		}
		return s.repos[space], space, nil
	}
	if s.ownedDB == nil || !spaceRefPattern.MatchString(space) {
		return nil, "", notConnected
	}
	const base = `SELECT s.space_id, COALESCE(u.username, ''), s.slug, gs.permission, s.owner_user_id, m.status, m.agent_permission FROM metatrash_oauth_grant_spaces gs JOIN metatrash_spaces s ON s.space_id = gs.space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id LEFT JOIN metatrash_memberships m ON m.space_id = gs.space_id AND m.user_id = ? WHERE gs.grant_id = ? AND s.provisioning_state = 'ready'`
	var query string
	args := []any{id.UserID, id.GrantID}
	if owner, slug, full := strings.Cut(space, "/"); full {
		if len(owner) > 32 || !usernamePattern.MatchString(owner) || !usernamePattern.MatchString(slug) {
			return nil, "", notConnected
		}
		query = base + ` AND u.username = ? AND s.slug = ?`
		args = append(args, owner, slug)
	} else if idPattern.MatchString(space) {
		query = base + ` AND gs.space_id = ?`
		args = append(args, space)
	} else {
		return nil, "", problem(404, "not_found", fmt.Sprintf("Space %q not found. Name private spaces as owner/slug, for example owner/%s. Call spaces to list the spaces this connection can use.", space, space))
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var spaceID, username, slug, consent, ownerID string
	var status, permission sql.NullString
	err := s.ownedDB.db.QueryRowContext(lookup, query, args...).Scan(&spaceID, &username, &slug, &consent, &ownerID, &status, &permission)
	if owner, oldSlug, full := strings.Cut(space, "/"); err == sql.ErrNoRows && full {
		// The space's address before a transfer still names it. Results use
		// the current owner/slug.
		var aliasID string
		var found bool
		if aliasID, found, err = s.ownedDB.spaceByAlias(lookup, owner, oldSlug); err == nil && !found {
			err = sql.ErrNoRows
		}
		if found {
			err = s.ownedDB.db.QueryRowContext(lookup, base+` AND gs.space_id = ?`, id.UserID, id.GrantID, aliasID).Scan(&spaceID, &username, &slug, &consent, &ownerID, &status, &permission)
		}
	}
	if err == sql.ErrNoRows {
		return nil, "", notConnected
	}
	if err != nil {
		return nil, "", fmt.Errorf("agent authorization unavailable")
	}
	repo := s.ownedRepository(spaceID)
	if repo == nil {
		return nil, "", notConnected
	}
	if ownerID != id.UserID && status.Valid && status.String == "suspended" {
		return nil, "", problem(403, "forbidden", "Your access to this space is suspended by its owner.")
	}
	access := effectiveAccess(id, consent, ownerID, status, permission)
	if access == "" {
		return nil, "", notConnected
	}
	if write && access != "read_write" {
		return nil, "", problem(403, "insufficient_scope", "This app has read-only access to this space.")
	}
	if err := s.reserveOwnedOperation(spaceID, client, write); err != nil {
		return nil, "", err
	}
	name := spaceID
	if username != "" {
		name = username + "/" + slug
	}
	return repo, name, nil
}

// listConnectedSpaces returns the public space and every consented space the
// connection can currently use, with its effective access.
func (s *Service) listConnectedSpaces(ctx context.Context, id oauthIdentity) (connectedSpaces, error) {
	result := connectedSpaces{Spaces: []connectedSpace{{Space: "public", Name: "Public space", Owner: "", Access: "read_write"}}}
	if s.ownedDB == nil {
		return result, fmt.Errorf("agent authorization unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := s.ownedDB.db.QueryContext(ctx, `SELECT s.space_id, s.slug, s.name, COALESCE(u.username, ''), gs.permission, s.owner_user_id, m.status, m.agent_permission FROM metatrash_oauth_grant_spaces gs JOIN metatrash_spaces s ON s.space_id = gs.space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id LEFT JOIN metatrash_memberships m ON m.space_id = gs.space_id AND m.user_id = ? WHERE gs.grant_id = ? AND s.provisioning_state = 'ready' ORDER BY s.name, s.space_id LIMIT 200`, id.UserID, id.GrantID)
	if err != nil {
		return result, fmt.Errorf("cannot list connected spaces")
	}
	defer rows.Close()
	for rows.Next() {
		var item connectedSpace
		var slug, consent, ownerID string
		var status, permission sql.NullString
		if err := rows.Scan(&item.ID, &slug, &item.Name, &item.Owner, &consent, &ownerID, &status, &permission); err != nil {
			return result, fmt.Errorf("cannot list connected spaces")
		}
		if item.Access = effectiveAccess(id, consent, ownerID, status, permission); item.Access == "" || s.ownedRepository(item.ID) == nil {
			continue
		}
		item.Space = item.ID
		if item.Owner != "" {
			item.Space = item.Owner + "/" + slug
		}
		result.Spaces = append(result.Spaces, item)
	}
	if rows.Err() != nil {
		return result, fmt.Errorf("cannot list connected spaces")
	}
	return result, nil
}

// pushAuthorFor returns the public username of the connection's user and the
// app name the user approved, for signing pushes.
func (s *Service) pushAuthorFor(ctx context.Context, id oauthIdentity) (pushAuthor, error) {
	if s.ownedDB == nil {
		return pushAuthor{}, problem(403, "forbidden", "Push needs a signed-in connection.")
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var a pushAuthor
	err := s.ownedDB.db.QueryRowContext(lookup, `SELECT COALESCE(u.username, ''), g.client_name FROM metatrash_oauth_grants g JOIN metatrash_users u ON u.user_id = g.user_id WHERE g.grant_id = ? AND g.user_id = ?`, id.GrantID, id.UserID).Scan(&a.Username, &a.Agent)
	if err != nil {
		return pushAuthor{}, fmt.Errorf("push author unavailable")
	}
	// A GitHub account the user linked (through Connect GitHub) names them on
	// GitHub too.
	links, err := s.ownedDB.githubInstallations(ctx, id.UserID)
	if err != nil {
		return pushAuthor{}, err
	}
	for _, inst := range links {
		if inst.GitHubUserID > 0 && githubLoginPattern.MatchString(inst.GitHubLogin) {
			a.GitHubID, a.GitHubLogin = inst.GitHubUserID, inst.GitHubLogin
			break
		}
	}
	return a, nil
}
