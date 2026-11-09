# Core 0.95.19: guided forms (default answers, show-if, auto flow, photos)

Prepared 2026-11-09 by Claude. Claude ran `gofmt`, `go vet ./...` and the full
`go test ./...` (with MariaDB 10.11 for the database tests) in a scratch copy
of v0.95.18; all passed. Pending David: the release helper's laptop checks,
staged review, commit, tag/push and Actions.

## What changes

Form fields take two new attributes. Syntax and examples are in Company's
`docs/forms.md`, section "Default answers and questions asked only when needed".

- `default=`: text, or `@name`, `@email`, `@today`, `@now`. An answer left out
  of a submission takes the default; a blank that is sent stays blank.
- `show-if=`: `field`, `!field`, `field=A|B`, `field!=A`, and `> >= < <=` for
  numbers, dates and times. A question not asked isn't required, its answer is
  ignored, and its block (paragraph, or list item / table row, with a choice's
  options) is left out of the filed copy. Quizzes don't mark it.

- `autoFlow: true` front matter (Slate shows heading sections one at a time)
  is checked to be true or false.
- `{{photo name=… max=N}}`: a list of data URLs or paths of .jpg/.png
  pictures in the form's space. JPEG and PNG only, 10 MB each, 40 MB a
  submission; Exif/XMP/IPTC/comments and PNG text/time/eXIf are taken out (a
  JPEG keeps an orientation-only Exif). Stored as virtual files under
  `<submitTo folder>assets/` in the copy's commit; the answer becomes their
  paths and the copy shows them. Needs `agents.downloads`. Not in quizzes.
  `mtsync submit -photo FIELD=PICTURE`.

Also: `POST …/submissions` takes `now` (HH:MM); mtsync submit sends it. The
submissions body limit is now about 54 MB (photos); other answers keep 64 KB.
Metatrash Sync's space listing, `mtsync -json whoami` and the site session
include the person's own `email`, for `@email` in Slate Viewer. Other apps'
listings don't. Tool schema 0.18.1 (description only).

No schema, grants, configuration or dependency changes. Templates without the
new attributes parse, check and render exactly as before.

## Files

`assets.go`, `CHANGELOG.md`, `api/tool-schema.json`, `cmd/mtsync/forms.go`,
`cmd/mtsync/main.go`, `internal/service/forms.go`,
`cmd/mtsync/forms_photo_test.go` (new),
`internal/service/forms_flow.go` (new), `internal/service/forms_flow_test.go`
(new), `internal/service/forms_photo.go` (new),
`internal/service/forms_photo_test.go` (new), `internal/service/mcp.go`,
`internal/service/oauth_resource.go`, `internal/service/quiz.go`,
`internal/service/runner_tools.go`, `internal/service/site_api.go`,
`internal/service/submissions.go`, `docs/release-0.95.19.md`.

## Release

Released together with Slate 0.22.0, Slate Viewer 0.35.0 and Company 0.96.27.
Follow Company `docs/release-0.96.27.md`; its helper run selects these files.

## Rollback

Install the previous Company release (Core 0.95.18). Copies filed with 0.95.19
stay valid: they are ordinary filed copies, without answers for questions that
weren't asked. A template using `default=` or `show-if=` is refused by 0.95.18
as a template mistake ("does not take default", "{{photo}} is not a field
type"), so remove those first or expect those forms to stop sending until the
new release is back. Photos already filed stay as virtual files beside their
copies.
