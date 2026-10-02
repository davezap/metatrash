package service

import (
	"context"
	"os"
	"testing"
	"time"
)

// Optional integration checks against a disposable MariaDB database at schema
// v5 with the documented runtime grants. Set METATRASH_TEST_DB_CONFIG to the
// path of an account database configuration file to run them; they are skipped
// otherwise. Never point this at a production database: tests create rows.
// Recreate the database before each run; owned spaces from an earlier run point
// at repositories that no longer exist and stop the service from loading.
func testDatabase(t *testing.T) *accountDatabase {
	t.Helper()
	path := os.Getenv("METATRASH_TEST_DB_CONFIG")
	if path == "" {
		t.Skip("METATRASH_TEST_DB_CONFIG not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, err := openAccountDatabase(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.ready(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDatabaseSchemaV5Ready(t *testing.T) {
	testDatabase(t)
}
