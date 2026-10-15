# mtsync

`mtsync` keeps folders on your computer in sync with metatrash spaces. Edit
the files with VS Code, git, grep or anything else while agents work in the
same spaces: their changes are downloaded as soon as they happen, and your
saves are uploaded within a few seconds.

One program is both a background service, which syncs every folder you
register, and the console you configure it with (like `mt` for the server).
It has no dependencies beyond the Go standard library and runs on Windows,
Linux and macOS.

## How it works

- **Sign-in.** `mtsync login` connects this computer to your account the same
  way Claude's connector does: the browser opens metatrash's consent page,
  where you choose which spaces it may use and whether it can write. The
  connection appears on Your account → **Services**, where you can change or
  revoke it. Access follows the usual rules: what you consented to, your
  ownership or membership of the space, the owner's per-member app permission
  and the token scope. A read-only space only downloads.
- **Sync.** mtsync keeps one request waiting on the server, which answers the
  moment the space changes (or after 25 seconds, and then mtsync asks again),
  so agents' changes arrive at once and an idle folder costs about two small
  requests a minute. When the space changes, mtsync asks for just the changes
  since the last sync (one request listing the changed and removed files) and
  downloads only the changed files; the whole space is listed only on the
  first sync. The folder itself is scanned every 5 seconds, without
  any network traffic unless something changed. Either one starts a pass.
  Against a server older than 0.34.0 it checks the space every 5 seconds
  instead. Each file is compared three ways: the folder, the
  space, and the version both last agreed on (kept in `.mtsync/state.json`).
  - Changed here: uploaded. Deleted here: deleted in the space.
  - Changed in the space: downloaded. Deleted there: deleted here (empty
    folders are removed).
  - Renamed here: a `move` in the space, so the file keeps its ID and history.
  - Changed on both sides: the space's version keeps the name and yours is
    saved beside it as `name.conflict-<UTC time>.ext`, then uploaded too.
- **What stays local.** Files a space can't hold (not UTF-8 text, over 64 KiB,
  names with spaces or a leading dot outside a GitHub folder) and editor or
  system files (`*.swp`, `*~`, `*.tmp`, `.DS_Store`, `Thumbs.db`, …). A `.git` folder is skipped entirely, so you can `git init` a synced folder for your own local history. Refusals
  are reported once and retried when the file changes. README.md and the root
  `.metatrash.json` can't be deleted in a space, so deleting them locally
  brings them back.
- **Safety.** Every write carries the space's state token, so an agent's
  change mid-pass is never overwritten: the pass starts again. A file saved
  within the last second waits for the next pass. Rate limits (`429`) are
  waited out.

## Build

Needs Go 1.25 or later. From the repository root:

```sh
go build -trimpath -o mtsync ./cmd/mtsync              # this computer (Linux, macOS)
go build -trimpath -o mtsync.exe ./cmd/mtsync          # this computer (Windows)
GOOS=windows GOARCH=amd64 go build -trimpath -o mtsync.exe ./cmd/mtsync
GOOS=linux   GOARCH=amd64 go build -trimpath -o mtsync-linux-amd64 ./cmd/mtsync
GOOS=darwin  GOARCH=arm64 go build -trimpath -o mtsync-macos ./cmd/mtsync
```

On Windows PowerShell, cross-compile with `$env:GOOS="linux"; go build ...`, then
`Remove-Item Env:GOOS` before building for Windows again. `go install ./cmd/mtsync`
puts the binary in `%USERPROFILE%\go\bin` instead.

Tests: `go test ./cmd/mtsync` runs the sync and the service against a real
metatrash service in memory (no database needed).

## Deploy

**Server.** mtsync needs metatrash 0.33.0 or later on the site: that release
adds the built-in sign-in client (`/oauth/clients/sync`) and the file hashes in
REST listings; 0.34.0 adds instant change notices (`after=` on list) and
0.37.0 change lists (`since=`), which older servers make up for with full
listings; `mtsync export` needs 0.38.0. Deploy
as usual (`upgrade.sh`). There is no schema, configuration or grant change,
and OAuth must already be enabled. The waiting request lasts 25 seconds, under
the example Apache config's `ProxyTimeout 60`; keep any proxy timeout above
30 seconds.

**Client.** Copy the binary somewhere on your `PATH`:

- Windows: `mtsync.exe` into a folder such as `C:\Tools` that is on `PATH`.
- Linux/macOS: `install -m 0755 mtsync ~/.local/bin/mtsync`.

Then, once per computer:

```sh
mtsync login       # choose spaces and access in the browser
mtsync install     # run the service at every login, and start it now
```

`install` runs the service as you (it needs your sign-in and writes files you
own), never as a system account, so it needs no administrator rights:

- Linux: a systemd user unit, `~/.config/systemd/user/mtsync.service`. It runs
  while you are logged in; `loginctl enable-linger $USER` keeps it running
  after logout.
