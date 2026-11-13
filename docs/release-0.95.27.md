# Core 0.95.27: extensions can send email; shorter console help

Prepared 2026-11-13 by Claude. Claude ran `gofmt`, `go vet ./...` and
`go test ./...` in a scratch copy of v0.95.26 (the database tests skip
without MariaDB; nothing here touches the database). Pending Dave: the
release helper, commit, tag/push and Actions, which Company 0.96.38's
manifest drives.

## What changes

Part of release 3 of the seats and billing plan (the controlled launch).
Tier: Core, because any instance's extensions may need to send an email,
and the mail settings and password already live in Core.

- The `extension.Host` given to modules also implements the new
  `extension.Mailer` interface: `SendMail(ctx, to, subject, body)` sends one
  plain-text message through accounts.json's `smtp*` settings, the same
  path as sign-in codes and security notices.
- Errors wrapping `extension.ErrMailRejected` are permanent (bad or
  reserved address, bad subject, a 5xx answer); others may be temporary.

Also (Dave's ask, 2026-11-13): console help. Tier: Core, because the
console and its help are Core's; Company only registers its own commands.

- `mt help` lists each command on one line with its subcommands.
- `mt <command> help` (also `mt help <command>`, `-h`, `--help`) prints
  one command in full; `mt help all` prints everything.
- `mt users` / `mt spaces` with no subcommand print that command's help.
- New `console.AddHelp` and `console.AddHelpNote` let a build add its
  commands to the list (Company adds billing, launch, orgs and
  `users comp|uncomp`).

Check after installing: `mt help`, `mt users help`, `mt help all`.

Nothing changes for an instance without an extension that sends mail. No
schema, grants, configuration, tool schema or dependency changes.

## Release

Through Company 0.96.38's manifest (`release-laptop.ps1 -Version 0.96.38`),
which commits and tags Core v0.95.27 first.

## Rollback

Install the previous Core and Company tags together (v0.95.26 with Company
v0.96.37). Nothing is stored by this change.
