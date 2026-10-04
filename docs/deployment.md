# Deployment and operations

Target: Amazon Linux 2023 with Apache HTTPS in front of one Go service on
`127.0.0.1:8080`. Build with Go 1.25+ (modules are pinned in `go.sum`); the
service needs Git at runtime and MariaDB only when accounts are enabled.

## Live server (as of 0.15.0)

- Amazon Linux 2023, aarch64. Checkout at `/home/ec2-user/metatrash`, cloned from
  `davezap/metatrash` with a read-only deploy key (`~/.ssh/metatrash_deploy`).
- Accounts and OAuth enabled: a systemd override sets
  `METATRASH_ACCOUNTS_CONFIG=/etc/metatrash/accounts.json`, which contains
  `"oauth": {"enabled": true}`. Database `metatrash` at schema v6 (from 0.17.0).
- Whole-domain Apache proxy from `deploy/apache-metatrash.conf.example`.

## Layout

| Item | Location |
| --- | --- |
| Executable | `/usr/local/bin/metatrash` (root, 0755) |
| Configuration | `/etc/metatrash/` (root:metatrash, 0750; files 0640) |
| Spaces, keys | `spaces.json`, `keys.json` |
| Accounts | `accounts.json`, `account-database.json`, `smtp-password`, `database-password` |
| GitHub App (optional) | `github-app.pem`, `github-client-secret`, `github-webhook-secret` |
| Data | `/var/lib/metatrash` (metatrash, 0700): `repos/`, `owned-repos/`, `.service-lock` |
| Unit | `/etc/systemd/system/metatrash.service` |

Command line: `metatrash [-listen 127.0.0.1:8080] [-data DIR] [-config spaces.json]
[-keys keys.json] [-accounts-config accounts.json] [-trusted-proxies CIDRs]
[-public-url URL]`, plus the subcommands `version`, `keygen` and
`accounts-migrate`.

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
   head -8 deploy/account-grants.sql | sudo mariadb          # v1 grants, needed by the import
   sudo -u metatrash /usr/local/bin/metatrash accounts-migrate -database-config /etc/metatrash/account-database.json -data /var/lib/metatrash -empty
   for v in 2 3 4 5 6; do sudo mariadb metatrash < deploy/account-schema-v$v.sql; done
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
a schema older than v6. Without the accounts setting the service runs public and
key spaces only, with no database.

### OAuth

Add `"oauth": {"enabled": true}` to `accounts.json` and restart. Optional
settings, validated even while disabled: `clientHosts` (default `claude.ai`,
`chatgpt.com`), `accessTokenMinutes` (60), `refreshTokenDays` (30),
`codeSeconds` (60), `grantIdleDays` (90). The issuer is `-public-url`. See
[oauth.md](oauth.md).

### GitHub

Optional `"github"` section for the Metatrash GitHub App; off until
`"enabled": true`. Registration settings, secret files and the connection flow
are in [github.md](github.md).

### Login log

Each login request writes one line to the journal, for example:

```
login send ip=203.0.113.5 email=emily@gmail.com account=none honeypot=pass pow=pass age=6s result=sent ua="Mozilla/5.0 …"
login verify ip=203.0.113.5 email=emily@gmail.com account=new result=ok ua="Mozilla/5.0 …"
```

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
administrator. Never edit IDs, slugs, ownership or `metatrash_account_meta`.

## Upgrades, backups and rollback

1. Stop the service. Back up the database (normal InnoDB procedure),
   `/etc/metatrash`, the data directory and the installed binary.
2. Apply any new schema script and `deploy/account-grants.sql`.
3. Install the new binary and start.

Schema scripts v1, v5 and v6 can be repeated safely. v2–v4 cannot (DDL commits
implicitly): if one is interrupted, keep the service stopped, compare
`SHOW CREATE TABLE` with the script, run only the missing statements and then
its guarded `UPDATE metatrash_account_meta`.

**0.17.0** needs schema v6 (`deploy/account-schema-v6.sql`, then
`account-grants.sql`) before the new binary starts; it adds only the GitHub
connections table. GitHub itself stays off until configured.

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

## Testing

- Unit tests run anywhere: `go test ./...`.
- MariaDB integration tests run when `METATRASH_TEST_DB_CONFIG` points at a
  database config file for a **freshly recreated, disposable** database at
  schema v6 with `account-grants.sql` applied (leftover owned spaces break the
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
