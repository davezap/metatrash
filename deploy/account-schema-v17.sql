-- Bring your own key and members' own actions (0.56.0). Stop the service and
-- back up the database first. Apply after schema v16 as the schema
-- administrator, then run deploy/account-grants.sql again. DDL implicitly
-- commits; the IF NOT EXISTS clauses make an interrupted run safe to repeat.
-- See docs/deployment.md.

-- The user whose change brought a job's file (NULL when nobody clear did,
-- as with a GitHub pull). An action with "pays": "contributor" runs on their
-- agent, and a member's own action runs only for that member's files.
ALTER TABLE metatrash_agent_jobs
    ADD COLUMN IF NOT EXISTS contributor_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL;

-- One row per action a user agreed to pay for with one of their agents: an
-- action of a folder ('' for the space root, otherwise a prefix ending in /)
-- whose owner set "pays": "contributor". Their files then run it on that
-- agent, under the agent's daily cap. Removing the agent removes its rows.
-- Times are Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_action_optins (
    optin_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    folder VARCHAR(240) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    action VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    agent_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (optin_id),
    UNIQUE KEY metatrash_action_optins_action (user_id, space_id, folder, action),
    KEY metatrash_action_optins_agent (agent_id),
    CONSTRAINT metatrash_action_optins_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_action_optins_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_action_optins_agent FOREIGN KEY (agent_id)
        REFERENCES metatrash_agents (agent_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 17
WHERE singleton_id = 1 AND schema_version = 16 AND migration_source <> '';
