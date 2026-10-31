-- Sign in with Microsoft (0.77.0). Stop the service and back up the database
-- first. Apply after schema v21 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; IF NOT EXISTS
-- makes an interrupted run safe to repeat. See docs/deployment.md.

-- One row per outside account linked to a Metatrash account as a way to sign
-- in. provider is 'microsoft' (later 'google'). issuer is the Microsoft
-- tenant ID (tid) and subject the object ID (oid) in that tenant: together
-- they name one person and never change. The email claim is never a key
-- (Entra does not verify it); email_hint and display_name only label the
-- row on Your account.
CREATE TABLE IF NOT EXISTS metatrash_identities (
    identity_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    provider VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    issuer VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    subject VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    email_hint VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
    display_name VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL DEFAULT '',
    created_at BIGINT NOT NULL,
    last_used_at BIGINT NOT NULL DEFAULT 0,
    UNIQUE KEY metatrash_identities_subject (provider, issuer, subject),
    KEY metatrash_identities_user (user_id),
    CONSTRAINT metatrash_identities_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 22
WHERE singleton_id = 1 AND schema_version = 21 AND migration_source <> '';
