package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"metatrash.com/metatrash"
	"metatrash.com/metatrash/internal/service"
)

// defaultAccountsConfig is where deploy/metatrash.service points
// METATRASH_ACCOUNTS_CONFIG.
const defaultAccountsConfig = "/etc/metatrash/accounts.json"

// commandUsage is each command's synopsis, in the order help lists them.
var commandUsage = map[string]string{
	"help":              "help",
	"version":           "version",
	"keygen":            "keygen",
	"accounts-migrate":  "accounts-migrate -database-config FILE -data DIR [-empty]",
	"check":             "check [-config FILE] [-keys FILE] [-data DIR]",
	"users list":        "users list [-json]",
	"users show":        "users show <email|username|id> [-json]",
	"users email-login": "users email-login <on|off> <email|username|id> [-no-notice]",
	"users set-limit":   "users set-limit <email|username|id> <spaces>",
	"spaces list":       "spaces list [-json]",
}

const usage = `metatrash %s

Usage:
  metatrash [server flags]      start the REST/MCP service (metatrash -h lists the flags)
  metatrash <command> [flags]

Commands:
  help                          show this help
  version                       print the version
  keygen                        print a new space key and its digest as JSON
  accounts-migrate              create or import the account database (docs/deployment.md)
  check                         run the service's startup checks without starting it:
                                config files, database schema and grants, space repositories
  users list                    list accounts with their spaces and sign-in methods
  users show <who>              one account in full: passkeys, spaces, memberships,
                                invitations, connected apps, GitHub
  users email-login on <who>    turn sign-in by emailed code back on (or off) and email
                                the account a notice (-no-notice to skip it)
  users set-limit <who> <n>     set how many private spaces the account may own
  spaces list                   list owned spaces with owner, visibility and members

<who> is an email address, username or user ID. list and show take -json.

Account commands read the same accounts.json and account database as the
service: -accounts-config, else METATRASH_ACCOUNTS_CONFIG, else
` + defaultAccountsConfig + `. check also reads -config (/etc/metatrash/spaces.json),
-keys (/etc/metatrash/keys.json) and -data (/var/lib/metatrash). Run them as
the service user, which the mt shortcut does: mt users list
`

func printUsage(w io.Writer) { fmt.Fprintf(w, usage, metatrash.Version) }

// runCommand runs a console command; the service starts only when the first
// argument is a flag or absent.
func runCommand(name string, args []string) error {
	switch name {
	case "help":
		printUsage(os.Stdout)
		return nil
	case "version":
		if len(args) != 0 {
			return fmt.Errorf("version takes no arguments")
		}
		fmt.Println(metatrash.Version)
		return nil
	case "keygen":
		if len(args) != 0 {
			return fmt.Errorf("keygen takes no arguments")
		}
		key, digest, err := service.GenerateKey()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"key": key, "sha256": digest})
	case "accounts-migrate":
		return accountsMigrate(args)
	case "users":
		return usersCommand(args)
	case "spaces":
		return spacesCommand(args)
	case "check":
		return checkCommand(args)
	}
	printUsage(os.Stderr)
	return fmt.Errorf("unknown command %q", name)
}

