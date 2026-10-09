package service

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Space ownership transfer (0.30.0, schema v9). An owner offers a space to one
// of its active members who has a public username; the offer waits up to
// transferLifetime until that member accepts or declines it, or the owner
// cancels it. Accepting moves ownership in one transaction:
//
//   - the space's owner becomes the member, whose membership row goes (an
//     owner has no membership), and the previous owner becomes an active
//     member whose connected apps may read and write;
//   - each person's app connections to the space keep working in their new
//     role (consent rows record member_user_id only for members);
//   - other members, their app permissions and pending invitations stay;
//   - the old owner/slug is kept as an alias, so website links and agents
//     that use it still reach the space. The address stays reserved: its
//     old owner cannot create or accept another space at that slug.
//
// The space ID never changes. GitHub folders then pull and push through the
// new owner's GitHub connections.

const transferLifetime = 7 * 24 * time.Hour

// spaceTransferOffer is an offer waiting for the signed-in user (Spaces).
type spaceTransferOffer struct {
	SpaceID, Name, Slug, From, Expires string
}

// spaceTransferPending is the offer an owner has made for one space (the
// sharing page). Expired offers are shown so the owner can make a new one.
type spaceTransferPending struct {
	ToID, ToEmail, ToUsername, Expires string
	Expired                            bool
}

// acceptedTransfer describes a completed transfer, for notices.
type acceptedTransfer struct {
	SpaceID, Name, Slug string
	From, To            userAccount
}

func (db *accountDatabase) checkTransferSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v9 required; follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('metatrash_space_transfers', 'metatrash_space_aliases') AND engine = 'InnoDB'").Scan(&count); err != nil || count != 2 {
		return bad
	}
	for _, index := range []struct{ table, name, columns string }{
		{"metatrash_space_transfers", "PRIMARY", "space_id"},
		{"metatrash_space_aliases", "PRIMARY", "owner_user_id,slug"},
	} {
		var columns sql.NullString
		var nonUnique, prefixes int
		if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", index.table, index.name).Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != index.columns || nonUnique != 0 || prefixes != 0 {
			return bad
		}
	}
	for _, query := range []string{
		"SELECT space_id, from_user_id, to_user_id, created_at, expires_at FROM metatrash_space_transfers LIMIT 0",
		"SELECT owner_user_id, slug, space_id, created_at FROM metatrash_space_aliases LIMIT 0",
	} {
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			return bad
		}
		rows.Close()
	}
	return nil
}

// offerSpaceTransfer replaces any offer for the space with one to toUserID,
// who must be an active member with a public username. The caller checks the
// owner confirmed it's them recently.
func (db *accountDatabase) offerSpaceTransfer(ctx context.Context, ownerID, spaceID, toUserID string, now time.Time) error {
	if !idPattern.MatchString(toUserID) {
		return invalid("Choose a member of this space.")
	}
	return db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		if toUserID == space.OwnerID {
			return invalid("You already own this space.")
		}
		var status string
		var username sql.NullString
		err := tx.QueryRowContext(ctx, "SELECT m.status, u.username FROM metatrash_memberships m JOIN metatrash_users u ON u.user_id = m.user_id WHERE m.space_id = ? AND m.user_id = ?", spaceID, toUserID).Scan(&status, &username)
		if err == sql.ErrNoRows || (err == nil && status != "active") {
			return invalid("Ownership can only go to an active member of this space.")
		}
		if err != nil {
			return fmt.Errorf("cannot read membership")
		}
		if !username.Valid || username.String == "" {
			return invalid("That member has no public username yet. The space’s address uses it, so ask them to choose one in Profile first.")
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_space_transfers WHERE space_id = ?", spaceID); err != nil {
			return fmt.Errorf("cannot save transfer offer")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_space_transfers (space_id, from_user_id, to_user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?)", spaceID, space.OwnerID, toUserID, now.Unix(), now.Add(transferLifetime).Unix()); err != nil {
			return fmt.Errorf("cannot save transfer offer")
		}
		return nil
	})
}

// cancelSpaceTransfer withdraws the owner's offer. Repeating it is harmless.
func (db *accountDatabase) cancelSpaceTransfer(ctx context.Context, ownerID, spaceID string) error {
	return db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, _ ownedSpace) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_space_transfers WHERE space_id = ?", spaceID); err != nil {
			return fmt.Errorf("cannot cancel transfer offer")
		}
		return nil
	})
}

