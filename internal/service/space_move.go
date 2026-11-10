package service

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Moving a space between owner accounts the person acts for (0.95.23): their
// own account and the ones an extension's ActsFor names, such as a Company
// organisation they administer. Unlike a transfer there is no offer to
// accept, since the same person decides for both sides. The rest is a
// transfer's: the space keeps its ID, files and history, its old address
// leads to it (an alias), and its members, invitations and app connections
// stay. Someone moving their own space away stays on it as a read-write
// member, as after a transfer.

type movedSpace struct {
	SpaceID, Name, Slug string
	From, To            userAccount
}

// moveSpace moves spaceID to the owner toUsername names, for actorID.
func (db *accountDatabase) moveSpace(ctx context.Context, actorID, spaceID, toUsername string, now time.Time) (movedSpace, error) {
	var result movedSpace
	toUsername = strings.ToLower(strings.TrimSpace(toUsername))
	if !usernamePattern.MatchString(toUsername) {
		return result, invalid("Choose where to move the space.")
	}
	err := db.membershipTransaction(ctx, spaceID, actorID, false, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		if !db.actsFor(ctx, actorID, space.OwnerID) {
			return missing()
		}
		// Moving a space out is how files would leave a gated owner
		// (SpaceGate, 0.95.24), so it needs what opening it needs.
		if err := db.gateFor(ctx, actorID, space.OwnerID); err != nil {
			return err
		}
		to, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE username = ? FOR UPDATE", toUsername), true)
		if err == sql.ErrNoRows {
			return invalid("You can move a space only to your own account or one you manage.")
		}
		if err != nil {
			return fmt.Errorf("cannot read the new owner")
		}
		if !db.actsFor(ctx, actorID, to.ID) {
			return invalid("You can move a space only to your own account or one you manage.")
		}
		if err := db.gateFor(ctx, actorID, to.ID); err != nil {
			return err
		}
		if to.ID == space.OwnerID {
			return invalid("The space is already there.")
		}
		from, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE user_id = ?", space.OwnerID), true)
		if err != nil {
			return fmt.Errorf("cannot read the space owner")
		}
		var clash, owned int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ? AND slug = ?", to.ID, space.Slug).Scan(&clash); err != nil {
			return fmt.Errorf("cannot check the new owner's spaces")
		}
		if clash > 0 {
			return problem(409, "conflict", to.Username+" already has a space at "+to.Username+"/"+space.Slug+", so this one cannot move to that address. Space addresses cannot be changed.")
		}
		var movedID string
		err = tx.QueryRowContext(ctx, "SELECT space_id FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", to.ID, space.Slug).Scan(&movedID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("cannot check the new owner's spaces")
		}
		if err == nil && movedID != spaceID {
			return problem(409, "reserved", "This space would move to "+to.Username+"/"+space.Slug+", but that address still leads to another space that moved away, so it cannot be reused.")
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ? AND provisioning_state IN ('provisioning', 'ready')", to.ID).Scan(&owned); err != nil {
			return fmt.Errorf("cannot check the new owner's spaces")
		}
		if owned >= to.MaxPrivateSpaces {
			return problem(409, "space_limit", to.Username+"'s private-space allowance is fully used.")
		}
		steps := []struct {
			query string
			args  []any
		}{
			// The new owner, if a member, stops being one: its consent rows
			// stop depending on the membership.
			{"UPDATE metatrash_oauth_grant_spaces SET member_user_id = NULL WHERE space_id = ? AND member_user_id = ?", []any{spaceID, to.ID}},
			{"DELETE FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", []any{spaceID, to.ID}},
			{"UPDATE metatrash_spaces SET owner_user_id = ? WHERE space_id = ? AND owner_user_id = ?", []any{to.ID, spaceID, from.ID}},
			{"INSERT INTO metatrash_space_aliases (owner_user_id, slug, space_id, created_at) VALUES (?, ?, ?, ?)", []any{from.ID, space.Slug, spaceID, now.Unix()}},
			{"DELETE FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", []any{to.ID, space.Slug}},
			{"DELETE FROM metatrash_space_transfers WHERE space_id = ?", []any{spaceID}},
		}
		if from.ID == actorID {
			// Moving your own space away: you stay on it as a member, and
			// your apps' consent rows depend on that membership, as after a
			// transfer. An account you act for (nobody signs in as it)
			// isn't made a member.
			steps = append(steps,
				struct {
					query string
					args  []any
				}{"INSERT INTO metatrash_memberships (space_id, user_id, role, status, joined_at, updated_at, agent_permission) VALUES (?, ?, 'member', 'active', ?, ?, 'read_write')", []any{spaceID, from.ID, now.Unix(), now.Unix()}},
				struct {
					query string
					args  []any
				}{"UPDATE metatrash_oauth_grant_spaces SET member_user_id = ? WHERE space_id = ? AND member_user_id IS NULL AND grant_id IN (SELECT grant_id FROM metatrash_oauth_grants WHERE user_id = ?)", []any{from.ID, spaceID, from.ID}},
			)
		}
		for i, step := range steps {
			res, err := tx.ExecContext(ctx, step.query, step.args...)
			if err != nil {
				return fmt.Errorf("cannot move the space")
			}
			if i == 2 {
				if n, err := res.RowsAffected(); err != nil || n != 1 {
					return fmt.Errorf("cannot move the space")
				}
			}
		}
		result = movedSpace{SpaceID: spaceID, Name: space.Name, Slug: space.Slug, From: from, To: to}
		return nil
	})
	return result, err
}

// moveSpace runs the database move, then points the open repository at its
// new owner.
func (s *Service) moveSpace(ctx context.Context, actorID, spaceID, toUsername string) (movedSpace, error) {
	if s.ownedDB == nil {
		return movedSpace{}, problem(503, "unavailable", "Spaces are temporarily unavailable.")
	}
	moved, err := s.ownedDB.moveSpace(ctx, actorID, spaceID, toUsername, time.Now().UTC())
	if err == nil {
		s.setRepositoryOwner(spaceID, moved.To.ID)
	}
	return moved, err
}

// actingOwnerNames lists the usernames of the owner accounts userID acts
// for, where it can move its spaces.
func (db *accountDatabase) actingOwnerNames(ctx context.Context, userID string) ([]string, error) {
	owners := db.actingOwners(ctx, userID)
	names := []string{}
	if len(owners) == 0 {
		return names, nil
	}
	if len(owners) > 100 {
		owners = owners[:100]
	}
	args := make([]any, len(owners))
	for i, id := range owners {
		args[i] = id
	}
	err := eachRow(ctx, db.db, func(scan func(...any) error) error {
		var name string
		if err := scan(&name); err != nil {
			return err
		}
		names = append(names, name)
		return nil
	}, "SELECT username FROM metatrash_users WHERE user_id IN (?"+strings.Repeat(", ?", len(owners)-1)+") AND username IS NOT NULL ORDER BY username", args...)
	if err != nil {
		return nil, fmt.Errorf("cannot list the accounts you manage")
	}
	return names, nil
}
