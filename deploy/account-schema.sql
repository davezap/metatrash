-- Fresh installation only. Run as schema administrator with the service stopped.
CREATE TABLE IF NOT EXISTS metatrash_account_meta (
    singleton_id TINYINT UNSIGNED NOT NULL PRIMARY KEY,
    schema_version INT UNSIGNED NOT NULL,
    migration_source VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT ''
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS metatrash_users (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    email VARCHAR(254) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    
    created_at VARCHAR(35) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    max_private_spaces BIGINT UNSIGNED NOT NULL DEFAULT 1,
    UNIQUE KEY metatrash_users_email (email)
) ENGINE=InnoDB;

ALTER TABLE metatrash_users
    ADD COLUMN username VARCHAR(32) CHARACTER SET ascii COLLATE ascii_general_ci NULL,
    ADD UNIQUE KEY metatrash_users_username (username);

CREATE TABLE metatrash_spaces (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    owner_user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    name VARCHAR(120) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL,
    slug VARCHAR(48) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    visibility VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    provisioning_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at VARCHAR(35) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    UNIQUE KEY metatrash_spaces_owner_slug (owner_user_id, slug),
    CONSTRAINT metatrash_spaces_owner FOREIGN KEY (owner_user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_spaces_private CHECK (visibility = 'private'),
    CONSTRAINT metatrash_spaces_state CHECK (provisioning_state IN ('provisioning', 'ready'))
) ENGINE=InnoDB;

CREATE TABLE metatrash_memberships (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    role VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    joined_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    PRIMARY KEY (space_id, user_id),
    KEY metatrash_memberships_user (user_id, status),
    CONSTRAINT metatrash_memberships_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_memberships_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_memberships_role CHECK (role = 'member'),
    CONSTRAINT metatrash_memberships_status CHECK (status IN ('active', 'suspended'))
) ENGINE=InnoDB;

CREATE TABLE metatrash_invitations (
    invitation_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    email VARCHAR(254) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    accepted_user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    UNIQUE KEY metatrash_invitations_space_email (space_id, email),
    KEY metatrash_invitations_email (email, status, expires_at),
    CONSTRAINT metatrash_invitations_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_invitations_user FOREIGN KEY (accepted_user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_invitations_status CHECK (status IN ('pending', 'accepted', 'cancelled')),
    CONSTRAINT metatrash_invitations_acceptance CHECK (
        (status = 'accepted' AND accepted_user_id IS NOT NULL) OR
        (status IN ('pending', 'cancelled') AND accepted_user_id IS NULL)),
    CONSTRAINT metatrash_invitations_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB;

ALTER TABLE metatrash_memberships
    ADD COLUMN IF NOT EXISTS agent_permission VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin
        NOT NULL DEFAULT 'read_write',
    ADD CONSTRAINT IF NOT EXISTS metatrash_memberships_agent_permission
        CHECK (agent_permission IN ('read_only', 'read_write'));

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

CREATE TABLE IF NOT EXISTS metatrash_github_installations (
    installation_id BIGINT UNSIGNED NOT NULL PRIMARY KEY,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    github_user_id BIGINT UNSIGNED NOT NULL,
    github_login VARCHAR(39) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    account_login VARCHAR(39) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    account_type VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    KEY metatrash_github_installations_user (user_id),
    CONSTRAINT metatrash_github_installations_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_github_installations_type
        CHECK (account_type IN ('User', 'Organization')),
    CONSTRAINT metatrash_github_installations_status
        CHECK (status IN ('active', 'suspended'))
) ENGINE=InnoDB;

ALTER TABLE metatrash_spaces DROP CONSTRAINT IF EXISTS metatrash_spaces_private;

ALTER TABLE metatrash_spaces
    ADD CONSTRAINT IF NOT EXISTS metatrash_spaces_visibility
        CHECK (visibility IN ('private', 'web'));

ALTER TABLE metatrash_users
    ADD COLUMN IF NOT EXISTS email_login TINYINT UNSIGNED NOT NULL DEFAULT 1;

ALTER TABLE metatrash_users
    ADD CONSTRAINT IF NOT EXISTS metatrash_users_email_login CHECK (email_login IN (0, 1));

CREATE TABLE IF NOT EXISTS metatrash_passkeys (
    passkey_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    credential_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    credential_id VARCHAR(1400) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    public_key VARBINARY(1024) NOT NULL,
    algorithm INT NOT NULL,
    sign_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    aaguid CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    transports VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    backup_eligible TINYINT UNSIGNED NOT NULL DEFAULT 0,
    backed_up TINYINT UNSIGNED NOT NULL DEFAULT 0,
    name VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
    created_at BIGINT NOT NULL,
    last_used_at BIGINT NOT NULL DEFAULT 0,
    UNIQUE KEY metatrash_passkeys_credential (credential_hash),
    KEY metatrash_passkeys_user (user_id),
    CONSTRAINT metatrash_passkeys_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_passkeys_algorithm CHECK (algorithm IN (-7, -8, -257))
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS metatrash_totp (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    secret_sealed VARBINARY(256) NOT NULL,
    created_at BIGINT NOT NULL,
    enabled_at BIGINT NOT NULL DEFAULT 0,
    last_used_step BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT metatrash_totp_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS metatrash_recovery_codes (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    code_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, code_hash),
    CONSTRAINT metatrash_recovery_codes_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS metatrash_space_transfers (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    from_user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    to_user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    KEY metatrash_space_transfers_to (to_user_id, expires_at),
    CONSTRAINT metatrash_space_transfers_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_space_transfers_from FOREIGN KEY (from_user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_space_transfers_to_user FOREIGN KEY (to_user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_space_transfers_people CHECK (from_user_id <> to_user_id),
    CONSTRAINT metatrash_space_transfers_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS metatrash_space_aliases (
    owner_user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    slug VARCHAR(48) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (owner_user_id, slug),
    KEY metatrash_space_aliases_space (space_id),
    CONSTRAINT metatrash_space_aliases_owner FOREIGN KEY (owner_user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT metatrash_space_aliases_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

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

ALTER TABLE metatrash_sessions
    ADD COLUMN IF NOT EXISTS device VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS ip VARCHAR(45) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '';

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

ALTER TABLE metatrash_agent_jobs
    ADD COLUMN IF NOT EXISTS agent_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD KEY IF NOT EXISTS metatrash_agent_jobs_agent (agent_id, finished_at);

ALTER TABLE metatrash_agent_jobs
    ADD COLUMN IF NOT EXISTS contributor_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL;

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

ALTER TABLE metatrash_spaces
    ADD COLUMN IF NOT EXISTS timezone VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL;

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

CREATE TABLE IF NOT EXISTS metatrash_web_objects (
    object_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    content_type VARCHAR(100) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    bytes BIGINT NOT NULL,
    uploaded_at BIGINT NOT NULL,
    PRIMARY KEY (object_hash),
    KEY metatrash_web_objects_uploaded (uploaded_at)
) ENGINE=InnoDB;

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

CREATE TABLE IF NOT EXISTS metatrash_virtual_refresh (
    space_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    file_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    every VARCHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    next_run_at BIGINT NOT NULL,
    last_run_at BIGINT NULL,
    stale_since BIGINT NULL,
    stale_reason VARCHAR(300) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NULL,
    PRIMARY KEY (space_id, file_id),
    KEY metatrash_virtual_refresh_due (next_run_at),
    CONSTRAINT metatrash_virtual_refresh_space FOREIGN KEY (space_id)
        REFERENCES metatrash_spaces (space_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

ALTER TABLE metatrash_spaces
    ADD COLUMN IF NOT EXISTS deleted_at BIGINT NULL;

ALTER TABLE metatrash_spaces DROP CONSTRAINT IF EXISTS metatrash_spaces_state;

ALTER TABLE metatrash_spaces
    ADD CONSTRAINT IF NOT EXISTS metatrash_spaces_state
        CHECK (provisioning_state IN ('provisioning', 'ready', 'deleted', 'purged'));

ALTER TABLE metatrash_users
    ADD COLUMN IF NOT EXISTS deletion_requested_at BIGINT NULL,
    ADD COLUMN IF NOT EXISTS deleted_at BIGINT NULL;

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
INSERT INTO metatrash_account_meta VALUES (1,25,'empty');
CREATE TABLE IF NOT EXISTS metatrash_schema_components (
 component VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 version INT UNSIGNED NOT NULL,
 checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
) ENGINE=InnoDB;
INSERT INTO metatrash_schema_components VALUES ('core',1,'1609e5a468263995274373ebbc024c8270a934cb522289c1ee92cadbdfa38b3d');
