-- OAuth agent access foundation (0.12.0). Stop the service and back up the
-- database and data directory first. Apply after schema v4 as the schema
-- administrator. DDL implicitly commits; MariaDB IF NOT EXISTS clauses make an
-- interrupted run safe to repeat. See docs/deployment.md.

-- Owner-set permission for a member's connected apps. Owners always have
-- read/write on their own spaces and have no membership row.
ALTER TABLE metatrash_memberships
    ADD COLUMN IF NOT EXISTS agent_permission VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin
        NOT NULL DEFAULT 'read_write',
    ADD CONSTRAINT IF NOT EXISTS metatrash_memberships_agent_permission
        CHECK (agent_permission IN ('read_only', 'read_write'));

-- One connection per account, client and protected resource. Consent may be
-- replaced by a later approval; revoking deletes the row and cascades.
CREATE TABLE IF NOT EXISTS metatrash_oauth_grants (
    grant_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    client_id VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    client_name VARCHAR(100) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
    resource VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    scope VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    last_used_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    UNIQUE KEY metatrash_oauth_grants_connection (user_id, client_id, resource),
    KEY metatrash_oauth_grants_expiry (expires_at),
    CONSTRAINT metatrash_oauth_grants_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_oauth_grants_scope
        CHECK (scope IN ('spaces:read', 'spaces:read spaces:write')),
    CONSTRAINT metatrash_oauth_grants_expiry_order CHECK (expires_at > created_at)
) ENGINE=InnoDB;

-- Consented spaces. member_user_id is NULL when the grant's account owns the
-- space, otherwise it equals the grant's account and binds the row to that
-- membership: removing the membership deletes the consent, so re-invitation
-- never revives it. Suspension keeps the row but access checks deny it.
CREATE TABLE IF NOT EXISTS metatrash_oauth_grant_spaces (
    grant_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    member_user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    permission VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    PRIMARY KEY (grant_id, space_id),
    KEY metatrash_oauth_grant_spaces_member (space_id, member_user_id),
    CONSTRAINT metatrash_oauth_grant_spaces_grant FOREIGN KEY (grant_id)
        REFERENCES metatrash_oauth_grants (grant_id) ON DELETE CASCADE ON UPDATE RESTRICT,
    CONSTRAINT metatrash_oauth_grant_spaces_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_oauth_grant_spaces_member FOREIGN KEY (space_id, member_user_id)
        REFERENCES metatrash_memberships (space_id, user_id) ON DELETE CASCADE ON UPDATE RESTRICT,
    CONSTRAINT metatrash_oauth_grant_spaces_permission
        CHECK (permission IN ('read_only', 'read_write'))
) ENGINE=InnoDB;

-- SHA-256 digests of opaque access and refresh tokens; the tokens themselves
-- are never stored. A spent refresh token is kept until expiry so that replay
-- can be detected and the whole grant revoked.
CREATE TABLE IF NOT EXISTS metatrash_oauth_tokens (
    token_digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    grant_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    kind VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    scope VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    used_at BIGINT NULL,
    KEY metatrash_oauth_tokens_grant (grant_id, kind),
    KEY metatrash_oauth_tokens_expiry (expires_at),
    CONSTRAINT metatrash_oauth_tokens_grant FOREIGN KEY (grant_id)
        REFERENCES metatrash_oauth_grants (grant_id) ON DELETE CASCADE ON UPDATE RESTRICT,
    CONSTRAINT metatrash_oauth_tokens_kind CHECK (kind IN ('access', 'refresh')),
    CONSTRAINT metatrash_oauth_tokens_scope
        CHECK (scope IN ('spaces:read', 'spaces:read spaces:write')),
    CONSTRAINT metatrash_oauth_tokens_use CHECK (used_at IS NULL OR kind = 'refresh')
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 5
WHERE singleton_id = 1 AND schema_version = 4 AND migration_source <> '';
