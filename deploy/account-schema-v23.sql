-- Measuring storage (0.89.0). Stop the service and back up the database
-- first. Apply after schema v22 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; IF NOT EXISTS
-- makes an interrupted run safe to repeat. See docs/deployment.md.

-- One row per owned space with what it takes up, so Settings and the
-- console can total it per owner. git_bytes is the space's Git repository on
-- disk (every version of every text file, so history counts). file_bytes and
-- file_objects are the space's objects under files/<space_id>/ in the
-- space-files bucket (virtual files' content and text, every version kept),
-- each object counted once however many files point at it. The service
-- updates the row after each write and measures it all again daily;
-- *_measured_at say when (Unix seconds; files_measured_at is NULL until the
-- bucket was first listed). The row goes when the space is purged.
CREATE TABLE IF NOT EXISTS metatrash_space_usage (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    git_bytes BIGINT NOT NULL DEFAULT 0,
    file_bytes BIGINT NOT NULL DEFAULT 0,
    file_objects INT NOT NULL DEFAULT 0,
    git_measured_at BIGINT NOT NULL,
    files_measured_at BIGINT NULL,
    CONSTRAINT metatrash_space_usage_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 23
WHERE singleton_id = 1 AND schema_version = 22 AND migration_source <> '';
