package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Only persistent account operations live here. Login challenges and sessions
// stay ephemeral. A test store can exercise the email flow without a database.
type accountStore interface {
	FindOrCreate(context.Context, string) (userAccount, error)
	// Exists reports whether an account uses this normalized email (login log).
	Exists(context.Context, string) (bool, error)
	ByID(context.Context, string) (userAccount, bool, error)
	ChooseUsername(context.Context, string, string) error
	Close() error
}

type accountDatabaseConfig struct {
	Network      string `json:"network"`
	Address      string `json:"address"`
	Database     string `json:"database"`
	Username     string `json:"username"`
	PasswordFile string `json:"passwordFile"`
}

type accountDatabase struct{ db *sql.DB }

func readLimitedFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("file cannot be read or exceeds limit")
	}
	return b, nil
}

func openAccountDatabase(ctx context.Context, configPath string) (*accountDatabase, error) {
	if configPath == "" {
		return nil, fmt.Errorf("accounts require databaseConfigFile; follow docs/deployment.md before upgrading")
	}
	b, err := readLimitedFile(configPath, 16*1024)
	if err != nil {
		return nil, fmt.Errorf("cannot read account database configuration")
	}
	var cfg accountDatabaseConfig
	if strictJSON(b, &cfg) != nil || cfg.Database == "" || cfg.Username == "" || cfg.PasswordFile == "" {
		return nil, fmt.Errorf("invalid account database configuration")
	}
	// Stage 1 uses the database already on this host. Remote DB/TLS configuration
	// is deliberately not exposed, nor are arbitrary DSN flags or SQL parameters.
	switch cfg.Network {
	case "unix":
		if !filepath.IsAbs(cfg.Address) {
			return nil, fmt.Errorf("database Unix socket must be an absolute path")
		}
	case "tcp":
		host, port, err := net.SplitHostPort(cfg.Address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || port == "" {
			return nil, fmt.Errorf("database TCP address must use a loopback IP and port")
		}
	default:
		return nil, fmt.Errorf("database network must be unix or tcp")
	}
	password, err := readLimitedFile(cfg.PasswordFile, 4096)
	if err != nil || len(strings.TrimSpace(string(password))) == 0 {
		return nil, fmt.Errorf("cannot read database password or password is empty")
	}
	driverConfig := mysql.NewConfig()
	driverConfig.User = cfg.Username
	driverConfig.Passwd = strings.TrimRight(string(password), "\r\n")
	driverConfig.Net = cfg.Network
	driverConfig.Addr = cfg.Address
	driverConfig.DBName = cfg.Database
	driverConfig.Timeout = 5 * time.Second
	driverConfig.ReadTimeout = 5 * time.Second
	driverConfig.WriteTimeout = 5 * time.Second
	// Driver logs can include server-provided text. Return generic application
	// errors instead of exposing connection details, email addresses, or secrets.
	driverConfig.Logger = &mysql.NopLogger{}
	connector, err := mysql.NewConnector(driverConfig)
	if err != nil {
		return nil, fmt.Errorf("invalid database connection settings")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(3 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("account database unavailable; check server, credentials, and grants")
	}
	return &accountDatabase{db: db}, nil
}

func (s *accountDatabase) Close() error { return s.db.Close() }

func (s *accountDatabase) ready(ctx context.Context) error {
	if err := s.checkEngines(ctx); err != nil {
		return err
	}
	var version int
	var source string
	if err := s.db.QueryRowContext(ctx, "SELECT schema_version, migration_source FROM metatrash_account_meta WHERE singleton_id = 1").Scan(&version, &source); err != nil || version != 6 || source == "" {
		return fmt.Errorf("account schema v6/migration is not ready; follow docs/deployment.md")
	}
	var usernameIndex int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_users' AND index_name = 'metatrash_users_username' AND non_unique = 0 AND column_name = 'username' AND seq_in_index = 1 AND sub_part IS NULL").Scan(&usernameIndex); err != nil || usernameIndex != 1 {
		return fmt.Errorf("username unique index is required; follow docs/deployment.md")
	}
	if err := s.checkOwnedSpaceSchema(ctx); err != nil {
		return err
	}
	if err := s.checkHumanMembershipSchema(ctx); err != nil {
		return err
	}
	if err := s.checkOAuthSchema(ctx); err != nil {
		return err
	}
	if err := s.checkGitHubSchema(ctx); err != nil {
		return err
	}
	// Validate persisted records at startup, including the preserved service cap.
	rows, err := s.db.QueryContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users LIMIT 10001")
	if err != nil {
		return fmt.Errorf("cannot read account database users")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if _, err := scanAccount(rows, true); err != nil || count > maxAccountRecords {
			return fmt.Errorf("invalid account database records or account limit exceeded")
		}
	}
	if rows.Err() != nil {
		return fmt.Errorf("cannot read account database users")
	}
	return nil
}

func (s *accountDatabase) checkEngines(ctx context.Context) error {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ('metatrash_account_meta', 'metatrash_users') AND engine = 'InnoDB'").Scan(&count)
	if err != nil || count != 2 {
		return fmt.Errorf("both account schema tables must exist and use InnoDB")
	}
	return nil
}

type accountScanner interface{ Scan(...any) error }

func scanAccount(row accountScanner, withUsername ...bool) (userAccount, error) {
	var user userAccount
	var created string
	var username sql.NullString
	fields := []any{&user.ID, &user.Email, &created, &user.MaxPrivateSpaces}
	if len(withUsername) > 0 && withUsername[0] {
		fields = append(fields, &username)
	}
	if err := row.Scan(fields...); err != nil {
		return userAccount{}, err
	}
	user.Username = username.String
	if username.Valid {
		normalized, err := normalizeUsername(username.String)
		if err != nil || normalized != username.String {
			return userAccount{}, fmt.Errorf("invalid stored username")
		}
	}
	var err error
	user.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil || !validAccount(user) {
		return userAccount{}, fmt.Errorf("invalid stored account")
	}
	return user, nil
}

func validAccount(user userAccount) bool {
	email, err := normalizeEmail(user.Email)
	return err == nil && email == user.Email && idPattern.MatchString(user.ID) && !user.CreatedAt.IsZero() && user.MaxPrivateSpaces >= 0
}

func (s *accountDatabase) ByID(ctx context.Context, id string) (userAccount, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	user, err := scanAccount(s.db.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE user_id = ?", id), true)
	if err == sql.ErrNoRows {
		return userAccount{}, false, nil
	}
	if err != nil {
		return userAccount{}, false, fmt.Errorf("account database unavailable")
	}
	return user, true, nil
}

// Lock the singleton metadata row for all registrations and imports, so the
// account cap and unique identities hold even across concurrent connections.
func lockAccountMeta(ctx context.Context, tx *sql.Tx) (string, error) {
	var version int
	var source string
	if err := tx.QueryRowContext(ctx, "SELECT schema_version, migration_source FROM metatrash_account_meta WHERE singleton_id = 1 FOR UPDATE").Scan(&version, &source); err != nil || (version < 1 || version > 6) {
		return "", fmt.Errorf("account schema version 1 through 6 is required")
	}
	return source, nil
}

func insertAccount(ctx context.Context, tx *sql.Tx, user userAccount) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO metatrash_users (user_id, email, created_at, max_private_spaces) VALUES (?, ?, ?, ?)", user.ID, user.Email, user.CreatedAt.UTC().Format(time.RFC3339Nano), user.MaxPrivateSpaces)
	return err
}

func (s *accountDatabase) Exists(ctx context.Context, email string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var one int
	err := s.db.QueryRowContext(ctx, "SELECT 1 FROM metatrash_users WHERE email = ? LIMIT 1", email).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *accountDatabase) FindOrCreate(ctx context.Context, email string) (userAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if normalized, err := normalizeEmail(email); err != nil || normalized != email {
		return userAccount{}, fmt.Errorf("invalid normalized email")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return userAccount{}, fmt.Errorf("account database unavailable")
	}
	defer tx.Rollback()
	source, err := lockAccountMeta(ctx, tx)
	if err != nil || source == "" {
		return userAccount{}, fmt.Errorf("account database migration required")
	}
	user, err := scanAccount(tx.QueryRowContext(ctx, "SELECT user_id, email, created_at, max_private_spaces, username FROM metatrash_users WHERE email = ?", email), true)
	if err == nil {
		// Read-only path: the deferred rollback releases the registration lock.
		return user, nil
	}
	if err != sql.ErrNoRows {
		return userAccount{}, fmt.Errorf("cannot read account")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metatrash_users").Scan(&count); err != nil || count >= maxAccountRecords {
		return userAccount{}, fmt.Errorf("registration unavailable or account limit reached")
	}
	id, err := randomHex(16)
	if err != nil {
		return userAccount{}, err
	}
	user = userAccount{ID: id, Email: email, CreatedAt: time.Now().UTC(), MaxPrivateSpaces: 1}
	if err := insertAccount(ctx, tx, user); err != nil {
		return userAccount{}, fmt.Errorf("cannot create account")
	}
	if err := tx.Commit(); err != nil {
		return userAccount{}, fmt.Errorf("cannot commit account")
	}
	return user, nil
}
