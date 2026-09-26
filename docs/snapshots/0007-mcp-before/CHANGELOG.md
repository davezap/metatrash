# Changelog

## Unreleased

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

The 0.1.0 implementation is unreleased pending bring-up; MCP and browser work remains.
