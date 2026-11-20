package service

import (
	"context"
	"database/sql"
	"log"
	"sync"
	"time"

	"metatrash.com/metatrash/extension"
)

// Space levels (0.100.0). An extension may decide what an account may do in
// the spaces of some owners (extension.Hooks.SpaceLevels): Company
// organisations give their members a level per space from Access page rows.
// For those owners' spaces a membership can only lower the extension's
// level or suspend it; without a level the space is hidden. Owning a space
// or acting for its owner still decides first, SpaceGate is still asked,
// and web visibility still lets anyone read. Answers are cached for
// spaceLevelsTTL per account, so a change reaches open sessions and agents
// within that time.

const spaceLevelsTTL = 10 * time.Second

const maxSpaceLevelsCache = 10000

// spaceLevels is the combined answer of the extensions for one account.
type spaceLevels struct {
	owners map[string]bool
	spaces map[string]string
}

// managed reports whether the extensions decide ownerID's spaces.
func (l spaceLevels) managed(ownerID string) bool { return l.owners[ownerID] }

type spaceLevelsEntry struct {
	levels  spaceLevels
	expires time.Time
}

// spaceLevelsFunc combines the extensions' SpaceLevels answers, cached per
// account. Two extensions deciding the same owner's space give the lower
// level. An error is logged and not cached.
func (s *Service) spaceLevelsFunc() func(context.Context, string) (spaceLevels, error) {
	var hooks []func(context.Context, string) (extension.SpaceLevels, error)
	for _, h := range s.extensions {
		if h.SpaceLevels != nil {
			hooks = append(hooks, h.SpaceLevels)
		}
	}
	if len(hooks) == 0 {
		return nil
	}
	var mu sync.Mutex
	entries := map[string]spaceLevelsEntry{}
	return func(ctx context.Context, userID string) (spaceLevels, error) {
		if !idPattern.MatchString(userID) {
			return spaceLevels{}, nil
		}
		now := time.Now()
		mu.Lock()
		if e, ok := entries[userID]; ok && now.Before(e.expires) {
			mu.Unlock()
			return e.levels, nil
		}
		mu.Unlock()
		levels := spaceLevels{owners: map[string]bool{}, spaces: map[string]string{}}
		seen := map[string]bool{}
		for _, hook := range hooks {
			answer, err := hook(ctx, userID)
			if err != nil {
				log.Printf("space levels: %s: %v", userID, err)
				return spaceLevels{}, errGateUnavailable
			}
			for _, id := range answer.Owners {
				if idPattern.MatchString(id) && id != userID {
					levels.owners[id] = true
				}
			}
			for spaceID, level := range answer.Spaces {
				if !idPattern.MatchString(spaceID) || level != "read_only" && level != "read_write" {
					continue
				}
				if seen[spaceID] {
					levels.spaces[spaceID] = lowerLevel(levels.spaces[spaceID], level)
				} else {
					seen[spaceID] = true
					levels.spaces[spaceID] = level
				}
			}
		}
		mu.Lock()
		if len(entries) >= maxSpaceLevelsCache {
			for k, e := range entries {
				if !now.Before(e.expires) {
					delete(entries, k)
				}
			}
			if len(entries) >= maxSpaceLevelsCache {
				entries = map[string]spaceLevelsEntry{}
			}
		}
		entries[userID] = spaceLevelsEntry{levels: levels, expires: now.Add(spaceLevelsTTL)}
		mu.Unlock()
		return levels, nil
	}
}

// lowerLevel is the lower of two levels ("" is none).
func lowerLevel(a, b string) string {
	if a == "" || b == "" {
		return ""
	}
	if a == "read_only" || b == "read_only" {
		return "read_only"
	}
	return "read_write"
}

// levelsFor returns the extensions' answer for userID: empty when no
// extension decides any space, or for no one signed in.
func (db *accountDatabase) levelsFor(ctx context.Context, userID string) (spaceLevels, error) {
	if db == nil || db.levels == nil || userID == "" {
		return spaceLevels{}, nil
	}
	return db.levels(ctx, userID)
}

