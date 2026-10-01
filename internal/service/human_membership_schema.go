package service

import (
	"context"
	"database/sql"
	"fmt"
)

func (db *accountDatabase) checkHumanMembershipSchema(ctx context.Context) error {
	badSchema := fmt.Errorf("human membership schema v4 required; follow docs/human-membership-storage.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('metatrash_memberships', 'metatrash_invitations') AND engine = 'InnoDB'").Scan(&count); err != nil || count != 2 {
		return badSchema
	}
	for _, index := range []struct{ table, name, columns string }{
		{"metatrash_memberships", "PRIMARY", "space_id,user_id"},
		{"metatrash_invitations", "PRIMARY", "invitation_id"},
		{"metatrash_invitations", "metatrash_invitations_space_email", "space_id,email"},
	} {
		var columns sql.NullString
		var nonUnique, prefixes int
		if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", index.table, index.name).Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != index.columns || nonUnique != 0 || prefixes != 0 {
			return badSchema
		}
	}
	for _, fk := range []struct{ table, name, column, target, targetColumn string }{
		{"metatrash_memberships", "metatrash_memberships_space", "space_id", "metatrash_spaces", "space_id"},
		{"metatrash_memberships", "metatrash_memberships_user", "user_id", "metatrash_users", "user_id"},
		{"metatrash_invitations", "metatrash_invitations_space", "space_id", "metatrash_spaces", "space_id"},
		{"metatrash_invitations", "metatrash_invitations_user", "accepted_user_id", "metatrash_users", "user_id"},
	} {
		if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.key_column_usage WHERE constraint_schema = DATABASE() AND table_name = ? AND constraint_name = ? AND column_name = ? AND referenced_table_name = ? AND referenced_column_name = ?", fk.table, fk.name, fk.column, fk.target, fk.targetColumn).Scan(&count); err != nil || count != 1 {
			return badSchema
		}
	}
	// Probe all runtime columns, including list-order/filter indexes' columns.
	for _, query := range []string{
		"SELECT space_id, user_id, role, status, joined_at, updated_at FROM metatrash_memberships LIMIT 0",
		"SELECT invitation_id, space_id, email, status, created_at, expires_at, accepted_user_id FROM metatrash_invitations LIMIT 0",
	} {
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			return badSchema
		}
		rows.Close()
	}
	return nil
}
