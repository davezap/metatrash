-- Folder agent tasks (0.54.0). Stop the service and back up the database
-- first. Apply after schema v14 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF NOT EXISTS
-- clause makes an interrupted run safe to repeat. See docs/deployment.md.

-- One row per job of a folder action (a folder's .metatrash.json "actions"):
-- the action, the folder holding it ('' for the space root, otherwise a
-- prefix ending in /), the file that set it off and the commit that brought
-- the file. status is queued, running, done, failed or skipped. The runner
-- records steps (tool calls) and tokens used, and error says why a job
-- failed or was skipped. Each job's outcome is also appended to runner.md in
-- the folder. Times are Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_agent_jobs (
    job_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    folder VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    action VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    file VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    commit_id CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    queued_at BIGINT NOT NULL,
    started_at BIGINT NULL,
    finished_at BIGINT NULL,
    steps INT NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    error TEXT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
    PRIMARY KEY (job_id),
    KEY metatrash_agent_jobs_status (status, queued_at),
    KEY metatrash_agent_jobs_folder (space_id, folder, queued_at),
    CONSTRAINT metatrash_agent_jobs_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 15
WHERE singleton_id = 1 AND schema_version = 14 AND migration_source <> '';
