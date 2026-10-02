package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// connectedApp is one OAuth connection shown on Your account.
type connectedApp struct {
	ID, Name, Host, Kind, Created, LastUsed string
	Spaces                                  []connectedAppSpace
}

type connectedAppSpace struct {
	Name, Access string
}

func formatUnix(value int64) string {
	return time.Unix(value, 0).UTC().Format("2006-01-02 15:04 UTC")
}

// connectedApps lists the account's live OAuth connections with the access
// each space currently has (consent narrowed by membership and permission).
func (h *httpAdapter) connectedApps(ctx context.Context, user userAccount) ([]connectedApp, error) {
	db := h.service.ownedDB
	if db == nil {
		return nil, fmt.Errorf("connections unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := time.Now().Unix()
	rows, err := db.db.QueryContext(ctx, "SELECT grant_id, client_id, client_name, resource, created_at, last_used_at FROM metatrash_oauth_grants WHERE user_id = ? AND expires_at > ? ORDER BY created_at, grant_id LIMIT 50", user.ID, now)
	if err != nil {
		return nil, fmt.Errorf("cannot list connections")
	}
	apps := []connectedApp{}
	index := map[string]int{}
	for rows.Next() {
		var app connectedApp
		var clientID, resource string
		var created, used int64
		if err := rows.Scan(&app.ID, &clientID, &app.Name, &resource, &created, &used); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cannot list connections")
		}
		if u, err := url.Parse(clientID); err == nil {
			app.Host = u.Hostname()
		}
		app.Kind = "MCP"
		if strings.HasSuffix(resource, "/api/v1/account") {
			app.Kind = "REST"
		}
		app.Created, app.LastUsed = formatUnix(created), formatUnix(used)
		index[app.ID] = len(apps)
		apps = append(apps, app)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, fmt.Errorf("cannot list connections")
	}
	if len(apps) == 0 {
		return apps, nil
	}
	rows, err = db.db.QueryContext(ctx, `SELECT g.grant_id, s.name, gs.permission, s.owner_user_id, m.status, m.agent_permission FROM metatrash_oauth_grants g JOIN metatrash_oauth_grant_spaces gs ON gs.grant_id = g.grant_id JOIN metatrash_spaces s ON s.space_id = gs.space_id LEFT JOIN metatrash_memberships m ON m.space_id = gs.space_id AND m.user_id = g.user_id WHERE g.user_id = ? AND g.expires_at > ? ORDER BY s.name, s.space_id LIMIT 2000`, user.ID, now)
	if err != nil {
		return nil, fmt.Errorf("cannot list connection spaces")
	}
	defer rows.Close()
	for rows.Next() {
		var grantID, name, consent, ownerID string
		var status, permission sql.NullString
		if err := rows.Scan(&grantID, &name, &consent, &ownerID, &status, &permission); err != nil {
			return nil, fmt.Errorf("cannot list connection spaces")
		}
		i, ok := index[grantID]
		if !ok {
			continue
		}
		access := "suspended by owner"
		if effective := effectiveAccess(oauthIdentity{UserID: user.ID, Scope: scopeReadWrite}, consent, ownerID, status, permission); effective == "read_write" {
			access = "read and write"
		} else if effective == "read_only" {
			access = "read only"
		}
		apps[i].Spaces = append(apps[i].Spaces, connectedAppSpace{Name: name, Access: access})
	}
	return apps, rows.Err()
}

// revokeConnection deletes one of the account's connections with its consent
// and tokens (cascade). Called only after the account form checks.
func (db *accountDatabase) revokeConnection(ctx context.Context, userID, grantID string) error {
	if !idPattern.MatchString(userID) || !idPattern.MatchString(grantID) {
		return missing()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_oauth_grants WHERE grant_id = ? AND user_id = ?", grantID, userID); err != nil {
		return fmt.Errorf("cannot revoke connection")
	}
	return nil
}

// setMemberAgentPermission lets a space owner choose whether a member's
// connected apps may write. Lowering it applies to the member's next operation.
func (db *accountDatabase) setMemberAgentPermission(ctx context.Context, ownerID, spaceID, userID, permission string) error {
	if !idPattern.MatchString(userID) || (permission != "read_only" && permission != "read_write") {
		return invalid("Invalid app permission.")
	}
	return db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		if userID == space.OwnerID {
			return invalid("The owner always has full access.")
		}
		result, err := tx.ExecContext(ctx, "UPDATE metatrash_memberships SET agent_permission = ?, updated_at = ? WHERE space_id = ? AND user_id = ?", permission, time.Now().UTC().Unix(), spaceID, userID)
		if err != nil {
			return fmt.Errorf("cannot change app permission")
		}
		if n, err := result.RowsAffected(); err != nil {
			return fmt.Errorf("cannot change app permission")
		} else if n == 0 {
			// Unchanged values report zero rows in MariaDB; confirm the member exists.
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", spaceID, userID).Scan(&exists); err != nil {
				return missing()
			}
		}
		return nil
	})
}