// memberAccess turns a membership row (status and permission, NULL when
// there is none) into what userID gets in a space owned by ownerID, after
// the extensions' levels (0.100.0). For an owner the extensions don't
// decide, it is returned unchanged. For one they do, a suspended membership
// stays suspended, a level is lowered by an active membership's permission,
// and without a level there is no access (NULL). Callers check ownership and
// acting for the owner first; an error refuses.
func (db *accountDatabase) memberAccess(ctx context.Context, userID, ownerID, spaceID string, status, permission sql.NullString) (sql.NullString, sql.NullString, error) {
	if db == nil || db.levels == nil || userID == "" || userID == ownerID {
		return status, permission, nil
	}
	levels, err := db.levelsFor(ctx, userID)
	if err != nil {
		return sql.NullString{}, sql.NullString{}, err
	}
	if !levels.managed(ownerID) {
		return status, permission, nil
	}
	level := levels.spaces[spaceID]
	switch {
	case level == "":
		return sql.NullString{}, sql.NullString{}, nil
	case status.Valid && status.String == "suspended":
		return status, permission, nil
	case status.Valid && status.String == "active":
		member := "read_only"
		if permission.String == "read_write" {
			member = "read_write"
		}
		return status, sql.NullString{String: lowerLevel(level, member), Valid: true}, nil
	}
	return sql.NullString{String: "active", Valid: true}, sql.NullString{String: level, Valid: true}, nil
}

// levelSpaces lists the ready spaces the extensions give userID a level in
// and that userID has no membership row for, for the spaces lists and the
// consent page. skip names spaces already listed.
func (db *accountDatabase) levelSpaces(ctx context.Context, userID string, skip map[string]bool) ([]accessibleSpace, error) {
	levels, err := db.levelsFor(ctx, userID)
	if err != nil || len(levels.spaces) == 0 {
		return nil, err
	}
	ids := make([]any, 0, len(levels.spaces)+1)
	ids = append(ids, userID)
	for id := range levels.spaces {
		if !skip[id] && len(ids) <= 400 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 1 {
		return nil, nil
	}
	query := `SELECT s.space_id, s.name, s.slug, COALESCE(u.username, ''), s.visibility, s.owner_user_id FROM metatrash_spaces s JOIN metatrash_users u ON u.user_id = s.owner_user_id
WHERE s.provisioning_state = 'ready' AND NOT EXISTS (SELECT 1 FROM metatrash_memberships m WHERE m.space_id = s.space_id AND m.user_id = ?) AND s.space_id IN (?` + repeatPlaceholders(len(ids)-2) + `) ORDER BY u.username, s.name, s.space_id LIMIT 400`
	rows, err := db.db.QueryContext(ctx, query, ids...)
	if err != nil {
		return nil, errGateUnavailable
	}
	defer rows.Close()
	out := []accessibleSpace{}
	for rows.Next() {
		var item accessibleSpace
		var visibility string
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.Owner, &visibility, &item.OwnerID); err != nil {
			return nil, errGateUnavailable
		}
		if !levels.managed(item.OwnerID) || item.OwnerID == userID {
			continue
		}
		item.CanWrite = levels.spaces[item.ID] == "read_write"
		item.Web = visibility == spaceWeb
		out = append(out, item)
	}
	if rows.Err() != nil {
		return nil, errGateUnavailable
	}
	return out, nil
}

func repeatPlaceholders(n int) string {
	b := make([]byte, 0, 3*n)
	for i := 0; i < n; i++ {
		b = append(b, ", ?"...)
	}
	return string(b)
}

// levelOf is userID's level in a space of ownerID after the extensions'
// levels, reading the membership itself: "read_only", "read_write" or ""
// for none (suspended, hidden). managed is false when no extension decides
// ownerID's spaces; then level is "" and Core's own rules apply. Callers
// check ownership and acting for the owner first.
func (db *accountDatabase) levelOf(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, userID, ownerID, spaceID string) (level string, managed bool, err error) {
	levels, err := db.levelsFor(ctx, userID)
	if err != nil {
		return "", true, err
	}
	if userID == ownerID || !levels.managed(ownerID) {
		return "", false, nil
	}
	var status, permission sql.NullString
	err = q.QueryRowContext(ctx, "SELECT status, agent_permission FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", spaceID, userID).Scan(&status, &permission)
	if err != nil && err != sql.ErrNoRows {
		return "", true, errGateUnavailable
	}
	status, permission, err = db.memberAccess(ctx, userID, ownerID, spaceID, status, permission)
	if err != nil {
		return "", true, err
	}
	if !status.Valid || status.String != "active" {
		return "", true, nil
	}
	return permission.String, true, nil
}
