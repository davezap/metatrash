# Core 0.95.30: mtsync synced folders follow the move

Prepared 2026-11-14 by Claude, driven by Company 0.96.46's manifest.

Tier: Core (mtsync).

0.95.29 moved mtsync's registry and saved sign-ins to https://collumii.com,
but not each folder's `.mtsync/state.json`, which also names the server.
The service refused those folders with "already syncs … on
https://metatrash.com; use another folder". `openSyncer` now maps a moved
server (`currentServer`) when it reads the state, and the next save writes
the new address.

Files: `cmd/mtsync/sync.go`, `cmd/mtsync/moved_test.go`, `assets.go`,
`CHANGELOG.md`, this file. No server, schema or grant change.

Workaround for folders on 0.95.29: change `"server":"https://metatrash.com"`
to `"server":"https://collumii.com"` in the folder's `.mtsync/state.json`.
