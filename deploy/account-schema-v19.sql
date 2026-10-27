-- Refreshing virtual files from their source (0.60.2). Stop the service and
-- back up the database first. Apply after schema v18 as the schema
-- administrator, then run deploy/account-grants.sql again. DDL implicitly
-- commits; IF NOT EXISTS makes an interrupted run safe to repeat. See
-- docs/deployment.md.

-- One row per virtual file whose pointer has a refresh schedule, keyed by
-- the file's ID so a move keeps it. The runner keeps the rows in step with
-- the pointers and fetches the source again when next_run_at comes round.
-- stale_since and stale_reason are set while refreshing fails (the file
-- keeps its last content) and cleared when it works again; they live here,
-- not in Git, so failures make no commits. Times are Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_virtual_refresh (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    file_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    every VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    next_run_at BIGINT NOT NULL,
    last_run_at BIGINT NULL,
    stale_since BIGINT NULL,
    stale_reason VARCHAR(300) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL,
    PRIMARY KEY (space_id, file_id),
    KEY metatrash_virtual_refresh_due (next_run_at),
    CONSTRAINT metatrash_virtual_refresh_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 19
WHERE singleton_id = 1 AND schema_version = 18 AND migration_source <> '';
