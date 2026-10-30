-- Deleting an account (0.71.0). Stop the service and back up the database
-- first. Apply after schema v20 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; IF NOT EXISTS
-- makes an interrupted run safe to repeat. See docs/deployment.md.

-- deletion_requested_at: the account asked to be deleted (Unix seconds). It
-- is signed out and its apps disconnected; signing in again within 30 days
-- clears it. deleted_at: the account was purged. Its sign-in methods,
-- memberships, connections and agents are gone and its email and username
-- are replaced, so the address can sign up afresh. The row stays because
-- other people's records (acknowledgements, form receipts, purged spaces)
-- still name it.
ALTER TABLE metatrash_users
    ADD COLUMN IF NOT EXISTS deletion_requested_at BIGINT NULL,
    ADD COLUMN IF NOT EXISTS deleted_at BIGINT NULL;

UPDATE metatrash_account_meta SET schema_version = 21
WHERE singleton_id = 1 AND schema_version = 20 AND migration_source <> '';
