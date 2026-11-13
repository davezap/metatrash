package service

import (
	"context"
	"errors"
	"log"

	"metatrash.com/metatrash/extension"
)

// Billing gates (0.95.26). Extensions may refuse three things Core does on
// its own (extension.Hooks.WriteGate, SpaceCreateGate and AdmitAccount):
// committing a change to an owned space, creating a space, and making a new
// account at a first sign-in. Company billing answers them from the
// account's membership and storage allowance, and the controlled launch's
// places. Core asks at the single place each happens, so every path is
// covered: commitFilesLocked for writes from the website, apps, Sync,
// folder actions, GitHub folders and virtual file refreshes;
// createOwnedSpace for spaces; FindOrCreate for accounts. As with
// SpaceGate, a refusal that is an *Error is shown as it is, and anything
// else is logged and refuses as unavailable.

type actorKey struct{}

// withActor records the account a change is made for, when the context
// has no signed-in identity: folder actions, GitHub folders and virtual
// file refreshes, which run as a space's owner or a folder action's payer.
func withActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

// actorOf is the account making a change: one set by withActor, else the
// signed-in identity (website, apps and Sync). Empty for space keys and the
// service's own changes.
func actorOf(ctx context.Context) string {
	if id, ok := ctx.Value(actorKey{}).(string); ok {
		return id
	}
	if id, ok := ctx.Value(mcpIdentityKey{}).(mcpIdentity); ok && id.oauth != nil {
		return id.oauth.UserID
	}
	return ""
}

var errBillingUnavailable = problem(503, "unavailable", "This can't be checked just now. Try again shortly.")

// gateAnswer turns a hook's answer into what Core returns.
func gateAnswer(gate, subject string, err error) error {
	if err == nil {
		return nil
	}
	var p *Error
	if errors.As(err, &p) {
		return p
	}
	log.Printf("%s: %s: %v", gate, subject, err)
	return errBillingUnavailable
}

// billingGates combines the extensions' billing hooks; nil fields when none
// asks. The first refusal wins.
type billingGates struct {
	write  []func(context.Context, extension.Write) error
	create []func(context.Context, string) error
	admit  []func(context.Context, string, string) error
}

func (s *Service) collectBillingGates() billingGates {
	var g billingGates
	for _, h := range s.extensions {
		if h.WriteGate != nil {
			g.write = append(g.write, h.WriteGate)
		}
		if h.SpaceCreateGate != nil {
			g.create = append(g.create, h.SpaceCreateGate)
		}
		if h.AdmitAccount != nil {
			g.admit = append(g.admit, h.AdmitAccount)
		}
	}
	return g
}

// checkWrite asks the write gates about a change to an owned repository.
// It runs with the repository's quota lock held, so hooks must answer
// quickly (Company caches).
func (s *Service) checkWrite(ctx context.Context, r *repository, bytes int64, onlyRemoves bool) error {
	if len(s.gates.write) == 0 || !r.owned {
		return nil
	}
	w := extension.Write{ActorID: actorOf(ctx), OwnerID: s.repositoryOwner(r), SpaceID: r.id, Bytes: bytes, OnlyRemoves: onlyRemoves}
	for _, hook := range s.gates.write {
		if err := gateAnswer("write gate", w.SpaceID, hook(ctx, w)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) checkSpaceCreate(ctx context.Context, userID string) error {
	for _, hook := range s.gates.create {
		if err := gateAnswer("space create gate", userID, hook(ctx, userID)); err != nil {
			return err
		}
	}
	return nil
}

func (db *accountDatabase) checkAdmit(ctx context.Context, userID, email string) error {
	if db.admit == nil {
		return nil
	}
	return db.admit(ctx, userID, email)
}

func (s *Service) admitAccountFunc() func(context.Context, string, string) error {
	hooks := s.gates.admit
	if len(hooks) == 0 {
		return nil
	}
	return func(ctx context.Context, userID, email string) error {
		for _, hook := range hooks {
			if err := gateAnswer("account admission", userID, hook(ctx, userID, email)); err != nil {
				return err
			}
		}
		return nil
	}
}
