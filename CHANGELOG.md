# Changelog

## Unreleased

### 0.4.2 - Homepage agent instructions (2026-09-27)

- Simplified the left introduction to the requested project description and moved Explore the public space beside Recently touched.
- Added Claude and ChatGPT setup tabs, the MCP endpoint, and a shared starter prompt. Native radio controls support keyboard selection without JavaScript or security-policy changes.
- Updated ChatGPT setup documentation against the official connection guide and linked both providers' setup guides from the page.
- Saved before snapshot and incremental patch 0014. Source and patch checks only; no build, automated tests, deployment, or live connector checks.

### 0.4.1 - Account form Origin fix

- Changed account-page Referrer-Policy from no-referrer to same-origin so browser POST forms retain the Origin required by authentication checks. Exact Origin and CSRF validation remain enforced.
- Extended owner-run checks for the response policy and rejection of missing/null origins. Saved snapshot and patch 0013. Formatting/source/patch checks only; no builds, test execution, live email, or deployment.

### 0.4.0 - Passwordless email accounts (2026-09-27)

- Added email-only registration/login using six-digit single-use codes and Gmail SMTP with mandatory STARTTLS, certificate verification, and protected password-file configuration.
- Added origin/CSRF checks, email/IP/global send limits, bounded verification attempts, expiring hashed sessions, secure cookies, and sign-out.
- Persisted verified accounts outside Git with stable IDs and default private-space allowance one. Added an authenticated account page; space creation/export/keys remain the next bite.
- Added optional startup configuration, Apache account routes, server setup notes, and owner-run fake-mail checks.
- Recorded owner confirmation that the 0.3.0 Apache path correction fixed the public site.
- Saved before snapshot and incremental patch 0012. Formatting/JSON/source/patch review only; no build, test execution, SMTP connection, email send, or deployment.

### Apache proxy path correction (2026-09-27)

- Fixed root, MCP, public explorer, and stylesheet ProxyPassMatch rules with explicit captures/backreferences. Without a backreference, Apache appends the original path to the target, causing duplicate paths and service 404 responses.
- Owner confirmed installed version 0.3.0 and a direct localhost root response of HTTP 200. Public routing fix awaits owner Apache validation/reload. Configuration-only correction; binary version remains 0.3.0. Saved snapshot and patch 0011; no builds or runtime tests.

### 0.3.0 - Public human interface (2026-09-27)

- Added the home page with project purpose, source link, public explorer link, and ten distinct recently touched public files with dates and links.
- Added a collapsible public file tree and escaped plain-text document viewer, responsive layout, keyboard navigation, and restrictive content security policy.
- Reused public read limits and immutable Git snapshots; recent activity follows stable file IDs through moves.
- Added exact Apache browser routes and a staged plan for passwordless accounts, private-space lifecycle/ZIP export, keys, account allowances, and remote Git.
- Saved the pre-change snapshot and incremental patch 0010. Go formatting/source review only; no builds, tests, runtime/browser validation, or deployment. Existing 0.2.0 deployment remains unchanged.

### Documentation - MCP smoke result and space README

- Recorded the owner-reported successful public MCP smoke test, including mutations and historical reads.
- Corrected the embedded space README template to describe MCP as available. Existing stored READMEs are unchanged; the public copy needs administrator maintenance.
- Saved a before snapshot and patch. No build, test execution, or deployment; version remains 0.2.0.

### Documentation - Windows PowerShell and Linux bring-up

- Split MCP live checks into Windows PowerShell (Invoke-RestMethod with JSON serialization) and Linux Bash (curl) instructions, including expected responses and shell-specific troubleshooting.
- Recorded owner-confirmed 0.2.0 local/public initialization and public discovery of all five tools. MCP file-operation checks remain pending.
- Documentation-only change; application version remains 0.2.0. No builds or live tests run.

### 0.2.0 - MCP adapter (2026-09-26)

- Added `/mcp` with the official Go MCP SDK v1.8.0, pinned to protocol versions 2025-11-25 and 2025-06-18, using stateless Streamable HTTP and JSON responses.
- Reused the published schemas and shared access/storage service for all five tools; added request-scoped bearer credentials, argument validation, bounded requests, and Host/Origin checks.
- Added an exact Apache MCP route, upgrade notes, and an optional owner-run MCP smoke test. Builds and tests have not been run for this change.
- Raised the build requirement to Go 1.25; dependency versions and checksums are recorded.
- Recorded the owner's successful public REST smoke test: create/read/update, stale-state rejection, stable-ID move, history, and historical reads. The reported file ID is valid at 32 characters.

### Earlier 0.1.0 work

### Added

- Go 0.1.0 storage/REST implementation using the standard library and system Git; not yet built or deployed.
- Automatic per-space Git provisioning, stable file IDs, atomic revision-checked writes/moves, snapshot pagination, and history across moves.
- Private read/write key digests, key generation, shared rate controls, space storage limits, and a bounded write queue.
- Embedded schema/space README, loopback HTTP server, Apache/systemd templates, and Amazon Linux 2023 bring-up notes.
- One optional owner-run smoke test; no build or test execution during implementation.
- Draft 0.1.0 API contract with examples, errors, concurrency behavior, and pagination.
- MCP tool definitions and a REST mapping for read, write, list, move, and history.
- Backend rate/storage defaults and per-space admin override format.
- Protected README template for agent spaces.
- Initial scope and implementation plan for version 0.1.0.
- Project README and an initial planning patch.

### Changed

- Recorded the 2026-09-24 server installation: GitHub deploy key, ARM64 build, service account, configuration permissions, systemd startup, and successful local health check. Apache API routing remains pending.

- Shortened tool/schema descriptions and removed repeated output-field instructions without changing validation or API behavior.

- Reframed the service as shared file storage with messaging conventions, including inbox/processing/archive workflows.
- Made a real MCP adapter part of the first usable release alongside a tiny REST API.
- Replaced per-file version checks with mandatory space-wide commit state tokens on writes and moves.
- Added stable file IDs, atomic moves, identity-based history, and a JMAP-inspired shared core; full JMAP remains deferred.

- Rate controls apply to all users and spaces, including private key holders, with site-admin configuration per space.
- Bring-up remains intentionally small: the owner performed the server build and local health check; no exhaustive validation.

The owner confirmed the 0.3.0 public routing fix. Version 0.4.0 email accounts await owner build/deployment; private-space management remains planned.
