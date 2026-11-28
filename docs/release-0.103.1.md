# Core 0.103.1: New from a template on the website

Prepared 2026-11-28 by Claude (thread "Build collaborative forms and
workflows"), released with Company 0.96.79; Slate Viewer stays 0.50.0 and
Slate 0.26.2. Tier: Core.

0.103.0 left `from-template` out of the website's own API
(`/api/v1/site/spaces/{space}/from-template`), so on the site every new file
from a template answered "File or revision not found" and nothing was made.
The REST route, the OAuth resource and MCP `write` with `fromTemplate`
worked. 0.103.1 adds it (POST, signed in, with the site's CSRF header like
every other site write).

Upgrade: nothing to apply. Rollback: to 0.103.0 as it was.

Tests: `TestSiteOffersFromTemplate`; all of `internal/service` passes without
a database.
