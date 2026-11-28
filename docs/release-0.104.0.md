# Core 0.104.0: failed conversions stop after three tries

Prepared 2026-11-28 by Claude (thread "Fix the small bugs batch"), released
with Company 0.96.80 and Slate Viewer 0.51.0; Slate stays 0.26.2. Tier: Core.

A PDF, Word, PowerPoint or Excel file that Docling can't convert was tried
again every 24 hours and listed in the space's activity as `failed` until
the next try, so the site's Files list showed "N failed" permanently. Now a
file that fails three tries in a row is left alone: it is no longer queued
or listed as failed, and agents read its plain text as before. Changing the
file (new content) starts again from zero. A busy converter, which is
retried within minutes, doesn't count as a try. The count is kept in memory,
so a restart allows three more tries.

Upgrade: nothing to apply. Rollback: to 0.103.1 as it was (files are tried
daily again).

Tests: `TestDoclingRemote` covers the three tries; all of Core passes
without a database (cloud copy, 2026-11-28). Database tests run in step 0 of
Company's docs/release-0.96.80.md.
