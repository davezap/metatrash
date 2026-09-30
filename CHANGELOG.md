# Changelog

## Unreleased

### 0.7.1 - Private-space quota prerequisite (2026-10-01)

- Fixed security-review F2 before Stage 3: unknown spaces, invalid credentials, and read-key write attempts no longer consume operation quotas. REST and MCP share the corrected access boundary.
- Reserve global, space, and client operation counters atomically. HTTP ingress counters also reserve together, preventing client-rejected attempts from draining the shared ingress allowance. Admitted downstream failures retain operation charges.
- Updated the contract and documented focused owner checks. Stage 3 storage/provisioning, owner browsing/URLs, and account-page integration remain separate upcoming bites.
- Saved before snapshot and incremental patch 0025. Go formatting, source review, and patch checks only; no builds, tests, database changes, or deployment.


### 0.7.0 - Public usernames and account page (2026-10-01)

- Added one-time public username selection with normalization, URL-safe validation, reserved names, and a database unique index. Conditional writes prevent changes after selection and handle concurrent claims.
- Added an authenticated, Origin/CSRF-protected and rate-limited save form; login remains available without a username. Account pages show My Spaces and Invitations placeholders.
- Added schema v2, a column-specific UPDATE grant, upgrade/recovery instructions, and focused owner checks. Stage 1 completion is owner-confirmed; Stage 3/4 and agent integration remain deferred.
- Saved before snapshot and incremental patch 0024. Go formatting/source and patch checks only; no builds, test execution, live SQL, or deployment.


### Documentation - Database user setup correction (2026-09-30)

- Added the missing CREATE USER step before GRANT, an administrator-client command, matching password-file instructions, grant inspection, and Unix-socket versus loopback-TCP account-host guidance.
- Saved before snapshot and incremental patch 0023. Documentation only; version remains 0.6.1. No database commands, builds, or tests run.

### 0.6.1 - MySQL logger build fix (2026-09-30)

- Fixed the owner-reported build failure: `mysql.NopLogger` implements `mysql.Logger` through a pointer receiver, so connection configuration now uses `&mysql.NopLogger{}`.
- Updated the migration guide to use 0.6.1. Saved before snapshot and incremental patch 0022. Verified the pinned driver's receiver declaration, formatting, and patch applicability; no builds or tests run.

### 0.6.0 - Database-backed human accounts (2026-09-30)

- Replaced runtime JSON account persistence with MariaDB/MySQL, a versioned InnoDB schema, protected local connection configuration, and pinned Go MySQL driver 1.9.3. Accounts-enabled upgrades now require explicit database setup and migration; accounts-disabled operation remains database-free.
- Added the offline `accounts-migrate` command using the service data lock, transactional import/completion marker, exact ID/email/time/allowance preservation, duplicate/conflict rejection, repeat verification, and explicit empty initialization. The source JSON is never modified and is not a fallback store.
- Retained email-code login and ephemeral sessions; sessions resolve users by immutable ID, existing account values survive login, and database outages fail closed. No private-space, username, membership, or agent integration work is included.
- Implemented prerequisite security-review F1: separate mail-attempt throttles and atomic delivery-budget reservations prevent narrower rejections draining the global allowance. Admitted failures retain reservations; F2/F3 remain pending.
- Added focused owner-run checks and database backup/cutover/recovery documentation. Saved before snapshot and incremental patch 0021. Formatting/syntax, source, JSON, and patch checks only; no builds, test execution, live SQL/SMTP, or deployment.

### Documentation - Private spaces plan alignment (2026-09-30)

- Updated the private-spaces plan and linked roadmaps with identity/allowance-preserving migration, provisioning recovery, existing-key compatibility, ownership and invitation rules, and security-fix sequencing.
- Clarified read-only content access for all humans; invited-user content permissions concern future agent access, whose implementation remains deferred. Retained export, deletion, key management, and remote Git as later bites.
- Saved before snapshot and incremental patch 0020. Documentation-only change; version remains 0.5.3. No builds or tests run.

### Documentation - Focused security review (2026-09-30)

- Reviewed system-escape boundaries and recorded availability findings, recommended fixes, and host-isolation limitations in `docs/security-review-2026-09-30.md`.
- No runtime source changes; version remains 0.5.3. Saved before snapshot and incremental patch 0019. No builds, test execution, live attacks, or deployment.

### 0.5.3 - Clean document URLs (2026-09-29)

- Serve public documents at `/spaces/public/<file-path>` and use these links in the explorer and initial/refreshed recent activity.
- Permanently redirect legacy `?path=` browser links; retain the space-root README, path validation, and folder-hosting prefix support. REST routes are unchanged.
- Existing whole-domain/folder Apache proxies need no changes. Saved before snapshot and incremental patch 0018; formatting and patch checks only, no builds or runtime tests.

### 0.5.2 - Slate document viewer (2026-09-29)

- Copied Slate 0.9.2 JavaScript and CSS into bundled assets and mounted Markdown documents in read-only preview with persistence and interactive tasks disabled.
- Added site-colour styling and an escaped plain-text fallback; other text files retain their plain-text view. Asset links respect folder hosting.
- Collapsed explorer folders by default and added recursive document counts beside their names.
- Saved before snapshot and incremental patch 0017. Focused syntax, copied-file integrity, and patch checks only; no builds or runtime tests.


### 0.5.1 - Folder hosting (2026-09-28)

- Added a validated public-url startup option for an HTTPS hostname and optional proxy-stripped prefix; root hosting remains the default.
- Prefixed browser/explorer links, asset URLs, activity fetches/JSON links, account forms/redirects, and displayed MCP addresses. MCP Host/Origin checks now use the configured public origin.
- Kept secure host-only account cookies with distinct names per installation prefix; account origin must match the public origin without its path.
- Added trailing-slash-correct Apache folder configuration and deployment notes. Saved snapshot and incremental patch 0016; formatting, JavaScript syntax and patch checks only, no builds or runtime tests.

### 0.5.0 - Dynamic interface foundation (2026-09-28)

- Replaced route-specific Apache proxies with one whole-domain proxy; Go rejects forwarded health requests and retains direct loopback health checks.
- Added a dedicated allowlisted handler for embedded CSS/JavaScript, isolated from templates and space storage. Only the homepage permits same-origin scripts and JSON connections; inline scripts remain blocked.
- Added the public recent-activity JSON endpoint with existing read permissions, rate limits, snapshot semantics, and a request deadline.
- Added manual and visible-page periodic refresh, timeout/error feedback and retry backoff. Retained server-rendered content on failure; all dynamic file data uses text nodes and fixed local links.
- Saved before snapshot and patch 0015. Go formatting, JavaScript syntax and patch checks only; no builds, runtime tests, or deployment.

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
