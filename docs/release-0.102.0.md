# Core 0.102.0: new files need reading, dashboard cards that list files

Prepared 2026-11-27 by Claude (thread "Build the small ideas batch", slice 3),
released with Company 0.96.76 and Slate Viewer 0.49.0. Tier: Core for both
(reading and acknowledging, and dashboards and their sources, are in every
Metatrash server).

1. **New files need reading**: `"newFiles": true` on a folder's acknowledge
   service, in a folder without `publishedVersion`. Tracked files added
   after the setting was turned on are listed by `GET .../acknowledgements`
   under a folder with `newFiles: true` and version 0, and confirmed with
   `POST {"path": ..., "version": 0}`. Confirming any text of the file
   counts, so edits don't ask again. Which files were already there comes
   from the settings file's history (`settingsHistory` in `publish.go`, the
   same walk `publishPoints` does; both share one cache). With
   `publishedVersion` the setting is refused.
2. **Pattern sources for dashboard cards**: a card's `source` with `*` or
   `?` lists the files it matches, any type, with `name`, `file`, `folder`
   and a Markdown file's front matter as fields. `**` on its own matches any
   number of folders. See docs/dashboards.md, Sources.

Upgrade: none beyond the executable. No schema, grant or configuration
change; acknowledgement rows with version 0 fit the existing column. The
previous executable doesn't know `newFiles`: after a rollback it reads a
`.metatrash.json` with it as no settings at all (so that folder's locks and
read-only files lapse too) and refuses to save one. Before rolling back,
remove `newFiles` from any folder that set it. Pattern sources just show
their error under the old executable. Acknowledgement rows stay either way.

Tests: `TestNewFilesFolders`, `TestNewFilesStatus`, `TestNewFilesSetting`,
`TestQueryPatternMatch`, patterns in `TestDashboardQuery`; all of
`internal/service` passes without a database. `TestNewFilesNeedReading` runs
where MariaDB is.
