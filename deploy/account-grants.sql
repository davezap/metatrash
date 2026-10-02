-- Runtime permissions for the service's database login, schema v1-v5.
-- Run as database administrator after the schema scripts. Safe to repeat.
-- For loopback TCP use 'metatrash_accounts'@'127.0.0.1' throughout instead.
-- The login needs no DDL, FILE or administration privileges.

-- v1 accounts
GRANT SELECT, INSERT ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT SELECT, UPDATE ON metatrash.metatrash_account_meta TO 'metatrash_accounts'@'localhost';
-- v2 usernames
GRANT UPDATE (username) ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
-- v3 owned spaces
GRANT SELECT, INSERT ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (provisioning_state) ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
-- v4 memberships and invitations
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_memberships TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (status, updated_at) ON metatrash.metatrash_memberships TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT ON metatrash.metatrash_invitations TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (invitation_id, status, created_at, expires_at, accepted_user_id)
    ON metatrash.metatrash_invitations TO 'metatrash_accounts'@'localhost';
-- v5 OAuth (metatrash_oauth_grants is a table of app connections, unrelated to GRANT)
GRANT UPDATE (agent_permission) ON metatrash.metatrash_memberships TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_grants TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (client_name, scope, updated_at, last_used_at, expires_at)
    ON metatrash.metatrash_oauth_grants TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_grant_spaces TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_tokens TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (used_at) ON metatrash.metatrash_oauth_tokens TO 'metatrash_accounts'@'localhost';

SHOW GRANTS FOR 'metatrash_accounts'@'localhost';
