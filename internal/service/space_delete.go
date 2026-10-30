package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Deleting a space (0.67.0). The owner deletes a space from its sharing
// page; it is hidden from everyone at once (state 'deleted') and kept for
// deletedSpaceHold, with its address, so the operator can restore it with
// `metatrash spaces restore`. Then the runner's daily housekeeping purges
// it: the repository and the space's virtual files in the bucket go, and the
// row becomes 'purged' with slug deleted-<space_id>, which frees the
// address. The row itself stays because other tables still name the space.
// Backups (restic) keep their own copies for as long as they keep snapshots.

const deletedSpaceHold = 30 * 24 * time.Hour

// sameSpaceName reports whether typed confirms name: the same text, ignoring
// case and surrounding space.
func sameSpaceName(typed, name string) bool {
	typed = strings.TrimSpace(typed)
	return typed != "" && strings.EqualFold(typed, name)
}

// deleteOwnedSpace marks the owner's ready space deleted, once its name was
// typed to confirm it. Pending invitations and ownership offers end with it.
func (db *accountDatabase) deleteOwnedSpace(ctx context.Context, ownerID, spaceID, typedName string, now time.Time) (ownedSpace, error) {
	var deleted ownedSpace
	err := db.membershipTransaction(ctx, spaceID, ownerID, true, func(ctx context.Context, tx *sql.Tx, space ownedSpace) error {
		if !sameSpaceName(typedName, space.Name) {
			return invalid("Type the space’s name, " + space.Name + ", to delete it.")
		}
		res, err := tx.ExecContext(ctx, "UPDATE metatrash_spaces SET provisioning_state = 'deleted', deleted_at = ? WHERE space_id = ? AND owner_user_id = ? AND provisioning_state = 'ready'", now.Unix(), spaceID, ownerID)
		if err != nil {
			return fmt.Errorf("cannot delete space")
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("cannot delete space")
		}
		for _, query := range []string{
			"UPDATE metatrash_invitations SET status = 'cancelled' WHERE space_id = ? AND status = 'pending'",
			"DELETE FROM metatrash_space_transfers WHERE space_id = ?",
		} {
			if _, err := tx.ExecContext(ctx, query, spaceID); err != nil {
				return fmt.Errorf("cannot delete space")
			}
		}
		deleted = space
		return nil
	})
	return deleted, err
}

// deleteOwnedSpace deletes the space in the database, then closes it: once
// the writes queued before it finish, nothing reaches the repository.
func (s *Service) deleteOwnedSpace(ctx context.Context, owner userAccount, spaceID, typedName string) (ownedSpace, error) {
	if s.ownedDB == nil {
		return ownedSpace{}, problem(503, "unavailable", "Spaces are temporarily unavailable.")
	}
	if a := s.accounts; a != nil && a.config.DocsSpace != "" {
		ownerName, slug, _ := strings.Cut(a.config.DocsSpace, "/")
		var docsID string
		err := s.ownedDB.db.QueryRowContext(ctx, "SELECT s.space_id FROM metatrash_spaces s JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE u.username = ? AND s.slug = ?", ownerName, slug).Scan(&docsID)
		if err == nil && docsID == spaceID {
			return ownedSpace{}, invalid("This space is the site’s documentation (docsSpace in accounts.json). Change that setting before deleting it.")
		}
	}
	space, err := s.ownedDB.deleteOwnedSpace(ctx, owner.ID, spaceID, typedName, time.Now().UTC())
	if err != nil {
		return ownedSpace{}, err
	}
	s.closeOwnedSpace(spaceID)
	return space, nil
}

// closeOwnedSpace takes a deleted space out of the open repositories, once
// the writes queued before the delete finish. A restart would too: deleted
// spaces are not loaded.
func (s *Service) closeOwnedSpace(spaceID string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = s.queued(ctx, func() (any, error) { return nil, nil })
	s.ownedMu.Lock()
	delete(s.ownedRepos, spaceID)
	s.ownedMu.Unlock()
}

// purgeDeletedSpaces purges every space deleted more than deletedSpaceHold
// ago. Files go first, then the row, so a purge that stops half way is done
// again the next day.
func (s *Service) purgeDeletedSpaces(ctx context.Context, now time.Time) {
	if s.ownedDB == nil {
		return
	}
	var ids []string
	err := eachRow(ctx, s.ownedDB.db, func(scan func(...any) error) error {
		var id string
		if err := scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	}, "SELECT space_id FROM metatrash_spaces WHERE provisioning_state = 'deleted' AND deleted_at <= ? ORDER BY deleted_at LIMIT 100", now.Add(-deletedSpaceHold).Unix())
	if err != nil {
		log.Printf("deleted spaces: %v", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := s.purgeSpace(ctx, id); err != nil {
			log.Printf("deleted spaces: purge %s: %v", id, err)
			continue
		}
		log.Printf("deleted spaces: purged %s", id)
	}
}

func (s *Service) purgeSpace(ctx context.Context, id string) error {
	if !idPattern.MatchString(id) || s.ownedRepository(id) != nil {
		return fmt.Errorf("space is open")
	}
	store, _ := s.spaceFiles()
	if sweeper, ok := store.(objectSweeper); ok {
		prefix := "files/" + id + "/"
		objects, err := sweeper.list(ctx, prefix)
		if err != nil {
			return err
		}
		for _, o := range objects {
			if !strings.HasPrefix(o.Key, prefix) {
				continue
			}
			if err := sweeper.remove(ctx, o.Key); err != nil {
				return err
			}
		}
	}
	if err := os.RemoveAll(filepath.Join(s.ownedRoot, id+".git")); err != nil {
		return err
	}
	tx, err := s.ownedDB.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("database unavailable")
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE metatrash_spaces SET provisioning_state = 'purged', slug = ? WHERE space_id = ? AND provisioning_state = 'deleted'", "deleted-"+id, id)
	if err != nil {
		return fmt.Errorf("cannot mark space purged")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("cannot mark space purged")
	}
	// Old addresses from transfers, and the space's schedules, go with it.
	for _, query := range []string{
		"DELETE FROM metatrash_space_aliases WHERE space_id = ?",
		"DELETE FROM metatrash_action_schedules WHERE space_id = ?",
		"DELETE FROM metatrash_virtual_refresh WHERE space_id = ?",
	} {
		if _, err := tx.ExecContext(ctx, query, id); err != nil {
			return fmt.Errorf("cannot mark space purged")
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot mark space purged")
	}
	return nil
}
