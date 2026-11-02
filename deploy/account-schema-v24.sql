-- Billing state from Paddle (0.91.0). Stop the service and back up the
-- database first. Apply after schema v23 as the schema administrator, then
-- run deploy/account-grants.sql again. DDL implicitly commits; IF NOT EXISTS
-- makes an interrupted run safe to repeat. See docs/deployment.md.

-- One row per account with a subscription, as Paddle last described it.
-- Webhooks write it, and a daily check against Paddle corrects it. status is
-- Paddle's: trialing, active, past_due, paused or canceled. storage_gb is the
-- quantity of the extra storage (1 GB) price on the subscription. Times are
-- Unix seconds. scheduled_change is Paddle's scheduled change (cancel, pause
-- or resume) and scheduled_at when it takes effect. paddle_updated_at is the
-- subscription's updated_at at Paddle (Unix milliseconds): an older
-- description never replaces a newer one, whatever order events arrive in.
-- The row goes when the account is removed for good.
CREATE TABLE IF NOT EXISTS metatrash_subscriptions (
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    customer_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    subscription_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    storage_gb INT NOT NULL DEFAULT 0,
    trial_ends_at BIGINT NULL,
    period_ends_at BIGINT NULL,
    next_billed_at BIGINT NULL,
    scheduled_change VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
    scheduled_at BIGINT NULL,
    canceled_at BIGINT NULL,
    paddle_updated_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    UNIQUE KEY metatrash_subscriptions_subscription (subscription_id),
    KEY metatrash_subscriptions_customer (customer_id),
    CONSTRAINT metatrash_subscriptions_user FOREIGN KEY (user_id)
        REFERENCES metatrash_users (user_id) ON DELETE RESTRICT ON UPDATE RESTRICT
) ENGINE=InnoDB;

-- Each Paddle event the webhook took, so a repeat delivery is applied once.
-- outcome says what it did (applied, older, unlinked, ignored). Rows older
-- than 90 days are removed by the daily check.
CREATE TABLE IF NOT EXISTS metatrash_paddle_events (
    event_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
    event_type VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    occurred_at BIGINT NOT NULL,
    received_at BIGINT NOT NULL,
    subscription_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
    outcome VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    KEY metatrash_paddle_events_received (received_at)
) ENGINE=InnoDB;

-- A comped account is a full subscriber with no charge (mt users comp).
-- NULL: not comped; otherwise when it was comped (Unix seconds).
ALTER TABLE metatrash_users
    ADD COLUMN IF NOT EXISTS comped_at BIGINT NULL;

UPDATE metatrash_account_meta SET schema_version = 24
WHERE singleton_id = 1 AND schema_version = 23 AND migration_source <> '';
