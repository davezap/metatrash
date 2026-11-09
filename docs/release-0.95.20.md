# Core 0.95.20: forms waiting for you (copies to review, forms sent back)

Prepared 2026-11-09 by Claude. Claude ran `gofmt`, `go vet ./...` and the full
`go test ./...` (with MariaDB 10.11 for the database tests) in a scratch copy
of v0.95.19. Pending David: the release helper's laptop checks, staged review,
commit, tag/push and Actions.

## What changes

David asked (2026-11-09) for a red dot that tells reviewers a new copy of a
form has arrived, and the sender that a reviewer asked for more. The
acknowledge service doesn't do this: it marks published files only, and filed
copies are never published.

`GET …/acknowledgements` (the answer Slate Viewer and `mtsync policies`
already ask for) gains two lists of space paths, sorted and always present:

- `toReview`: filed copies in this space whose status is `submitted`, for a
  caller who can write to the space (reviewing needs that); empty for others.
  Shared by everyone who can review: one person's Approve, Decline or Ask for
  More takes it off every list. A fixed copy sent again is a new copy, so it
  appears again.
- `toFix`: forms in this space whose newest copy the caller sent is
  `needs-info`. Sending the form again (with `resends`) clears it.

Both are read from `metatrash_form_submissions` with the existing
`target_space_id` and `(space_id, user_id, submitted_at)` indexes. Copies
moved or deleted in Git drop out.

mtsync `policies` cuts both to the synced folder, prints them in `-json`, and
lists them in its text answer. An older server answers neither; mtsync and
Slate Viewer treat that as nothing to do.

No schema, grants, configuration or dependency changes.

## Files

`assets.go`, `CHANGELOG.md`, `internal/service/acknowledgements.go`,
`internal/service/quiz_test.go`, `cmd/mtsync/policies.go`,
`cmd/mtsync/policies_test.go`, `docs/release-0.95.20.md`.

## Release

Released together with Slate Viewer 0.35.1 and Company 0.96.28 (Slate
unchanged at 0.22.0). Follow Company `docs/release-0.96.28.md`.

## Rollback

Install Company 0.96.27 (Core 0.95.19). The two lists go away and the dots
with them; nothing was stored.
