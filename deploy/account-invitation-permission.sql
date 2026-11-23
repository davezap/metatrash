-- What a pending invitation's connected apps may do (Core 0.101.0): read_write
-- (the default, as before) or read_only, copied to the membership on acceptance.
-- Additive: compatible with older executables and schema v25, and kept on rollback.
-- Does not rewrite rows or component markers. Run as schema administrator, then
-- deploy/account-grants.sql (its UPDATE (agent_permission) on invitations needs
-- this column). Fresh installations already have it from deploy/account-schema.sql.
SET SESSION lock_wait_timeout = 30;
ALTER TABLE metatrash_invitations
    ADD COLUMN IF NOT EXISTS agent_permission VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin
        NOT NULL DEFAULT 'read_write',
    ADD CONSTRAINT IF NOT EXISTS metatrash_invitations_agent_permission
        CHECK (agent_permission IN ('read_only', 'read_write'));
