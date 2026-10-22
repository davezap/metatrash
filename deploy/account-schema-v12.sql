-- Policy acknowledgements (0.48.0). Stop the service and back up the
-- database first. Apply after schema v11 as the schema administrator, then
-- run deploy/account-grants.sql again. DDL implicitly commits; the IF NOT
-- EXISTS clause makes an interrupted run safe to repeat. See
-- docs/deployment.md.

-- One row per person, file and published text they confirmed reading in a
-- folder with an acknowledge service. A new published version asks again only
-- for files whose text (blob_hash, the Git blob) changed. Rows are never
-- updated; reading the same text again keeps the first row. file_id is the
-- space's file ID, path where the file was when confirmed. Times are Unix
-- seconds.
CREATE TABLE IF NOT EXISTS metatrash_acknowledgements (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    file_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    blob_hash CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    version INT NOT NULL,
    path VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    acknowledged_at BIGINT NOT NULL,
    PRIMARY KEY (space_id, user_id, file_id, blob_hash),
    KEY metatrash_acknowledgements_file (space_id, file_id),
    CONSTRAINT metatrash_acknowledgements_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_acknowledgements_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_acknowledgements_version CHECK (version > 0)
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 12
WHERE singleton_id = 1 AND schema_version = 11 AND migration_source <> '';
