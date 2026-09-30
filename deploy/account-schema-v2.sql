-- Stage 2 upgrade: stop the service and back up the database first.
-- Apply ONCE after schema v1 and the completed legacy import/empty initialization.
-- DDL implicitly commits; see docs/public-usernames.md for interrupted upgrades.
ALTER TABLE metatrash_users
    ADD COLUMN username VARCHAR(32) CHARACTER SET ascii COLLATE ascii_general_ci NULL,
    ADD UNIQUE KEY metatrash_users_username (username);

UPDATE metatrash_account_meta SET schema_version = 2
WHERE singleton_id = 1 AND schema_version = 1 AND migration_source <> '';
