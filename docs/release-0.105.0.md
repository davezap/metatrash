# Core 0.105.0: record pointers and the record filter hook

Prepared 2026-11-28 by Claude (thread "Build collaborative forms and
workflows", slice 2), released with Company 0.96.81; Slate Viewer stays
0.51.0 and Slate 0.26.2. Tier: Core. Every route that sends a file's content
is in Core, so the filter has to be there or something leaks; Core does not
decide who may read what. Company's Enterprise workflows (slice 3b) answer
the hook, as they answer `SpaceLevels` today.

1. **Record pointers**: a file whose text is
   `metatrash-virtual 1` / `record …` / `section …` / `step …` / `org …` /
   `form …` / `from …` / `type text/markdown`, marked `record` in the index.
   Only extension code writes one (`FileChange.Record` in `Apply`, or
   `RecordWriter.OpenSpace` from the module's Host), in account spaces only.
   Writing that text to a new path, and writing, editing, appending or
   uploading over a pointer, is refused with `record_pointer`. Moves within
   the space keep it working.
2. **The hook**: `Hooks.RecordView(ctx, {UserID, SpaceID, Path, Record,
   Route})` answers the Markdown this reader may see, the attachments it may
   link, or no access (blank, with a note). Asked on every read, never
   cached. Hook view: space pages with `?raw` / `?format=md` (no-store), REST
   and site reads and `.../content`, MCP `read`, folder-action jobs (the
   payer reads). Pointer only: MT Sync, revisions, history, export, GitHub
   push. Search and the search index skip the text; dashboards skip the file.
3. **Attachments**: `GET /api/v1/records/{record}/sections/{section}/files/{name}`
   for the signed-in person. `Hooks.RecordFile` checks the reader and names
   the space and path; Core streams it only when that file is virtual, with
   `no-store`. Refusals, forged sections and non-virtual paths read as 404.

Without an extension that sets `RecordView`, nothing can make a record
pointer, so on a plain Core server (and on Collumii until slice 3b) the
feature is inert: nothing changes for anyone.

Upgrade: nothing to apply; start 0.105.0. Rollback: 0.104.0 ignores the
index's `record` mark, so a record pointer reads as its pointer text (record
IDs and the sender, no record content). A commit made on 0.104.0 drops the
mark for good; no extension writes pointers before slice 3b, so in practice
there is nothing to undo.

Known limit, for slice 3b: Slate Viewer still treats a record pointer as an
ordinary file. It shows the view, and Save is refused with `record_pointer`.
A Viewer that caches by blob may keep showing a view after access ends until
it reads the file again.

Tests: `TestRecordPointers`, `TestRecordPointerHTTP`,
`TestRecordPointerInJobs`, `TestRecordHooksChecked`; all of Core
(`internal/service`, `cmd/mtsync`, `console`, `internal/filenames`) passes
without a database.
