# Core 0.95.25: pages people see when a space is held back or a connection ends, and a guarded append

Prepared 2026-11-11 by Claude. Claude ran `gofmt`, `go vet` and the full
`go test ./...` (with MariaDB for the database tests, on a Core-only
database) in a scratch copy of v0.95.24. Pending Dave: the release helper,
commit, tag/push and Actions, which Company 0.96.34's manifest drives.

## What changes

Found while testing organisations on 2026-11-11. Tier: Core (what any
instance's sign-in and connect pages need); the message on the held-back
page comes from the extension.

- Opening a held-back space in the browser shows a page with the reason and
  the way to fix it (Sign-in settings, Your spaces, Sign out), not JSON.
  Programs, `?raw`, `?format=md` and `?export` still get the JSON.
- Connect an app: "Create your first space" sits below "Choose spaces".
- A repeated press of Connect or Cancel within a minute goes back to the app
  with the first press's answer instead of "This connection request
  expired".
- Signing in during a connection restarts its ten minutes.
- The expired page says requests also end when Metatrash restarts, and what
  to do next.
- `append` takes an optional `ifBlob`: it adds only to the file version the
  caller last saw (409 `conflict` otherwise), so a resent append can't add
  twice. Tool schema 0.19.0.

No schema, grants, configuration or dependency changes.

## Release

Through Company 0.96.34's manifest (`release-laptop.ps1 -Version 0.96.34`),
which commits and tags Core v0.95.25 first.
