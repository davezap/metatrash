package service

import (
	"context"
	"database/sql"
	"fmt"
)

// Schema v5 readiness: member agent permissions and OAuth grant storage. Checked
// at startup with the other account schema checks; missing pieces fail closed.
func (db *accountDatabase) checkOAuthSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v5 required; follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('metatrash_oauth_grants', 'metatrash_oauth_grant_spaces', 'metatrash_oauth_tokens') AND engine = 'InnoDB'").Scan(&count); err != nil || count != 3 {
		return bad
	}
	for _, index := range []struct{ table, name, columns string }{
		{"metatrash_oauth_grants", "PRIMARY", "grant_id"},
		{"metatrash_oauth_grants", "metatrash_oauth_grants_connection", "user_id,client_id,resource"},
		{"metatrash_oauth_grant_spaces", "PRIMARY", "grant_id,space_id"},
		{"metatrash_oauth_tokens", "PRIMARY", "token_digest"},
	} {
		var columns sql.NullString
		var nonUnique, prefixes int
		if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?", index.table, index.name).Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != index.columns || nonUnique != 0 || prefixes != 0 {
			return bad
		}
	}
	// Cascades carry revocation and membership removal; verify the delete rules.
	for _, fk := range []struct{ table, name, columns, target, targetColumns, onDelete string }{
		{"metatrash_oauth_grants", "metatrash_oauth_grants_user", "user_id", "metatrash_users", "user_id", "RESTRICT"},
		{"metatrash_oauth_grant_spaces", "metatrash_oauth_grant_spaces_grant", "grant_id", "metatrash_oauth_grants", "grant_id", "CASCADE"},
		{"metatrash_oauth_grant_spaces", "metatrash_oauth_grant_spaces_space", "space_id", "metatrash_spaces", "space_id", "RESTRICT"},
		{"metatrash_oauth_grant_spaces", "metatrash_oauth_grant_spaces_member", "space_id,member_user_id", "metatrash_memberships", "space_id,user_id", "CASCADE"},
		{"metatrash_oauth_tokens", "metatrash_oauth_tokens_grant", "grant_id", "metatrash_oauth_grants", "grant_id", "CASCADE"},
	} {
		var columns, target, targetColumns sql.NullString
		if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY ordinal_position SEPARATOR ','), MIN(referenced_table_name), GROUP_CONCAT(referenced_column_name ORDER BY ordinal_position SEPARATOR ',') FROM information_schema.key_column_usage WHERE constraint_schema = DATABASE() AND table_name = ? AND constraint_name = ?", fk.table, fk.name).Scan(&columns, &target, &targetColumns); err != nil || columns.String != fk.columns || target.String != fk.target || targetColumns.String != fk.targetColumns {
			return bad
		}
		var onDelete string
		if err := db.db.QueryRowContext(ctx, "SELECT delete_rule FROM information_schema.referential_constraints WHERE constraint_schema = DATABASE() AND table_name = ? AND constraint_name = ?", fk.table, fk.name).Scan(&onDelete); err != nil || onDelete != fk.onDelete {
			return bad
		}
	}
	for _, query := range []string{
		"SELECT agent_permission FROM metatrash_memberships LIMIT 0",
		"SELECT grant_id, user_id, client_id, client_name, resource, scope, created_at, updated_at, last_used_at, expires_at FROM metatrash_oauth_grants LIMIT 0",
		"SELECT grant_id, space_id, member_user_id, permission FROM metatrash_oauth_grant_spaces LIMIT 0",
		"SELECT token_digest, grant_id, kind, scope, created_at, expires_at, used_at FROM metatrash_oauth_tokens LIMIT 0",
	} {
		rows, err := db.db.QueryContext(ctx, query)
		if err != nil {
			return bad
		}
		rows.Close()
	}
	return nil
}
