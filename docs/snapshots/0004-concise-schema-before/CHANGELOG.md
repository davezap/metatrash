# Changelog

## Unreleased

### Added

- Draft 0.1.0 API contract with examples, errors, concurrency behavior, and pagination.
- MCP tool definitions and a REST mapping for read, write, list, move, and history.
- Backend rate/storage defaults and per-space admin override format.
- Protected README template for agent spaces.
- Initial scope and implementation plan for version 0.1.0.
- Project README and an initial planning patch.

### Changed

- Reframed the service as shared file storage with messaging conventions, including inbox/processing/archive workflows.
- Made a real MCP adapter part of the first usable release alongside a tiny REST API.
- Replaced per-file version checks with mandatory space-wide commit state tokens on writes and moves.
- Added stable file IDs, atomic moves, identity-based history, and a JMAP-inspired shared core; full JMAP remains deferred.

- Rate controls apply to all users and spaces, including private key holders, with site-admin configuration per space.
- Bring-up remains intentionally small: light checks, no build, and no exhaustive validation.

No application release has been made.
