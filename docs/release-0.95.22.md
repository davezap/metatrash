# Core 0.95.22: My Submissions and the to-do count across all spaces

Prepared 2026-11-10 by Claude. Claude ran `gofmt`, `go vet ./...` and the full
`go test ./...` (with MariaDB 10.11 for the database tests) in a scratch copy
of v0.95.21. Pending David: the release helper, commit, tag/push and Actions.

## What changes

David asked (2026-11-10) for the account menu's count and My Submissions to
cover all the spaces a person owns or shares, and for My Submissions to let
people start a new copy from an old one's answers when the form hasn't
changed. Two site API routes, signed-in only:

- `GET /api/v1/site/submissions[?id=ID]`: the person's receipts from every
  space, each with `space`, `current` (the form's path now) and `sameForm`
  (the form's text, or published text, still matches the recorded
  `template_blob`).
- `GET /api/v1/site/todo`: `total`, and per space `toRead`, `toReview`,
  `sentBack`; `partial` when some spaces were skipped.

Each answer opens at most 40 spaces, on those spaces' read limits.

The site session answer (`GET /api/v1/site/session`) also has `docs:
"/docs/"` when `docsSpace` is set, for the Viewer's Change Space.

No schema, grants, configuration or dependency changes. The desktop app
(mtsync) keeps its per-space answers.

## Files

`assets.go`, `CHANGELOG.md`, `internal/service/forms_account.go` (new),
`internal/service/submissions.go`, `internal/service/site_api.go`,
`internal/service/quiz_test.go`, `internal/service/site_api_test.go`,
`docs/release-0.95.22.md`.

## Release

With Slate Viewer 0.36.0 and Company 0.96.30 (Slate unchanged at 0.22.0).
Follow Company `docs/release-0.96.30.md`.

## Rollback

Install Company 0.96.29 (Core 0.95.21). Nothing was stored; the Viewer
falls back to the open space's count and receipts.
