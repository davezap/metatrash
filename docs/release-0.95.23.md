# Core 0.95.23: acting for an owner account, and moving spaces to it

Prepared 2026-11-10 by Claude. Claude ran `gofmt`, `go vet ./...` and the full
`go test ./...` (with MariaDB 10.11 for the database tests) in a scratch copy
of v0.95.22. Pending David: the release helper, commit, tag/push and Actions.

## What changes

Step 2, slices 2 and 3 of Company `docs/roadmap-organisations.md`. An organisation
is an owner account nobody signs in as; its admins need to run its spaces.
Core gains one extension hook for that:

- `extension.Hooks.ActsFor(ctx, userID) ([]string, error)`: the owner
  accounts this account may act for. Core treats it as the owner of their
  spaces on the website, for apps, for reviewing forms and in Settings →
  Spaces (sharing, visibility, time zone, transfer, delete).
- Answers are cached per account for 10 seconds. Removing an admin takes
  effect within that time, for open sessions and connected apps too.
- Rate limits, Confirm it's you and notices stay with the person acting.
- `GET /api/v1/site/account` lists those spaces with `owner` set (and
  their storage), and `manages` lists the usernames acted for.
- `POST /api/v1/site/account/spaces/{id}/move {"owner": "<username>"}`
  moves a space between your own account and one you act for, after Confirm
  it's you, with no offer to accept. Like a transfer it keeps the space's ID,
  members, apps and old address; moving your own space away leaves you a
  member. The sharing state's `moveTo` lists where it can go. Transfer offers
  made while acting are refused.
- Mail to `.invalid` addresses is never sent.
- The sign-in page after a Microsoft sign-in no account has linked yet
  (from David's step 1 testing): "Finish signing in with Microsoft" says to
  confirm the address with an emailed code, that this signs in to an existing
  account or creates one, and that linking follows. The Microsoft button is
  not offered again there.

Without an extension that sets `ActsFor` (Core on its own, or Company
without organisations turned on) nothing changes.

Not covered: folder actions and GitHub links in organisation spaces.

No schema, grants, configuration or dependency changes.

## Files

`assets.go`, `CHANGELOG.md`, `extension/extension.go`,
`internal/service/acting_owners.go` (new),
`internal/service/acting_owners_test.go` (new),
`internal/service/space_move.go` (new),
`internal/service/account_database.go`, `internal/service/extensions.go`,
`internal/service/site_api.go`, `internal/service/owned_space_http.go`,
`internal/service/oauth_access.go`, `internal/service/oauth_store.go`,
`internal/service/oauth_server.go`, `internal/service/account_apps.go`,
`internal/service/site_account.go`, `internal/service/account_http.go`,
`internal/service/account_mail.go`, `internal/service/submissions.go`,
`internal/service/human_membership_http.go`,
`internal/service/extensions_test.go`, `internal/service/web/login.html`,
`internal/service/web/signin.css`,
`docs/release-0.95.23.md`.

## Release

With Slate Viewer 0.37.0 and Company 0.96.32 (Slate unchanged). Follow Company
`docs/release-0.96.32.md`.

## Rollback

Install Company 0.96.31 (Core 0.95.22). Spaces already moved into an
organisation stay there (they are ordinary owner rows to older Core), but its
admins can't reach them until 0.95.23 is back; move them out first if you
roll back for long.
