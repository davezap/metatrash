# Deployment and operations

Target: Amazon Linux 2023 with Apache HTTPS in front of one Go service on
`127.0.0.1:8080`. Build with Go 1.25+ (modules are pinned in `go.sum`); the
service needs Git at runtime and MariaDB only when accounts are enabled.

## Live server

- Amazon Linux 2023, aarch64. Checkout at `/home/ec2-user/metatrash`, cloned from
  `davezap/metatrash` with a read-only deploy key (`~/.ssh/metatrash_deploy`).
- Accounts and OAuth enabled: a systemd override sets
  `METATRASH_ACCOUNTS_CONFIG=/etc/metatrash/accounts.json`, which contains
  `"oauth": {"enabled": true}`. Database `metatrash` at schema v17 (from 0.56.0); 0.57.0 needs v18.
- Whole-domain Apache proxy from `deploy/apache-metatrash.conf.example`.

## Layout

| Item | Location |
| --- | --- |
| Executable | `/usr/local/bin/metatrash` (root, 0755) |
| Configuration | `/etc/metatrash/` (root:metatrash, 0750; files 0640) |
| Spaces, keys | `spaces.json`, `keys.json` |
| Accounts | `accounts.json`, `account-database.json`, `smtp-password`, `database-password`, `totp-key` |
| Folder actions (optional) | `agents-key` (seals users' agent API keys) |
| GitHub App (optional) | `github-app.pem`, `github-client-secret`, `github-webhook-secret` |
| Data | `/var/lib/metatrash` (metatrash, 0700): `repos/`, `owned-repos/`, `.service-lock` |
| Backups (0.53.0) | `/usr/local/sbin/metatrash-backup`, settings in `/etc/metatrash-backup/` (root, 0700), staging in `/var/backups/metatrash`; see Backups |
| Unit | `/etc/systemd/system/metatrash.service` |

Command line: `metatrash [-listen 127.0.0.1:8080] [-data DIR] [-config spaces.json]
[-keys keys.json] [-accounts-config accounts.json] [-trusted-proxies CIDRs]
[-public-url URL]`, plus the subcommands `help`, `version`, `keygen`,
`accounts-migrate`, `check`, `users …` (list, show, email-login, set-limit,
rename), `spaces list` and `spaces export`. `metatrash help`
lists them.

### Console commands

`upgrade.sh` installs `deploy/mt` as `/usr/local/bin/mt`, a shortcut that runs
the binary as `metatrash` through sudo, so it can read the service's files:

```sh
mt check                         # startup checks without starting the service
mt users list                    # every account, oldest first (-json for JSON)
mt users show dave               # one account in full (email, username or user ID)
mt users email-login on name@example.com
mt users set-limit dave 3        # private spaces the account may own
mt users rename dave david       # change the username (old addresses break)
mt spaces list                   # every owned space (-json for JSON)
mt spaces export dave/notes      # one space as a zip in the current folder
mt spaces export -all /srv/backup/metatrash   # every space, for backups
```

They use the same `accounts.json` and account database as the service, with
the same database login: `-accounts-config`, else `METATRASH_ACCOUNTS_CONFIG`,
else `/etc/metatrash/accounts.json`. They run beside the service without
stopping it, and changes apply at once (the service reads sign-in settings and
limits from the database on each request).

- **`check`** runs what startup checks, without the data directory lock:
  `spaces.json` and keys (`-config`, `-keys`, defaults as in the unit file),
  `accounts.json` and every file it names, the database schema, the login's
  grants against `deploy/account-grants.sql` (missing ones fail, extra ones are
  listed), and that each ready owned space's repository exists under `-data`.
  It exits 1 if anything fails. Run it before `upgrade.sh`, or after editing
  configuration and before restarting.
- **`users list`** shows username, email, creation date, owned spaces against
  the limit, active memberships and sign-in methods (email on or off,
  passkeys, authenticator app, recovery codes left).
- **`users show`** adds passkey names and last use, the authenticator app (or an
  unfinished setup), when recovery codes were made, owned spaces, memberships,
  invitations waiting, connected apps, GitHub installations and, from 0.32.0,
  signed-in browsers (device, sign-in address, since, last use; last use in
  the database lags by up to five minutes).
- **`users email-login on|off`** is the switch on Security. Turning it off still
  needs a passkey or authenticator app and recovery codes. The account is
  emailed a notice through the service's SMTP settings (`-no-notice` skips it);
  if the email fails the change still stands and the command says so.
- **`users set-limit`** sets `max_private_spaces` (0 to 1000). Spaces already
  owned stay when the limit goes below their number. It needs the 0.28.0 grant.
- **`users rename <who> <new-username>`** (0.39.0) sets the account's
  username, whether or not it had one, under the rules on Your account. It is
  the only name an account has (there is no separate display name). Everything
  else refers to the account by user ID, so its spaces, memberships, apps and
  sign-ins follow at once. Addresses with the old name stop working:
  `/spaces/<old>/<slug>/` links and apps or mtsync folders configured with
  `<old>/<slug>` (space IDs keep working), and the old name is free for anyone
  to choose. If `docsSpace` in `accounts.json` names the old username, change
  it and restart; the command warns. The account is emailed a notice
  (`-no-notice` skips it); if the email fails the rename still stands.
- **`spaces list`** shows owned spaces with owner, name, visibility, state,
  active members and invitations waiting. Spaces from `spaces.json` are not in
  the database and are not listed.
- **`spaces export <space> [FILE]`** writes a space's current files to a zip,
  with `.metatrash-export.json` naming the state and each file's ID (the same
  archive as the REST export, see docs/api-contract.md). `<space>` is
  `owner/slug`, a space ID, or a `spaces.json` name such as `public`. `FILE`
  defaults to `<owner>-<slug>-<date>-<state>.zip` in the current folder; `-`
  writes to standard output. It reads the repository under `-data`
  (`/var/lib/metatrash`) directly, so it works with the service running and
  is not rate limited. Files are written readable only by the service user.
