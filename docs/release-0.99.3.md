# Core 0.99.3: optional storage limit per space

Prepared 2026-11-19 by Claude (thread "Build the batch of small site fixes")
for Company 0.96.60, with Slate 0.25.0 and Slate Viewer 0.45.0.

Tier: Core. One person running their own instance may want to cap a space
(a shared space that members fill), and the check belongs where every write
is admitted. The account-wide allowance it sits under is Company billing's
(WriteGate), unchanged.

## What changes

- Rule (Dave, 19 Nov): a space has no size of its own by default; it grows up
  to the owner's pooled allowance. Only bytes are pooled: file count,
  single-file size and repository size stay server-wide safety ceilings in
  spaces.json.
- An owner may set a limit per space (Settings → Spaces → space → General).
  Over it, adding changes are refused with 507 `space_limit`; removing files
  still works. `space_limit.go`, called first in `checkWrite`.
- `POST /api/v1/site/account/spaces/{id}/storage-limit {"limit": bytes|null}`;
  the account answer gains `spaceLimits` and each space `limit`.

## Database

Additive, schema stays v25, safe for the old executable (rollback keeps the
column). Order matters: the column first, then the grants.

    sudo mariadb metatrash < deploy/account-space-storage-limit.sql
    sudo mariadb < deploy/account-grants.sql

If the column is missing the new executable still starts; it logs "space
storage limits are off" and Settings shows no limit field.

## spaces.json

`defaults.storage.maxRepositoryBytes` is still each space's ceiling. With
spaces now meant to grow up to the owner's allowance, raise it to what the
disk can hold (the servers have 12 GB, about 88% used).

## Checked in the cloud

gofmt, go vet, and the full `go test ./...` against MariaDB with
account-schema.sql and account-grants.sql (see the thread for the result).
