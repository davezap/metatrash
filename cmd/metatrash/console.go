package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
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
	"users rename":      "users rename <email|username|id> <new-username> [-no-notice]",
	"spaces list":       "spaces list [-json]",
	"spaces export":     "spaces export <owner/slug|id|name> [FILE|-] [-data DIR] | spaces export -all DIR [-data DIR]",
	"spaces restore":    "spaces restore <owner/slug|id>",
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
  users rename <who> <name>     change the account's username; addresses with the old
                                name stop working (emails a notice; -no-notice skips it)
  spaces list                   list owned spaces with owner, visibility and members
  spaces export <space> [FILE]  write the space as it is now to a zip (- for stdout;
                                default <owner>-<slug>-<date>-<state>.zip here)
  spaces export -all DIR        export every space into DIR, for backups
  spaces restore <space>        bring back a space its owner deleted in the last 30
                                days (restart the service afterwards)

<who> is an email address, username or user ID. list and show take -json.
<space> is owner/slug or a space ID, or a spaces.json name such as public.

Account commands read the same accounts.json and account database as the
service: -accounts-config, else METATRASH_ACCOUNTS_CONFIG, else
` + defaultAccountsConfig + `. check also reads -config (/etc/metatrash/spaces.json),
-keys (/etc/metatrash/keys.json) and -data (/var/lib/metatrash); spaces export
reads -data. Run them as
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
		return fmt.Errorf("users needs a subcommand: list, show, email-login, set-limit or rename")
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
	case "rename":
		noNotice = flags.Bool("no-notice", false, "Do not email the account a notice")
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
	case "rename":
		u, err := db.FindUser(ctx, rest[0])
		if err != nil {
			return err
		}
		username, changed, err := db.RenameUser(ctx, u, rest[1])
		if err != nil {
			return err
		}
		if !changed {
			fmt.Printf("%s is already %s. Nothing changed.\n", u.Email, username)
			return nil
		}
		fmt.Printf("%s is now %s (was %s).\n", u.Email, username, orDash(u.Username))
		if u.Username != "" && u.OwnedSpaces > 0 {
			fmt.Printf("Its owned spaces (%d) moved from /spaces/%s/… to /spaces/%s/…; the old addresses no longer work.\n", u.OwnedSpaces, u.Username, username)
		}
		if owner, _, _ := strings.Cut(db.DocsSpace(), "/"); u.Username != "" && owner == u.Username {
			fmt.Printf("accounts.json docsSpace is %s: change it to %s/… and restart the service.\n", db.DocsSpace(), username)
		}
		if *noNotice {
			return nil
		}
		if err := db.RenameNotice(ctx, u, username); err != nil {
			return fmt.Errorf("the change is made, but the notice was not sent: %v", err)
		}
		fmt.Println("The account was emailed a notice.")
		return nil
	}
	return nil
}

func spacesCommand(args []string) error {
	if len(args) > 0 && args[0] == "export" {
		return spacesExport(args[1:])
	}
	if len(args) > 0 && args[0] == "restore" {
		return spacesRestore(args[1:])
	}
	if len(args) == 0 || args[0] != "list" {
		printUsage(os.Stderr)
		return fmt.Errorf("spaces needs a subcommand: spaces list, spaces export or spaces restore")
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
		state := s.State
		if s.PurgeAt != nil {
			state += " (purged " + day(*s.PurgeAt) + ")"
		}
		fmt.Fprintf(t, "%s/%s\t%s\t%s\t%s\t%d\t%d\t%s\n", s.Owner, s.Slug, oneLine(s.Name), s.Visibility, state, s.Members, s.Invited, day(s.CreatedAt))
	}
	t.Flush()
	fmt.Println(count(len(spaces), "owned space", "owned spaces") + " (spaces.json spaces are not listed)")
	return nil
}

// spacesRestore makes a space its owner deleted ready again, while it is
// still kept. The running service loads it when it next starts.
func spacesRestore(args []string) error {
	flags := flag.NewFlagSet("spaces restore", flag.ContinueOnError)
	accountsConfig := accountsConfigFlag(flags)
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: metatrash %s", commandUsage["spaces restore"])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := service.OpenAdminDB(ctx, *accountsConfig)
	if err != nil {
		return err
	}
	defer db.Close()
	space, err := db.FindSpace(ctx, rest[0])
	if err != nil {
		return err
	}
	if err := db.RestoreSpace(ctx, space); err != nil {
		return err
	}
	fmt.Printf("Restored %s/%s (%s). Restart the service to open it: sudo systemctl restart metatrash\n", space.Owner, space.Slug, oneLine(space.Name))
	return nil
}

