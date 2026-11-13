package console

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpListsGroupsAndAddedCommands(t *testing.T) {
	defer func(g []HelpGroup, n []string) { helpGroups, helpNotes = g, n }(append([]HelpGroup(nil), helpGroups...), helpNotes)
	AddHelp(
		HelpGroup{Name: "users", Subcommands: []string{"comp"}, Detail: "  users comp <who>              comp it"},
		HelpGroup{Name: "billing", Summary: "plans", Subcommands: []string{"show"}, Detail: "  billing show", AccountConfig: true},
	)
	AddHelpNote("Company note.")

	var top bytes.Buffer
	printUsage(&top)
	for _, want := range []string{"users             accounts: list, show, usage, email-login, set-limit, rename, comp", "billing           plans: show", "mt users help"} {
		if !strings.Contains(top.String(), want) {
			t.Errorf("help lacks %q:\n%s", want, top.String())
		}
	}
	if strings.Contains(top.String(), "users show <who>") {
		t.Errorf("top-level help shows subcommand detail:\n%s", top.String())
	}

	var users bytes.Buffer
	if err := printCommandHelp(&users, "users"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"users show <who> [-json]", "users comp <who>", "METATRASH_ACCOUNTS_CONFIG", "Company note."} {
		if !strings.Contains(users.String(), want) {
			t.Errorf("users help lacks %q:\n%s", want, users.String())
		}
	}

	var all bytes.Buffer
	if err := printCommandHelp(&all, "all"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"spaces restore <space>", "billing show", "keygen"} {
		if !strings.Contains(all.String(), want) {
			t.Errorf("help all lacks %q", want)
		}
	}
	if err := printCommandHelp(&bytes.Buffer{}, "nope"); err == nil {
		t.Error("help nope succeeded")
	}
}

func TestEveryCommandHasHelp(t *testing.T) {
	for _, name := range []string{"help", "version", "keygen", "accounts-migrate", "check", "users", "spaces"} {
		if !HasHelp(name) {
			t.Errorf("no help for %s", name)
		}
	}
	for usage := range commandUsage {
		if g, _, _ := strings.Cut(usage, " "); !HasHelp(g) {
			t.Errorf("no help group for %s", usage)
		}
	}
}
