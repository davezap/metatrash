package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// connectedSpace is one space an OAuth connection can currently use.
type connectedSpace struct {
	Space  string `json:"space"`
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
// spaces are never reachable with OAuth (D3). Space IDs select a repository;
// they never grant authority.
func (s *Service) agentSpaceAccess(ctx context.Context, id oauthIdentity, space, client string, write bool) (*repository, error) {
	notConnected := problem(404, "not_found", "Space not found or not connected to this app.")
	if sc, configured := s.config.Spaces[space]; configured {
		if sc.Visibility != "public" {
			return nil, notConnected
		}
		if err := s.Access(space, "", client, write); err != nil {
			return nil, err
		}
		return s.repos[space], nil
	}
	repo := s.ownedRepository(space)
	if repo == nil || s.ownedDB == nil || !idPattern.MatchString(space) {
		return nil, notConnected
	}
	lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var consent, ownerID string
	var status, permission sql.NullString
	err := s.ownedDB.db.QueryRowContext(lookup, `SELECT gs.permission, s.owner_user_id, m.status, m.agent_permission FROM metatrash_oauth_grant_spaces gs JOIN metatrash_spaces s ON s.space_id = gs.space_id LEFT JOIN metatrash_memberships m ON m.space_id = gs.space_id AND m.user_id = ? WHERE gs.grant_id = ? AND gs.space_id = ? AND s.provisioning_state = 'ready'`, id.UserID, id.GrantID, space).Scan(&consent, &ownerID, &status, &permission)
	if err == sql.ErrNoRows {
		return nil, notConnected
	}
	if err != nil {
		return nil, fmt.Errorf("agent authorization unavailable")
	}
	if ownerID != id.UserID && status.Valid && status.String == "suspended" {
		return nil, problem(403, "forbidden", "Your access to this space is suspended by its owner.")
	}
	access := effectiveAccess(id, consent, ownerID, status, permission)
	if access == "" {
		return nil, notConnected
	}
	if write && access != "read_write" {
		return nil, problem(403, "insufficient_scope", "This app has read-only access to this space.")
	}
	if err := s.reserveOwnedOperation(space, client, write); err != nil {
		return nil, err
	}
	return repo, nil
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
	rows, err := s.ownedDB.db.QueryContext(ctx, `SELECT s.space_id, s.name, COALESCE(u.username, ''), gs.permission, s.owner_user_id, m.status, m.agent_permission FROM metatrash_oauth_grant_spaces gs JOIN metatrash_spaces s ON s.space_id = gs.space_id JOIN metatrash_users u ON u.user_id = s.owner_user_id LEFT JOIN metatrash_memberships m ON m.space_id = gs.space_id AND m.user_id = ? WHERE gs.grant_id = ? AND s.provisioning_state = 'ready' ORDER BY s.name, s.space_id LIMIT 200`, id.UserID, id.GrantID)
	if err != nil {
		return result, fmt.Errorf("cannot list connected spaces")
	}
	defer rows.Close()
	for rows.Next() {
		var item connectedSpace
		var consent, ownerID string
		var status, permission sql.NullString
		if err := rows.Scan(&item.Space, &item.Name, &item.Owner, &consent, &ownerID, &status, &permission); err != nil {
			return result, fmt.Errorf("cannot list connected spaces")
		}
		if item.Access = effectiveAccess(id, consent, ownerID, status, permission); item.Access == "" || s.ownedRepository(item.Space) == nil {
			continue
		}
		result.Spaces = append(result.Spaces, item)
	}
	if rows.Err() != nil {
		return result, fmt.Errorf("cannot list connected spaces")
	}
	return result, nil
}
