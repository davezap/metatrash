package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Renaming an owned space (0.95.29): an administrator gives it a new name
// and, optionally, a new address under the same owner (owner/new-slug). The
// space keeps its ID, files, history, members, invitations and app
// connections. Like a move, its old address keeps leading to it (an alias in
// metatrash_space_aliases), so website links, synced folders and agents that
// use the old owner/slug still reach it; the owner cannot make a new space
// there while it does. Renaming back to the old address drops that alias.

// ErrSpaceAddressTaken is returned when the new address is in use.
var ErrSpaceAddressTaken = errors.New("that address is taken")

// RenamedSpace is what RenameSpace changed.
type RenamedSpace struct {
	Owner          string
	OldName, Name  string
	OldSlug, Slug  string
	AddressChanged bool
}

// RenameSpace sets space's name and, when slug is not "", its slug. The
// running service reads names and addresses from the database on each
// request, so the change shows at once.
func (d *AdminDB) RenameSpace(ctx context.Context, space AdminSpace, name, slug string) (RenamedSpace, error) {
	if slug == "" {
		slug = space.Slug
	}
	name, slug, err := normalizeSpaceName(name, slug)
	var p *Error
	if errors.As(err, &p) {
		return RenamedSpace{}, errors.New(p.Message)
	}
	if err != nil {
		return RenamedSpace{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result := RenamedSpace{Owner: space.Owner, OldName: space.Name, Name: name, OldSlug: space.Slug, Slug: slug, AddressChanged: slug != space.Slug}
	tx, err := d.store.db.BeginTx(ctx, nil)
	if err != nil {
		return RenamedSpace{}, fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	var ownerID, state, current string
	err = tx.QueryRowContext(ctx, "SELECT owner_user_id, provisioning_state, slug FROM metatrash_spaces WHERE space_id = ? FOR UPDATE", space.ID).Scan(&ownerID, &state, &current)
	if err == sql.ErrNoRows {
		return RenamedSpace{}, ErrNoSpace
	}
	if err != nil {
		return RenamedSpace{}, fmt.Errorf("account database unavailable")
	}
	if state != "ready" {
		return RenamedSpace{}, fmt.Errorf("the space is %s; only a ready space can be renamed", state)
	}
	if current != space.Slug {
		return RenamedSpace{}, fmt.Errorf("the space changed while renaming; try again")
	}
	if result.AddressChanged {
		var clash int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ? AND slug = ?", ownerID, slug).Scan(&clash); err != nil {
			return RenamedSpace{}, fmt.Errorf("account database unavailable")
		}
		if clash > 0 {
			return RenamedSpace{}, fmt.Errorf("%w: %s already has a space (or a deleted one kept for restoring) at %s/%s", ErrSpaceAddressTaken, space.Owner, space.Owner, slug)
		}
		var aliasID string
		err = tx.QueryRowContext(ctx, "SELECT space_id FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", ownerID, slug).Scan(&aliasID)
		if err != nil && err != sql.ErrNoRows {
			return RenamedSpace{}, fmt.Errorf("account database unavailable")
		}
		if err == nil && aliasID != space.ID {
			return RenamedSpace{}, fmt.Errorf("%w: %s/%s still leads to another space that moved or was renamed away", ErrSpaceAddressTaken, space.Owner, slug)
		}
	}
	res, err := tx.ExecContext(ctx, "UPDATE metatrash_spaces SET name = ?, slug = ? WHERE space_id = ? AND provisioning_state = 'ready'", name, slug, space.ID)
	if err != nil {
		return RenamedSpace{}, fmt.Errorf("cannot rename the space; run deploy/account-grants.sql again (0.95.29 added UPDATE (name)): %v", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return RenamedSpace{}, fmt.Errorf("cannot rename the space")
	}
	if result.AddressChanged {
		for _, step := range []struct {
			query string
			args  []any
		}{
			// The new address was this space's own old one: it is real again.
			{"DELETE FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", []any{ownerID, slug}},
			{"DELETE FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", []any{ownerID, space.Slug}},
			{"INSERT INTO metatrash_space_aliases (owner_user_id, slug, space_id, created_at) VALUES (?, ?, ?, ?)", []any{ownerID, space.Slug, space.ID, time.Now().Unix()}},
		} {
			if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
				return RenamedSpace{}, fmt.Errorf("cannot keep the old address")
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return RenamedSpace{}, fmt.Errorf("cannot rename the space")
	}
	return result, nil
}
