# Core 0.99.1: dashboard follow-ups

Prepared 2026-11-18 by Claude (thread "Build Collumii dashboards") from
Dave's 0.96.56 checks on ip-172-31-26-202, for Company 0.96.57 with Slate
Viewer 0.44.1.

Tier: Core. Both fix Core features of 0.99.0 (the home page, forms).

## What changes

- **Web home page.** A space's address on the website opened README.md
  whatever its `home` setting or `dashboard.md` said: the server picks the
  file (`defaultPage`), and 0.99.0 changed only Slate Viewer.
  `repository.homePage` (browser.go) now picks the `home` setting's file,
  else `dashboard.md`, else the old default; used for owned spaces
  (`owned_space_http.go`) and the public space (`browser.go`). Subfolders
  keep README.md (a subfolder `home` is on the roadmap).
- **`submitTo` paths (breaking, Dave's call).** `formTarget`
  (submissions.go): `/owner/space/folder/` is a space; every other path is a
  folder of the form's space from the form's folder (`relativeSubmitTo`,
  refusing a climb above the top). A form written `owner/space/folder/`
  (before 0.99.1) now files into that folder path beside itself: every such
  form needs the leading `/` (Company 0.96.57's guide lists the ones to
  change). The refusal and the "submitTo is missing" messages name both.
- docs/dashboards.md: a whole example dashboard, the home rule on the
  website, the `submitTo` forms.

No schema, grant, configuration, dependency or tool schema change.

## Checked in the cloud

`gofmt`, `go vet ./...`, and `go test ./...` with MariaDB. New
`TestRelativeSubmitTo`; `TestDashboardNoticesAgainstDatabase` adds copies
filed beside the form, in a subfolder, up a folder, by `/owner/space/` and
the old way (now a folder); every test form now uses `/owner/space/`; and the
home page (README.md, then Dashboard.md, then the `home` setting, a missing
`home` file falling back).

## Checks after release (ip-172-31-26-202)

1. A test space with dashboard.md and README.md at the top, opened in a
   private window at its address: it opens dashboard.md.
2. Properties → General → Opens on: README.md, Save, private window: it
   opens README.md. Clear it afterwards.
3. A form with `submitTo: done/` and `review: none`: the copy lands in
   `done/` beside the form. One with `submitTo: /OWNER/OTHER-SPACE/folder/`
   files in that space. One with the old `OWNER/OTHER-SPACE/folder/` files
   into a folder of that name beside the form.