// declineSpaceTransfer turns down an offer made to userID. Repeating it is
// harmless.
func (db *accountDatabase) declineSpaceTransfer(ctx context.Context, userID, spaceID string) error {
	return db.membershipTransaction(ctx, spaceID, userID, false, func(ctx context.Context, tx *sql.Tx, _ ownedSpace) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_space_transfers WHERE space_id = ? AND to_user_id = ?", spaceID, userID); err != nil {
			return fmt.Errorf("cannot decline transfer offer")
		}
		return nil
	})
}

// acceptSpaceTransfer makes userID the owner of the space offered to them.
// done is false when the transfer had already happened (a retry after a lost
// response): nothing changes and no notices should be sent.
func (db *accountDatabase) acceptSpaceTransfer(ctx context.Context, userID, spaceID string, now time.Time) (result acceptedTransfer, done bool, err error) {
	unavailable := problem(409, "conflict", "This ownership offer is no longer available. Ask the owner to offer it again.")
	err = db.membershipTransaction(ctx, spaceID, userID, false, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		var fromID string
		var expires int64
		err := tx.QueryRowContext(ctx, "SELECT from_user_id, expires_at FROM metatrash_space_transfers WHERE space_id = ? AND to_user_id = ?", spaceID, userID).Scan(&fromID, &expires)
		if err == sql.ErrNoRows {
			if space.OwnerID == userID {
				return nil // already accepted
			}
			return unavailable
		}
		if err != nil {
			return fmt.Errorf("cannot read transfer offer")
		}
		if fromID != space.OwnerID || expires <= now.Unix() {
			return unavailable
		}
		// Lock the new owner's row, as creating a space does, so the
		// allowance holds against a space being created at the same time.
		to, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE user_id = ? FOR UPDATE", userID), true)
		if err != nil {
			return fmt.Errorf("cannot read your account")
		}
		from, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE user_id = ?", fromID), true)
		if err != nil {
			return fmt.Errorf("cannot read the space owner")
		}
		if to.Username == "" {
			return invalid("Choose a public username in Profile first: the space’s address uses it.")
		}
		var status string
		err = tx.QueryRowContext(ctx, "SELECT status FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", spaceID, userID).Scan(&status)
		if err == sql.ErrNoRows || (err == nil && status != "active") {
			return unavailable
		}
		if err != nil {
			return fmt.Errorf("cannot read membership")
		}
		var clash, owned int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ? AND slug = ?", userID, space.Slug).Scan(&clash); err != nil {
			return fmt.Errorf("cannot check your spaces")
		}
		if clash > 0 {
			return problem(409, "conflict", "You already own a space at "+to.Username+"/"+space.Slug+", so this one cannot move to that address. Space addresses cannot be changed.")
		}
		// An address the user gave up in an earlier transfer stays with that
		// space, unless this is that space coming back.
		var movedID string
		err = tx.QueryRowContext(ctx, "SELECT space_id FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", userID, space.Slug).Scan(&movedID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("cannot check your spaces")
		}
		if err == nil && movedID != spaceID {
			return problem(409, "reserved", "This space would move to "+to.Username+"/"+space.Slug+", but that address still leads to a space you transferred to someone else, so it cannot be reused.")
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_spaces WHERE owner_user_id = ?", userID).Scan(&owned); err != nil {
			return fmt.Errorf("cannot check your spaces")
		}
		if owned >= to.MaxPrivateSpaces {
			return problem(409, "space_limit", "Accepting would take you over your private-space allowance, which is fully used.")
		}
		steps := []struct {
			query string
			args  []any
		}{
			// The new owner's consent rows stop depending on their membership.
			{"UPDATE metatrash_oauth_grant_spaces SET member_user_id = NULL WHERE space_id = ? AND member_user_id = ?", []any{spaceID, userID}},
			{"DELETE FROM metatrash_memberships WHERE space_id = ? AND user_id = ?", []any{spaceID, userID}},
			{"UPDATE metatrash_spaces SET owner_user_id = ? WHERE space_id = ? AND owner_user_id = ?", []any{userID, spaceID, fromID}},
			{"INSERT INTO metatrash_memberships (space_id, user_id, role, status, joined_at, updated_at, agent_permission) VALUES (?, ?, 'member', 'active', ?, ?, 'read_write')", []any{spaceID, fromID, now.Unix(), now.Unix()}},
			// The previous owner's consent rows now depend on their membership,
			// so removing them later also disconnects their apps.
			{"UPDATE metatrash_oauth_grant_spaces SET member_user_id = ? WHERE space_id = ? AND member_user_id IS NULL AND grant_id IN (SELECT grant_id FROM metatrash_oauth_grants WHERE user_id = ?)", []any{fromID, spaceID, fromID}},
			// The old owner holds no alias at this slug: they owned a space
			// there until now, and an alias never shares its owner/slug with
			// a space (reserveOwnedSpace and the checks above).
			{"INSERT INTO metatrash_space_aliases (owner_user_id, slug, space_id, created_at) VALUES (?, ?, ?, ?)", []any{fromID, space.Slug, spaceID, now.Unix()}},
			// A space moving back to an earlier owner takes its address back.
			{"DELETE FROM metatrash_space_aliases WHERE owner_user_id = ? AND slug = ?", []any{userID, space.Slug}},
			{"DELETE FROM metatrash_space_transfers WHERE space_id = ?", []any{spaceID}},
		}
		for i, step := range steps {
			res, err := tx.ExecContext(ctx, step.query, step.args...)
			if err != nil {
				return fmt.Errorf("cannot transfer ownership")
			}
			if i == 2 {
				if n, err := res.RowsAffected(); err != nil || n != 1 {
					return fmt.Errorf("cannot transfer ownership")
				}
			}
		}
		result = acceptedTransfer{SpaceID: spaceID, Name: space.Name, Slug: space.Slug, From: from, To: to}
		done = true
		return nil
	})
	return result, done, err
}