- **`spaces export -all DIR`** exports every ready owned space and every
  `spaces.json` space into `DIR` (which must exist and be writable by the
  service user), one zip each, and prints one line per space. A space that
  fails is reported, the rest still run, and the command then exits 1. For a
  nightly backup, run it from cron as the service user and prune old files
  yourself, for example
  `mt spaces export -all /srv/backup/metatrash && find /srv/backup/metatrash -name '*.zip' -mtime +30 -delete`.
  An export holds current files only, not history; the nightly backup
  (Backups below) keeps whole repositories.
- **`spaces readme`** was removed in 0.45.0: README.md is an ordinary file
  now. At startup the service gives every folder of every space the
  `AGENTS.md` and `.metatrash.json` it lacks (one commit per space; owned
  spaces when first opened). The root `AGENTS.md` starts from
  `docs/space-AGENTS.template.md`; existing ones are never overwritten.

## Build and install

```sh
cd ~/metatrash && git pull
go build -trimpath -o bin/metatrash ./cmd/metatrash
./bin/metatrash version
sudo systemctl stop metatrash
sudo cp -a /usr/local/bin/metatrash /usr/local/bin/metatrash.previous
sudo install -o root -g root -m 0755 bin/metatrash /usr/local/bin/metatrash
sudo systemctl start metatrash
curl -fsS http://127.0.0.1:8080/healthz
```

First installation only:

```sh
sudo useradd --system --user-group --home-dir /var/lib/metatrash --shell /sbin/nologin metatrash
sudo install -d -o metatrash -g metatrash -m 0700 /var/lib/metatrash
sudo install -d -o root -g metatrash -m 0750 /etc/metatrash
sudo install -o root -g metatrash -m 0640 config/spaces.example.json /etc/metatrash/spaces.json
printf '{}\n' | sudo tee /etc/metatrash/keys.json >/dev/null
sudo chown root:metatrash /etc/metatrash/keys.json && sudo chmod 0640 /etc/metatrash/keys.json
sudo install -o root -g root -m 0644 deploy/metatrash.service /etc/systemd/system/metatrash.service
sudo systemctl daemon-reload && sudo systemctl enable --now metatrash
```

## Automated builds and releases

GitHub Actions (`.github/workflows/`) tests and builds on GitHub's machines, so
no Go toolchain is needed locally:

- **CI** (`ci.yml`) runs on every push and pull request: `gofmt`, `go vet`,
  `go test ./...` on Linux with the MariaDB integration tests against a
  disposable MariaDB 11.4 container (schema v1–v18 and `account-grants.sql`,
  as below), a cross-compile check of both programs for every release
  platform, and `go test ./cmd/mtsync` on Windows and macOS.
- **Release** (`release.yml`) runs when a tag `vX.Y.Z` is pushed. The tag must
  match `Version` in `assets.go` and `CHANGELOG.md` must have a `## X.Y.Z `
  section. It runs CI, builds with `CGO_ENABLED=0 go build -trimpath`, and
  publishes a GitHub Release with that changelog section as its notes:

  | File | Contents |
  | --- | --- |
  | `mtsync-windows-amd64.exe`, `mtsync-windows-arm64.exe` | mtsync for Windows |
  | `mtsync-linux-amd64`, `mtsync-linux-arm64` | mtsync for Linux |
  | `mtsync-macos-amd64`, `mtsync-macos-arm64` | mtsync for macOS (Intel, Apple silicon) |
  | `metatrash-linux-amd64`, `metatrash-linux-arm64` | the service and console (`mt`); the live server is arm64 |
  | `SHA256SUMS` | `sha256sum -c` format, every file above |
  | `mtsync.json` | `{version, tag, files: {"windows-amd64": {name, url, sha256, size}, ...}}` for installers and the editor |

  A tag with a suffix (`v0.41.0-rc.1`) is published as a pre-release; its
  `Version` must carry the same suffix.

To release: bump `Version`, add the changelog section, commit, then
`git tag v0.41.0 && git push origin v0.41.0`. A failed run publishes nothing;
fix, move the tag (`git tag -f`, `git push -f origin v0.41.0`) and it runs again.

### Code signing

The release signs the Windows files (`mtsync-windows-*.exe`, the setup and the
uninstaller inside it) with Azure Artifact Signing (formerly Trusted Signing),
account `zaptronicssigning` at `https://eus.codesigning.azure.net/`. Jsign
does it on the Linux runner; the NSIS setup signs its uninstaller and itself
through `-DSIGN`. The release job runs in the GitHub environment `release` and
signs in to Azure with GitHub's OIDC token, so no Azure secret is stored in
GitHub. Slate Viewer's release signs the same way, with the same identity.

One-time setup:

1. **Azure: an identity for GitHub.** Microsoft Entra ID → App registrations →
   New registration (for example `github-release-signing`). Note its
   Application (client) ID and Directory (tenant) ID. Under Certificates &
   secrets → Federated credentials, add one per repository: scenario *GitHub
   Actions deploying Azure resources*, organization `davezap`, repository
   `metatrash` (and `slate.viewer`), entity type *Environment*, environment
   `release`. The subject is then `repo:davezap/metatrash:environment:release`.
