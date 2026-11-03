-- Runtime grants; apply as schema administrator.
GRANT SELECT, INSERT ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT SELECT, UPDATE ON metatrash.metatrash_account_meta TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (username) ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (provisioning_state) ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (visibility) ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_memberships TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (status, updated_at) ON metatrash.metatrash_memberships TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT ON metatrash.metatrash_invitations TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (invitation_id, status, created_at, expires_at, accepted_user_id)
    ON metatrash.metatrash_invitations TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (agent_permission) ON metatrash.metatrash_memberships TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_grants TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (client_name, scope, updated_at, last_used_at, expires_at)
    ON metatrash.metatrash_oauth_grants TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_grant_spaces TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_oauth_tokens TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (used_at) ON metatrash.metatrash_oauth_tokens TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_github_installations TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (github_user_id, github_login, account_login, account_type, status, updated_at)
    ON metatrash.metatrash_github_installations TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (email_login) ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_passkeys TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (sign_count, backed_up, last_used_at, name) ON metatrash.metatrash_passkeys TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_totp TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (enabled_at, last_used_step) ON metatrash.metatrash_totp TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_recovery_codes TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (email) ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (max_private_spaces) ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (owner_user_id) ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (member_user_id) ON metatrash.metatrash_oauth_grant_spaces TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_space_transfers TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_space_aliases TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_sessions TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (auth_at, last_used_at) ON metatrash.metatrash_sessions TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT ON metatrash.metatrash_acknowledgements TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT ON metatrash.metatrash_form_submissions TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (status, score, marks, reviewed_by, reviewed_at, note, text)
    ON metatrash.metatrash_form_submissions TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT ON metatrash.metatrash_agent_jobs TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (status, started_at, finished_at, steps, input_tokens, output_tokens, error)
    ON metatrash.metatrash_agent_jobs TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_agents TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (is_default, name, daily_cap, key_sealed, key_hint) ON metatrash.metatrash_agents TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (agent_id) ON metatrash.metatrash_agent_jobs TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_action_optins TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (timezone) ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_action_schedules TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (every, next_run_at, last_run_at) ON metatrash.metatrash_action_schedules TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_web_objects TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (uploaded_at) ON metatrash.metatrash_web_objects TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_web_refs TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_virtual_refresh TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (every, next_run_at, last_run_at, stale_since, stale_reason)
    ON metatrash.metatrash_virtual_refresh TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (deleted_at, slug) ON metatrash.metatrash_spaces TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (deletion_requested_at, deleted_at) ON metatrash.metatrash_users TO 'metatrash_accounts'@'localhost';
GRANT SELECT, INSERT, DELETE ON metatrash.metatrash_space_usage TO 'metatrash_accounts'@'localhost';
GRANT UPDATE (git_bytes, file_bytes, file_objects, git_measured_at, files_measured_at)
    ON metatrash.metatrash_space_usage TO 'metatrash_accounts'@'localhost';
GRANT SELECT ON metatrash.metatrash_schema_components TO 'metatrash_accounts'@'localhost';
