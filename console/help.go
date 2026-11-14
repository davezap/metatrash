package console

import (
	"fmt"
	"io"
	"strings"

	"metatrash.com/metatrash"
)

// HelpGroup is one top-level console command as help shows it. `help` lists
// every group on one line each; `help <name>` (also `<name> help`) prints its
// Detail.
type HelpGroup struct {
	// Name is the command, such as users.
	Name string
	// Summary says what the command is for, in a few words.
	Summary string
	// Subcommands are listed after the summary, such as list, show.
	Subcommands []string
	// Detail is the full help: one line per subcommand with its arguments,
	// then any notes. Each line starts with two spaces.
	Detail string
	// AccountConfig marks commands that read the account database, so their
	// help explains where its configuration comes from.
	AccountConfig bool
}

var helpGroups = []HelpGroup{
	{Name: "help", Summary: "show this help",
		Detail: `  help                          the command list
  help <command>                one command in full (same as <command> help)
  help all                      every command in full`},
	{Name: "version", Summary: "print the version (-all names every component and library)",
		Detail: `  version [-all] [-core]        print the version; -all adds every component and
                                library in this build, -core only Core's version`},
	{Name: "keygen", Summary: "print a new space key and its digest as JSON",
		Detail: `  keygen                        print a new space key and its digest as JSON`},
	{Name: "accounts-migrate", Summary: "create or import the account database (docs/deployment.md)",
		Detail: `  accounts-migrate -database-config FILE -data DIR [-empty]
                                create or import the account database; -empty
                                starts one with no legacy accounts.json`},
	{Name: "check", Summary: "run the service's startup checks without starting it", AccountConfig: true,
		Detail: `  check [-config FILE] [-keys FILE] [-data DIR]
                                config files, database schema and grants, space
                                repositories. -config defaults to
                                /etc/metatrash/spaces.json, -keys to
                                /etc/metatrash/keys.json, -data to /var/lib/metatrash`},
	{Name: "search-index", Summary: "the search index (docs/search-index.md)",
		Subcommands: []string{"status", "rebuild"},
		Detail: `  search-index status [<space>] [-json]
                                files indexed, files waiting, size and last
                                update of each space's index (c-<name> for a
                                configured space, o-<id> for an owned one)
  search-index rebuild <space>|-all
                                ask the running server to build an index again
                                (it scans meanwhile). Both take -config FILE
                                (default /etc/metatrash/spaces.json) and -data
                                DIR (default /var/lib/metatrash); run them as
                                root or the service account`},
	{Name: "users", Summary: "accounts", AccountConfig: true,
		Subcommands: []string{"list", "show", "usage", "email-login", "set-limit", "rename"},
		Detail: `  users list [-json]            list accounts with their spaces and sign-in methods
  users show <who> [-json]      one account in full: passkeys, spaces, memberships,
                                invitations, connected apps, GitHub
  users usage [<who>] [-json]   storage per account, largest first: the spaces it
                                owns (Git with history, plus virtual files), as
                                last measured
  users email-login on|off <who> [-no-notice]
                                turn sign-in by emailed code on or off and email
                                the account a notice (-no-notice skips it)
  users set-limit <who> <n>     set how many private spaces the account may own
  users rename <who> <name> [-no-notice]
                                change the account's username; addresses with the
                                old name stop working (emails a notice)
  <who> is an email address, username or user ID.`},
	{Name: "spaces", Summary: "spaces", AccountConfig: true,
		Subcommands: []string{"list", "usage", "export", "restore", "rename"},
		Detail: `  spaces list [-json]           list owned spaces with owner, visibility and members
  spaces usage [<who>] [-json]  storage per owned space, largest first (one
                                account's spaces with <who>)
  spaces export <space> [FILE|-] [-data DIR]
                                write the space as it is now to a zip (- for
                                stdout; default <owner>-<slug>-<date>-<state>.zip)
  spaces export -all DIR [-data DIR]
                                export every space into DIR, for backups
  spaces restore <space>        bring back a space its owner deleted in the last
                                30 days (restart the service afterwards)
  spaces rename <space> <name> [-slug NEW-SLUG]
                                give a space a new name; -slug also moves it to
                                owner/NEW-SLUG, and the old address keeps leading
                                to it
  <space> is owner/slug or a space ID, or a spaces.json name such as public.
  <who> is an email address, username or user ID. -data defaults to
  /var/lib/metatrash.`},
}