2. **Azure: permission to sign.** On the certificate profile (or the whole
   `zaptronicssigning` account) → Access control (IAM) → Add role assignment:
   *Artifact Signing Certificate Profile Signer* (shown as *Trusted Signing
   Certificate Profile Signer* in older portals) for that app registration.
   No subscription role is needed.
3. **GitHub, in each repository:** Settings → Environments → New environment
   `release` (optionally limit it to tags `v*`). In it add three variables
   (not secrets; none of them is sensitive): `AZURE_CLIENT_ID`,
   `AZURE_TENANT_ID` and `AZURE_SIGNING_PROFILE` (the certificate profile's
   name in the signing account).

A missing variable stops the release before anything is published. To check a
download on Windows: Properties → Digital Signatures, or
`Get-AuthenticodeSignature mtsync-windows-setup.exe` (Status `Valid`). A new
certificate's reputation builds with downloads, so SmartScreen may still warn
at first. The macOS and Linux files are not signed: macOS refuses a
browser-downloaded binary until it is allowed in System Settings → Privacy &
Security (`curl` downloads are not quarantined), and signing there needs an
Apple Developer ID and notarization.

If the repository is private, release downloads need a GitHub sign-in or token,
so public downloads of mtsync need either a public repository or a copy on the
site (for example `/downloads/mtsync.json`).

`upgrade.sh` still builds the service from source on the server. It could
install the release binary instead, with a fine-grained token (Contents: read)
on the server for `gh` (the read-only deploy key covers git only):

```sh
gh release download "v$VERSION" -R davezap/metatrash -D bin --clobber \
  -p metatrash-linux-arm64 -p SHA256SUMS
(cd bin && sha256sum -c --ignore-missing SHA256SUMS)
install -m 0755 bin/metatrash-linux-arm64 bin/metatrash
```

## Apache

Put `deploy/apache-metatrash.conf.example` inside the existing HTTPS
VirtualHost (needs mod_proxy, mod_proxy_http, mod_headers). It proxies the whole
domain, clears client-supplied forwarding headers and keeps `ProxyPreserveHost
On`. Go answers `/healthz` only to direct loopback requests. Then
`sudo apachectl configtest` and, only after `Syntax OK`, `sudo systemctl reload
httpd`. If SELinux blocks the proxy connection, check the audit log.

- `-trusted-proxies 127.0.0.1/32` (in the unit) makes rate limits see the real
  client address. Configure any CDN in front of Apache first.
- Never enable request body or header logging (`mod_dumpio`,
  `mod_log_forensic`): OAuth codes and tokens travel in headers and POST bodies.

**Hosting under a folder:** start Go with `-public-url https://example.com/metatrash/`
and use the commented folder lines in the Apache example instead of the root
proxy (plus `mod_alias`). Apache strips the prefix; Go adds it to links,
redirects, forms and cookie names. With OAuth, also add the two root discovery
lines from the example. Set the account `origin` to `https://example.com`,
without the folder. A folder is not an isolation boundary from other
applications on the same host.

## Key spaces (optional)

Add a space to `spaces.json` with `"visibility": "private"` and any `overrides`
of the default rates or storage. `metatrash keygen` prints a key and its digest;
store only digests in `keys.json`:

```json
{"team":{"readHashes":["<sha256>"],"writeHashes":["<sha256>"]}}
```

Each private space needs a write digest. Remove a digest and restart to revoke.
Clients send `Authorization: Bearer <key>` to `/mcp` or `/api/v1/spaces/…`.

## Accounts

### Email

Gmail SMTP on port 587 with STARTTLS, using an app password. Copy
`config/accounts.example.json` to `/etc/metatrash/accounts.json`, set
`origin`, sender and the password file, and put only the app password in
`/etc/metatrash/smtp-password` (`sudoedit`, never a command argument).

### Database

1. As administrator, check the socket (`SHOW VARIABLES LIKE 'socket'`), then
   create the database and login (keep the password out of shell history:
   `sudo env MYSQL_HISTFILE=/dev/null mariadb`):

   ```sql
   CREATE DATABASE metatrash CHARACTER SET utf8mb4;
   CREATE USER 'metatrash_accounts'@'localhost' IDENTIFIED BY '<password>';
   ```

   For loopback TCP use `@'127.0.0.1'` everywhere instead of `@'localhost'`.
2. Copy `config/account-database.example.json` to
   `/etc/metatrash/account-database.json` (real socket path) and put the password
   in `/etc/metatrash/database-password`. Set `databaseConfigFile` in
   `accounts.json`.
3. With the service stopped and the new binary installed, apply the schema
   scripts in order, initialise, then grant:

   ```sh
   sudo mariadb metatrash < deploy/account-schema-v1.sql
   sed -n '/^-- v1 /,/^-- v2 /p' deploy/account-grants.sql | sudo mariadb   # v1 grants, needed by the import
   sudo -u metatrash /usr/local/bin/metatrash accounts-migrate -database-config /etc/metatrash/account-database.json -data /var/lib/metatrash -empty
   for v in 2 3 4 5 6 7 8 9 10 11 12; do sudo mariadb metatrash < deploy/account-schema-v$v.sql; done
   sudo mariadb < deploy/account-grants.sql
   ```

   `accounts-migrate` without `-empty` imports a legacy `accounts.json` (pre-0.6.0)
   instead; it runs with the service stopped and never modifies its source.
