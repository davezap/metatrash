-- Stage 3 storage foundation. Stop service and back up database/data first.
-- Apply ONCE after schema v2; DDL implicitly commits. See docs/deployment.md.
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

UPDATE metatrash_account_meta SET schema_version = 3
WHERE singleton_id = 1 AND schema_version = 2 AND migration_source <> '';
