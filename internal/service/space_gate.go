package service

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// Space gates (0.95.24). An extension may refuse an account the spaces of
// an owner it doesn't own itself (extension.Hooks.SpaceGate): Company
// organisations pinned to Microsoft tenants admit only people with a recent
// Microsoft sign-in from them. Core asks at the three doors to a space it
// doesn't own: the site API (siteSpaceAccess), the space's pages and files
// (serveOwnedSpace) and apps and Sync (agentSpaceAccess, and the spaces
// list). Being an owner, acting for one, membership and web visibility
// still decide everything else: a gate only takes access away, and a space
// readable on the web stays readable by everyone. Answers are cached for
// spaceGateTTL per account and owner.

const spaceGateTTL = 10 * time.Second

const maxSpaceGateCache = 10000

var errGateUnavailable = problem(503, "unavailable", "This space can't be checked just now. Try again shortly.")

type spaceGateEntry struct {
	err     error
	expires time.Time
}

// spaceGateFunc combines the extensions' SpaceGate answers. The first
// refusal wins. A refusal that isn't an *Error is logged and refuses as
// unavailable, uncached.
func (s *Service) spaceGateFunc() func(context.Context, string, string) error {
	var hooks []func(context.Context, string, string) error
	for _, h := range s.extensions {
		if h.SpaceGate != nil {
			hooks = append(hooks, h.SpaceGate)
		}
	}
	if len(hooks) == 0 {
		return nil
	}
	var mu sync.Mutex
	entries := map[string]spaceGateEntry{}
	return func(ctx context.Context, userID, ownerID string) error {
		if userID == ownerID {
			return nil
		}
		key := userID + "/" + ownerID
		now := time.Now()
		mu.Lock()
		if e, ok := entries[key]; ok && now.Before(e.expires) {
			mu.Unlock()
			return e.err
		}
		mu.Unlock()
		var refusal error
		for _, hook := range hooks {
			err := hook(ctx, userID, ownerID)
			if err == nil {
				continue
			}
			var p *Error
			if !errors.As(err, &p) {
				log.Printf("space gate: %s for %s: %v", userID, ownerID, err)
				return errGateUnavailable
			}
			refusal = p
			break
		}
		mu.Lock()
		if len(entries) >= maxSpaceGateCache {
			for k, e := range entries {
				if !now.Before(e.expires) {
					delete(entries, k)
				}
			}
			if len(entries) >= maxSpaceGateCache {
				entries = map[string]spaceGateEntry{}
			}
		}
		entries[key] = spaceGateEntry{err: refusal, expires: now.Add(spaceGateTTL)}
		mu.Unlock()
		return refusal
	}
}

// gateFor reports why userID may not use ownerID's spaces now, or nil.
// Signed out (userID "") nothing is asked: web visibility decides.
func (db *accountDatabase) gateFor(ctx context.Context, userID, ownerID string) error {
	if db == nil || db.gate == nil || userID == "" || ownerID == "" || userID == ownerID {
		return nil
	}
	return db.gate(ctx, userID, ownerID)
}
