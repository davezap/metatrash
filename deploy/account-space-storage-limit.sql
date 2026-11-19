-- Space storage limits (Core 0.99.3): an owner's optional limit per space, in bytes.
-- Additive: compatible with older executables and schema v25, and kept on rollback.
-- Does not rewrite rows or component markers. Run as schema administrator, then
-- deploy/account-grants.sql (its UPDATE (storage_limit_bytes) needs this column).
-- Fresh installations already have it from deploy/account-schema.sql.
SET SESSION lock_wait_timeout = 30;
ALTER TABLE metatrash_spaces
    ADD COLUMN IF NOT EXISTS storage_limit_bytes BIGINT UNSIGNED NULL;
