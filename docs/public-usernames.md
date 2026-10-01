# Public usernames — Stage 2, 0.7.0

**0.8.0 follow-up:** after v2, apply [the owned-space schema v3 upgrade](owned-space-storage.md) before starting an accounts-enabled 0.8.0 binary. The instructions below describe the original 0.7.0 upgrade.

The account page now offers a one-time public username choice, plus My Spaces
and Invitations placeholders. Existing and new accounts start without a username;
email-code login still works. No name is derived from email. User IDs remain the
relationship keys. Space creation and invitations remain later stages; Stage 3
must require a username for creation, while invitation acceptance must not.

Names are 3–32 ASCII letters or digits with optional single interior hyphens.
Leading/trailing whitespace is trimmed and letters are lowercased before saving.
No leading, trailing, or repeated hyphens, underscores, dots, or Unicode are allowed.
Reserved names are: public, admin, administrator, api, mcp, login, logout, account,
accounts, spaces, assets, health, healthz, static, docs, support, help, www, mail,
root, system, metatrash, settings, signup, register, robots, sitemap, favicon.

The nullable username column permits multiple accounts with no chosen name. Its
full-length case-insensitive unique index enforces global uniqueness, including
concurrent selections. A conditional update only changes NULL, so competing
requests cannot replace a selected name. The authenticated POST uses exact Origin,
an action-specific session CSRF token, bounded form input, and a per-user attempt
limit (20 per ten minutes). Account responses retain no-store caching. Names are
public; email stays private. Folder-hosting prefixes remain in forms and redirects.

## Upgrade an existing Stage 1 installation

Owner-run steps; no server or database commands were run during implementation.
Use the existing build workflow for 0.7.0. Stop the service before schema changes
and take a protected database backup using the Stage 1 backup procedure. Keep the
old binary and configuration. Do not rerun the legacy import for this upgrade.

In an administrator client, confirm schema_version is 1 and migration_source is
nonempty in metatrash_account_meta. Then apply the upgrade once from the checkout:

```bash
sudo mariadb metatrash < deploy/account-schema-v2.sql
```

Grant the existing service login permission to update only the username column:

```sql
GRANT UPDATE (username) ON metatrash.metatrash_users
TO 'metatrash_accounts'@'localhost';
SHOW GRANTS FOR 'metatrash_accounts'@'localhost';
SELECT schema_version, migration_source FROM metatrash_account_meta;
SHOW CREATE TABLE metatrash_users;
```

Use the existing account host (for example 127.0.0.1 for TCP) in place of localhost
where appropriate. Retain the existing SELECT/INSERT and metadata grants. Confirm
version 2, a nullable username column, and the metatrash_users_username unique
index. Install the new binary using the existing deployment procedure and start
the service. Login sessions expire on restart as before. Accounts-enabled 0.7.0
requires schema 2; accounts-disabled operation still needs no database.

For a fresh installation, apply v1, complete accounts-migrate (import or explicit
empty initialization), then apply v2 and its column grant before starting 0.7.0.
The migration command can still verify a previous import on either schema version
without reading or overwriting usernames.

## Interrupted upgrade and recovery

MySQL/MariaDB DDL implicitly commits. This script is intentionally not repeatable:
do not blindly reapply it or ignore SQL errors. Keep the service stopped and inspect
SHOW CREATE TABLE and the metadata row. If the column and unique index are both
present with the exact definitions in v2 but the version is still 1, run only the
final guarded UPDATE from the script. If neither exists, rerun the script. For
any other partial state, reconcile against the backup with the administrator
before starting the service. Missing completed import metadata is not repaired
by the v2 upgrade.

Prefer fixing forward. To revert to the Stage 1 binary while preserving data,
stop the service, back up the current database, and set schema_version back to 1
only after confirming the v1 columns and import marker are intact. Leave username
data and its unique index in place: Stage 1 ignores the extra column. Returning
to 0.7.0 then requires restoring version 2, not rerunning ALTER TABLE. Never drop
the username column or restore an old backup over accounts/names created since
upgrade without reconciling those records first.

## Focused owner checks

- Sign in with an existing account and a new account; both work without usernames.
- Choose a mixed-case name; the page shows the lowercase name after reload/login,
  hides the form, and retains email and allowance. Check mobile layout as well.
- Reject reserved/invalid names, cross-origin or missing-CSRF POSTs, and expired
  sessions. Repeat selection with another name must leave the first unchanged.
- With two accounts, race the same normalized name: exactly one succeeds. Race
  two different names for one account: exactly one persists. Verify the database
  constraint directly in a disposable database if desired.
- Check a folder-hosted installation, database outage behavior, and the My Spaces
  and Invitations placeholders. No private-space content or agent access is added.

Focused fake-store owner tests were extended for validation, CSRF, fixed-name UI,
and expired sessions. They do not substitute for the real SQL race checks above.
No build, test execution, live SQL, or deployment was performed. Before snapshot:
`docs/snapshots/0024-public-usernames-before/`; incremental patch:
`patch/0024-public-usernames.patch`.
