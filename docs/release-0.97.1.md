# Core 0.97.1: uploaded pictures get a smaller preview copy

Prepared 2026-11-15 by Claude (thread "Fix uploaded pictures and add
previews"), for Company 0.96.52's combined release.

Tier: Core. Storing pictures and making a copy that documents can show is
what one person running their own instance needs as much as the hosted
service.

Dave asked for smaller preview versions next to an uploaded picture, so
documents can show those instead of the full-size file. Uploading a JPEG,
PNG or GIF that is over 1280 pixels on its longer side, or over 512 KB, now
also stores `<name>.preview.jpg` (or `.preview.png` for a picture with
see-through parts) beside it: 1280 pixels on the longer side, turned the
right way up from the photo's Exif orientation, with no metadata. It is an
ordinary virtual file written in a second commit; a new version of the
picture makes it again. The upload's JSON answer gains `preview`.

Not made: from files already named `*.preview.*`, from pictures over 50
megapixels, over a file at that name that isn't a virtual file, for WebP.
A preview that fails leaves the upload as it was.

Files: `internal/service/virtual_preview.go` (new),
`internal/service/virtual_preview_test.go` (new),
`internal/service/virtual_upload.go`, `internal/service/types.go`,
`assets.go`, `CHANGELOG.md`, this file. No schema, grant, configuration,
dependency or tool schema change.

Checked in the cloud: `go vet ./...` and `go test ./...` with MariaDB, all
passing.

Check after release (signed in on the website, in a space you own):

1. Upload a phone photo (several MB, taken in portrait). Files lists
   `<name>.preview.jpg` beside it within a few seconds.
2. Click the preview: it shows upright and about 1280 pixels on its longer
   side (Open in New Tab shows the size in the tab title).
3. Upload a small picture (under 1280 pixels and 512 KB): no preview.
