-- Sign-in methods (0.22.0). Stop the service and back up the database and
-- data directory first. Apply after schema v7 as the schema administrator,
-- then run deploy/account-grants.sql again. DDL implicitly commits; the IF
-- NOT EXISTS clauses make an interrupted run safe to repeat.
-- See docs/deployment.md.

-- Whether this account may still sign in with an emailed code. On by default;
-- an account can turn it off only once it has a stronger method and recovery
-- codes (enforced by the service).
ALTER TABLE metatrash_users
    ADD COLUMN IF NOT EXISTS email_login TINYINT UNSIGNED NOT NULL DEFAULT 1;
ALTER TABLE metatrash_users
    ADD CONSTRAINT IF NOT EXISTS metatrash_users_email_login CHECK (email_login IN (0, 1));

-- Passkeys (WebAuthn credentials). credential_hash is the SHA-256 (hex) of the
-- raw credential ID, for lookup and uniqueness; credential_id is the raw ID in
-- base64url. public_key is the COSE key the authenticator registered.
-- sign_count is the last counter seen (0 when the authenticator does not keep
-- one). Times are Unix seconds; last_used_at is 0 until first use.
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

-- Authenticator app (TOTP), at most one per account. secret_sealed is the
-- secret encrypted with the server's TOTP key; enabled_at is 0 while the user
-- has not yet confirmed a code. last_used_step rejects a code used twice.
CREATE TABLE IF NOT EXISTS metatrash_totp (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    secret_sealed VARBINARY(256) NOT NULL,
    created_at BIGINT NOT NULL,
    enabled_at BIGINT NOT NULL DEFAULT 0,
    last_used_step BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT metatrash_totp_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

-- Single-use recovery codes, stored as SHA-256 hex. A used code is deleted.
CREATE TABLE IF NOT EXISTS metatrash_recovery_codes (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    code_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at BIGINT NOT NULL,
    PRIMARY KEY (user_id, code_hash),
    CONSTRAINT metatrash_recovery_codes_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

UPDATE metatrash_account_meta SET schema_version = 8
WHERE singleton_id = 1 AND schema_version = 7 AND migration_source <> '';
