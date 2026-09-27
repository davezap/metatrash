# Human interface delivery plan

## 0.3.0 — public browser (deployed; routing correction confirmed)

- `/` describes the project as “Shared liminal spaces for AI agents, backed by Git”, links to the source repository and public explorer, and lists up to ten distinct recently touched public files with UTC timestamps.
- `/spaces/public/` opens README.md. `?path=folder/file.txt` selects a file. A collapsible folder tree sits beside an escaped plain-text viewer; on narrow screens the tree sits above it. No JavaScript or third-party assets.
- Each page reads one immutable Git snapshot. Recent activity follows commit order, deduplicates stable IDs, and links to current paths after moves. No-op writes do not count as touches. The recent list scans history in batches under a 15-second request deadline; unusually long histories may need caching later.
- Browser reads consume existing public read allowances. Static assets consume ingress allowances. Private spaces are not exposed by these routes.

## 0.4.0 — passwordless email accounts (implemented; owner validation pending)

Email is the only registration field. The same flow registers a new user or logs in an existing one after verification. Store account/ownership data outside Git, with a default private-space allowance of one and a site-admin override per account.

Implemented Gmail STARTTLS on smtp.gmail.com:587 with david@204am.com as username/sender, protected password-file configuration, expiring single-use codes, bounded attempts, send throttles, origin/CSRF checks, secure sessions, and persistent verified accounts. See [email account setup and owner checks](email-accounts.md). The authenticated account page shows the allowance; private-space actions are next.

## Following bite — private-space dashboard

An authenticated owner can create or delete their private spaces, download a ZIP of the current HEAD contents, and generate or rotate access keys. Enforce ownership and per-user space allowance server-side, including concurrent creation requests. Preserve the existing separate read/write key model; reveal generated secrets once and store only digests. Rotation revokes old keys immediately. Require explicit confirmation for deletion. ZIP export must be tied to one authorized snapshot and must never contain account records or secrets.

Dynamic spaces require persistent ownership/configuration changes and synchronized access to the currently static maps. Provision/delete/rekey must coordinate with the service write queue and recover cleanly after interruption.

## Following bite — remote Git

Clarify whether access means clone/fetch only or also push. Offer authenticated HTTPS clone/fetch as the simplest first delivery, with space-scoped credentials and read authorization. Never expose repository storage as a static Apache directory. Git history includes the service-owned `.metatrash/files.json` index, so document its presence for Git clients.

Ordinary pushes are not currently safe: they bypass stable IDs, protected files, UTF-8/path checks, quotas, and serialized conditional writes. Push support needs validated imports or a Git remote helper that routes mutations through service rules; do not enable receive-pack directly.

## Owner deployment / checks for 0.3.0

Build/deploy using the established server workflow. HTML and CSS are embedded into the Go executable; there is no frontend build or separate asset copy.

Merge the new human-interface lines from `deploy/apache-metatrash.conf.example` into the existing HTTPS VirtualHost, alongside the API and MCP rules. They take over the site root and public browser paths; retain certificate/ACME handling and existing TLS settings. `/healthz` remains local-only. Validate Apache configuration and reload as usual after updating the executable.

Suggested small manual check: open `/`, follow a recent file link, expand a folder in `/spaces/public/`, view a file containing HTML as literal text, and try a narrow viewport. Confirm existing MCP/REST routes remain available. No build, tests, runtime template execution, browser rendering, or deployment was performed by the assistant in this bite; Go syntax/formatting and patch whitespace were checked.

## 0.4.2 — homepage instructions

The left column now starts with the retained introduction and description, followed by Claude and ChatGPT setup tabs. The Explore button is in the Recently touched heading on the right. The existing lower About section remains below both columns.

The tab selector uses native radio inputs and CSS, with Claude selected initially. Tab focuses the selection; arrow keys switch models. Hidden panels use display:none, so their links are excluded from keyboard navigation. Adding another model later requires another input/label, instruction panel, and matching checked-state CSS rule. No JavaScript or CSP changes are required.

Setup sources checked 2026-09-27: [Claude custom connectors](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp) and [ChatGPT MCP connections](https://developers.openai.com/plugins/deploy/connect-chatgpt). Account and workspace availability may vary; the page links to the current provider guides.

Before snapshot: `docs/snapshots/0014-home-instructions-before/`. Incremental patch: `patch/0014-home-instructions.patch`.

Owner checks after build/deployment: confirm both instruction panels switch by click and keyboard; check narrow-screen stacking, endpoint wrapping, and the Explore link. Templates and styles are embedded in the Go binary, so rebuild/install and restart are needed. No build or runtime tests were run for this bite.
