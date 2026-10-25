-- Users' agents (0.55.0). Stop the service and back up the database first.
-- Apply after schema v15 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF NOT EXISTS
-- clauses make an interrupted run safe to repeat. See docs/deployment.md.

-- One row per agent a user adds in Your account → Services: the model
-- provider (minimax or anthropic), an optional endpoint ('' for the
-- provider's own), the model, the most output tokens per reply, the daily
-- token cap (0 = none) and the API key, sealed with AES-256-GCM under the
-- server's agents.keyFile (key_hint is its last four characters, to tell keys
-- apart). A user's default agent runs their folder actions unless an action
-- names a model. Times are Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_agents (
    agent_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    provider VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    base_url VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    model VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    max_tokens INT NOT NULL,
    daily_cap BIGINT NOT NULL,
    key_sealed VARBINARY(1024) NOT NULL,
    key_hint VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    is_default TINYINT(1) NOT NULL DEFAULT 0,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (agent_id),
    UNIQUE KEY metatrash_agents_name (user_id, name),
    CONSTRAINT metatrash_agents_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

-- The agent each job ran on, for its daily cap ('' until a job starts).
-- Removing an agent keeps its jobs' history.
ALTER TABLE metatrash_agent_jobs
    ADD COLUMN IF NOT EXISTS agent_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD KEY IF NOT EXISTS metatrash_agent_jobs_agent (agent_id, finished_at);

UPDATE metatrash_account_meta SET schema_version = 16
WHERE singleton_id = 1 AND schema_version = 15 AND migration_source <> '';
