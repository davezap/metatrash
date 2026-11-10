# Core 0.95.21: approved copies listed for a green tick

Prepared 2026-11-09 by Claude. Claude ran `gofmt`, `go vet ./...` and the full
`go test ./...` (with MariaDB 10.11 for the database tests) in a scratch copy
of v0.95.20. Pending David: the release helper, commit, tag/push and Actions.

## What changes

David asked (2026-11-09) for approved forms to carry a green tick in the
folder view. `GET …/acknowledgements` gains `approved`: the filed copies in
the space whose status is `approved` (a quiz's: passed), sorted space paths,
for anyone who can read the space. It is read in the same query as
`toReview`. mtsync `policies` cuts it to the synced folder.

No schema, grants, configuration or dependency changes.

## Files

`assets.go`, `CHANGELOG.md`, `internal/service/acknowledgements.go`,
`internal/service/quiz_test.go`, `cmd/mtsync/policies.go`,
`cmd/mtsync/policies_test.go`, `docs/release-0.95.21.md`.

## Release

With Slate Viewer 0.35.2 and Company 0.96.29 (Slate unchanged at 0.22.0).
Follow Company `docs/release-0.96.29.md`.

## Rollback

Install Company 0.96.28 (Core 0.95.20). The ticks go; nothing was stored.
