# Deployment and operations

Target: Amazon Linux 2023 with Apache HTTPS in front of one Go service on
`127.0.0.1:8080`. Build with Go 1.25+ (modules are pinned in `go.sum`); the
service needs Git at runtime and MariaDB only when accounts are enabled.

## Live server (as of 0.15.0)

- Amazon Linux 2023, aarch64. Checkout at `/home/ec2-user/metatrash`, cloned from
  `davezap/metatrash` with a read-only deploy key (`~/.ssh/metatrash_deploy`).
- Accounts and OAuth enabled: a systemd override sets
  `METATRASH_ACCOUNTS_CONFIG=/etc/metatrash/accounts.json`, which contains
  `"oauth": {"enabled": true}`. Database `metatrash` at schema v11 (from 0.32.0).
- Whole-domain Apache proxy from `deploy/apache-metatrash.conf.example`.

## Layout

| Item | Location |
| --- | --- |
| Executable | `/usr/local/bin/metatrash` (root, 0755) |
| Configuration | `/etc/metatrash/` (root:metatrash, 0750; files 0640) |
| Spaces, keys | `spaces.json`, `keys.json` |
| Accounts | `accounts.json`, `account-database.json`, `smtp-password`, `database-password`, `totp-key` |
| GitHub App (optional) | `github-app.pem`, `github-client-secret`, `github-webhook-secret` |
| Data | `/var/lib/metatrash` (metatrash, 0700): `repos/`, `owned-repos/`, `.service-lock` |
| Unit | `/etc/systemd/system/metatrash.service` |

Command line: `metatrash [-listen 127.0.0.1:8080] [-data DIR] [-config spaces.json]
[-keys keys.json] [-accounts-config accounts.json] [-trusted-proxies CIDRs]
[-public-url URL]`, plus the subcommands `help`, `version`, `keygen`,
`accounts-migrate`, `check`, `users …` and `spaces list`. `metatrash help`
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
mt spaces list                   # every owned space (-json for JSON)
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
- **`spaces list`** shows owned spaces with owner, name, visibility, state,
  active members and invitations waiting. Spaces from `spaces.json` are not in
  the database and are not listed.

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
   for v in 2 3 4 5 6 7 8 9; do sudo mariadb metatrash < deploy/account-schema-v$v.sql; done
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

Change an allowance with
`UPDATE metatrash_users SET max_private_spaces = N WHERE user_id = '…'` as
administrator. Never edit IDs, slugs, ownership or `metatrash_account_meta`:
ownership moves only through Transfer ownership on Manage sharing, which also
moves memberships and app connections and keeps the old address.

## Upgrades, backups and rollback

1. Stop the service. Back up the database (normal InnoDB procedure),
   `/etc/metatrash`, the data directory and the installed binary.
2. Apply any new schema script and `deploy/account-grants.sql`.
3. Install the new binary and start.

Schema scripts v1 and v5–v11 can be repeated safely. v2–v4 cannot (DDL commits
implicitly): if one is interrupted, keep the service stopped, compare
`SHOW CREATE TABLE` with the script, run only the missing statements and then
its guarded `UPDATE metatrash_account_meta`.

**0.17.0** needs schema v6 (`deploy/account-schema-v6.sql`, then
`account-grants.sql`) before the new binary starts; it adds only the GitHub
connections table. GitHub itself stays off until configured.

**0.21.0** needs schema v7 before the new binary starts:

```sh
sudo systemctl stop metatrash
sudo mariadb metatrash < deploy/account-schema-v7.sql
sudo mariadb < deploy/account-grants.sql
# install the new binary (upgrade.sh), then
sudo systemctl start metatrash
```

v7 lets a space's `visibility` be `web` (readable in the website explorer)
and grants the service `UPDATE (visibility)`. Every space stays `private`
until its owner changes it. To roll back to 0.20.x, set every space back to
`private` first (`UPDATE metatrash_spaces SET visibility = 'private'`), then
`schema_version` to 6.

**0.22.0** needs schema v8 before the new binary starts:

```sh
sudo systemctl stop metatrash
sudo mariadb metatrash < deploy/account-schema-v8.sql
sudo mariadb < deploy/account-grants.sql
# install the new binary (upgrade.sh), then
sudo systemctl start metatrash
```

v8 adds the sign-in method tables (`metatrash_passkeys`, `metatrash_totp`,
`metatrash_recovery_codes`) and an `email_login` column on `metatrash_users`
(default 1), and grants the service access to them. 0.22.0 uses only the
passkeys table. Passkeys are bound to the host name of `origin` in
`accounts.json` (`metatrash.com`): changing that host makes every passkey
unusable. To roll back to 0.21.x, set `schema_version` to 7; the new tables
can stay.

