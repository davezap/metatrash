-- Space ownership transfer (0.30.0). Stop the service and back up the
-- database and data directory first. Apply after schema v8 as the schema
-- administrator, then run deploy/account-grants.sql again. DDL implicitly
-- commits; the IF NOT EXISTS clauses make an interrupted run safe to repeat.
-- See docs/deployment.md.

-- A pending offer to hand a space to one of its active members, at most one
-- per space. The row is deleted when the offer is accepted, declined or
-- cancelled; an expired row is ignored and replaced by the next offer.
-- Times are Unix seconds.
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

-- Earlier addresses of transferred spaces: owner_user_id/slug was the space's
-- address before a transfer. Website links and agents that use the old
-- owner/slug still reach the space, and the address stays reserved: that
-- owner cannot create or accept another space at the slug. If the space
-- comes back to them, it takes the address back and the row is deleted.
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

UPDATE metatrash_account_meta SET schema_version = 9
WHERE singleton_id = 1 AND schema_version = 8 AND migration_source <> '';
