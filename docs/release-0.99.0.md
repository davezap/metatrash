# Core 0.99.0: dashboards

Prepared 2026-11-18 by Claude (thread "Build Collumii dashboards"), for
Company 0.96.56's combined release with Slate 0.24.0 and Slate Viewer 0.44.0.
Spec: project file `dashboards/phase1-spec.md`.

Tier: Core. One person running their own instance with a team needs
dashboards to be complete, and they build on forms, folder settings and
Slate, which are Core. Company adds only the Collumii chart colours.

## What changes

- **Queries** (`internal/service/dashboard_query.go`, new): a card's source
  (a folder of `.md` rows or a `.csv` file) and query (`where`, `group`,
  `measure`, `sort`, `limit`, `columns`) answered from the files at head;
  parsed rows cached by the source's blobs (64 sources). `@me` is the
  signed-in person.
- **REST/site** `POST .../query` (everywhere reading works) and
  `GET/POST .../notices` (account and site APIs) in `dashboard_http.go`
  (new), `http.go`, `site_api.go`.
- **MCP tool `query`** on `/mcp` and `/mcp/account`; not offered to folder
  actions. Tool schema 0.21.0 (also: `.metatrash.json` `home`).
- **Notices**: a Markdown file without form fields shown as a card is
  acknowledged per text (blob) in `metatrash_acknowledgements` (version 1);
  files an acknowledge service tracks still use acknowledgements.
- **`review: none`** on a form: copies are filed `status: complete`, can't
  be reviewed (409 `no_review`), and never wait for review. Not for quizzes.
- **Folder setting `home`**: the `.md` file the folder opens on; never
  inherited; lockable.
- **mtsync** `query`, `notices`, `notice` for Slate Viewer on the desktop.
- **`/assets/charts/charts.js`**: Slate's Chart.js module, embedded from
  `internal/service/web/charts/` (or a front-end bundle's
  `slate/charts/charts.js`); `scripts/build-frontends.ps1` copies it.

No schema, grant, configuration or dependency change.

Checked in the cloud: `gofmt`, `go vet` and the full `go test ./...` with
MariaDB. New tests `TestDashboardQuery`, `TestQueryWhereParse`,
`TestFolderHomeSetting`, `TestDashboardNoticesAgainstDatabase`; charts in
`TestSlateAndMermaidAssets` and `TestFrontendEditorIsolation`; tool counts
in `TestMCPSmoke` (10) and `TestOAuthProtectedMCPAgainstDatabase` (16).

## Check after release

See Company docs/release-0.96.56.md.
