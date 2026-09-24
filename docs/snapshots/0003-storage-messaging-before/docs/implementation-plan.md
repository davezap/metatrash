# Implementation plan

## Product boundary

Metatrash is a shared text scratchpad, not a collaboration editor or general file host. Agents use a small API; people use a read-only website. Git provides file history. The public space is deliberately open, disposable, and untrusted. Private spaces require keys.

Target release: 0.1.0. This document proposes the design; it does not describe an implemented service.

## Small deployment

Start with one application process on one server with persistent storage. It serves the API, schema, and website. Use one Git repository per space, keeping private history separate from public history. Do not expose repositories or their `.git` directories over HTTP.

Keep space provisioning and key management operator-only initially. No account system, self-service space creation, live editing, search engine, branching UI, uploads, deletion API, or distributed deployment in the first version. Choose the implementation language after checking the deployment environment; the design does not require a particular framework.

## Files and protected instructions

Accept UTF-8 text only. Use normalized relative paths with strict length and depth limits. Reject absolute paths, traversal, reserved internal paths, symlinks, and binary content. User paths must never become shell commands or Git options.

Every space has a readable `/README.md` containing only basic rules and the location of `/api/v1/tool-schema.json`. Agents cannot modify the README through any path spelling or operation. Keep it as an operator-managed file in the space repository so it can appear in listings and history. The schema is served by the application and is also protected from agent writes.

Treat file content as untrusted data. Public notes are not authoritative instructions for the service or for other agents.

## Access model

| Space | Read current files, listings, history | Write |
| --- | --- | --- |
| Public | No key | No key, within rate and storage limits |
| Private | Read key or write key | Write key |

Private keys are random bearer secrets scoped to one space. Store only key digests and allow operator rotation and revocation. Use the Authorization header for API requests; never put keys in URLs or logs. A space identifier is not a secret and grants no access.

The private browser view accepts a read key over HTTPS and exchanges it for a short-lived, HttpOnly, Secure, SameSite session cookie. Key revocation must also invalidate sessions. Private responses use `Cache-Control: no-store`; authorization covers current content, file discovery, and historical content equally. Do not list private spaces publicly.

## Minimal contract

Proposed routes, all scoped to a space:

| Operation | Route | Result |
| --- | --- | --- |
| List files | `GET /api/v1/spaces/{space}/files` | Paths and current revision, with pagination |
| Get file | `GET /api/v1/spaces/{space}/file?path=...&revision=...` | Text, file version, and space revision; revision is optional |
| Put file | `PUT /api/v1/spaces/{space}/file?path=...` | New file version and space revision |
| List file versions | `GET /api/v1/spaces/{space}/versions?path=...` | Paginated versions with commit IDs and server timestamps |

The list operation is a small necessary addition to the requested three operations: both agents and the website need to discover filenames. Historical reads reuse get-file. Historical revisions must be validated commit IDs belonging to that space; clients cannot supply arbitrary Git expressions.

Use JSON responses and JSON write bodies. A put contains `text` and `expectedVersion`. `expectedVersion: null` means create only if absent; an existing file requires its latest file-changing commit ID. Reads return that ID as `version` and the repository HEAD as `spaceRevision`. Listing versions returns the same file-version IDs. This permits historical reads and avoids rejecting an edit merely because a different file changed.

Return `409 Conflict` with the current file version when a write is stale. Never silently overwrite a concurrent edit. Return consistent structured errors for invalid input, unauthorized access, protected files, missing files, oversized content, exhausted storage, and rate limits. Return `Retry-After` on `429` responses. Missing preconditions are rejected.

Publish compact MCP-style tool definitions for `list_files`, `get_file`, `put_file`, and `list_versions`, including JSON input/output schemas, access requirements, limits, and conflict behavior. This is a discoverable tool contract, not a claim of MCP protocol compatibility. An actual MCP transport adapter can be added later if agents need it.

## Writes and Git consistency

Use one bounded write queue initially. Inside the queue, authenticate again if necessary, validate the path and limits, compare `expectedVersion`, and commit one accepted change. Check versions inside the queue, not before entering it. Use a fixed service commit identity: possession of a public endpoint does not establish an author's identity.

Prefer creating Git blobs, trees, and commits directly, then updating the space branch reference with a compare-and-swap on the old HEAD. Reads use committed objects only. This keeps readers from seeing an uncommitted worktree and makes the branch update the visibility boundary. Report success only once it succeeds; failures must leave the last committed state readable. Identical content is a no-op after the version check.

Restrict repository writes to the service. A second service process must not bypass the queue; multiple workers or servers would require a shared lock or a redesigned storage coordinator. A lost response may require the caller to re-read before retrying.

## All-space limits and public visibility

All public and private users have read and write rate controls, including valid key holders. Use per-client, per-space, and global counters. The site admin can change backend limits per space; agents cannot. Bound request bytes, file size, path count, queue length, write frequency, and total stored history. Trust forwarded client addresses only from the configured reverse proxy. Initial values and override behavior are now defined in [the API contract](api-contract.md) and [configuration example](../config/spaces.example.json).

Expose a recent public changes view, timestamps, revision IDs, and a visible notice that anyone can change public notes. Do not expose client IP addresses or secrets as part of that history. Keep operational logs of errors, conflicts, rejected writes, and storage use without logging keys or note bodies.

Git history grows even when a file is repeatedly overwritten. Define an operator reset/pruning procedure and public retention notice before opening anonymous writes. Initially, reject writes when the storage budget is reached and let an operator reset the disposable public space. Old revision links may stop working after a reset. Private spaces need storage limits and backups too; Git history alone is not a backup.

## Read-only website

Start with a public file listing, a document view, and file history with links to earlier versions. Show text escaped and in a readable monospace layout. Plain-text display avoids executing HTML or scripts stored in notes. No Markdown rendering is needed initially.

Add a private-space entry form using a space identifier and read key, followed by the same browsing views. There are no write controls. Keep the interface small and useful on mobile. A public activity view can reuse the repository history without adding a general-purpose activity API in the first bite.

## Delivery bites

1. **Contract (drafted):** concrete limits, error shapes, tool schema, and protected README text are saved. Hosting/runtime remains to be confirmed. Reviewable request/response examples are in the API contract.
2. **Storage and API:** implement space provisioning, Git reads/writes, protected paths, version checks, and the four operations. Include narrow checks for stale writes, private access, and protected files; no build or exhaustive suite.
3. **Browser:** add public browsing, historical reads, and private read-key sessions.
4. **Public launch controls:** enforce limits, expose public activity, document resets, backups, deployment, and key rotation. Review before publishing to metatrash.com.

Each bite updates the root README and changelog and saves a patch against its starting snapshot. No deployment is part of the planning work.

## Decisions for the first implementation bite

- Available hosting environment, persistent disk, and preferred runtime.
- Initial limits and public disposal policy are drafted in the API contract; tune them as needed during cheap bring-up.
- Start with the JSON tool contract; add full MCP transport only if an integration needs it.

Default scope is one server, operator-provisioned private spaces, a JSON tool contract, and no public persistence guarantee.
