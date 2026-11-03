package extension

import (
	"context"
	"database/sql"
	"fmt"
)

// RequireSchema verifies an administrator-installed component baseline.
// Runtime logins have SELECT access only; startup never migrates a database.
func RequireSchema(ctx context.Context, db *sql.DB, name string, version int, checksum string) error {
	var gotVersion int
	var gotChecksum string
	if db == nil {
		return fmt.Errorf("%s requires an account database", name)
	}
	err := db.QueryRowContext(ctx, "SELECT version, checksum FROM metatrash_schema_components WHERE component = ?", name).Scan(&gotVersion, &gotChecksum)
	if err != nil || gotVersion != version || gotChecksum != checksum {
		return fmt.Errorf("%s component schema is not ready; install or adopt the documented baseline", name)
	}
	return nil
}