func accountsMigrate(args []string) error {
	flags := flag.NewFlagSet("accounts-migrate", flag.ContinueOnError)
	databaseConfig := flags.String("database-config", "", "Protected database configuration file (required)")
	data := flags.String("data", "", "Service data directory containing accounts.json (required)")
	empty := flags.Bool("empty", false, "Initialize an installation with no legacy accounts.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *databaseConfig == "" || *data == "" {
		return fmt.Errorf("accounts-migrate requires -database-config and -data, with optional -empty")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	count, err := service.MigrateAccounts(ctx, *databaseConfig, *data, *empty)
	if err != nil {
		return err
	}
	fmt.Printf("Account migration complete: %d legacy accounts imported or verified. Source file unchanged.\n", count)
	return nil
}

// parseArgs parses flags placed before, between or after the arguments.
func parseArgs(flags *flag.FlagSet, args []string) ([]string, error) {
	rest := []string{}
	for {
		if err := flags.Parse(args); err != nil {
			return nil, err
		}
		args = flags.Args()
		if len(args) == 0 {
			return rest, nil
		}
		rest = append(rest, args[0])
		args = args[1:]
	}
}

func usersCommand(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return fmt.Errorf("users needs a subcommand: list, show, email-login or set-limit")
	}
	name := "users " + args[0]
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	accountsConfig := accountsConfigFlag(flags)
	var asJSON, noNotice *bool
	var want int
	switch args[0] {
	case "list":
		asJSON = flags.Bool("json", false, "Print JSON instead of a table")
	case "show":
		asJSON = flags.Bool("json", false, "Print JSON instead of text")
		want = 1
	case "email-login":
		noNotice = flags.Bool("no-notice", false, "Do not email the account a notice")
		want = 2
	case "set-limit":
		want = 2
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", name)
	}
	rest, err := parseArgs(flags, args[1:])
	if err != nil {
		return err
	}
	if len(rest) != want {
		return fmt.Errorf("usage: metatrash %s", commandUsage[name])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := service.OpenAdminDB(ctx, *accountsConfig)
	if err != nil {
		return err
	}
	defer db.Close()
	switch args[0] {
	case "list":
		users, err := db.Users(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(users)
		}
		printUsers(os.Stdout, users)
		return nil
	case "show":
		u, err := db.FindUser(ctx, rest[0])
		if err != nil {
			return err
		}
		detail, err := db.UserDetail(ctx, u)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(detail)
		}
		printUser(os.Stdout, detail)
		return nil
	case "email-login":
		if rest[0] != "on" && rest[0] != "off" {
			return fmt.Errorf("usage: metatrash %s", commandUsage[name])
		}
		on := rest[0] == "on"
		u, err := db.FindUser(ctx, rest[1])
		if err != nil {
			return err
		}
		changed, err := db.SetEmailLogin(ctx, u, on)
		if err != nil {
			return err
		}
		if !changed {
			fmt.Printf("Email sign-in was already %s for %s. Nothing changed.\n", rest[0], u.Email)
			return nil
		}
		fmt.Printf("Email sign-in is now %s for %s.\n", rest[0], u.Email)
		if *noNotice {
			return nil
		}
		if err := db.EmailLoginNotice(ctx, u, on); err != nil {
			// SMTP responses may contain addresses; the change itself stands.
			return fmt.Errorf("the change is made, but the notice was not sent: %v", err)
		}
		fmt.Println("The account was emailed a notice.")
		return nil
	case "set-limit":
		limit, err := strconv.Atoi(rest[1])
		if err != nil {
			return fmt.Errorf("usage: metatrash %s", commandUsage[name])
		}
		u, err := db.FindUser(ctx, rest[0])
		if err != nil {
			return err
		}
		if err := db.SetSpaceLimit(ctx, u, limit); err != nil {
			return err
		}
		fmt.Printf("%s may now own %s (was %d; owns %d).\n", u.Email, count(limit, "private space", "private spaces"), u.MaxPrivateSpaces, u.OwnedSpaces)
		if u.OwnedSpaces > limit {
			fmt.Println("Spaces it already owns stay; it cannot create more until it is under the limit.")
		}
		return nil
	}
	return nil
}

func spacesCommand(args []string) error {
	if len(args) == 0 || args[0] != "list" {
		printUsage(os.Stderr)
		return fmt.Errorf("spaces needs a subcommand: spaces list")
	}
	flags := flag.NewFlagSet("spaces list", flag.ContinueOnError)
	accountsConfig := accountsConfigFlag(flags)
	asJSON := flags.Bool("json", false, "Print JSON instead of a table")
	rest, err := parseArgs(flags, args[1:])
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("usage: metatrash %s", commandUsage["spaces list"])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := service.OpenAdminDB(ctx, *accountsConfig)
	if err != nil {
		return err
	}
	defer db.Close()
	spaces, err := db.Spaces(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(spaces)
	}
	t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "SPACE\tNAME\tVISIBILITY\tSTATE\tMEMBERS\tINVITED\tCREATED")
	for _, s := range spaces {
		fmt.Fprintf(t, "%s/%s\t%s\t%s\t%s\t%d\t%d\t%s\n", s.Owner, s.Slug, oneLine(s.Name), s.Visibility, s.State, s.Members, s.Invited, day(s.CreatedAt))
	}
	t.Flush()
	fmt.Println(count(len(spaces), "owned space", "owned spaces") + " (spaces.json spaces are not listed)")
	return nil
}

func checkCommand(args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	o := service.CheckOptions{}
	flags.StringVar(&o.SpacesConfig, "config", "/etc/metatrash/spaces.json", "Space configuration")
	flags.StringVar(&o.Keys, "keys", "/etc/metatrash/keys.json", "Private key digest JSON file")
	flags.StringVar(&o.Data, "data", "/var/lib/metatrash", "Persistent data directory")
	accountsConfig := accountsConfigFlag(flags)
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("usage: metatrash %s", commandUsage["check"])
	}
	o.AccountsConfig = *accountsConfig
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	failed := 0
	t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, r := range service.Check(ctx, o) {
		mark := "ok"
		if !r.OK {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(t, "%s\t%s\t%s\n", mark, r.Name, r.Detail)
	}
	t.Flush()
	if failed > 0 {
		return fmt.Errorf("%s failed", count(failed, "check", "checks"))
	}
	fmt.Println("All checks passed.")
	return nil
}

func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func accountsConfigFlag(flags *flag.FlagSet) *string {
	value := os.Getenv("METATRASH_ACCOUNTS_CONFIG")
	if value == "" {
		value = defaultAccountsConfig
	}
	return flags.String("accounts-config", value, "Account configuration file")
}

func printUsers(w io.Writer, users []service.AdminUser) {
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "USERNAME\tEMAIL\tCREATED\tSPACES\tMEMBER OF\tSIGN-IN")
	for _, u := range users {
		fmt.Fprintf(t, "%s\t%s\t%s\t%d/%d\t%d\t%s\n", orDash(u.Username), u.Email, day(u.CreatedAt), u.OwnedSpaces, u.MaxPrivateSpaces, u.Memberships, signInMethods(u))
	}
	t.Flush()
	fmt.Fprintln(w, count(len(users), "account", "accounts"))
}

