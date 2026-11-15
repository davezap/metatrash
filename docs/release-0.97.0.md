# Core 0.97.0: best matches, across spaces

Prepared 2026-11-15 by Claude (thread "Plan search and indexing", phase 3).

Tier: Core (Dave, 2026-11-14): search of one's own spaces is part of any
Metatrash server. Works with the search index on or off.

`search` gains `rank: true` (best matching files by word, with their best
lines), and on the account connection `space: "*"` searches every connected
space. The HTTP and site APIs take `rank=true`, ready for the search box in
Slate Viewer 0.40.0 (website and desktop). Exact-line search is unchanged.

Files: `internal/service/search_rank.go` (new), `search_rank_test.go` (new),
`service.go`, `types.go`, `mcp.go`, `http.go`, `api/tool-schema.json`
(0.20.0), `assets.go`, `CHANGELOG.md`, `docs/search-index.md`, this file.

No schema, grant or configuration change. Old clients don't send `rank`, so
they see no change.

Tests: the full suite with MariaDB (`METATRASH_TEST_DB_CONFIG`).

Check after release:

1. In an agent connected to the server, search a space with `rank: true`
   for a few words: files come back best first, each with lines.
2. On an account connection, search with `space: "*"` and `rank: true`:
   files from several spaces, each naming its space.
3. `curl -fsS 'https://<server>/api/v1/spaces/public/search?query=agents&rank=true'`
   returns `files`.
