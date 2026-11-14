# Core 0.95.29: after the move to collumii.com

Prepared 2026-11-14 by Claude. Claude ran `gofmt`, `go vet` and the full
`go test ./...` against a disposable MariaDB 11 (schema and grants from
`deploy/`) in a scratch copy of v0.95.28: all passed. Pending Dave: the
release helper, commit, tag/push and Actions, driven by Company 0.96.45's
manifest.

Tier: Core. The footer and space renaming are useful to any instance;
mtsync's default server is the hosted service, which moved.

## What changes

- **Footers up to three lines.** `checkFooter` accepts `\n` between lines,
  at most `maxFooterLines` (3); other control characters stay refused.
  Tool schema 0.19.1.
- **mtsync's default server is https://collumii.com.** `movedServers`
  maps `https://metatrash.com` to it for the registry (`sync-folders.json`),
  saved sign-ins (`sync-credentials.json`) and `-server`.
- **`metatrash spaces rename <space> <name> [-slug NEW-SLUG]`.** Changes the
  name, and with `-slug` the address; the old owner/slug stays as an alias.
  The running service sees it at once (names and addresses are read per
  request). If the space is the docs space, change `docsSpace` in the
  accounts configuration and restart.
- Folder actions' User-Agent points at the project on GitHub.

## Deploy notes

- **Grant first:** `sudo mariadb metatrash < deploy/account-grants.sql`
  (adds `UPDATE (name)` on `metatrash_spaces`). The service's startup check
  lists required grants, so install the binary only after this.
- No schema change. Older mtsync keeps working against collumii.com once
  given `-server https://collumii.com`.
