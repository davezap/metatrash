package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Console commands (cmd/metatrash) read the same accounts.json and account
// database as the running service, with the same database login and grants.
// They run beside the service, so they never take its data directory lock.
// The service reads email_login and max_private_spaces from the database on
// every request, so changes made here apply at once.

// AdminUser is one account as the console lists it.
type AdminUser struct {
	ID               string    `json:"id"`
	Username         string    `json:"username"`
	Email            string    `json:"email"`
	CreatedAt        time.Time `json:"createdAt"`
	MaxPrivateSpaces int       `json:"maxPrivateSpaces"`
	OwnedSpaces      int       `json:"ownedSpaces"`
	Memberships      int       `json:"memberships"`
	EmailLogin       bool      `json:"emailLogin"`
	Passkeys         int       `json:"passkeys"`
	AuthenticatorApp bool      `json:"authenticatorApp"`
	RecoveryCodes    int       `json:"recoveryCodes"`
}

// AdminUserDetail is one account as users show prints it.
type AdminUserDetail struct {
	AdminUser
	PasskeyList []AdminPasskey `json:"passkeyList"`
	// AppSetupUnfinished is an authenticator app that was started but never
	// confirmed with a code; it does not sign in.
	AppSetupUnfinished   bool                `json:"appSetupUnfinished"`
	AppEnabledAt         time.Time           `json:"appEnabledAt,omitzero"`
	RecoveryCodesCreated time.Time           `json:"recoveryCodesCreated,omitzero"`
	Spaces               []AdminSpace        `json:"spaces"`
	MemberOf             []AdminMembership   `json:"memberOf"`
	Invitations          []AdminInvitation   `json:"invitations"`
	Apps                 []AdminApp          `json:"apps"`
	GitHub               []AdminInstallation `json:"github"`
}

type AdminPasskey struct {
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitzero"`
	BackedUp   bool      `json:"backedUp"`
}