// acceptSpaceTransfer runs the database transfer, then points the open
// repository at its new owner.
func (s *Service) acceptSpaceTransfer(ctx context.Context, userID, spaceID string) (acceptedTransfer, bool, error) {
	if s.ownedDB == nil {
		return acceptedTransfer{}, false, problem(503, "unavailable", "Spaces are temporarily unavailable.")
	}
	result, done, err := s.ownedDB.acceptSpaceTransfer(ctx, userID, spaceID, time.Now().UTC())
	if err == nil {
		s.setRepositoryOwner(spaceID, userID)
	}
	return result, done, err
}

// transferOffers lists the valid offers waiting for userID.
func (db *accountDatabase) transferOffers(ctx context.Context, userID string, now time.Time) ([]spaceTransferOffer, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.db.QueryContext(ctx, `SELECT s.space_id, s.name, s.slug, COALESCE(u.username, ''), t.expires_at FROM metatrash_space_transfers t
JOIN metatrash_spaces s ON s.space_id = t.space_id AND s.owner_user_id = t.from_user_id
JOIN metatrash_users u ON u.user_id = s.owner_user_id
JOIN metatrash_memberships m ON m.space_id = t.space_id AND m.user_id = t.to_user_id AND m.status = 'active'
WHERE t.to_user_id = ? AND t.expires_at > ? AND s.provisioning_state = 'ready' ORDER BY t.expires_at, s.space_id LIMIT 200`, userID, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	offers := []spaceTransferOffer{}
	for rows.Next() {
		var item spaceTransferOffer
		var expires int64
		if err := rows.Scan(&item.SpaceID, &item.Name, &item.Slug, &item.From, &expires); err != nil {
			return nil, err
		}
		item.Expires = time.Unix(expires, 0).UTC().Format("2006-01-02 15:04 UTC")
		offers = append(offers, item)
	}
	return offers, rows.Err()
}

// pendingTransfer returns the owner's offer for the space, or nil.
func (db *accountDatabase) pendingTransfer(ctx context.Context, spaceID, ownerID string, now time.Time) (*spaceTransferPending, error) {
	var p spaceTransferPending
	var username sql.NullString
	var expires int64
	err := db.db.QueryRowContext(ctx, "SELECT t.to_user_id, u.email, u.username, t.expires_at FROM metatrash_space_transfers t JOIN metatrash_users u ON u.user_id = t.to_user_id WHERE t.space_id = ? AND t.from_user_id = ?", spaceID, ownerID).Scan(&p.ToID, &p.ToEmail, &username, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.ToUsername = username.String
	p.Expired = expires <= now.Unix()
	p.Expires = time.Unix(expires, 0).UTC().Format("2006-01-02 15:04 UTC")
	return &p, nil
}

// spaceByAlias finds the space that was at owner/slug before a transfer. A
// real space at owner/slug always wins (the space came back to that owner).
func (db *accountDatabase) spaceByAlias(ctx context.Context, owner, slug string) (string, bool, error) {
	var id string
	err := db.db.QueryRowContext(ctx, `SELECT a.space_id FROM metatrash_space_aliases a JOIN metatrash_users u ON u.user_id = a.owner_user_id
WHERE u.username = ? AND a.slug = ? AND NOT EXISTS (SELECT 1 FROM metatrash_spaces s WHERE s.owner_user_id = a.owner_user_id AND s.slug = a.slug)`, owner, slug).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}