4. Enable accounts with `sudo systemctl edit metatrash`:

   ```ini
   [Service]
   Environment=METATRASH_ACCOUNTS_CONFIG=/etc/metatrash/accounts.json
   ```

Startup refuses to run with a missing configuration, an unreachable database or
a schema older than v11. Without the accounts setting the service runs public and
key spaces only, with no database.

### OAuth

Add `"oauth": {"enabled": true}` to `accounts.json` and restart. Optional
settings, validated even while disabled: `clientHosts` (default `claude.ai`,
`chatgpt.com`), `accessTokenMinutes` (60), `refreshTokenDays` (30),
`codeSeconds` (60), `grantIdleDays` (90). The issuer is `-public-url`. See
[oauth.md](oauth.md).

### Authenticator app

Optional `"totpKeyFile"` (from 0.24.0) turns on sign-in with an authenticator
app. The file holds the key that encrypts each account's authenticator secret
in the database: 64 hex digits, readable only by the service. Create it once:

```sh
openssl rand -hex 32 | sudo tee /etc/metatrash/totp-key >/dev/null
sudo chown root:metatrash /etc/metatrash/totp-key && sudo chmod 0640 /etc/metatrash/totp-key
```

Back it up with `/etc/metatrash`, separately from database backups. Losing or
changing it makes every authenticator app unusable (those accounts sign in
with a passkey or an emailed code and set the app up again); there is no
rotation yet. Empty or absent: the authenticator app is not offered and
`/login/totp` answers 503.

### GitHub

Optional `"github"` section for the Metatrash GitHub App; off until
`"enabled": true`. Registration settings, secret files and the connection flow
are in [github.md](github.md).

### Folder actions (agents)

Optional `"agents"` section: turns folder actions on (api-contract.md,
Folder actions). Off until `"enabled": true`; actions are checked and stored
either way, and run only while it is on.

```json
"agents": {
  "enabled": true,
  "keyFile": "/etc/metatrash/agents-key",
  "timezone": "Pacific/Auckland",
  "search": {"provider": "brave", "keyFile": "/etc/metatrash/brave-key"},
  "downloads": {
    "endpoint": "https://s3.us-west-004.backblazeb2.com",
    "bucket": "metatrash-web",
    "keyID": "004…",
    "keyFile": "/etc/metatrash/web-bucket-key"
  }
}
```

From 0.55.0 the server holds no API key. Each person adds their own agents in
Your account → Services → Agents: a name, the provider (`minimax`, MiniMax's
Anthropic-compatible API at `https://api.minimax.io/anthropic`, usual model
`MiniMax-M3`; or `anthropic`, `https://api.anthropic.com`, usual model
`claude-sonnet-5-5`), the model, their API key, an optional https endpoint
and a daily token cap (2,000,000 unless they set another; `0` for none).
Up to 10 agents each; the first is the default, and Test makes one tiny call
to check the key. An action runs on the space owner's agent, so the owner
pays for every job in their spaces, members' files included, unless the
action says `"pays": "contributor"` (whoever brings a file pays, once they
opt in on the page their notice email links to) or is a member's own action
(that member pays, for their own files only; 0.56.0, api-contract.md, Folder
actions). Whoever pays and has no agent gets the job recorded as skipped. The configured spaces (the
public space among them) refuse actions altogether.

- `keyFile` (required): 64 hex digits that encrypt the API keys in the
  database (AES-256-GCM, bound to each user and agent). Create it once and
  back it up with `/etc/metatrash`; losing or changing it makes every stored
  key unreadable, and people must add their agents again.

  ```sh
  openssl rand -hex 32 | sudo tee /etc/metatrash/agents-key >/dev/null
  sudo chown root:metatrash /etc/metatrash/agents-key
  sudo chmod 0640 /etc/metatrash/agents-key
  ```

- `timezone`: IANA zone of spaces that have not chosen their own (their
  `every` schedules and `runner.md` times; owners set a space's zone with the
  clock icon on Your account, and a new space takes its creator's browser
  zone), and when daily caps reset (default `Pacific/Auckland`).
- `search` (0.57.0, optional): turns on `web_search` for actions.
  `provider` is `brave`; `keyFile` holds a Brave Search API key (sign up at
  https://api-dashboard.search.brave.com, subscribe to a Search plan, create
  a key; about US$5 per 1,000 searches). `dailyLimit` is the most searches
  each payer's jobs make in a day (default 200).
- `downloads` (0.57.0, optional): turns on `web_fetch`, which keeps what it
  downloads in an S3-compatible bucket of its own, never in Git. `endpoint`
  is the bucket's S3 address and `region` its region (worked out from a
  Backblaze B2 endpoint); `bucket`, `keyID` and `keyFile` (the application
  key) name and open it. `dailyLimit` is the most fetches each payer's jobs
  make in a day (default 500). The server needs outbound HTTPS (and HTTP) to
  the public internet; it never fetches private or loopback addresses.

  Set up the bucket in Backblaze B2 (separate from the restic backup bucket):

  1. Buckets → Create a Bucket: a name such as `metatrash-web`, Private,
     no Object Lock, default encryption on if you like.
  2. On the bucket, Lifecycle Settings → Use custom lifecycle rules: file
     path `web/`, days till hide `30`, days till delete `1`. Metatrash
     counts on files going 30 days after upload: references expire a day
     before.
  3. Application Keys → Add a New Application Key: access to that bucket
     only, Read and Write. Put its `keyID` in `accounts.json` and the
     `applicationKey` in the key file. The bucket page shows the endpoint
     (`s3.<region>.backblazeb2.com`).

  ```sh
  sudo install -m 0640 -o root -g metatrash /dev/null /etc/metatrash/brave-key
  sudoedit /etc/metatrash/brave-key             # the Brave key, one line
  sudo install -m 0640 -o root -g metatrash /dev/null /etc/metatrash/web-bucket-key
  sudoedit /etc/metatrash/web-bucket-key        # the applicationKey, one line
  ```

  Without `search` and `downloads`, actions run without the web tools.