- macOS: a LaunchAgent, `~/Library/LaunchAgents/com.metatrash.mtsync.plist`.
- Windows: a **Metatrash Sync** entry in your startup programs (the `HKCU\…\Run`
  registry key, listed under Settings → Apps → Startup) that runs
  `mtsync start`, which launches the service without a window.

`install` waits for the service to come up and says so if it does not; then
`mtsync log` (or `mtsync run`, which shows the same in the window) says why.

After replacing the binary, run `mtsync stop` and then `mtsync start`. `mtsync uninstall` stops it and removes
the login entry; registered folders are kept.

## Use

```sh
mtsync spaces                                 # the spaces this computer may use
mtsync add dave-zap/bartco ~/spaces/bartco    # register a folder; the service starts it
mtsync add dave-zap/notes ~/spaces/notes
mtsync add dave-zap/bartco/site ~/work/site   # just the site/ folder of a space
mtsync list                                   # each folder and how it is doing
mtsync log                                    # the last 40 lines of the service log
mtsync remove ~/spaces/notes                  # stop syncing it (or: mtsync remove dave-zap/notes)
mtsync export dave-zap/notes                  # back up the space to a zip here
```

To sync one folder of a space instead of all of it, add its path after the
space: `owner/slug/folder/sub` (or `public/inbox/dave`). The local folder then
holds that folder's contents, the rest of the space is left alone, and new
files are created inside it. Different folders of one space can go to
different places on your computer.

The running service picks up `add` and `remove` within a few seconds; there is
nothing to restart. `add` checks that this computer can reach the space and
refuses a folder inside, or containing, one already registered. It creates the
folder if needed. `remove` leaves the files where they are.

`mtsync export SPACE [FILE]` saves the whole space as it is now to a zip
(default `owner-slug-<date>-<state>.zip` in the current folder), whether or
not it is synced anywhere. The zip also holds `.metatrash-export.json`, which
records the state and each file's ID. It needs metatrash 0.38.0 on the site and
read access to the space; it takes a whole space, not a folder of it.

`mtsync list` shows, per folder, "in sync, checked <time>", the last error
(for example signed out, or the space no longer connected; the service tries
such a folder again every minute), or "service not running".

All commands:

| Command | What it does |
| --- | --- |
| `login`, `logout` | connect this computer to your account, or forget it |
| `spaces` | the spaces this computer may use, with access |
| `add SPACE[/PATH] FOLDER` | register a folder for a space, or for one folder of it |
| `remove FOLDER` or `remove SPACE` | unregister a folder |
| `list` | registered folders and their state |
| `install`, `uninstall` | start the service at every login (and now), or stop that |
| `start` | start the service in the background (no window) |
| `run` | run the service in this window instead (Ctrl+C stops it) |
| `stop` | stop the running service |
| `log [N]` | the last N lines of the service log |
| `export SPACE [FILE]` | save the space as it is now to a zip, for backups |
| `version` | the version |
| `SPACE[/PATH] FOLDER` | sync one folder in this window without registering it; `-once` for one pass |

Flags go before the command:

| Flag | Default | Meaning |
| --- | --- | --- |
| `-server` | `https://metatrash.com` | the metatrash site, for `login`, `logout`, `spaces`, `add`, `export` and a one-off sync |
| `-interval` | `5s` | time between scans of each folder (and between checks of an older server) |
| `-once` | off | one-off sync: one pass, then exit |
| `-key-env VAR` | | one-off sync and `export`: use the space key in environment variable `VAR` instead of sign-in (administrator key spaces) |

One local folder syncs one space (or one folder of it); a local folder already
synced with something else is refused. Only one mtsync works on a folder at a time (the service, or a
one-off sync), and only one service runs per user.

## Files it keeps

- `<folder>/.mtsync/state.json`: the last agreed version of each file. Never
  uploaded. Delete the `.mtsync` folder to start over; the next pass then
  treats every differing file as a conflict and keeps both versions.
- `<folder>/.mtsync/lock`: held while an mtsync syncs the folder.
- In the `metatrash` folder of your user config folder (`%AppData%` on
  Windows, `~/.config` on Linux, `~/Library/Application Support` on macOS),
  readable only by you:
  - `sync-credentials.json`: sign-in tokens per site. Refresh tokens rotate on
    every use; a lock file beside it keeps two processes from using the same
    one.
  - `sync-folders.json`: the registered folders (written by `add` and
    `remove`).
  - `sync-status.json`: what the service last saw of each folder, for `list`.
  - `mtsync.log` (and `mtsync.log.1`, the previous 5 MB): the service log.
  - `mtsync-service.lock`: held while the service runs.

A lock file left behind by a process that crashed is taken over after two
minutes.

## Limits

- Spaces hold text only: up to 1,000 files, 64 KiB each, 16 MiB in all.
- Each computer gets 120 reads and 20 writes a minute per space, so the first
  download of a large space, or saving many files at once, can take a few
  minutes. mtsync waits rather than failing.
- Windows treats `A.txt` and `a.txt` as the same file; a space can hold both.
  Avoid names that differ only in case.
- Local saves are found by scanning, so allow up to the interval for an upload.