var helpNotes []string

// AddHelp adds console commands to help, after Core's, so a build that adds
// its own commands lists them too. A group whose Name is already listed adds
// its subcommands and detail to that group (such as more users subcommands).
func AddHelp(groups ...HelpGroup) {
	for _, g := range groups {
		if i := helpIndex(g.Name); i >= 0 {
			h := &helpGroups[i]
			h.Subcommands = append(h.Subcommands, g.Subcommands...)
			if g.Detail != "" {
				h.Detail = strings.TrimRight(h.Detail, "\n") + "\n" + g.Detail
			}
			h.AccountConfig = h.AccountConfig || g.AccountConfig
			continue
		}
		helpGroups = append(helpGroups, g)
	}
}

// AddHelpNote adds a paragraph to the account configuration note that help
// prints for commands that read the account database.
func AddHelpNote(note string) { helpNotes = append(helpNotes, strings.TrimSpace(note)) }

func helpIndex(name string) int {
	for i, g := range helpGroups {
		if g.Name == name {
			return i
		}
	}
	return -1
}

// IsHelp reports whether arg asks for help: help, -h, -help or --help.
func IsHelp(arg string) bool {
	switch arg {
	case "help", "-h", "-help", "--help":
		return true
	}
	return false
}

// HasHelp reports whether name is a command help knows.
func HasHelp(name string) bool { return helpIndex(name) >= 0 }

func printUsage(w io.Writer) {
	fmt.Fprintf(w, "metatrash %s\n\n", metatrash.Version)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  mt <command> [arguments]      run a console command as the service user")
	fmt.Fprintln(w, "  mt <command> help             one command in full, with its arguments")
	fmt.Fprintln(w, "  mt help all                   every command in full")
	fmt.Fprintln(w, "  metatrash [server flags]      start the REST/MCP service (metatrash -h lists the flags)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	width := 0
	for _, g := range helpGroups {
		width = max(width, len(g.Name))
	}
	for _, g := range helpGroups {
		line := g.Summary
		if len(g.Subcommands) > 0 {
			line += ": " + strings.Join(g.Subcommands, ", ")
		}
		fmt.Fprintf(w, "  %-*s  %s\n", width, g.Name, line)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Example: mt users help")
}

// printCommandHelp prints one command's help, or every command's with "all".
func printCommandHelp(w io.Writer, name string) error {
	if name == "all" {
		fmt.Fprintf(w, "metatrash %s\n", metatrash.Version)
		for _, g := range helpGroups {
			fmt.Fprintf(w, "\n%s\n", g.Name)
			fmt.Fprintln(w, strings.TrimRight(g.Detail, "\n"))
		}
		fmt.Fprintln(w)
		printConfigNote(w)
		return nil
	}
	i := helpIndex(name)
	if i < 0 {
		printUsage(w)
		return fmt.Errorf("no help for %q", name)
	}
	g := helpGroups[i]
	fmt.Fprintf(w, "%s: %s\n\n", g.Name, g.Summary)
	fmt.Fprintln(w, strings.TrimRight(g.Detail, "\n"))
	if g.AccountConfig {
		fmt.Fprintln(w)
		printConfigNote(w)
	}
	return nil
}

func printConfigNote(w io.Writer) {
	fmt.Fprintln(w, `Commands that use the account database read the same accounts.json as the
service: -accounts-config, else METATRASH_ACCOUNTS_CONFIG, else
`+defaultAccountsConfig+`. Run them as the service user, which mt does.`)
	for _, note := range helpNotes {
		fmt.Fprintln(w)
		fmt.Fprintln(w, note)
	}
}
