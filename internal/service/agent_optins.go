package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Opting in (0.56.0, metatrash_action_optins in schema v17). An action whose
// owner set "pays": "contributor" runs on the agent of whoever brought the
// file, but only once they have agreed: they pick one of their agents for
// the action's model on the action's page (/account/actions/<job>, linked
// from the notice they get when a file of theirs is skipped). Nothing falls
// back to another key or model; a file from someone not opted in runs
// nothing.

const maxOptInsPerUser = 100

// actionOptIn is one row of metatrash_action_optins.
type actionOptIn struct {
	ID, UserID, SpaceID, Folder, Action, AgentID string
	Created                                      time.Time
}

// optInView is one row of Actions you pay for, on the Agents part of Services.
type optInView struct {
	actionOptIn
	// Space is the space's address (owner/slug) and name; AgentName the
	// agent that pays.
	Space, SpaceName, AgentName string
}

// FolderName is the folder for display: "/" for the space root.
func (o actionOptIn) FolderName() string {
	if o.Folder == "" {
		return "/"
	}
	return o.Folder
}

const optInColumns = "optin_id, user_id, space_id, folder, action, agent_id, created_at"

// optInFor returns the user's opt-in for one action; false when they have
// none.
func (db *accountDatabase) optInFor(ctx context.Context, userID, spaceID, folder, action string) (actionOptIn, bool, error) {
	var o actionOptIn
	var created int64
	err := db.db.QueryRowContext(ctx, "SELECT "+optInColumns+" FROM metatrash_action_optins WHERE user_id = ? AND space_id = ? AND folder = ? AND action = ?", userID, spaceID, folder, action).
		Scan(&o.ID, &o.UserID, &o.SpaceID, &o.Folder, &o.Action, &o.AgentID, &created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return o, false, nil
	case err != nil:
		return o, false, fmt.Errorf("cannot read opt-ins")
	}
	o.Created = time.Unix(created, 0).UTC()
	return o, true, nil
}

// userOptIns lists the actions a user pays for, newest first.
func (db *accountDatabase) userOptIns(ctx context.Context, userID string) ([]optInView, error) {
	rows, err := db.db.QueryContext(ctx, `SELECT o.optin_id, o.user_id, o.space_id, o.folder, o.action, o.agent_id, o.created_at,
		COALESCE(u.username, ''), s.slug, s.name, a.name
		FROM metatrash_action_optins o
		JOIN metatrash_spaces s ON s.space_id = o.space_id
		JOIN metatrash_users u ON u.user_id = s.owner_user_id
		JOIN metatrash_agents a ON a.agent_id = o.agent_id
		WHERE o.user_id = ? AND s.provisioning_state = 'ready' ORDER BY o.created_at DESC, o.optin_id LIMIT ?`, userID, maxOptInsPerUser)
	if err != nil {
		return nil, fmt.Errorf("cannot read opt-ins")
	}
	defer rows.Close()
	var out []optInView
	for rows.Next() {
		var v optInView
		var created int64
		var owner, slug string
		if err := rows.Scan(&v.ID, &v.UserID, &v.SpaceID, &v.Folder, &v.Action, &v.AgentID, &created, &owner, &slug, &v.SpaceName, &v.AgentName); err != nil {
			return nil, fmt.Errorf("cannot read opt-ins")
		}
		v.Created = time.Unix(created, 0).UTC()
		v.Space = owner + "/" + slug
		out = append(out, v)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read opt-ins")
	}
	return out, nil
}

// saveOptIn records that a user pays for an action with one of their agents,
// replacing the agent of an earlier opt-in to the same action.
func (db *accountDatabase) saveOptIn(ctx context.Context, o actionOptIn) error {
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cannot save the opt-in")
	}
	defer tx.Rollback()
	// Lock the agent's row, so it cannot be removed under the new opt-in.
	var id string
	switch err := tx.QueryRowContext(ctx, "SELECT agent_id FROM metatrash_agents WHERE agent_id = ? AND user_id = ? FOR UPDATE", o.AgentID, o.UserID).Scan(&id); {
	case errors.Is(err, sql.ErrNoRows):
		return invalid("Choose one of your agents.")
	case err != nil:
		return fmt.Errorf("cannot save the opt-in")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_action_optins WHERE user_id = ? AND NOT (space_id = ? AND folder = ? AND action = ?)", o.UserID, o.SpaceID, o.Folder, o.Action).Scan(&count); err != nil {
		return fmt.Errorf("cannot save the opt-in")
	}
	if count >= maxOptInsPerUser {
		return problem(409, "conflict", fmt.Sprintf("You pay for the most actions allowed (%d). Opt out of one first.", maxOptInsPerUser))
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM metatrash_action_optins WHERE user_id = ? AND space_id = ? AND folder = ? AND action = ?", o.UserID, o.SpaceID, o.Folder, o.Action); err != nil {
		return fmt.Errorf("cannot save the opt-in")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metatrash_action_optins ("+optInColumns+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		o.ID, o.UserID, o.SpaceID, o.Folder, o.Action, o.AgentID, o.Created.Unix()); err != nil {
		return fmt.Errorf("cannot save the opt-in")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cannot save the opt-in")
	}
	return nil
}

// removeOptIn deletes one of the user's opt-ins.
func (db *accountDatabase) removeOptIn(ctx context.Context, userID, optInID string) error {
	result, err := db.db.ExecContext(ctx, "DELETE FROM metatrash_action_optins WHERE optin_id = ? AND user_id = ?", optInID, userID)
	if err != nil {
		return fmt.Errorf("cannot remove the opt-in")
	}
	if n, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("cannot remove the opt-in")
	} else if n == 0 {
		return missing()
	}
	return nil
}
