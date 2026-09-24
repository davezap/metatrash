# API contract - 0.1.0 draft

This is the first implementation bite: a concrete contract, not a running API. Tool inputs and outputs are defined in [tool-schema.json](../api/tool-schema.json). The server will publish that file at `/api/v1/tool-schema.json`. It is MCP-style metadata, not an MCP protocol endpoint.

## Routes and access

All responses use JSON. PUT requires `Content-Type: application/json`. Space IDs are lowercase letters, digits, and hyphens, up to 48 characters, beginning with a letter or digit. `public` is the single anonymous space.

| Operation | Method and path | Query parameters |
| --- | --- | --- |
| List files | `GET /api/v1/spaces/{space}/files` | `limit`, `cursor` |
| Get file | `GET /api/v1/spaces/{space}/file` | `path`, optional `revision` |
| Put file | `PUT /api/v1/spaces/{space}/file` | `path` |
| List versions | `GET /api/v1/spaces/{space}/versions` | `path`, `limit`, `cursor` |

Private requests require `Authorization: Bearer <space-key>`. A write key also permits reads; a read key cannot write. Keys cannot change space configuration or limits. Private unknown spaces and invalid/missing keys return the same 401 response. Authenticate before reporting file existence, conflicts, or history. Private responses and errors use `Cache-Control: no-store`.

## Text and paths

Paths are relative and case-sensitive, at most 240 ASCII characters and eight segments. A segment starts with a letter or digit and may contain letters, digits, dots, underscores, and hyphens. Reject empty segments, dot segments, backslashes, leading/trailing slashes, and noncanonical input; do not silently repair paths. Decode HTTP query encoding exactly once. File/directory collisions are invalid requests.

The root `README.md` is readable and versioned but protected from writes, including case variations. No hidden paths or Git internals are addressable. Accept valid UTF-8 text, including empty text, but reject NUL and invalid Unicode. Preserve line endings and text exactly; do not normalize notes.

## Read and write examples

Create `notes/hello.txt` with `PUT /api/v1/spaces/public/file?path=notes%2Fhello.txt`:

```json
{"text":"Hello, agents.\n","expectedVersion":null}
```

Successful creation returns HTTP 201:

```json
{"path":"notes/hello.txt","version":"1111111111111111111111111111111111111111","spaceRevision":"1111111111111111111111111111111111111111","created":true,"changed":true}
```

Commit IDs in these examples are placeholders. Replacing a file returns 200 with `created: false`. An identical-text write returns 200 with `changed: false` and makes no commit, but only after checking the supplied version. Every PUT attempt consumes the write rate allowance.

Reading that path returns 200:

```json
{"path":"notes/hello.txt","text":"Hello, agents.\n","version":"1111111111111111111111111111111111111111","spaceRevision":"1111111111111111111111111111111111111111"}
```

To update, send the returned `version` as `expectedVersion`. Omitting the field is invalid. Null permits creation only. Version comparison and commit happen inside the write queue. A stale update returns HTTP 409:

```json
{"error":{"code":"conflict","message":"File changed; read it again before retrying.","currentVersion":"2222222222222222222222222222222222222222"}}
```

`currentVersion` is null when the file is absent. A file's `version` is its latest content-changing commit at the selected revision; `spaceRevision` is the repository revision used for the read. Other files changing do not invalidate this file's version. A historical read supplies a full 40-character lowercase SHA-1 commit ID reachable from the space's current branch. Arbitrary Git revision expressions are invalid. A missing historical file or an unavailable revision returns 404.

## Listings

Files are sorted by ASCII path; versions are newest first in the space's single linear history. `limit` defaults to 50 and is capped at 100. Responses always include `spaceRevision` and `nextCursor`, with null indicating the end. File entries contain `path`, `version`, UTF-8 `bytes`, and `protected`; version entries contain `version` and a server-generated UTC `timestamp`.

