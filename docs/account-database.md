# Account database and migration — 0.6.1

Stage 1 is implemented locally. The owner must build, prepare the server
database, migrate, deploy, and perform the focused checks below. No live database
or server was accessed during implementation.

MariaDB/MySQL now stores human accounts. Git still stores space content and
history. Existing account IDs, normalized verified emails, creation instants
(including nanoseconds), and private-space allowances are retained. New accounts
keep the default allowance of one. No usernames, private-space creation,
memberships, browser content editing, or agent authentication changes are added.

## Storage and configuration

The versioned schema is [account-schema-v1.sql](../deploy/account-schema-v1.sql).
Both tables use InnoDB. `metatrash_users.user_id` is the immutable primary key;
normalized email has a unique index. `created_at` stores RFC3339Nano UTC text to
avoid truncating existing Go timestamp precision. `metatrash_account_meta`
records schema version 1 and the completed import's SHA-256 source fingerprint
(or `empty` for explicit first-time initialization).

The account configuration gains one required field when accounts are enabled:

```json
"databaseConfigFile": "/etc/metatrash/account-database.json"
```

Use [account-database.example.json](../config/account-database.example.json)
for the separate database configuration. Set the actual socket path reported
by your server, not an assumed path. The example uses `/var/lib/mysql/mysql.sock`.
Alternatively use `"network": "tcp"` and `"address": "127.0.0.1:3306"` with
matching local database grants. Stage 1 accepts only Unix sockets or literal
loopback TCP addresses; remote database connections are outside this bite.

