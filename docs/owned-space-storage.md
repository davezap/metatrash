# Owned-space storage — Stage 3 first bite, 0.8.0

**0.9.0 follow-up:** the [account creation/listing and owner-only browser flow](owned-space-browser.md) is now implemented using this same schema and grants. The remainder of this document records the 0.8.0 foundation.

This release adds the internal storage/provisioning foundation. It does not yet
offer a browser create form, owned-space listing, or private document URLs.
Those are the next two Stage 3 bites. Sharing and agent integration stay deferred.

## Metadata and allowance

Schema v3 adds `metatrash_spaces`: immutable `space_id`, owner `user_id`, name,
fixed slug, private visibility, creation time, and `provisioning`/`ready` state.
The owner foreign key is authoritative; no owner membership is duplicated.
The database enforces `(owner_user_id, slug)` uniqueness. Names allow 1–120
Unicode characters without controls; slugs normalize to lowercase and allow
1–48 ASCII letters/digits with single interior hyphens. Names are not routing keys.

The internal creation method requires an existing owner with a username. Future
HTTP callers must take the immutable owner ID from the authenticated session.
It locks the owner row and counts every reservation in a READ COMMITTED
transaction before inserting. This preserves the existing default allowance of
one and administrator overrides, including zero, under concurrent requests.
No owned-space endpoint is exposed in this release.

A retry with the same owner, normalized name, and slug reuses the existing ID,
even when the allowance is full. A different name at an occupied slug conflicts.
There is no automatic slug suffix or allowance refund on timeout/failure.

## Git provisioning and access boundary

Each owned repository lives at `<data>/owned-repos/<space_id>.git`; no username,
slug, or display name is used in its filesystem path. Configured repositories
remain in `<data>/repos` and are never assigned to accounts.

Creation commits the database reservation first. The existing service write
queue then provisions a bare Git repository with a protected README explaining
read-only human access and deferred agent access. It uses the existing temporary
repository plus rename procedure. Only after Git is ready does it mark the
database row ready and publish the repository into a separate RWMutex-protected
registry. Existing configuration/repository maps remain immutable while serving.
Owned repositories use the configured default storage limits.

The shared agent access check explicitly rejects registered owned IDs. Pending
IDs are absent from the configured-space registry and are also rejected. No owned
repository is passed to agent Dispatch, public browsing, or recent activity.
Startup rejects any owned ID that collides with an administrator-configured name.
Existing bearer-key spaces keep their behavior. Knowing an ID grants no access.

## Owner upgrade

No live SQL, build, tests, or deployment were performed during implementation.
For accounts-disabled use, no database upgrade is needed.

1. Stop the service. Keep the old binary/configuration and take a protected
   database backup plus a matching backup of the service data directory. Follow
   the backup procedure in [account database setup](account-database.md).
2. As database administrator, confirm schema version 2 and a nonempty migration
   marker. Apply the new script **once** from this checkout:

   ```bash
   sudo mariadb metatrash < deploy/account-schema-v3.sql
   ```

3. Retain existing grants and add these for the existing service login. Substitute
   its actual account host if using the loopback-TCP account rather than localhost:

   ```sql
   GRANT SELECT, INSERT ON metatrash.metatrash_spaces
   TO 'metatrash_accounts'@'localhost';
   GRANT UPDATE (provisioning_state) ON metatrash.metatrash_spaces
   TO 'metatrash_accounts'@'localhost';
   SHOW GRANTS FOR 'metatrash_accounts'@'localhost';
   SELECT schema_version, migration_source FROM metatrash_account_meta;
   SHOW CREATE TABLE metatrash_spaces;
   ```

4. Confirm version 3, InnoDB, the primary key, owner/slug unique key, owner foreign
   key, and state/visibility checks. Build/install 0.8.0 using the normal owner
   workflow, then restart. Account sessions expire on restart as before.

The runtime login has no DELETE grant or UPDATE access to IDs, names, ownership,
or slugs. Accounts-enabled 0.8.0 requires v3; it does not silently accept v2.
For a fresh installation: v1, account import/explicit empty initialization, v2,
then v3 and their grants. Do not rerun the legacy import for this upgrade.

DDL implicitly commits. If interrupted, keep the service stopped and inspect
the table and version. If the table matches the script exactly but version is
still 2, run only its final guarded UPDATE. If no table exists, reapply the
script. Reconcile any other partial state against the backup before restarting;
do not blindly ignore SQL errors or overwrite a populated table.

## Provisioning recovery

- A reservation with no published repository is retried on startup using its
  original ID. Temporary work is never treated as a ready repository.
- If Git publication completed but the database ready update did not, startup
  validates that repository and completes the update. A lost database response
  is handled by rereading state before another queued preparation attempt.
- A failed pending attempt is logged by ID, stays hidden, and retains its allowance.
  Correct the disk, permissions, storage-limit, or database problem and restart.
  The future create handler can also retry the same name/slug through the internal
  creation method. There is no retry loop or public recovery endpoint in this bite.
- A ready row whose repository is missing or unreadable stops startup. Restore
  the correct repository from backup or repair its permissions; do not mark it
  provisioning to manufacture an empty replacement.

To abandon a failed reservation, stop the service and back up both stores. As
administrator, verify the row is still provisioning, inspect its exact ID-derived
repository and any orphan `.provision-*`/`.objects-*` directories under owned-repos,
and preserve any recoverable content outside managed storage. Only after safely
reconciling/removing that attempt's repository may the administrator delete that
specific provisioning row to release allowance. Never delete a ready row this way,
and never remove a live process's service lock or temporary directories. The
application deliberately offers no automatic destructive cleanup/refund.

Prefer fixing forward. To roll back the binary, stop the service, back up current
state, and reconcile outstanding provisioning first. A schema-v2 binary requires
the metadata version restored to 2; retain the spaces table and owned-repos intact.
It cannot expose or manage those spaces. Returning to 0.8.0 requires restoring
version 3 after checking the table, not rerunning CREATE TABLE. Never restore an
old database backup over accounts, usernames, or spaces created since that backup.

## Focused owner validation

For this foundation, use a disposable database and an in-package service harness
to call `createOwnedSpace`; the browser controls are intentionally still placeholders.

- Confirm login/public browsing and existing configured REST/MCP spaces still work.
- Reject creation without a username; accept normalized valid names/slugs, reject
  controls and invalid/oversized slugs. Two owners can use the same slug.
- With allowance one, race different slugs for one owner: exactly one reservation
  wins. Repeat the same name/slug concurrently: one row/repository/ID results.
  Verify allowance zero and a larger administrator override.
- Interrupt before Git publication and after publication but before ready marking.
  Restart; the same ID is recovered, with no extra allowance consumed. Force a
  provisioning failure and confirm its reservation continues to count.
- Temporarily withhold a ready repository in the disposable setup: startup must
  fail rather than create an empty repository. Restore it before restarting.
- Check read/write/list/history/move through both agent transports with an owned
  ID and any configured bearer key: all must fail. Public listings/recent activity
  must never include owned content. Check concurrent public requests during creation.

Implementation verification was limited to Go formatting, source inspection, and
patch checks. Database behavior and interrupted-process recovery need owner validation.