The 0.54.x settings `owners`, `provider`, `baseURL`, `model`, `apiKeyFile`,
`maxTokens` and `dailyTokenCap` are refused with a message saying so; remove
them (and the old `agents-api-key` file, once its key is added as an agent).

Endpoints must be https and resolve only to public addresses, checked when
the agent is added and again on every connection; redirects are not
followed. Keys never appear on pages, in logs or in Git; the page shows the
last four characters.

`metatrash check` validates the section and the key file, and lists
"folder actions (users' agents)" among the account features. Run as root
(`sudo`), it also checks that the `metatrash` account can read the key file
("agents key", and "web search key" and "web downloads key" when set);
otherwise the service exits at start. Agents are rows in
`metatrash_agents`, jobs in `metatrash_agent_jobs` (schema v16), opt-ins
in `metatrash_action_optins` (schema v17), and schedules, downloads and
web references in `metatrash_action_schedules`, `metatrash_web_objects` and
`metatrash_web_refs` (schema v18). Notices to people who have not
opted in go out through the account SMTP settings, at most once a day per
person and action, within the site-wide 100 emails a day.

```sql
SELECT FROM_UNIXTIME(queued_at), folder, action, file, contributor_id, agent_id, status, steps, input_tokens, output_tokens, error
FROM metatrash_agent_jobs ORDER BY queued_at DESC LIMIT 20;
```

### Site docs

Optional `"docsSpace": "owner/slug"` (from 0.21.1) shows that space under
`/docs/` as part of the website, without its owner and space header, and adds
Privacy and Terms links to every page's footer, pointing at
`/docs/legal/privacy.md` and `/docs/legal/terms.md`. The space must be
readable on the web (Your account → Spaces); while it is not, `/docs/` is
not found for everyone. Anyone with write access to the space changes what the
site shows there. The space's `about.md` replaces the built-in About page: `/about` redirects to
`/docs/about.md` (from 0.21.2). Search engines may index every `/docs/` page. Empty or absent: no `/docs/`,
no footer links.

### Login log

Each login request writes one line to the journal, for example:

```
login send ip=203.0.113.5 email=emily@gmail.com account=none honeypot=pass pow=pass age=6s result=sent ua="Mozilla/5.0 …"
login verify ip=203.0.113.5 email=emily@gmail.com account=new result=ok ua="Mozilla/5.0 …"
login totp ip=203.0.113.5 email=emily@gmail.com account=existing result=invalid_code ua="Mozilla/5.0 …"
login recovery ip=203.0.113.5 email=emily@gmail.com account=existing result=ok ua="Mozilla/5.0 …"
```

A send line ends `result=email_off` when the account has turned email sign-in
off: no code was sent, the account was emailed a notice instead.

`account` is `existing` (an account uses this address), `none`, `new` (on a
verify line: this sign-in created the account) or `unknown` (the database
did not answer).

