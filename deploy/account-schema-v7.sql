-- Web-readable spaces (0.21.0). Stop the service and back up the database and
-- data directory first. Apply after schema v6 as the schema administrator,
-- then run deploy/account-grants.sql again. DDL implicitly commits; the IF
-- EXISTS / IF NOT EXISTS clauses make an interrupted run safe to repeat.
-- See docs/deployment.md.

-- A space's owner may make it readable by anyone in the website's read-only
-- explorer ('web'). Agent access (MCP and REST) is unchanged: it still needs
-- an app connection the owner or a member approved.
ALTER TABLE metatrash_spaces DROP CONSTRAINT IF EXISTS metatrash_spaces_private;
ALTER TABLE metatrash_spaces
    ADD CONSTRAINT IF NOT EXISTS metatrash_spaces_visibility
        CHECK (visibility IN ('private', 'web'));

UPDATE metatrash_account_meta SET schema_version = 7
WHERE singleton_id = 1 AND schema_version = 6 AND migration_source <> '';
