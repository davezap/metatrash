-- Sessions that survive restarts (0.31.0). Stop the service and back up the
-- database first. Apply after schema v9 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; the IF NOT EXISTS
-- clause makes an interrupted run safe to repeat. See docs/deployment.md.

-- One row per signed-in browser. session_digest is the SHA-256 hex of the
-- session cookie's value (a random 256-bit token), never the token itself.
-- auth_at is when the session last proved who the user is (sign-in or
-- confirmation), for the 10-minute step-up window. last_used_at is updated at
-- most every few minutes. Signing out deletes the row; expired rows are
-- ignored and swept. Times are Unix seconds.
CREATE TABLE IF NOT EXISTS metatrash_sessions (
    session_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    auth_at BIGINT NOT NULL,
    last_used_at BIGINT NOT NULL,
    KEY metatrash_sessions_user (user_id, expires_at),
    KEY metatrash_sessions_expiry (expires_at),
    CONSTRAINT metatrash_sessions_user_ref FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_sessions_expiry_after CHECK (expires_at > created_at)
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 10
WHERE singleton_id = 1 AND schema_version = 9 AND migration_source <> '';