// spacesExport writes one space, or with -all every space, as a zip of its
// current files. It reads the repositories directly, beside the service.
func spacesExport(args []string) error {
	flags := flag.NewFlagSet("spaces export", flag.ContinueOnError)
	accountsConfig := accountsConfigFlag(flags)
	data := flags.String("data", "/var/lib/metatrash", "Persistent data directory")
	all := flags.String("all", "", "Export every space into this directory")
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if (*all == "" && (len(rest) < 1 || len(rest) > 2)) || (*all != "" && len(rest) != 0) {
		return fmt.Errorf("usage: metatrash %s", commandUsage["spaces export"])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *all != "" {
		return exportAll(ctx, *accountsConfig, *data, *all)
	}
	name, gitDir, err := findSpaceRepository(ctx, *accountsConfig, *data, rest[0])
	if err != nil {
		return err
	}
	state, b, err := service.ExportRepository(ctx, gitDir, name)
	if err != nil {
		return err
	}
	target := service.ExportFileName(name, state)
	if len(rest) == 2 {
		target = rest[1]
	}
	if target == "-" {
		_, err = os.Stdout.Write(b)
		return err
	}
	if err := writeExport(target, b); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Exported %s at %s to %s (%s).\n", name, state[:12], target, size(len(b)))
	return nil
}

// findSpaceRepository resolves owner/slug, a space ID or a spaces.json name
// to the space's name and its repository under data.
func findSpaceRepository(ctx context.Context, accountsConfig, data, ref string) (string, string, error) {
	ref = strings.ToLower(strings.Trim(ref, "/"))
	name, id := ref, ref
	owned := strings.Contains(ref, "/") || len(ref) == 32 && strings.Trim(ref, "0123456789abcdef") == ""
	if owned {
		db, err := service.OpenAdminDB(ctx, accountsConfig)
		if err != nil {
			return "", "", err
		}
		space, err := db.FindSpace(ctx, ref)
		db.Close()
		if err != nil {
			return "", "", err
		}
		name, id = space.Owner+"/"+space.Slug, space.ID
	}
	gitDir, err := service.SpaceRepositoryPath(data, owned, id)
	return name, gitDir, err
}

type spaceRepository struct{ name, gitDir string }

// allSpaceRepositories lists every spaces.json space and every ready owned
// space with its repository under data.
func allSpaceRepositories(ctx context.Context, accountsConfig, data string) ([]spaceRepository, error) {
	jobs := []spaceRepository{}
	names, err := service.ConfiguredSpaces(data)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		gitDir, _ := service.SpaceRepositoryPath(data, false, n)
		jobs = append(jobs, spaceRepository{n, gitDir})
	}
	db, err := service.OpenAdminDB(ctx, accountsConfig)
	if err != nil {
		return nil, err
	}
	spaces, err := db.Spaces(ctx)
	db.Close()
	if err != nil {
		return nil, err
	}
	for _, s := range spaces {
		if s.State != "ready" {
			continue
		}
		gitDir, err := service.SpaceRepositoryPath(data, true, s.ID)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, spaceRepository{s.Owner + "/" + s.Slug, gitDir})
	}
	return jobs, nil
}

// exportAll exports every ready owned space and every spaces.json space into
// dir. One failure does not stop the rest; the error counts them.
func exportAll(ctx context.Context, accountsConfig, data, dir string) error {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	jobs, err := allSpaceRepositories(ctx, accountsConfig, data)
	if err != nil {
		return err
	}
	failed := 0
	for _, j := range jobs {
		state, b, err := service.ExportRepository(ctx, j.gitDir, j.name)
		if err == nil {
			target := filepath.Join(dir, service.ExportFileName(j.name, state))
			if err = writeExport(target, b); err == nil {
				fmt.Printf("%s\t%s\t%s\n", j.name, state[:12], target)
				continue
			}
		}
		failed++
		fmt.Fprintf(os.Stderr, "%s: %v\n", j.name, err)
	}
	if failed > 0 {
		return fmt.Errorf("%s of %d failed", count(failed, "export", "exports"), len(jobs))
	}
	fmt.Fprintf(os.Stderr, "Exported %s.\n", count(len(jobs), "space", "spaces"))
	return nil
}

// writeExport writes b to path through a temporary file, readable only by
// the service user: private spaces are in it.
func writeExport(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".export-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func size(n int) string {
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
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
	switch {
	case u.DeletedAt != nil:
		fmt.Fprintf(t, "Deleted\t%s\n", minute(*u.DeletedAt))
	case u.DeletionRequestedAt != nil:
		fmt.Fprintf(t, "Deletion\tasked %s; purged after %s unless they sign in\n", minute(*u.DeletionRequestedAt), day(u.DeletionRequestedAt.Add(service.AccountDeletionGrace)))
	}
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
	if u.DeletedAt != nil {
		return "deleted"
	}
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
	if u.DeletionRequestedAt != nil {
		methods = append(methods, "deletion pending")
	}
	return strings.Join(methods, ", ")
}