type AdminSpace struct {
	ID         string    `json:"id"`
	OwnerID    string    `json:"ownerId"`
	Owner      string    `json:"owner"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	Visibility string    `json:"visibility"`
	State      string    `json:"state"`
	Members    int       `json:"members"`
	Invited    int       `json:"invited"`
	CreatedAt  time.Time `json:"createdAt"`
}

type AdminMembership struct {
	Space           string    `json:"space"`
	Status          string    `json:"status"`
	AgentPermission string    `json:"agentPermission"`
	JoinedAt        time.Time `json:"joinedAt"`
}

type AdminInvitation struct {
	Space     string    `json:"space"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type AdminApp struct {
	Name       string    `json:"name"`
	Scope      string    `json:"scope"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type AdminInstallation struct {
	Account     string `json:"account"`
	GitHubLogin string `json:"githubLogin"`
	Status      string `json:"status"`
}

// AdminDB is an account database opened for console commands.
type AdminDB struct {
	store  *accountDatabase
	config accountConfig
}

// ErrNoAccount is returned when users show and friends find no account.
var ErrNoAccount = errors.New("no account with that email, username or ID")

// OpenAdminDB reads accounts.json for its databaseConfigFile (and, only when
// a notice is sent, its SMTP settings) and checks the schema is v8.
func OpenAdminDB(ctx context.Context, accountsConfigPath string) (*AdminDB, error) {
	if accountsConfigPath == "" {
		return nil, fmt.Errorf("no account configuration: pass -accounts-config or set METATRASH_ACCOUNTS_CONFIG")
	}
	b, err := os.ReadFile(accountsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read account configuration: %w", err)
	}
	var cfg accountConfig
	if err := strictJSON(b, &cfg); err != nil {
		return nil, fmt.Errorf("invalid account configuration")
	}
	store, err := openAccountDatabase(ctx, cfg.DatabaseConfigFile)
	if err != nil {
		return nil, err
	}
	if err := store.checkEngines(ctx); err != nil {
		store.Close()
		return nil, err
	}
	var version int
	if err := store.db.QueryRowContext(ctx, "SELECT schema_version FROM metatrash_account_meta WHERE singleton_id = 1").Scan(&version); err != nil || version != 8 {
		store.Close()
		return nil, fmt.Errorf("account schema v8 is required; follow docs/deployment.md")
	}
	return &AdminDB{store: store, config: cfg}, nil
}

func (d *AdminDB) Close() error { return d.store.Close() }

const adminUserQuery = `SELECT u.user_id, u.email, u.created_at, u.max_private_spaces, u.username, u.email_login,
	(SELECT COUNT(*) FROM metatrash_spaces s WHERE s.owner_user_id = u.user_id),
	(SELECT COUNT(*) FROM metatrash_memberships m WHERE m.user_id = u.user_id AND m.status = 'active'),
	(SELECT COUNT(*) FROM metatrash_passkeys p WHERE p.user_id = u.user_id),
	(SELECT COUNT(*) FROM metatrash_totp t WHERE t.user_id = u.user_id AND t.enabled_at > 0),
	(SELECT COUNT(*) FROM metatrash_recovery_codes r WHERE r.user_id = u.user_id)
FROM metatrash_users u`

func scanAdminUser(row accountScanner) (AdminUser, error) {
	var u AdminUser
	var created string
	var username sql.NullString
	var emailLogin, totp int
	if err := row.Scan(&u.ID, &u.Email, &created, &u.MaxPrivateSpaces, &username, &emailLogin, &u.OwnedSpaces, &u.Memberships, &u.Passkeys, &totp, &u.RecoveryCodes); err != nil {
		return u, err
	}
	// Show what is stored even when it would fail the service's checks,
	// since the console is where an administrator looks when it does.
	u.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	u.Username = username.String
	u.EmailLogin = emailLogin == 1
	u.AuthenticatorApp = totp > 0
	return u, nil
}

// Users lists every account, oldest first.
func (d *AdminDB) Users(ctx context.Context) ([]AdminUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	rows, err := d.store.db.QueryContext(ctx, adminUserQuery+" ORDER BY u.created_at, u.user_id LIMIT 10001")
	if err != nil {
		return nil, fmt.Errorf("cannot read account database users")
	}
	defer rows.Close()
	users := []AdminUser{}
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, fmt.Errorf("cannot read account database users")
		}
		users = append(users, u)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read account database users")
	}
	return users, nil
}

// FindUser finds an account by email address (any case), username or user ID.
func (d *AdminDB) FindUser(ctx context.Context, who string) (AdminUser, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	who = strings.TrimSpace(who)
	column, value := "u.username", strings.ToLower(who)
	switch {
	case strings.Contains(who, "@"):
		email, err := normalizeEmail(who)
		if err != nil {
			return AdminUser{}, fmt.Errorf("invalid email address")
		}
		column, value = "u.email", email
	case idPattern.MatchString(who):
		column = "u.user_id"
	}
	u, err := scanAdminUser(d.store.db.QueryRowContext(ctx, adminUserQuery+" WHERE "+column+" = ?", value))
	if err == sql.ErrNoRows {
		return AdminUser{}, ErrNoAccount
	}
	if err != nil {
		return AdminUser{}, fmt.Errorf("account database unavailable")
	}
	return u, nil
}

func unixTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

// UserDetail adds everything users show prints to an account from FindUser.
func (d *AdminDB) UserDetail(ctx context.Context, u AdminUser) (AdminUserDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	detail := AdminUserDetail{AdminUser: u, PasskeyList: []AdminPasskey{}, MemberOf: []AdminMembership{}, Invitations: []AdminInvitation{}, Apps: []AdminApp{}, GitHub: []AdminInstallation{}}
	fail := fmt.Errorf("account database unavailable")
	db := d.store.db
	if err := eachRow(ctx, db, func(scan func(...any) error) error {
		var p AdminPasskey
		var created, used int64
		var backedUp int
		if err := scan(&p.Name, &created, &used, &backedUp); err != nil {
			return err
		}
		p.CreatedAt, p.LastUsedAt, p.BackedUp = unixTime(created), unixTime(used), backedUp == 1
		detail.PasskeyList = append(detail.PasskeyList, p)
		return nil
	}, "SELECT name, created_at, last_used_at, backed_up FROM metatrash_passkeys WHERE user_id = ? ORDER BY created_at, passkey_id", u.ID); err != nil {
		return detail, fail
	}
	var enabled int64
	switch err := db.QueryRowContext(ctx, "SELECT enabled_at FROM metatrash_totp WHERE user_id = ?", u.ID).Scan(&enabled); {
	case err == sql.ErrNoRows:
	case err != nil:
		return detail, fail
	case enabled > 0:
		detail.AppEnabledAt = unixTime(enabled)
	default:
		detail.AppSetupUnfinished = true
	}
	var codesCreated int64
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(created_at), 0) FROM metatrash_recovery_codes WHERE user_id = ?", u.ID).Scan(&codesCreated); err != nil {
		return detail, fail
	}
	detail.RecoveryCodesCreated = unixTime(codesCreated)
	var err error
	if detail.Spaces, err = d.spaces(ctx, "WHERE s.owner_user_id = ?", u.ID); err != nil {
		return detail, fail
	}
	if err := eachRow(ctx, db, func(scan func(...any) error) error {
		var m AdminMembership
		var owner, slug string
		var joined int64
		if err := scan(&owner, &slug, &m.Status, &m.AgentPermission, &joined); err != nil {
			return err
		}
		m.Space, m.JoinedAt = owner+"/"+slug, unixTime(joined)
		detail.MemberOf = append(detail.MemberOf, m)
		return nil
	}, `SELECT COALESCE(o.username, o.email), s.slug, m.status, m.agent_permission, m.joined_at
FROM metatrash_memberships m JOIN metatrash_spaces s ON s.space_id = m.space_id JOIN metatrash_users o ON o.user_id = s.owner_user_id
WHERE m.user_id = ? ORDER BY m.joined_at, s.space_id`, u.ID); err != nil {
		return detail, fail
	}
	if err := eachRow(ctx, db, func(scan func(...any) error) error {
		var i AdminInvitation
		var owner, slug string
		var expires int64
		if err := scan(&owner, &slug, &expires); err != nil {
			return err
		}
		i.Space, i.ExpiresAt = owner+"/"+slug, unixTime(expires)
		detail.Invitations = append(detail.Invitations, i)
		return nil
	}, `SELECT COALESCE(o.username, o.email), s.slug, i.expires_at
FROM metatrash_invitations i JOIN metatrash_spaces s ON s.space_id = i.space_id JOIN metatrash_users o ON o.user_id = s.owner_user_id
WHERE i.email = ? AND i.status = 'pending' AND i.expires_at > ? ORDER BY i.created_at`, u.Email, time.Now().Unix()); err != nil {
		return detail, fail
	}
	if err := eachRow(ctx, db, func(scan func(...any) error) error {
		var a AdminApp
		var used, expires int64
		if err := scan(&a.Name, &a.Scope, &used, &expires); err != nil {
			return err
		}
		a.LastUsedAt, a.ExpiresAt = unixTime(used), unixTime(expires)
		detail.Apps = append(detail.Apps, a)
		return nil
	}, "SELECT client_name, scope, last_used_at, expires_at FROM metatrash_oauth_grants WHERE user_id = ? AND expires_at > ? ORDER BY created_at, grant_id", u.ID, time.Now().Unix()); err != nil {
		return detail, fail
	}
	if err := eachRow(ctx, db, func(scan func(...any) error) error {
		var g AdminInstallation
		if err := scan(&g.Account, &g.GitHubLogin, &g.Status); err != nil {
			return err
		}
		detail.GitHub = append(detail.GitHub, g)
		return nil
	}, "SELECT account_login, github_login, status FROM metatrash_github_installations WHERE user_id = ? ORDER BY created_at, installation_id", u.ID); err != nil {
		return detail, fail
	}
	return detail, nil
}

func eachRow(ctx context.Context, db *sql.DB, each func(scan func(...any) error) error, query string, args ...any) error {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := each(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Spaces lists every owned space, oldest first. Spaces from spaces.json
// (public and configured private spaces) are not in the database.
func (d *AdminDB) Spaces(ctx context.Context) ([]AdminSpace, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	spaces, err := d.spaces(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("cannot read owned spaces")
	}
	return spaces, nil
}

func (d *AdminDB) spaces(ctx context.Context, where string, args ...any) ([]AdminSpace, error) {
	spaces := []AdminSpace{}
	err := eachRow(ctx, d.store.db, func(scan func(...any) error) error {
		var s AdminSpace
		var created string
		if err := scan(&s.ID, &s.OwnerID, &s.Owner, &s.Slug, &s.Name, &s.Visibility, &s.State, &created, &s.Members, &s.Invited); err != nil {
			return err
		}
		s.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		spaces = append(spaces, s)
		return nil
	}, `SELECT s.space_id, s.owner_user_id, COALESCE(o.username, o.email), s.slug, s.name, s.visibility, s.provisioning_state, s.created_at,
	(SELECT COUNT(*) FROM metatrash_memberships m WHERE m.space_id = s.space_id AND m.status = 'active'),
	(SELECT COUNT(*) FROM metatrash_invitations i WHERE i.space_id = s.space_id AND i.status = 'pending' AND i.expires_at > UNIX_TIMESTAMP())
FROM metatrash_spaces s JOIN metatrash_users o ON o.user_id = s.owner_user_id `+where+` ORDER BY s.created_at, s.space_id LIMIT 10001`, args...)
	return spaces, err
}

// SetEmailLogin turns sign-in by emailed code on or off for an account.
// Turning it off needs what Security needs: a passkey or authenticator app
// and unused recovery codes. changed is false when it was already so.
func (d *AdminDB) SetEmailLogin(ctx context.Context, u AdminUser, on bool) (changed bool, err error) {
	changed, err = d.store.setEmailLogin(ctx, u.ID, on)
	var p *Error
	if errors.As(err, &p) {
		return false, errors.New(p.Message)
	}
	return changed, err
}

// EmailLoginNotice emails the account that an administrator changed its
// email sign-in, using the service's SMTP settings.
func (d *AdminDB) EmailLoginNotice(ctx context.Context, u AdminUser, on bool) error {
	state, what := "off", "Sign in at "+d.config.Origin+"/login with a passkey, your authenticator app or a recovery code."
	if on {
		state, what = "back on", "You can sign in at "+d.config.Origin+"/login with a code emailed to this address."
	}
	body := "A Metatrash administrator turned sign-in by emailed code " + state + " for your account.\n" +
		"When: " + time.Now().UTC().Format("2 January 2006 15:04 UTC") + "\n\n" + what + "\n" +
		"If you did not ask for this, reply to this email.\n"
	return d.notice(ctx, u.Email, "Sign-in method changed on your Metatrash account", body)
}

func (d *AdminDB) notice(ctx context.Context, email, subject, body string) error {
	cfg := d.config
	if cfg.SMTPHost == "" || cfg.SMTPPasswordFile == "" {
		return fmt.Errorf("SMTP is not configured")
	}
	password, err := os.ReadFile(cfg.SMTPPasswordFile)
	if err != nil || strings.TrimSpace(string(password)) == "" {
		return fmt.Errorf("cannot read SMTP password file")
	}
	if cfg.SMTPFrom, err = normalizeEmail(cfg.SMTPFrom); err != nil {
		return fmt.Errorf("invalid SMTP sender")
	}
	return sendMail(ctx, cfg, strings.TrimSpace(string(password)), email, subject, body)
}

// MaxSpaceLimit caps users set-limit.
const MaxSpaceLimit = 1000

// SetSpaceLimit sets how many private spaces an account may own. Spaces it
// already owns stay when the limit goes below their number.
func (d *AdminDB) SetSpaceLimit(ctx context.Context, u AdminUser, limit int) error {
	if limit < 0 || limit > MaxSpaceLimit {
		return fmt.Errorf("limit must be 0 to %d", MaxSpaceLimit)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := d.store.db.ExecContext(ctx, "UPDATE metatrash_users SET max_private_spaces = ? WHERE user_id = ?", limit, u.ID)
	var denied *mysql.MySQLError
	if errors.As(err, &denied) && (denied.Number == 1142 || denied.Number == 1143) {
		return fmt.Errorf("the database login cannot change space limits; run deploy/account-grants.sql again (0.28.0 added UPDATE (max_private_spaces))")
	}
	if err != nil {
		return fmt.Errorf("account database unavailable")
	}
	return nil
}

// CheckResult is one line of the check command.
type CheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// CheckOptions are the paths the service is started with.
type CheckOptions struct {
	SpacesConfig, Keys, AccountsConfig, Data string
}

// Check runs the service's startup checks without starting it or taking the
// data directory lock: spaces.json and keys, accounts.json and the files it
// names, the database schema and the login's grants, and the repositories of
// ready owned spaces.
func Check(ctx context.Context, o CheckOptions) []CheckResult {
	results := []CheckResult{}
	add := func(name string, err error, ok string) bool {
		if err != nil {
			results = append(results, CheckResult{name, false, err.Error()})
			return false
		}
		results = append(results, CheckResult{name, true, ok})
		return true
	}
	c, _, err := loadConfig(o.SpacesConfig, o.Keys)
	add("spaces config", err, fmt.Sprintf("%s: %d configured spaces", o.SpacesConfig, len(c.Spaces)))
	if pid, err := os.ReadFile(filepath.Join(o.Data, ".service-lock", "pid")); err == nil {
		results = append(results, CheckResult{"service", true, "running (pid " + strings.TrimSpace(string(pid)) + "), or its lock was left behind"})
	} else {
		results = append(results, CheckResult{"service", true, "not running (no lock in " + o.Data + ")"})
	}
	if o.AccountsConfig == "" {
		results = append(results, CheckResult{"accounts config", true, "not set: accounts off"})
		return results
	}
	a, _, err := loadAccountSettings(o.AccountsConfig)
	if err != nil {
		add("accounts config", err, "")
		return results
	}
	add("accounts config", nil, o.AccountsConfig+": SMTP"+accountFeatures(a))
	store, err := openAccountDatabase(ctx, a.config.DatabaseConfigFile)
	if !add("database", err, "connected") {
		return results
	}
	defer store.Close()
	if !add("schema", store.ready(ctx), "v8 ready") {
		return results
	}
	missing, extra, err := store.checkPrivileges(ctx)
	switch {
	case err != nil:
		add("grants", err, "")
	case len(missing) > 0:
		also := ""
		if len(extra) > 0 {
			also = "; also has " + strings.Join(extra, ", ") + " (not needed)"
		}
		add("grants", fmt.Errorf("missing %s, so what needs it fails; run deploy/account-grants.sql%s", strings.Join(missing, ", "), also), "")
	case len(extra) > 0:
		add("grants", nil, "all present; also has "+strings.Join(extra, ", ")+" (not needed)")
	default:
		add("grants", nil, "exactly as deploy/account-grants.sql")
	}
	ready, missingRepos := 0, []string{}
	err = eachRow(ctx, store.db, func(scan func(...any) error) error {
		var id string
		if err := scan(&id); err != nil {
			return err
		}
		ready++
		if _, err := os.Lstat(filepath.Join(o.Data, "owned-repos", id+".git")); err != nil {
			missingRepos = append(missingRepos, id)
		}
		return nil
	}, "SELECT space_id FROM metatrash_spaces WHERE provisioning_state = 'ready'")
	switch {
	case err != nil:
		add("owned spaces", fmt.Errorf("cannot read owned spaces"), "")
	case len(missingRepos) > 0:
		add("owned spaces", fmt.Errorf("repository missing for %s; the service will not start", strings.Join(missingRepos, ", ")), "")
	default:
		add("owned spaces", nil, fmt.Sprintf("%d ready, all repositories present", ready))
	}
	return results
}

func accountFeatures(a *accounts) string {
	s := ""
	if a.oauth != nil {
		s += ", OAuth"
	}
	if a.github != nil {
		s += ", GitHub App"
	}
	if a.totpKey != nil {
		s += ", authenticator app"
	}
	return s
}

// accountGrants is deploy/account-grants.sql as table privileges ("table
// PRIV") and column privileges ("table PRIV (column)"). A test keeps the two
// in step.
var accountGrants = []string{
	"metatrash_users SELECT", "metatrash_users INSERT",
	"metatrash_users UPDATE (username)", "metatrash_users UPDATE (email_login)", "metatrash_users UPDATE (email)", "metatrash_users UPDATE (max_private_spaces)",
	"metatrash_account_meta SELECT", "metatrash_account_meta UPDATE",
	"metatrash_spaces SELECT", "metatrash_spaces INSERT",
	"metatrash_spaces UPDATE (provisioning_state)", "metatrash_spaces UPDATE (visibility)",
	"metatrash_memberships SELECT", "metatrash_memberships INSERT", "metatrash_memberships DELETE",
	"metatrash_memberships UPDATE (status)", "metatrash_memberships UPDATE (updated_at)", "metatrash_memberships UPDATE (agent_permission)",
	"metatrash_invitations SELECT", "metatrash_invitations INSERT",
	"metatrash_invitations UPDATE (invitation_id)", "metatrash_invitations UPDATE (status)", "metatrash_invitations UPDATE (created_at)", "metatrash_invitations UPDATE (expires_at)", "metatrash_invitations UPDATE (accepted_user_id)",
	"metatrash_oauth_grants SELECT", "metatrash_oauth_grants INSERT", "metatrash_oauth_grants DELETE",
	"metatrash_oauth_grants UPDATE (client_name)", "metatrash_oauth_grants UPDATE (scope)", "metatrash_oauth_grants UPDATE (updated_at)", "metatrash_oauth_grants UPDATE (last_used_at)", "metatrash_oauth_grants UPDATE (expires_at)",
	"metatrash_oauth_grant_spaces SELECT", "metatrash_oauth_grant_spaces INSERT", "metatrash_oauth_grant_spaces DELETE",
	"metatrash_oauth_tokens SELECT", "metatrash_oauth_tokens INSERT", "metatrash_oauth_tokens DELETE", "metatrash_oauth_tokens UPDATE (used_at)",
	"metatrash_github_installations SELECT", "metatrash_github_installations INSERT", "metatrash_github_installations DELETE",
	"metatrash_github_installations UPDATE (github_user_id)", "metatrash_github_installations UPDATE (github_login)", "metatrash_github_installations UPDATE (account_login)", "metatrash_github_installations UPDATE (account_type)", "metatrash_github_installations UPDATE (status)", "metatrash_github_installations UPDATE (updated_at)",
	"metatrash_passkeys SELECT", "metatrash_passkeys INSERT", "metatrash_passkeys DELETE",
	"metatrash_passkeys UPDATE (sign_count)", "metatrash_passkeys UPDATE (backed_up)", "metatrash_passkeys UPDATE (last_used_at)", "metatrash_passkeys UPDATE (name)",
	"metatrash_totp SELECT", "metatrash_totp INSERT", "metatrash_totp DELETE", "metatrash_totp UPDATE (enabled_at)", "metatrash_totp UPDATE (last_used_step)",
	"metatrash_recovery_codes SELECT", "metatrash_recovery_codes INSERT", "metatrash_recovery_codes DELETE",
}

// checkPrivileges compares the login's privileges in this database with
// accountGrants. Database-wide privileges count for every table, and table
// privileges for every column; anything beyond accountGrants is extra.
func (s *accountDatabase) checkPrivileges(ctx context.Context) (missing, extra []string, err error) {
	have := map[string]bool{}
	schemaWide := map[string]bool{}
	read := func(query string, add func(scan func(...any) error) error) error {
		return eachRow(ctx, s.db, add, query)
	}
	// information_schema shows only the current login's privileges.
	if err := read("SELECT privilege_type FROM information_schema.schema_privileges WHERE table_schema = DATABASE()", func(scan func(...any) error) error {
		var p string
		if err := scan(&p); err != nil {
			return err
		}
		schemaWide[p] = true
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("cannot read the login's privileges")
	}
	if err := read("SELECT table_name, privilege_type FROM information_schema.table_privileges WHERE table_schema = DATABASE()", func(scan func(...any) error) error {
		var t, p string
		if err := scan(&t, &p); err != nil {
			return err
		}
		have[t+" "+p] = true
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("cannot read the login's privileges")
	}
	if err := read("SELECT table_name, column_name, privilege_type FROM information_schema.column_privileges WHERE table_schema = DATABASE()", func(scan func(...any) error) error {
		var t, c, p string
		if err := scan(&t, &c, &p); err != nil {
			return err
		}
		have[t+" "+p+" ("+c+")"] = true
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("cannot read the login's privileges")
	}
	want := map[string]bool{}
	for _, g := range accountGrants {
		want[g] = true
		table, rest, _ := strings.Cut(g, " ")
		privilege, _, _ := strings.Cut(rest, " ")
		if !have[g] && !have[table+" "+privilege] && !schemaWide[privilege] {
			missing = append(missing, g)
		}
	}
	for g := range have {
		if !want[g] {
			table, rest, _ := strings.Cut(g, " ")
			privilege, column, _ := strings.Cut(rest, " ")
			// A column privilege under a granted table privilege is not extra.
			if column != "" && want[table+" "+privilege] {
				continue
			}
			extra = append(extra, g)
		}
	}
	for p := range schemaWide {
		extra = append(extra, "database-wide "+p)
	}
	sort.Strings(extra)
	return missing, extra, nil
}