**0.24.0** needs no schema change (it uses the v8 `metatrash_totp` table).
Add `totpKeyFile` to `accounts.json` (see [Authenticator app](#authenticator-app))
to offer it; without it the release behaves like 0.23.0. To roll back to 0.23.x,
install the older binary: authenticator apps already set up stay in the table,
unused, and work again after upgrading.

**0.25.0** needs no schema or configuration change (it uses the v8
`metatrash_recovery_codes` table and `email_login` column). Rolling back to
0.24.x is safe but weakens accounts that turned email sign-in off: the older
binary ignores the switch, so emailed codes sign them in again.

**0.26.0** needs no schema or configuration change, but run
`deploy/account-grants.sql` again before starting it: changing an email
address needs `UPDATE (email)` on `metatrash_users`.

```sh
sudo mariadb < deploy/account-grants.sql   # safe while 0.25.x is running
./upgrade.sh
```

Rolling back to 0.25.x is safe; addresses already changed stay changed.

**0.28.0** adds console commands and the `mt` shortcut; no schema or
configuration change. Run `deploy/account-grants.sql` again for the one new
grant, `UPDATE (max_private_spaces)` on `metatrash_users`, which only
`mt users set-limit` uses (everything else works without it):

```sh
sudo mariadb < deploy/account-grants.sql   # safe while 0.27.x is running
./upgrade.sh
mt check
```

**0.32.0** needs schema v11 before the new binary starts. No new grants (the
v10 table grants cover the new columns), but running `account-grants.sql`
again is harmless:

```sh
sudo mariadb metatrash < deploy/account-schema-v11.sql
./upgrade.sh
mt check
```

v11 adds `device` (a label such as "Chrome on Windows", made from the
User-Agent at sign-in; the header is not kept) and `ip` (the sign-in address,
from `X-Forwarded-For` through the trusted proxy) to `metatrash_sessions`.
Both go when the session does: sign-out or 24 hours. Sessions are kept across
this upgrade; ones from 0.31.0 show "Unknown browser". To roll back to
0.31.x, set `schema_version` to 10; the columns are ignored.

**0.31.0** needs schema v10 and the grants before the new binary starts.
`upgrade.sh` restarts the service, so apply them first; 0.30.x keeps running
on v10 until it restarts:

```sh
sudo mariadb metatrash < deploy/account-schema-v10.sql
sudo mariadb < deploy/account-grants.sql
./upgrade.sh
mt check
```

v10 adds `metatrash_sessions` (one row per signed-in browser: the SHA-256 of
the cookie's token, never the token, with user, created, expires, last
confirmed and last used) and grants `SELECT, INSERT, DELETE` and
`UPDATE (auth_at, last_used_at)` on it. From then on a restart or deploy keeps
everyone signed in. This upgrade itself still signs everyone out once (0.30.x
kept sessions in memory). The journal shows `account sessions-loaded count=N`
at startup. Restoring a database backup brings back sessions that were signed
out since it was taken (each lasts at most 24 hours). To roll back to 0.30.x,
set `schema_version` to 9; the table is ignored and everyone is signed out on
that restart.

**0.30.0** needs schema v9 and the grants before the new binary starts.
`upgrade.sh` restarts the service, so apply them first; 0.29.x keeps running
on v9 until it restarts:

```sh
sudo mariadb metatrash < deploy/account-schema-v9.sql
sudo mariadb < deploy/account-grants.sql
./upgrade.sh
mt check
```

v9 adds `metatrash_space_transfers` (pending ownership offers) and
`metatrash_space_aliases` (addresses of transferred spaces), and grants
`UPDATE (owner_user_id)` on `metatrash_spaces` and `UPDATE (member_user_id)`
on `metatrash_oauth_grant_spaces`. To roll back to 0.29.x, set
`schema_version` to 8; spaces already transferred keep their new owner, and
their old addresses stop working until 0.30.0 is back.

**0.27.0** needs no schema, configuration or grant change. Rolling back to
0.26.x is safe.

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
  backup.
- **`.service-lock`** prevents two processes using one data directory. Remove it
  only after confirming no service or migration is running.
- **Public reset**: stop, move `repos/public.git` to a private backup, start; a
  fresh public space is provisioned.
- Git history is not a backup. Do not run Git maintenance or external writers
  against live repositories. Existing space READMEs are not changed by
  rebuilding; only new spaces get the current template.

### An account locked out with email sign-in off

There is no self-service recovery (decided 2026-10-08): locked-out users email
the administrator, who can turn email sign-in back on for one account after checking who is asking (for
example, a reply from that address):

```sh
mt users email-login on name@example.com
```

The account is emailed a notice. Without the 0.28.0 binary, the same change in
SQL is `UPDATE metatrash_users SET email_login = 1 WHERE email = '…'`.

They can then sign in with an emailed code and create new recovery codes.

Users change their own address on Security (0.26.0). The journal line
`account email-change user=<id> result=ok` records each change; the
addresses are only in the notice emails.

## Testing

- Unit tests run anywhere: `go test ./...`.
- MariaDB integration tests run when `METATRASH_TEST_DB_CONFIG` points at a
  database config file for a **freshly recreated, disposable** database at
  schema v11 with `account-grants.sql` applied (leftover owned spaces break the
  run). See `internal/service/db_integration_test.go`.
- There is no Go toolchain on the Windows development PC. A cloud workspace
  needed Go 1.25 built from the golang/go source on GitHub and golang.org/x
  modules resolved from GitHub mirrors through a private `-modfile`.

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
