-- Signed-in devices (0.32.0). Stop the service and back up the database
-- first. Apply after schema v10 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF NOT EXISTS
-- clauses make an interrupted run safe to repeat. See docs/deployment.md.

-- What Security shows for each signed-in browser: a label made from the
-- browser's User-Agent at sign-in ("Chrome on Windows"; the header itself is
-- not kept) and the IP address it signed in from. Both are deleted with the
-- session (sign-out or expiry within 24 hours). Sessions from before v11 have
-- neither.
ALTER TABLE metatrash_sessions
    ADD COLUMN IF NOT EXISTS device VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ip VARCHAR(45) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '';

UPDATE metatrash_account_meta SET schema_version = 11
WHERE singleton_id = 1 AND schema_version = 10 AND migration_source <> '';
