# Core 0.95.24: space gates

Prepared 2026-11-11 by Claude. Claude ran `go vet` and the full `go test ./...`
(with MariaDB 10.11 for the database tests, on a Core-only database) in a
scratch copy of v0.95.23. Pending David: the release helper, commit, tag/push
and Actions, which Company 0.96.33's manifest drives.

## What changes

Step 3 of Company `docs/roadmap-organisations.md` (pinning an organisation to
its Microsoft tenants) needs Core to ask "may this person use this owner's
spaces right now?" before opening one. Core gains one extension hook:

- `extension.Hooks.SpaceGate(ctx, userID, ownerID) error`, asked for spaces
  the person doesn't own themselves, on the website (site API and the
  space's own pages and files), for apps and for Sync. nil lets Core's usual
  checks decide; a `*server.Error` is shown to the person as it is; any other
  error refuses as "unavailable".
- A gate only takes access away. A space readable on the web stays readable
  by everyone, read-only for someone held back.
- Held-back spaces are left out of the `spaces` tool, My Submissions and the
  to-do count, and stay listed on the website, where opening one says why.
- Managing a space you act for, and moving it out or in, need what opening it
  needs.
- Answers are cached per person and owner for 10 seconds.

Without an extension that sets `SpaceGate` (Core on its own, or Company
without tenant pinning turned on) nothing changes.

No schema, grants, configuration or dependency changes.

## Release

Through Company 0.96.33's manifest (`release-laptop.ps1 -Version 0.96.33`),
which commits and tags Core v0.95.24 first.
