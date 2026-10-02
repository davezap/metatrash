-- Stage 4 first bite. Apply ONCE after v3 with the service stopped.
-- DDL implicitly commits; see docs/deployment.md for recovery.
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

-- One current invitation slot per address/space. Reissuing a terminal/expired
-- slot replaces its ID so stale forms cannot accept or cancel the new invitation.
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

UPDATE metatrash_account_meta SET schema_version = 4
WHERE singleton_id = 1 AND schema_version = 3 AND migration_source <> '';
