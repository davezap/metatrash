package service

import (
	"context"
	"log"
	"sync"
	"time"
)

// Acting for an owner (0.95.23). An extension may let an account act as the
// owner of other owner accounts' spaces (extension.Hooks.ActsFor): Company
// organisations are owner accounts nobody signs in as, whose admins act for
// them. Wherever Core asks "is this the space's owner?", it asks actsFor.
// Answers are cached for actingOwnersTTL, so a change reaches open sessions
// and agents within that time.

const actingOwnersTTL = 10 * time.Second

const maxActingOwnersCache = 10000

type actingOwnersEntry struct {
	owners  []string
	expires time.Time
}

type actingOwnersCache struct {
	mu      sync.Mutex
	entries map[string]actingOwnersEntry
}

// actingOwnersFunc combines the extensions' ActsFor answers, cached per
// account. Errors are logged and grant nothing.
func (s *Service) actingOwnersFunc() func(context.Context, string) []string {
	var hooks []func(context.Context, string) ([]string, error)
	for _, h := range s.extensions {
		if h.ActsFor != nil {
			hooks = append(hooks, h.ActsFor)
		}
	}
	if len(hooks) == 0 {
		return nil
	}
	cache := &actingOwnersCache{entries: map[string]actingOwnersEntry{}}
	return func(ctx context.Context, userID string) []string {
		if !idPattern.MatchString(userID) {
			return nil
		}
		now := time.Now()
		cache.mu.Lock()
		if e, ok := cache.entries[userID]; ok && now.Before(e.expires) {
			cache.mu.Unlock()
			return e.owners
		}
		cache.mu.Unlock()
		owners := []string{}
		for _, hook := range hooks {
			ids, err := hook(ctx, userID)
			if err != nil {
				log.Printf("acting owners: %s: %v", userID, err)
				return nil // not cached: try again next time
			}
			for _, id := range ids {
				if idPattern.MatchString(id) && id != userID {
					owners = append(owners, id)
				}
			}
		}
		cache.mu.Lock()
		if len(cache.entries) >= maxActingOwnersCache {
			for k, e := range cache.entries {
				if !now.Before(e.expires) {
					delete(cache.entries, k)
				}
			}
			if len(cache.entries) >= maxActingOwnersCache {
				cache.entries = map[string]actingOwnersEntry{}
			}
		}
		cache.entries[userID] = actingOwnersEntry{owners: owners, expires: now.Add(actingOwnersTTL)}
		cache.mu.Unlock()
		return owners
	}
}

// actingOwners lists the owner accounts userID may act for, not counting
// itself.
func (db *accountDatabase) actingOwners(ctx context.Context, userID string) []string {
	if db == nil || db.actingFor == nil || userID == "" {
		return nil
	}
	return db.actingFor(ctx, userID)
}

// actsFor reports whether userID is ownerID or may act for it as owner.
func (db *accountDatabase) actsFor(ctx context.Context, userID, ownerID string) bool {
	if userID == "" || ownerID == "" {
		return false
	}
	if userID == ownerID {
		return true
	}
	for _, id := range db.actingOwners(ctx, userID) {
		if id == ownerID {
			return true
		}
	}
	return false
}

// spaceOwnerFor returns the account to act as on a space's owner-only
// pages and changes: the space's owner when user owns it or may act for its
// owner, otherwise user itself, whose owner checks then fail as before.
func (s *Service) spaceOwnerFor(ctx context.Context, user userAccount, spaceID string) (userAccount, error) {
	db := s.ownedDB
	if db == nil || db.actingFor == nil || !idPattern.MatchString(spaceID) {
		return user, nil
	}
	var ownerID string
	err := db.db.QueryRowContext(ctx, "SELECT owner_user_id FROM metatrash_spaces WHERE space_id = ?", spaceID).Scan(&ownerID)
	if err != nil || ownerID == user.ID || !db.actsFor(ctx, user.ID, ownerID) {
		return user, nil
	}
	// Managing the space needs the same as opening it (SpaceGate, 0.95.24),
	// or someone shut out could make it readable on the web.
	if err := db.gateFor(ctx, user.ID, ownerID); err != nil {
		return user, err
	}
	owner, ok, err := s.accounts.store.ByID(ctx, ownerID)
	if err != nil {
		return user, err
	}
	if !ok {
		return user, nil
	}
	return owner, nil
}