Database and SMTP passwords stay in separate protected files outside Git and
the document root. The database password file contains the password followed
optionally by a newline. Connection parameters are constructed through the
pinned [Go MySQL driver](https://github.com/go-sql-driver/mysql/tree/v1.9.3), not
through arbitrary DSN options. Pool size is four with bounded connection/query
timeouts. Driver/server errors are not printed with credentials or account data.

## Prepare the database before maintenance

Use the existing local MariaDB/MySQL installation. From an administrator client,
check the server and socket (`SELECT VERSION(); SHOW VARIABLES LIKE 'socket';`).
Create a **dedicated** database named `metatrash` and a local login named
`metatrash_accounts` with a new private password. Use your usual protected
administration method; keep the password out of shell arguments and history.
The steps below assume those names and local administrator socket authentication;
adjust to the server's existing administration method if necessary.

```sql
CREATE DATABASE metatrash CHARACTER SET utf8mb4;
```

Apply the schema from the checkout as an administrator:

```bash
sudo mariadb metatrash < deploy/account-schema-v1.sql
```

The schema script is repeatable and does not clear existing data or overwrite
the schema version. Do not use it to disguise a conflicting existing schema.
The application and importer require both tables to exist and use InnoDB.

After creating the local database login, grant only these table permissions:

```sql
GRANT SELECT, INSERT ON metatrash.metatrash_users
    TO 'metatrash_accounts'@'localhost';
GRANT SELECT, UPDATE ON metatrash.metatrash_account_meta
    TO 'metatrash_accounts'@'localhost';
```

The same login can perform the import and runtime account operations. It needs
no DDL, DELETE, FILE, or server administration privileges. The metadata-row lock
serializes registration/import and protects the 10,000-account cap. Use an
administrator for allowance updates; the service never overwrites an existing
user during login.

Create protected configuration files on first setup, then edit the socket,
database/login names, and password locally:

```bash
sudo install -o root -g metatrash -m 0640 config/account-database.example.json /etc/metatrash/account-database.json
sudo install -o root -g metatrash -m 0640 /dev/null /etc/metatrash/database-password
sudoedit /etc/metatrash/account-database.json /etc/metatrash/database-password
```

The empty-password-file command is **first setup only**; do not overwrite a
working password file. The existing systemd sandbox must be able to reach the
socket; if it lives under `/tmp`, `PrivateTmp` may hide it. Prefer the server's
normal socket under `/run` or `/var/lib`, or use loopback TCP.

Build 0.6.1 using the existing owner build workflow, leaving the currently running
binary in place until the migration succeeds. From the checkout, the new binary
is assumed to be `bin/metatrash` below. Apache routes do not change.

## Maintenance, backup, and cutover

Stop the service to prevent any old JSON account writes. Keep it stopped until
the new database-backed binary is installed. Confirm it is stopped before
continuing:

```bash
sudo systemctl stop metatrash
sudo systemctl is-active metatrash
```

Expect `inactive`. Use the actual service data directory throughout; these
examples use `/var/lib/metatrash`. Take a private backup of `accounts.json`,
`/etc/metatrash`, and the installed binary. For example, with a new backup path:

```bash
backup_dir="/var/backups/metatrash-accounts-$(date +%Y%m%d-%H%M%S)"
sudo install -d -o root -g root -m 0700 "$backup_dir"
sudo cp -a /etc/metatrash "$backup_dir/config"
sudo cp -a /usr/local/bin/metatrash "$backup_dir/metatrash-before"
sudo cp -a /var/lib/metatrash/accounts.json "$backup_dir/accounts.json"
```

If this is genuinely a fresh installation with no `accounts.json`, omit that
last copy and use the explicit `-empty` initialization below. A missing file on
an existing installation is an error to investigate, not an empty account set.
Keep these account/configuration backups separate from public repositories.

Make the owner-built binary available to the service user without changing the
installed service binary yet:

```bash
sudo install -o root -g metatrash -m 0750 bin/metatrash /var/lib/metatrash/metatrash-migrate-0.6.1
sudo -u metatrash /var/lib/metatrash/metatrash-migrate-0.6.1 accounts-migrate \
  -database-config /etc/metatrash/account-database.json \
  -data /var/lib/metatrash
```

Only for an installation with **no** legacy account file, append `-empty` to
that command. It refuses to ignore an existing file, even an empty one.

The importer shares the service's `.service-lock`. A live lock stops migration;
never remove it while either process is running. Import does not touch Git,
send email, or modify the JSON source. The first import requires an empty users
table. Accounts and the completion marker commit in one transaction. If interrupted
before commit they roll back; after an uncertain commit, rerun the same command
with the unchanged source to verify the imported records. A different source,
duplicate identity, invalid record, or changed imported value is rejected.
Later-added database users are not deleted by verification of the original import.

After success, add `databaseConfigFile` to the **existing**
`/etc/metatrash/accounts.json`, preserving its SMTP/origin configuration. Then:

```bash
sudoedit /etc/metatrash/accounts.json
sudo install -o root -g root -m 0755 bin/metatrash /usr/local/bin/metatrash
/usr/local/bin/metatrash version
sudo systemctl start metatrash
sudo systemctl status metatrash --no-pager
```

Expect version `0.6.1`. Leave the legacy JSON file intact as a protected historical
source; it is no longer read by the service. Include the database in ongoing
private backups, using the site's normal consistent InnoDB backup procedure.
Back up before administrator account changes as well.

Accounts remain optional: without `-accounts-config` or its environment setting,
public/REST/MCP operation requires no database. With accounts enabled, missing
configuration, unavailable DB, unsupported schema, or incomplete migration causes
startup failure. At runtime an account DB outage returns a generic 503 for
authenticated account reads/login verification, with no JSON fallback. Login
codes may still be requested; verification cannot succeed until storage returns.

## Recovery and administration

If migration fails before cutover, keep the source unchanged, correct the
configuration/schema problem, and retry. If necessary, restore the saved binary
and account configuration before restarting the old version. That version does
not recognize `databaseConfigFile`.

Before any database-backed account writes have happened, the saved JSON is a
valid rollback source. **After cutover has accepted new accounts or allowance
changes, do not restart an old binary against the stale JSON file.** Stop writes,
back up the current database, and repair/restore the database-backed version.
If a binary downgrade becomes necessary, first export and reconcile all current
database accounts into a validated version-1 JSON store, retaining IDs, emails,
creation times, and allowances, then restore the old configuration/binary. An
automatic reverse exporter is outside Stage 1; prefer forward recovery.

To change an allowance, use an administrator transaction against the immutable
`user_id`, updating only `max_private_spaces` to a nonnegative integer. Do not
edit legacy JSON; account-page reads now see database values. Do not edit IDs,
change schema/migration markers, or assign existing bearer-key spaces to accounts.

## Focused owner checks

Optional fake-mail/store and import-validation checks (no SMTP or SQL server):

```bash
go test ./internal/service -run '^TestAccounts(Codes|HTTP|MigrationValidation|MailReservation)$' -count=1
```

These cover code handling, immutable session identity, storage-failure behavior,
source validation, and atomic mail-budget reservations. They do not verify the
SQL driver, grants, schema, or database transaction behavior.

On a disposable local database first, apply the schema and import a small
version-1 fixture with a known ID, nanosecond timestamp, and non-default allowance.
Confirm exact preservation, rerun successfully, and verify that a changed source
or an existing conflicting destination is rejected without changing records.
Confirm that startup refuses a schema without a completed migration marker.

After actual cutover, compare the imported count and a known ID/allowance privately;
sign in with that existing email, sign out, and sign in again. Register one new
account and confirm allowance one, then restart and confirm accounts persist while
sessions expire. Confirm public browsing and existing REST/MCP still work. Keep
live mail checks small. On a disposable instance, interrupt DB access and confirm
account reads/verification fail without modifying JSON or issuing a session.

The F1 fix keeps per-IP attempt throttling (30 requests per ten minutes) separate
from atomic delivery reservations. Rejections by narrower delivery limits consume
none of the send budgets. Once admitted, SMTP failures and busy-mail responses
retain their reservation; counters are never refunded. Existing per-email/IP/day
delivery ceilings remain unchanged. F2 and F3 remain pending separate work.

Assistant validation: Go formatting/syntax parsing, source review, example JSON
parsing, and incremental patch checks only. No project build, automated test run,
SMTP send, database connection/import, deployment, or live verification.
Before snapshot: `docs/snapshots/0021-account-database-before/`.
Incremental patch: `patch/0021-account-database.patch`.
