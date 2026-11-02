-- Yearly billing (0.93.0). Stop the service and back up the database first.
-- Apply after schema v24 as the schema administrator, then run
-- deploy/account-grants.sql again. DDL implicitly commits; IF NOT EXISTS
-- makes an interrupted run safe to repeat. See docs/deployment.md.

-- How often Paddle bills the subscription: month or year. Rows from before
-- 0.93.0 are all monthly.
ALTER TABLE metatrash_subscriptions
    ADD COLUMN IF NOT EXISTS billing_interval VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'month';

UPDATE metatrash_account_meta SET schema_version = 25
WHERE singleton_id = 1 AND schema_version = 24 AND migration_source <> '';
