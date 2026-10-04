-- GitHub connections (0.17.0). Stop the service and back up the database and
-- data directory first. Apply after schema v5 as the schema administrator. DDL
-- implicitly commits; IF NOT EXISTS makes an interrupted run safe to repeat.
-- See docs/deployment.md.

-- One row per Metatrash GitHub App installation linked to a Metatrash account.
-- An installation belongs to one account. github_user_id/github_login are the
-- GitHub user who linked it (verified with a user token that is not stored);
-- account_login/account_type are where the app is installed. Uninstalling the
-- app on GitHub (webhook) or disconnecting on Your account deletes the row.
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

UPDATE metatrash_account_meta SET schema_version = 6
WHERE singleton_id = 1 AND schema_version = 5 AND migration_source <> '';
