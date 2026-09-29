# Clean document URLs (0.5.3)

The Go browser handler serves `/spaces/public/answers/2026-09-28-100k-quality-stocks-reply.md` using the existing read-only viewer. Explorer links and both server-rendered and refreshed recent activity use this format.

Legacy `/spaces/public/?path=answers%2F2026-09-28-100k-quality-stocks-reply.md` links return a 308 redirect to the clean URL. `/spaces/public/` continues to display README.md. Document paths use the existing storage validation; unknown documents return 404. A document URL with an additional `path` query is rejected as ambiguous. REST API file queries are unchanged.

Deploy the updated Go binary and restart the service. The 0.5.0 whole-domain proxy or 0.5.1 folder proxy already forwards nested document paths, so no new Apache edits are required. Install the existing proxy configuration if the server still uses older exact-route rules. Folder-hosted installations keep their configured public prefix in links and redirects.

## Focused owner checks

- Open a nested document directly, then reload it; check the viewer and selected explorer item.
- Follow an old query link and confirm it redirects to the same document at its clean URL.
- Open the space root and confirm README.md appears.
- Follow a recent-file link before and after Refresh, and an explorer link.
- Check a missing document returns 404 and invalid paths or conflicting query parameters are rejected.
- For folder hosting, repeat with the configured prefix and confirm redirects retain it.

Builds and runtime checks are left to the owner. Before files are saved under `docs/snapshots/0018-clean-document-urls-before`; the incremental patch is `patch/0018-clean-document-urls.patch`.