func printUser(w io.Writer, u service.AdminUserDetail) {
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(t, "Email\t%s\n", u.Email)
	fmt.Fprintf(t, "Username\t%s\n", orDash(u.Username))
	fmt.Fprintf(t, "User ID\t%s\n", u.ID)
	fmt.Fprintf(t, "Created\t%s\n", minute(u.CreatedAt))
	fmt.Fprintf(t, "Spaces\t%d of %d allowed\n", u.OwnedSpaces, u.MaxPrivateSpaces)
	email := "on"
	if !u.EmailLogin {
		email = "off"
	}
	fmt.Fprintf(t, "Email sign-in\t%s\n", email)
	app := "none"
	switch {
	case !u.AppEnabledAt.IsZero():
		app = "on since " + day(u.AppEnabledAt)
	case u.AppSetupUnfinished:
		app = "setup started, not finished (does not sign in)"
	}
	fmt.Fprintf(t, "Authenticator app\t%s\n", app)
	codes := "none"
	if u.RecoveryCodes > 0 {
		codes = fmt.Sprintf("%d left, created %s", u.RecoveryCodes, day(u.RecoveryCodesCreated))
	}
	fmt.Fprintf(t, "Recovery codes\t%s\n", codes)
	t.Flush()
	section(w, "Passkeys", len(u.PasskeyList), func(t *tabwriter.Writer) {
		for _, p := range u.PasskeyList {
			used := "never used"
			if !p.LastUsedAt.IsZero() {
				used = "last used " + day(p.LastUsedAt)
			}
			synced := ""
			if p.BackedUp {
				synced = "synced"
			}
			fmt.Fprintf(t, "  %s\tadded %s\t%s\t%s\n", oneLine(p.Name), day(p.CreatedAt), used, synced)
		}
	})
	section(w, "Owned spaces", len(u.Spaces), func(t *tabwriter.Writer) {
		for _, s := range u.Spaces {
			fmt.Fprintf(t, "  %s/%s\t%s\t%s\t%s\t%s\n", s.Owner, s.Slug, oneLine(s.Name), s.Visibility, count(s.Members, "member", "members"), count(s.Invited, "invited", "invited"))
		}
	})
	section(w, "Member of", len(u.MemberOf), func(t *tabwriter.Writer) {
		for _, m := range u.MemberOf {
			fmt.Fprintf(t, "  %s\t%s\tapps %s\tjoined %s\n", m.Space, m.Status, strings.ReplaceAll(m.AgentPermission, "_", "-"), day(m.JoinedAt))
		}
	})
	section(w, "Invitations waiting", len(u.Invitations), func(t *tabwriter.Writer) {
		for _, i := range u.Invitations {
			fmt.Fprintf(t, "  %s\texpires %s\n", i.Space, day(i.ExpiresAt))
		}
	})
	section(w, "Connected apps", len(u.Apps), func(t *tabwriter.Writer) {
		for _, a := range u.Apps {
			fmt.Fprintf(t, "  %s\t%s\tlast used %s\texpires %s\n", oneLine(a.Name), a.Scope, day(a.LastUsedAt), day(a.ExpiresAt))
		}
	})
	section(w, "GitHub", len(u.GitHub), func(t *tabwriter.Writer) {
		for _, g := range u.GitHub {
			fmt.Fprintf(t, "  %s\tconnected by %s\t%s\n", g.Account, g.GitHubLogin, g.Status)
		}
	})
	section(w, "Signed in", len(u.Sessions), func(t *tabwriter.Writer) {
		for _, s := range u.Sessions {
			fmt.Fprintf(t, "  %s\t%s\tsince %s\tlast used %s\n", orDash(oneLine(s.Device)), orDash(s.IP), minute(s.CreatedAt), minute(s.LastUsedAt))
		}
	})
}

func section(w io.Writer, title string, n int, rows func(*tabwriter.Writer)) {
	if n == 0 {
		fmt.Fprintf(w, "\n%s: none\n", title)
		return
	}
	fmt.Fprintf(w, "\n%s:\n", title)
	t := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	rows(t)
	t.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func day(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02")
}

func minute(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// oneLine keeps user-chosen names (spaces, passkeys, apps) from moving the
// terminal's cursor or breaking a table row.
func oneLine(s string) string {
	return strings.Map(func(c rune) rune {
		if c < 32 || c == 127 || (c >= 0x80 && c < 0xa0) || c == 0x2028 || c == 0x2029 {
			return ' '
		}
		return c
	}, s)
}

func signInMethods(u service.AdminUser) string {
	methods := []string{"email"}
	if !u.EmailLogin {
		methods[0] = "email off"
	}
	if u.Passkeys > 0 {
		methods = append(methods, count(u.Passkeys, "passkey", "passkeys"))
	}
	if u.AuthenticatorApp {
		methods = append(methods, "app")
	}
	if u.RecoveryCodes > 0 {
		methods = append(methods, count(u.RecoveryCodes, "recovery code", "recovery codes"))
	}
	return strings.Join(methods, ", ")
}
