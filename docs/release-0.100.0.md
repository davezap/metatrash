# Core 0.100.0: extensions can decide what a person may do in a space

Prepared 2026-11-20 by Claude (thread "Build the Enterprise plan") for
Company 0.96.62 (Enterprise release 1a), with Slate Viewer 0.47.0.

Tier: Core, as a small generic hook. Core itself never decides levels this
way; an extension does. Metatrash's Company edition uses it for the Access
page rows of an organisation's spaces (Enterprise only).

## What changes

- `extension.Hooks.SpaceLevels func(ctx, userID) (extension.SpaceLevels, error)`.
  An answer names the owner accounts whose spaces the extension decides
  (`Owners`) and the account's level in each space it opens (`Spaces`, space
  ID to `read_only` or `read_write`). A space of those owners not in
  `Spaces` is hidden from the account.
- For those owners, a membership can only lower the level (a read-only share
  of a read-write space gives read-only), or suspend it. A membership in a
  space without a level opens nothing. For every other owner nothing changes.
- Owners and accounts acting for them (ActsFor) are unaffected. SpaceGate is
  still asked after. A space readable on the web stays readable.
- Answers are cached for 10 seconds per account (`spaceLevelsTTL`). An error
  is logged and refuses, as `unavailable`, the account's access to spaces it
  doesn't own or act for, until an answer comes.
- Every door that worked out a member's access now asks it: the site API,
  space pages and files, apps and Sync, the spaces lists and the consent
  page (which list spaces opened by a level alone), consent and Change
  spaces, Connected apps and reviewers. Consent given through a level alone
  is stored with no membership (`member_user_id` NULL, as an owner's is); it
  is checked again on every operation.
- Known gap: the readers list of a notice (`spaceReaders`, acknowledgements)
  still counts members only.
- No schema, grant, configuration, dependency or tool schema change. With no
  extension offering the hook, behaviour is exactly as 0.99.4.

## Checks

`go build ./...`, `go vet ./internal/service ./extension` and
`go test ./internal/service -run TestSpaceLevels` passed on the laptop's Linux
VM (Go 1.25.4). `TestSpaceLevelsAgainstDatabase` needs the disposable
MariaDB (`METATRASH_TEST_DB_CONFIG`), so it runs in GitHub Actions with the
rest of the database tests.
