-- Deleting a space (0.67.0). Stop the service and back up the database
-- first. Apply after schema v19 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF EXISTS /
-- IF NOT EXISTS clauses make an interrupted run safe to repeat. See
-- docs/deployment.md.

-- An owner's deleted space is hidden from everyone and kept for 30 days
-- ('deleted', deleted_at in Unix seconds), so the operator can restore it
-- with `metatrash spaces restore`. Then the runner removes its repository
-- and virtual files and marks the row 'purged', renaming its slug to
-- deleted-<space_id> so the address is free again. The row stays: other
-- tables still name the space.
ALTER TABLE metatrash_spaces
    ADD COLUMN IF NOT EXISTS deleted_at BIGINT NULL;
ALTER TABLE metatrash_spaces DROP CONSTRAINT IF EXISTS metatrash_spaces_state;
ALTER TABLE metatrash_spaces
    ADD CONSTRAINT IF NOT EXISTS metatrash_spaces_state
        CHECK (provisioning_state IN ('provisioning', 'ready', 'deleted', 'purged'));

UPDATE metatrash_account_meta SET schema_version = 20
WHERE singleton_id = 1 AND schema_version = 19 AND migration_source <> '';