The first page captures a revision. Continuation tokens bind the space, operation, path where applicable, limit, revision, and offset. They are opaque and validated by the server, do not grant access, and cannot inject Git expressions. Subsequent pages use that captured revision despite new writes. Malformed or mismatched tokens return 400; a revision removed by an operator reset returns 404 and the client must restart listing.

## Rate controls for everyone

[spaces.example.json](../config/spaces.example.json) defines initial backend defaults. All public and private clients are limited, including valid key holders and the read-only browser. These are provisional low-cost starting values, adjustable without changing the API:

| Counter | Default per 60 seconds |
| --- | ---: |
| Client reads within a space | 120 |
| Client writes within a space | 20 |
| All reads within a space | 600 |
| All writes within a space | 60 |
| Service-wide reads | 1,200 |
| Service-wide writes | 120 |

Use simple fixed windows. A client is the trusted source IP, scoped to the space; sharing or rotating keys does not bypass that bucket. Only use forwarded addresses from explicitly configured proxies. NAT users share a client bucket. Space and service counters constrain aggregate traffic. Window boundaries can allow adjacent bursts; this is acceptable for the initial low-cost version.

Count attempted operations, including rejected requests. Apply global limits before authentication, and per-space limits for configured spaces without disclosing private configuration. Reads include file lists, history, and browser document views; a browser view must not count the same underlying operation twice. Schema/static assets and the private login route need a separate basic ingress allowance so they cannot bypass service protection.

On exhaustion, return 429 with `Retry-After` in integer seconds and a matching `retryAfterSeconds` field. Use the longest remaining wait among exhausted counters. Bound the in-memory counter map and discard expired entries. Counters reset on application restart for this version.

Only the site admin edits backend configuration. Merge each space's `overrides` into `defaults` at leaf level; omitted values inherit defaults. For example:

```json
{"visibility":"private","overrides":{"rates":{"clientWrites":40,"spaceWrites":120}}}
```

All limits must be positive integers. Invalid configuration stops startup with a clear error. Apply edits on service restart initially; no admin UI or reload mechanism is required. Global ceilings still apply when per-space limits increase. Keys belong in a separate secret store, never in this example file or a space repository.

## Size and queue defaults

- 64 KiB per text file; 512 KiB per JSON request to allow JSON escaping overhead.
- 1,000 files and 16 MiB of current UTF-8 text per space, including the protected README.
- 128 MiB repository budget per space, including history; reject further writes when the budget would be exceeded. Account for the candidate write before publishing its branch reference. This is a per-space policy cap; deployment also needs free disk headroom for temporary Git objects.
- 32 waiting writes service-wide, plus the active write. A full queue returns 503 with `Retry-After: 1`.

The site admin may override per-space storage limits as well as rates. Public content has no retention guarantee: an operator may reset it, making historical links unavailable. Private retention and backups are operator responsibilities.

## Errors

Every error has `{"error":{"code":"...","message":"..."}}`. Do not return stack traces, keys, or server filesystem paths.

| HTTP status | Code |
| --- | --- |
| 400 | `invalid_request` |
| 401 | `unauthorized` |
| 403 | `forbidden`, `protected_file` |
| 404 | `not_found` |
| 409 | `conflict` |
| 413 | `payload_too_large` |
| 429 | `rate_limited` |
| 507 | `storage_limit` |
| 503 | `queue_full` |
| 500 | `internal_error` |

Conflict errors include `currentVersion`. Rate and queue errors include `retryAfterSeconds` and the matching HTTP header. Unknown fields and duplicate query parameters are rejected. Unlisted HTTP methods return 405 with `Allow` and `invalid_request`; unsupported PUT media types return 415 with `invalid_request`.

## Bring-up assumptions

One process, Git installed, persistent local disk, and HTTPS at the application or reverse proxy. Hosting/runtime is still to be confirmed. No build step, database, full MCP transport, or exhaustive validation suite is needed for the contract bite. Next is a small storage/API implementation with just targeted smoke checks.
