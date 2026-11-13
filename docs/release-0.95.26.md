# Core 0.95.26: billing gates

Prepared 2026-11-13 by Claude. Claude ran `gofmt`, `go vet` and the full
`go test ./...` (with MariaDB for the database tests, on a Core-only
database) in a scratch copy of v0.95.25. Pending Dave: the release helper,
commit, tag/push and Actions, which Company 0.96.35's manifest drives.

## What changes

Release 1 of the seats and billing plan. Tier: Core, because any instance
may need to limit what an account does or stores, or who may sign up; the
rules come from an extension (Company billing).

- `extension.Hooks` gains `WriteGate`, `SpaceCreateGate` and
  `AdmitAccount`. Core asks them before committing a change to an owned
  space (every path: website, apps, Sync, folder actions, GitHub folders,
  virtual file refreshes), before creating a space, and before making a new
  account at a first sign-in.
- A refusal that is a `*server.Error` is answered as it is; anything else
  is logged and refuses as 503 `unavailable`.
- Folder actions, GitHub auto-pull and virtual file refreshes record the
  account they act for, so the write gate knows who is writing.

Without an extension that sets the hooks, nothing changes. No schema,
grants, configuration, tool schema or dependency changes.

## Release

Through Company 0.96.35's manifest (`release-laptop.ps1 -Version 0.96.35`),
which commits and tags Core v0.95.26 first.
