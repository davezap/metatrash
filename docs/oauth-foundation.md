# OAuth foundation — schema v5 and configuration, 0.12.0

First bite of the [OAuth agent access plan](oauth-agent-access-plan.md). It adds
storage and configuration only: no OAuth endpoint exists yet, no token is issued
and agent access to owned spaces stays closed. Later bites build on this schema
without further SQL.

## Library decision

Neither candidate fitted the narrow profile Metatrash needs (public clients,
PKCE S256, authorization code plus rotating refresh tokens, opaque digested
tokens, Client ID Metadata Documents, RFC 8707 `resource`):

- `ory/fosite` has had no tagged release since v0.49.0 (December 2024), pulls a
  large dependency tree (ory/x, OpenTelemetry, viper, pop and others) and has no
  `resource` or CIMD support.
- `zitadel/oidc` is active, but releases from v3.51.9 require Go 1.26. It is
  OIDC-first (signing keys, JWKS, ID tokens), has a 38-method storage interface,
  owns the authorize routing and login handoff, and has no `resource` field.

Owner decision (2026-10-02): implement the profile in the service with the
standard library, against the specifications, with every protocol rule covered
by unit tests. No new Go module dependencies.

## Schema v5

- `metatrash_memberships.agent_permission`: `read_only` or `read_write`,
  default `read_write`. Owners have no membership row and always have
  read/write on their own spaces.
- `metatrash_oauth_grants`: one connection per account, client and protected
  resource, with the client's display name, granted scope, and creation, update,
  last-use and idle-expiry times (Unix seconds).
- `metatrash_oauth_grant_spaces`: consented spaces with `read_only` or
  `read_write`. Rows for joined spaces reference the membership through a
  composite foreign key with `ON DELETE CASCADE`, so removing a member deletes
  that space from all of the member's grants and re-invitation never revives
  old consent. Suspension keeps the row; access checks deny it.
- `metatrash_oauth_tokens`: SHA-256 digests of opaque access and refresh tokens,
  never the tokens. Spent refresh tokens are kept until expiry so that replay
  can be detected. Deleting a grant cascades to its spaces and tokens.

Pending authorizations and authorization codes are short-lived (minutes, and
60 seconds single-use) and are held in service memory, like login codes and
sessions. A restart during sign-in means the app retries; nothing persistent is
lost. Client metadata documents are cached in memory for at most 24 hours.

Startup with accounts enabled requires schema version 5 and checks the new
tables, unique keys, foreign keys with their delete rules, and columns.

## Configuration

The account configuration file accepts an optional `oauth` object. OAuth stays
off unless `enabled` is `true`, and the settings are validated at startup even
when disabled:

```json
"oauth": {
  "enabled": false,
  "clientHosts": ["claude.ai", "chatgpt.com"],
  "accessTokenMinutes": 60,
  "refreshTokenDays": 30,
  "codeSeconds": 60,
  "grantIdleDays": 90
}
```

- `clientHosts` (D5): hosts whose Client ID Metadata Documents may be fetched.
  Exact DNS names, lowercase, 1–32 entries; omit for the default pair.
- Lifetimes (D4): access token 5–1440 minutes, refresh token 1–365 days
  (sliding, rotated on every use), code 10–600 seconds, grant idle expiry
  1–730 days and at least the refresh lifetime. Zero or omitted uses the
  defaults shown.
- The issuer is the service's `-public-url` (for example
  `https://metatrash.com`), so it cannot drift from the address users see.

## Other changes

- Repository-bound dispatch: transports pass the repository admitted by their
  access check to `dispatchRepository`. `Dispatch` still reaches configured
  spaces only and `Access` still refuses owned spaces.
- Owned-space operation quotas moved into one helper shared by human browsing
  now and agent operations later; writes get their own per-client bucket.

## Owner upgrade

Accounts-disabled installations need no database changes.

1. Stop the service, keep the previous binary and configuration, and take
   protected database and data-directory backups ([database guide](account-database.md)).
2. Confirm schema version 4 and a nonempty migration marker, then apply:

   ```bash
   sudo mariadb metatrash < deploy/account-schema-v5.sql
   ```

3. Keep existing grants and add these as database administrator (substitute the
   service account host for loopback TCP installations):

   ```sql
   GRANT UPDATE (agent_permission) ON metatrash.metatrash_memberships
   TO 'metatrash_accounts'@'localhost';
   GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_grants
   TO 'metatrash_accounts'@'localhost';
   GRANT UPDATE (client_name, scope, updated_at, last_used_at, expires_at)
   ON metatrash.metatrash_oauth_grants TO 'metatrash_accounts'@'localhost';
   GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_grant_spaces
   TO 'metatrash_accounts'@'localhost';
   GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_tokens
   TO 'metatrash_accounts'@'localhost';
   GRANT UPDATE (used_at) ON metatrash.metatrash_oauth_tokens
   TO 'metatrash_accounts'@'localhost';
   SHOW GRANTS FOR 'metatrash_accounts'@'localhost';
   SELECT schema_version, migration_source FROM metatrash_account_meta;
   ```

4. Confirm version 5, build and install, and start the service. Startup fails
   closed with a pointer to this page if anything is missing.

Fresh installations apply v1, the import or empty initialization, then v2, v3,
v4 and v5 with the corresponding grants.

The script uses MariaDB `IF NOT EXISTS` clauses, so an interrupted run can be
repeated as administrator; the final metadata update is guarded on version 4.
If a repeat reports an error, keep the service stopped and compare the tables
with the script before changing anything.

To return to 0.11.0: stop the service, back up, leave the new column and tables
in place and set the metadata version from 5 back to 4. The 0.11.0 binary
ignores them. To come back, set it to 5 again; never recreate the tables.

## Checks

Builds, vet and the existing unit tests were run for this bite against Go
1.25.1. The schema script, readiness check and the grants above were applied
to a disposable MariaDB 10.11 database (`METATRASH_TEST_DB_CONFIG`, see
`internal/service/db_integration_test.go`); a v4 database is refused at startup
and repeating the script is harmless. Owner checks:

- Upgrade with the steps above; the service starts with accounts enabled.
- Browsing, sharing and the public `/mcp` behave exactly as in 0.11.0.
- Set `oauth.enabled` with an invalid host or lifetime and confirm startup
  refuses the configuration.
