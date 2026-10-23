-- Reviews and quizzes (0.50.0). Stop the service and back up the database
-- first. Apply after schema v13 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF NOT EXISTS
-- clauses make an interrupted run safe to repeat. See docs/deployment.md.

-- Every filed copy now has a review status: submitted (waiting), approved
-- or declined. A reviewer's review updates the row (status, score, marks,
-- reviewed_by, reviewed_at, note and the copy's new text). template_blob is
-- the template text the answers were given to, target_file_id the filed
-- copy's file ID. Quiz copies (quiz = 1) keep a score once marked and marks
-- (JSON: field name to true, false or null while a reviewer has to tick
-- it). answers is the checked answers as JSON. Rows from 0.49 keep the
-- defaults: status submitted, no answers, so they cannot be reviewed.
ALTER TABLE metatrash_form_submissions
    ADD COLUMN IF NOT EXISTS template_blob CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER template_revision,
    ADD COLUMN IF NOT EXISTS target_file_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '' AFTER target_path,
    ADD COLUMN IF NOT EXISTS quiz TINYINT(1) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'submitted',
    ADD COLUMN IF NOT EXISTS score SMALLINT NULL,
    ADD COLUMN IF NOT EXISTS answers MEDIUMTEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
    ADD COLUMN IF NOT EXISTS marks TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
    ADD COLUMN IF NOT EXISTS reviewed_by CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD COLUMN IF NOT EXISTS reviewed_at BIGINT NULL,
    ADD COLUMN IF NOT EXISTS note TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
    ADD KEY IF NOT EXISTS metatrash_form_submissions_quiz (space_id, quiz, user_id);

UPDATE metatrash_account_meta SET schema_version = 14
WHERE singleton_id = 1 AND schema_version = 13 AND migration_source <> '';