`honeypot` is `pass`, `filled` or `absent` (form posted without it). `pow` is
`pass`, `missing`, `malformed`, `forged` (not this browser's challenge),
`expired`, `reused`, `wrong` or `busy`; `-` means the request was refused
before the checks ran. `result` is `sent`, `blocked`, `rate_limited`,
`invalid_email`, `bad_origin`, `bad_form`, `bad_csrf`, a mail error code, or
for verify `ok`, `invalid_code` and similar. Read them with:

```
sudo journalctl -u metatrash --since today | grep 'login '
sudo journalctl -u metatrash --since -7d | grep 'login send' | grep -o 'account=[a-z]* honeypot=[a-z-]* pow=[a-z-]*.*result=[a-z_]*' | sort | uniq -c
sudo journalctl -u metatrash --since -7d -o cat | grep 'login ' | cut -d' ' -f3-   # without the journal prefix
```

### Administration

Use the console commands above (`mt users set-limit`, `mt users rename`,
`mt users email-login`) rather than SQL. Never edit IDs, slugs, ownership or `metatrash_account_meta`:
ownership moves only through Transfer ownership on Manage sharing, which also
moves memberships and app connections and keeps the old address.

## Backups

`deploy/backup.sh` (from 0.53.0) copies everything needed to rebuild the
server to an encrypted [restic](https://restic.net) repository on Backblaze
B2, every night. `upgrade.sh` installs it as `/usr/local/sbin/metatrash-backup`;
`deploy/metatrash-backup.timer` runs it as root at about 03:30 server time.
Each run:

1. **Dumps the account database** with `mariadb-dump --single-transaction`:
   one consistent snapshot, no locks, the service keeps running. File writes
   never touch the database, so nothing needs pausing.
2. **Bundles each space** (`git bundle create … refs/heads/main`, read-only)
   whose `main` moved since its last bundle, into
   `/var/backups/metatrash/spaces/{repos,owned-repos}/<name>.bundle`. A bundle
   is the whole repository, history included, in one file. Unchanged spaces
   keep their bundle, so every snapshot holds every space. The database is
   dumped first, so each space it lists as ready is in the same backup with at
   least the commits it had then; a space created in between is in the backup
   without its row (harmless; see Restore). Nothing is written to the live
   repositories.
3. **Copies the configuration**: `/etc/metatrash` without `totp-key`, and the
   service's unit and override.
4. **Writes `MANIFEST.txt`**: time, binary version, schema version and each
   bundle's `main` commit.
5. **Uploads** `/var/backups/metatrash` with `restic backup`. restic encrypts
   on the server and sends only data it has not stored before. On Sundays it
   also forgets snapshots past the retention (14 daily, 8 weekly, 12 monthly),
   prunes, and checks the repository, reading back 5% of it.

A space that fails to bundle is reported and the rest still run; the run then
exits 1. Read a run with `journalctl -u metatrash-backup`; see when it next
runs with `systemctl list-timers metatrash-backup.timer`.

### Keep these off the server

Store them in a password manager, not only on the server. Without the first
two, the backups cannot be read.

- **restic password** (`/etc/metatrash-backup/restic-password`).
- **`totp-key`**: it unlocks the authenticator secrets in the database, so it
  is left out of the B2 backups on purpose (the emailed configuration below
  carries it, encrypted).
- **Configuration passphrase** (`/etc/metatrash-backup/config-passphrase`),
  which opens the emailed configuration.
- The B2 account sign-in and the application key.

### One-time setup

1. **B2 bucket.** In Backblaze B2: Buckets → Create a Bucket, private, with
   **Object Lock** on and a default retention of 30 days (Governance mode).
   Locked file versions can't be deleted until the lock runs out, even with
   the server's key, so someone who takes over the server can hide backups but
   not destroy them for 30 days. Under Lifecycle Settings choose "Keep prior
   versions for this number of days: 30". Note the bucket's S3 endpoint.
2. **Application key.** Application Keys → Add a New Application Key, limited
   to that bucket, Read and Write. Note `keyID` and `applicationKey` (shown
   once).
3. **restic** (Amazon Linux has no package): download the newest
   `restic_<version>_linux_arm64.bz2` from
   https://github.com/restic/restic/releases, then
   ```sh
   bunzip2 restic_*_linux_arm64.bz2
   sudo install -o root -g root -m 0755 restic_*_linux_arm64 /usr/local/bin/restic
   restic version
   ```
   Later updates: `sudo restic self-update`.
4. **Settings and password.**
   ```sh
   sudo install -d -o root -g root -m 0700 /etc/metatrash-backup
   sudo install -o root -g root -m 0600 deploy/backup.env.example /etc/metatrash-backup/backup.env
   sudo sh -c 'umask 077; openssl rand -base64 33 > /etc/metatrash-backup/restic-password'
   sudo cat /etc/metatrash-backup/restic-password   # into the password manager now
   sudoedit /etc/metatrash-backup/backup.env        # repository, keyID, applicationKey
   ```
5. **Create the repository and run once.**
   ```sh
   sudo bash -c 'set -a; . /etc/metatrash-backup/backup.env; restic init'
   sudo install -o root -g root -m 0755 deploy/backup.sh /usr/local/sbin/metatrash-backup
   sudo metatrash-backup
   ```
   It should end `metatrash-backup: done`.
6. **Timer.**
   ```sh
   sudo install -o root -g root -m 0644 deploy/metatrash-backup.service deploy/metatrash-backup.timer /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now metatrash-backup.timer
   ```
7. **Optional alert.** Set `HEALTHCHECK_URL` in `backup.env` to a
   healthchecks.io check (daily, with a grace period of a few hours). It is
   pinged after each good run and at `/fail` after a failed one, and emails
   when a run fails or doesn't happen.
8. **EBS snapshots (optional, second layer).** In EC2 → Lifecycle Manager,
   create a policy for the instance's volume: daily, keep 7, optionally copy to
   another region. That gives a quick whole-server restore; B2 covers losing
   the AWS account or region.

Then do a test restore (below) once, and after that every few months.

### Emailed configuration

`deploy/backup-config.sh` (from 0.53.1; `upgrade.sh` installs it as
`/usr/local/sbin/metatrash-backup-config`) emails the configuration every day
at about 05:00, separately from B2: `/etc/metatrash` (with `totp-key`),
`/etc/metatrash-backup` (B2 key and restic password; not the passphrase
itself) and the service's unit and override, as one tar archive encrypted
with gpg (AES-256) under its own passphrase. It is sent to `CONFIG_MAIL_TO` in
`backup.env` through the SMTP settings in `accounts.json` (the Gmail account
that sends sign-in codes); the body also shows how the last B2 backup run
ended, so the daily email doubles as a check that backups run. Anyone with
the mailbox *and* the passphrase has every secret of the server, so keep the
passphrase only in the password manager.

Setup, after the B2 setup above. Amazon Linux comes with `gnupg2-minimal`,
which has no `gpg-agent` (gpg then fails with "error running
'/usr/bin/gpg-agent'"), so swap in the full package first:

```sh
sudo dnf swap -y gnupg2-minimal gnupg2-full
sudo sh -c 'umask 077; openssl rand -base64 24 > /etc/metatrash-backup/config-passphrase'
sudo cat /etc/metatrash-backup/config-passphrase   # into the password manager now
sudoedit /etc/metatrash-backup/backup.env          # CONFIG_MAIL_TO=david@metatrash.com
sudo install -o root -g root -m 0755 deploy/backup-config.sh /usr/local/sbin/metatrash-backup-config
sudo metatrash-backup-config                       # ends "sent … to david@metatrash.com"
sudo install -o root -g root -m 0644 deploy/metatrash-backup-config.service deploy/metatrash-backup-config.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now metatrash-backup-config.timer
```

Test it: save the attachment, then in Git Bash on Windows (or any shell with
gpg) run `gpg -d metatrash-config-<host>-<date>.tar.gz.gpg | tar -xz` and
enter the passphrase. It unpacks into `etc/…`; check `etc/metatrash/totp-key`
and `etc/metatrash-backup/restic-password` match the password manager. If
the email doesn't arrive, look in spam and in
`journalctl -u metatrash-backup-config`; Gmail refuses a `From` that is not
the SMTP account or one of its aliases.

### Look at backups

```sh
sudo bash -c 'set -a; . /etc/metatrash-backup/backup.env; restic snapshots'
sudo bash -c 'set -a; . /etc/metatrash-backup/backup.env; restic dump latest /var/backups/metatrash/MANIFEST.txt'
```

The same commands work from any computer with restic, the password and the
B2 key, which is how to restore if the server is gone.

### Restore

Load the settings (`set -a; . /etc/metatrash-backup/backup.env; set +a` as
root, or the same variables on another machine) and fetch a snapshot
(`latest`, or an ID from `restic snapshots`):

```sh
restic restore latest --target /root/restore
B=/root/restore/var/backups/metatrash
cat $B/MANIFEST.txt
```

**One space** (for example a missing ready repository, which stops startup):

```sh
sudo systemctl stop metatrash
repo=/var/lib/metatrash/owned-repos/<space_id>.git      # or repos/<name>.git
git init -q --bare --object-format=sha1 --initial-branch=main "$repo"
git -C "$repo" fetch -q $B/spaces/owned-repos/<space_id>.bundle refs/heads/main:refs/heads/main
chown -R metatrash:metatrash "$repo"
mt check && sudo systemctl start metatrash
```

Move an existing broken repository aside first; never fetch into a live one.

**A whole server** (new instance, after the first-installation steps above
and with the same binary version as `MANIFEST.txt`, service stopped):

1. Configuration: copy `$B/config/metatrash/` to `/etc/metatrash/`
   (root:metatrash, 0750, files 0640), put `totp-key` back from the password
   manager or the emailed archive, and copy `$B/config/metatrash.service.d` to
   `/etc/systemd/system/` if it is there.
2. Database: create the login as in Accounts → Database step 1, then
   ```sh
   sudo mariadb < $B/db/metatrash.sql          # creates the metatrash database
   sudo mariadb < deploy/account-grants.sql
   ```
3. Spaces:
   ```sh
   for b in $B/spaces/*/*.bundle; do
     kind=$(basename "$(dirname "$b")"); name=$(basename "$b" .bundle)
     repo=/var/lib/metatrash/$kind/$name.git
     sudo install -d -o metatrash -g metatrash -m 0700 /var/lib/metatrash/$kind
     git init -q --bare --object-format=sha1 --initial-branch=main "$repo"
     git -C "$repo" fetch -q "$b" refs/heads/main:refs/heads/main
   done
   sudo chown -R metatrash:metatrash /var/lib/metatrash
   ```
4. **Sign everyone out and end app connections**: the backup may hold
   sessions and tokens revoked after it was taken. People sign in again and
   apps reconnect.
   ```sh
   sudo mariadb metatrash -e 'DELETE FROM metatrash_sessions; DELETE FROM metatrash_oauth_tokens;'
   ```
5. `mt check` (it fails on any ready space whose repository is missing),
   then start the service and check a few spaces, a sign-in and an app. A
   space created after the dump has a repository but no row: nothing refers
   to it, and it can be left or removed.

**Test restore**: on a spare machine or a throwaway instance, restore
`latest`, run `git bundle verify` on each bundle (inside any Git repository),
rebuild one space as above and compare its `git log` with the live one, and
load the dump into a scratch MariaDB. Note the date in "Owner checks" on the
roadmap.

## Upgrades, backups and rollback

1. Stop the service. Back up the database, `/etc/metatrash`, the data
   directory (`sudo metatrash-backup` does all three, see Backups) and the
   installed binary.
2. Apply any new schema script and `deploy/account-grants.sql`.
3. Install the new binary and start.

Schema scripts v1 and v5–v18 can be repeated safely. v2–v4 cannot (DDL commits
implicitly): if one is interrupted, keep the service stopped, compare
`SHOW CREATE TABLE` with the script, run only the missing statements and then
its guarded `UPDATE metatrash_account_meta`.

Releases that changed the schema or grants (the live server has all of them):

| Release | Before the new binary starts | To roll back, set `schema_version` to |
| --- | --- | --- |
| 0.17.0 | schema v6 (GitHub connections) | 5 |
| 0.21.0 | schema v7 (`web` visibility) | 6, after `UPDATE metatrash_spaces SET visibility = 'private'` |
| 0.22.0 | schema v8 (passkeys, TOTP, recovery codes, `email_login`) | 7 |
| 0.26.0 | grants (`UPDATE (email)`) | (grants only) |
| 0.28.0 | grants (`UPDATE (max_private_spaces)`, for `mt users set-limit`) | (grants only) |
| 0.30.0 | schema v9 (transfers and aliases) | 8; transferred spaces keep their new owner and lose their old address |
| 0.31.0 | schema v10 (`metatrash_sessions`; that upgrade signed everyone out once) | 9; everyone is signed out |
| 0.32.0 | schema v11 (session `device` and `ip`) | 10 |
| 0.48.0 | schema v12 (`metatrash_acknowledgements`) and grants (`SELECT, INSERT` on it) | 11; confirmations stay in the table |
| 0.49.0 | schema v13 (`metatrash_form_submissions`) and grants (`SELECT, INSERT` on it) | 12; receipts stay in the table, filed copies stay in their spaces |
| 0.50.0 | schema v14 (review and quiz columns on `metatrash_form_submissions`) and grants (`UPDATE (status, score, marks, reviewed_by, reviewed_at, note, text)` on it) | 13; reviews stay in the table, but 0.49 cannot show them |
| 0.54.0 | schema v15 (`metatrash_agent_jobs`) and grants (`SELECT, INSERT` and `UPDATE (status, started_at, finished_at, steps, input_tokens, output_tokens, error)` on it) | 14, after removing `actions` from every `.metatrash.json`: older releases treat a file with `actions` as invalid, so that folder's locks and `readOnly` would be ignored. Jobs stay in the table |
| 0.55.0 | schema v16 (`metatrash_agents`, `agent_id` on jobs) and grants (`SELECT, INSERT, DELETE` and `UPDATE (is_default)` on `metatrash_agents`, `UPDATE (agent_id)` on jobs); in `accounts.json` replace the agents settings with `keyFile` (Folder actions) | 15, after putting the 0.54.x agents settings back in `accounts.json`; agents stay in the table, unused |
| 0.56.0 | schema v17 (`metatrash_action_optins`, `contributor_id` on jobs) and grants (`SELECT, INSERT, DELETE` on `metatrash_action_optins`) | 16, after removing `pays` and `member` from every action: 0.55 treats a file with them as invalid. Opt-ins stay in the table, unused |
| 0.57.0 | schema v18 (`metatrash_action_schedules`, `metatrash_web_objects`, `metatrash_web_refs`, `timezone` on spaces) and grants (`SELECT, INSERT, DELETE` on the three tables, `UPDATE (every, next_run_at, last_run_at)` on schedules, `UPDATE (uploaded_at)` on web objects, `UPDATE (timezone)` on spaces); optional `agents.search` and `agents.downloads` (Folder actions) | 17, after removing `every` from every action: 0.56 refuses it. Schedules, downloads and time zones stay, unused |

Apply a schema script and `account-grants.sql` before running `upgrade.sh`,
which restarts the service; the running binary keeps working on the newer
schema until then. Releases from 0.32.0 to 0.47.0 need no schema,
configuration or grant change. mtsync needs OAuth enabled; its waiting list
requests last 25 seconds, so keep any proxy timeout above 30 seconds.
Passkeys are bound to the host name of `origin` in `accounts.json`
(`metatrash.com`): changing that host makes every passkey unusable. Rolling
back past 0.25.0 lets emailed codes sign in accounts that turned email
sign-in off.

Prefer fixing forward. To run an older binary, leave the new tables in place and
set `schema_version` back to what that binary expects; set it forward again
later instead of recreating tables. Never restore an old database backup over
accounts, spaces or memberships created since that backup.

## Recovery

- **Unfinished space creation** is retried at startup under its original ID. To
  abandon one: stop the service, back up, confirm the row is still
  `provisioning`, remove that ID's repository and any `.provision-*` or
  `.objects-*` leftovers under `owned-repos/`, then delete that row to release the
  allowance. Never delete a `ready` row.
- **A ready repository missing** stops startup on purpose: restore it from
  backup (Backups → Restore, one space).
- **`.service-lock`** prevents two processes using one data directory. Remove it
  only after confirming no service or migration is running.
- **Public reset**: stop, move `repos/public.git` to a private backup, start; a
  fresh public space is provisioned.
- Git history is not a backup. Do not run Git maintenance or external writers
  against live repositories (the console's own commands are safe). Existing `AGENTS.md` files are not changed by
  rebuilding; new spaces get the current template.

### An account locked out with email sign-in off

There is no self-service recovery (decided 2026-10-08): locked-out users email
the administrator, who can turn email sign-in back on for one account after checking who is asking (for
example, a reply from that address):

```sh
mt users email-login on name@example.com
```

The account is emailed a notice.

They can then sign in with an emailed code and create new recovery codes.

Users change their own address on Security (0.26.0). The journal line
`account email-change user=<id> result=ok` records each change; the
addresses are only in the notice emails.

## Testing

- Unit tests run anywhere: `go test ./...`.
- MariaDB integration tests run when `METATRASH_TEST_DB_CONFIG` points at a
  database config file for a **freshly recreated, disposable** database at
  schema v18 with `account-grants.sql` applied (leftover owned spaces break the
  run). See `internal/service/db_integration_test.go`.
- CI runs both on every push (see Automated builds and releases).

Quick live checks:

```sh
curl -fsS http://127.0.0.1:8080/healthz                                  # on the server
curl -fsS https://metatrash.com/.well-known/oauth-authorization-server
curl -i -X POST https://metatrash.com/mcp/account                         # expect 401 + WWW-Authenticate
curl -fsS https://metatrash.com/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"check","version":"1"}}}'
```

MCP tool failures return HTTP 200 with `result.isError: true`, so check the
body, not just the status. In Windows PowerShell `curl` is an alias for
`Invoke-WebRequest`; use `curl.exe` or run these on Linux.
