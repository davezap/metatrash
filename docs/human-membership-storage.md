# Human membership storage — Stage 4 first bite, 0.10.0

This bite implements schema v4 and internal transactional operations for human
invitations and memberships. Stage 4 is **not complete**: there are no invitation
forms, management page, account invitation/joined-space lists, or member browser
access yet. Existing owner browsing and configured REST/MCP access are unchanged.
No invitation email is sent. Agent integration remains separate.

## Storage and transactions

`metatrash_memberships` has one row per immutable `(space_id, user_id)`, with role
`member`, active/suspended status, and Unix-second join/update timestamps. Ownership
continues to come only from `metatrash_spaces.owner_user_id`. Removal deletes the
membership; restoration updates an existing suspended row and never inserts one.
Joined memberships do not enter owned-space allowance calculations.

`metatrash_invitations` has one current slot per space and normalized login email.
It can exist before the recipient registers. Pending slots expire after seven days;
expiry is evaluated from `expires_at`, without a cleanup job. Duplicate live invites
return the same ID without extending the deadline. Reissuing an expired/cancelled
or previously accepted slot generates a new ID and clears its acceptance binding.
This is current-state storage, not an invitation or membership audit trail.

All mutations lock the ready space row first, in a READ COMMITTED transaction with
a five-second timeout. Owner-only operations verify current immutable ownership
inside that transaction. Acceptance looks up the session user's verified email
in the database, checks the matching invitation and expiry, inserts membership,
and binds acceptance to user ID atomically. No username is required for acceptance.
An acceptance retry succeeds only while its bound membership remains active;
it cannot recreate a removed membership or restore a suspended one. Existing
members cannot be reinvited. Owners cannot be invited, suspended, or removed.
Changes are scoped to the selected space. SQL errors returned to callers are generic.

## Owner upgrade

No build, tests, live SQL, or deployment were run. Accounts-disabled operation
requires no database changes. For accounts-enabled 0.10.0:

1. Stop the service, preserve the prior binary/configuration, and take protected
   database and service-data backups using [the database guide](account-database.md).
2. Confirm schema version 3 and a nonempty migration marker, then apply once:

   ```bash
   sudo mariadb metatrash < deploy/account-schema-v4.sql
   ```

3. Retain existing grants and add these as database administrator. Substitute the
   actual service database account host for loopback TCP installations:

   ```sql
   GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_memberships
   TO 'metatrash_accounts'@'localhost';
   GRANT UPDATE (status, updated_at) ON metatrash.metatrash_memberships
   TO 'metatrash_accounts'@'localhost';
   GRANT SELECT, INSERT ON metatrash.metatrash_invitations
   TO 'metatrash_accounts'@'localhost';
   GRANT UPDATE (invitation_id, status, created_at, expires_at, accepted_user_id)
   ON metatrash.metatrash_invitations TO 'metatrash_accounts'@'localhost';
   SHOW GRANTS FOR 'metatrash_accounts'@'localhost';
   SELECT schema_version, migration_source FROM metatrash_account_meta;
   SHOW CREATE TABLE metatrash_memberships;
   SHOW CREATE TABLE metatrash_invitations;
   ```

4. Confirm version 4 and the exact InnoDB tables, keys, foreign keys, collations,
   and checks from the script. Runtime startup checks engines, unique identity
   keys, foreign keys, and required columns. Build/install using the owner workflow.
   Accounts-enabled startup fails closed on old schema versions.

Fresh installations apply v1, import/explicit empty initialization, v2, v3, then
v4 with the corresponding grants. Do not repeat the legacy import to upgrade.

DDL implicitly commits. If interrupted, keep the service stopped and compare
both tables with the script. Execute only the missing CREATE statements after
confirming existing definitions match exactly, then its guarded metadata UPDATE.
Do not rerun the whole script over existing tables or ignore SQL errors. Reconcile
unexpected definitions against backups before restarting.

Prefer fixing forward. To return to 0.9.0, stop service, back up current state,
retain both new tables unchanged, and as administrator set metadata version 4
back to 3. The old binary cannot use invitations/memberships and remains owner-only.
Returning to 0.10.0 requires checking the retained schema and restoring metadata
version 4, not recreating tables. Never overwrite newer accounts/spaces with an
old database backup. This rollback guidance changes when member browsing ships.

## Focused owner checks and next bite

Use a disposable database and in-package harness for the internal methods
`inviteHuman`, `cancelHumanInvitation`, `acceptHumanInvitation`, and
`manageHumanMember`; there is deliberately no HTTP endpoint in this bite.

- Invite before registration; log in with the normalized matching email and accept
  without a username. A different account and a space owner cannot accept it.
- Race duplicate invitations and duplicate acceptance: one live slot and one
  membership result. Cancel/accept races serialize; stale invitation IDs cannot
  affect a replacement. Check expiry at and after the seven-day deadline.
- Suspend, replay acceptance, restore, remove, replay again, then issue a fresh
  invitation. Only explicit restoration or acceptance of the fresh invitation
  restores access state. A restore action after removal must not insert a row.
- Try owner-management calls as another user and compare two spaces sharing a
  member: only the selected owner's space can change. Check owner protection.
- Confirm schema-v3 startup rejection, v4 login/account creation and existing
  owner browsing, and unchanged owned-space denial through REST/MCP.

Next bite: bounded owner management and account listing queries; authenticated,
Origin/CSRF-protected, rate-limited forms; clear no-email-sent messaging; joined
space lists; and current active membership checks on **every** private browser
request, preserving no-store responses and read-only content for all humans.
Formatting, source inspection and incremental patch checks are the only local
verification performed here; database concurrency and UI validation remain owner work.
