-- Web tools and schedules for folder actions (0.57.0). Stop the service and
-- back up the database first. Apply after schema v17 as the schema
-- administrator, then run deploy/account-grants.sql again. DDL implicitly
-- commits; the IF NOT EXISTS clauses make an interrupted run safe to repeat.
-- See docs/deployment.md.

-- Each space's own time zone (an IANA name such as Pacific/Auckland) for
-- "every" schedules and runner.md times. NULL uses the server's
-- agents.timezone. New spaces take the creator's browser time zone.
ALTER TABLE metatrash_spaces
    ADD COLUMN IF NOT EXISTS timezone VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;

-- One row per action with "every": when it runs next and last ran. The
-- runner keeps the rows in step with the folders' .metatrash.json files and
-- queues a job when next_run_at comes round, so a restart neither skips nor
-- doubles a run (a run missed while the service was down happens once, on
-- start). Times are Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_action_schedules (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    folder VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    action VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    every VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    next_run_at BIGINT NOT NULL,
    last_run_at BIGINT NULL,
    PRIMARY KEY (space_id, folder, action),
    KEY metatrash_action_schedules_due (next_run_at),
    CONSTRAINT metatrash_action_schedules_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

-- The downloads cache of web_fetch. Each download is stored once in the
-- downloads bucket as web/<object_hash> (SHA-256 of its bytes); the bucket's
-- lifecycle rule deletes it 30 days after uploaded_at.
CREATE TABLE IF NOT EXISTS metatrash_web_objects (
    object_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    content_type VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    bytes BIGINT NOT NULL,
    uploaded_at BIGINT NOT NULL,
    PRIMARY KEY (object_hash),
    KEY metatrash_web_objects_uploaded (uploaded_at)
) ENGINE=InnoDB;

-- One row per fetch a user's agent made: the URL, its original download and
-- (for a page) its Markdown text, which agents read as web/<ref_id>.<ext>.
-- A reference belongs to the user who paid for the job and expires a day
-- before its files leave the bucket.
CREATE TABLE IF NOT EXISTS metatrash_web_refs (
    ref_id CHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    url VARCHAR(2048) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    url_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    original_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    text_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    extension VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    title VARCHAR(300) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
    text_lines INT NOT NULL,
    etag VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    last_modified VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    PRIMARY KEY (ref_id),
    KEY metatrash_web_refs_url (user_id, url_hash, created_at),
    KEY metatrash_web_refs_expires (expires_at),
    KEY metatrash_web_refs_original (original_hash),
    KEY metatrash_web_refs_text (text_hash),
    CONSTRAINT metatrash_web_refs_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_web_refs_original FOREIGN KEY (original_hash)
        REFERENCES metatrash_web_objects (object_hash) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_web_refs_text FOREIGN KEY (text_hash)
        REFERENCES metatrash_web_objects (object_hash) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 18
WHERE singleton_id = 1 AND schema_version = 17 AND migration_source <> '';
