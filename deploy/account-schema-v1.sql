-- Apply to a dedicated, empty MetaTrash database as its schema administrator.
-- DDL is intentionally separate from the transactional account import.
-- Re-running this script preserves existing tables and migration state.
CREATE TABLE IF NOT EXISTS metatrash_account_meta (
    singleton_id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    schema_version INT UNSIGNED NOT NULL,
    migration_source VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT ''
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS metatrash_users (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    email VARCHAR(254) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    -- RFC3339Nano UTC preserves existing Go timestamp precision exactly.
    created_at VARCHAR(35) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    max_private_spaces BIGINT UNSIGNED NOT NULL DEFAULT 1,
    UNIQUE KEY metatrash_users_email (email)
) ENGINE=InnoDB;

INSERT INTO metatrash_account_meta (singleton_id, schema_version, migration_source)
SELECT 1, 1, '' WHERE NOT EXISTS (
    SELECT 1 FROM metatrash_account_meta WHERE singleton_id = 1
);
