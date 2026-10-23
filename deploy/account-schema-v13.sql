-- Form submissions (0.49.0). Stop the service and back up the database
-- first. Apply after schema v12 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF NOT EXISTS
-- clause makes an interrupted run safe to repeat. See docs/deployment.md.

-- One row per form a person sent: the receipt behind My Submissions. The
-- filled-in copy is filed as a file in the target space (target_space_id,
-- target_path); text keeps that copy, so the person can see what they sent
-- without access to the target space. space_id is the space holding the
-- template, template_path where it was and template_revision the space's
-- state the answers were checked against. Rows are never updated. Times are
-- Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_form_submissions (
    submission_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    form VARCHAR(48) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    title VARCHAR(200) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    template_path VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    template_file_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    template_revision CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    target_space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    target_path VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    submitted_at BIGINT NOT NULL,
    text MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    PRIMARY KEY (submission_id),
    KEY metatrash_form_submissions_person (space_id, user_id, submitted_at),
    KEY metatrash_form_submissions_target (target_space_id),
    CONSTRAINT metatrash_form_submissions_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_form_submissions_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_form_submissions_target FOREIGN KEY (target_space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 13
WHERE singleton_id = 1 AND schema_version = 12 AND migration_source <> '';
