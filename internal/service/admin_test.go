package service

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestOpenAdminDBNeedsConfig(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenAdminDB(ctx, ""); err == nil || !strings.Contains(err.Error(), "METATRASH_ACCOUNTS_CONFIG") {
		t.Fatal("missing configuration accepted", err)
	}
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(`{"databaseConfigFile":"/x","unknown":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAdminDB(ctx, path); err == nil || err.Error() != "invalid account configuration" {
		t.Fatal("unknown field accepted", err)
	}
}

func TestDatabaseAdminUsers(t *testing.T) {
	db := testDatabase(t)
	ctx := context.Background()
	email := randomName(t, "admin-") + "@example.com"
	user, err := db.FindOrCreate(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	for _, digest := range []string{strings.Repeat("1", 64), strings.Repeat("2", 64)} {
		if _, err := db.db.ExecContext(ctx, "INSERT INTO metatrash_recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)", user.ID, digest, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// Only databaseConfigFile is needed: no SMTP password or other secrets.
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(`{"databaseConfigFile":"`+os.Getenv("METATRASH_TEST_DB_CONFIG")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdminDB(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	users, err := admin.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u.ID != user.ID {
			continue
		}
		if u.Email != email || u.Username != "" || !u.CreatedAt.Equal(user.CreatedAt) || u.MaxPrivateSpaces != 1 || u.OwnedSpaces != 0 || u.Memberships != 0 || !u.EmailLogin || u.Passkeys != 0 || u.AuthenticatorApp || u.RecoveryCodes != 2 {
			t.Fatalf("unexpected user %+v", u)
		}
		return
	}
	t.Fatal("new account not listed")
}

// accountGrants must say the same as deploy/account-grants.sql.
func TestAccountGrantsMatchSQL(t *testing.T) {
	b, err := os.ReadFile("../../deploy/account-grants.sql")
	if err != nil {
		t.Fatal(err)
	}
	statement := regexp.MustCompile(`(?s)GRANT (.+?)\s+ON metatrash\.(\w+) TO 'metatrash_accounts'@'localhost';`)
	privilege := regexp.MustCompile(`(\w+)(?: \(([^)]*)\))?`)
	fromSQL := map[string]bool{}
	for _, m := range statement.FindAllStringSubmatch(string(b), -1) {
		for _, p := range privilege.FindAllStringSubmatch(m[1], -1) {
			if p[2] == "" {
				fromSQL[m[2]+" "+p[1]] = true
				continue
			}
			for _, column := range strings.Split(p[2], ",") {
				fromSQL[m[2]+" "+p[1]+" ("+strings.TrimSpace(column)+")"] = true
			}
		}
	}
	fromGo := map[string]bool{}
	for _, g := range accountGrants {
		fromGo[g] = true
		if !fromSQL[g] {
			t.Errorf("%s is not in account-grants.sql", g)
		}
	}
	for g := range fromSQL {
		if !fromGo[g] {
			t.Errorf("%s from account-grants.sql is not in accountGrants", g)
		}
	}
	if len(fromSQL) < 60 {
		t.Fatalf("parsed only %d privileges", len(fromSQL))
	}
}

func testAdminDB(t *testing.T) (*accountDatabase, *AdminDB) {
	t.Helper()
	db := testDatabase(t)
	path := filepath.Join(t.TempDir(), "accounts.json")
	if err := os.WriteFile(path, []byte(`{"databaseConfigFile":"`+os.Getenv("METATRASH_TEST_DB_CONFIG")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	admin, err := OpenAdminDB(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	return db, admin
}

func TestDatabaseAdminFindAndChange(t *testing.T) {
	db, admin := testAdminDB(t)
	ctx := context.Background()
	email := randomName(t, "find-") + "@example.com"
	user, err := db.FindOrCreate(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	username := randomName(t, "find-")
	if err := db.ChooseUsername(ctx, user.ID, username); err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{strings.ToUpper(email), " " + strings.ToUpper(username), user.ID} {
		u, err := admin.FindUser(ctx, who)
		if err != nil || u.ID != user.ID || u.Username != username {
			t.Fatalf("find %q: %+v %v", who, u, err)
		}
	}
	if _, err := admin.FindUser(ctx, "nobody-"+username); err != ErrNoAccount {
		t.Fatal("unknown username found", err)
	}
	u, _ := admin.FindUser(ctx, email)
	detail, err := admin.UserDetail(ctx, u)
	if err != nil || len(detail.PasskeyList) != 0 || detail.AppSetupUnfinished || len(detail.Spaces) != 0 || len(detail.Apps) != 0 {
		t.Fatalf("detail %+v %v", detail, err)
	}
	// Turning email sign-in off needs a strong method and recovery codes,
	// as on Security.
	if _, err := admin.SetEmailLogin(ctx, u, false); err == nil || !strings.Contains(err.Error(), "first add a passkey") {
		t.Fatal("email sign-in turned off without another method", err)
	}
	if changed, err := admin.SetEmailLogin(ctx, u, true); err != nil || changed {
		t.Fatal("already on reported as a change", err)
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE metatrash_users SET email_login = 0 WHERE user_id = ?", user.ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := admin.SetEmailLogin(ctx, u, true); err != nil || !changed {
		t.Fatal("email sign-in not turned back on", err)
	}
	if allowed, err := db.emailLoginAllowed(ctx, email); err != nil || !allowed {
		t.Fatal("service still refuses emailed codes", err)
	}
	if err := admin.SetSpaceLimit(ctx, u, MaxSpaceLimit+1); err == nil {
		t.Fatal("limit over the cap accepted")
	}
	if err := admin.SetSpaceLimit(ctx, u, 4); err != nil {
		t.Fatal(err)
	}
	if stored, ok, err := db.ByID(ctx, user.ID); err != nil || !ok || stored.MaxPrivateSpaces != 4 {
		t.Fatal("limit not stored", stored, err)
	}
}

func TestDatabaseAdminPrivileges(t *testing.T) {
	db := testDatabase(t)
	missing, extra, err := db.checkPrivileges(context.Background())
	if err != nil || len(missing) != 0 || len(extra) != 0 {
		t.Fatalf("test database login should have exactly deploy/account-grants.sql: missing %v, extra %v, %v", missing, extra, err)
	}
}
