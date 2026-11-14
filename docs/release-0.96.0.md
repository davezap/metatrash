# Core 0.96.0: search index

Prepared 2026-11-14 by Claude (thread "Plan search and indexing").

Tier: Core (Dave, 2026-11-14): fast search is something one person's own
instance needs too; it is off until the server's operator sets it up.

An optional per-space Bleve index that makes `search` read only the files
that can hold the query. Results are unchanged. Off by default: with no
`global.searchIndex` in the spaces configuration nothing changes, so this
release is safe to deploy everywhere without setting it up. Setup:
[search-index.md](search-index.md).

Files: `internal/service/search_index.go` (new), `search_index_test.go`
(new), `edit.go` (search asks the index), `config.go` (`global.searchIndex`),
`service.go` (start and stop), `space_delete.go` (remove a deleted space's
index), `admin.go` (`metatrash check`), `edit_test.go` (index on with
`METATRASH_TEST_SEARCH_INDEX=1`), `console/console.go`, `console/help.go`,
`console/help_test.go`, `go.mod`, `go.sum`, `assets.go`, `README.md`,
`CHANGELOG.md`, `docs/search-index.md` (new), this file.

New dependency: `github.com/blevesearch/bleve/v2 v2.6.1` and its modules
(pure Go; the arm64 build is unchanged). No schema, grant, tool schema or
configuration change unless you add the block.

Tests: the full service suite passed with the index off and again with
`METATRASH_TEST_SEARCH_INDEX=1` (index on for every `search`).

Check after release, on a server where it is turned on:

1. `sudo metatrash check` shows `search index  on: ...`.
2. Search a space twice (the first opens the index), then
   `sudo metatrash search-index status` lists `c-public` or `o-<id>` with its
   files and size.
3. Save a file with a new word and search for it at once: it is found.
