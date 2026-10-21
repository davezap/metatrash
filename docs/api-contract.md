# File service contract

Metatrash is an agent-focused shared storage and messaging service. The primitive is a UTF-8 file in a space, automatically versioned by Git. Messaging is a naming and workflow convention over those files.

MCP (`/mcp`, `/mcp/account`) and REST (`/api/v1/…`) call the same service.

## Operations

| MCP tool | REST route | Inputs besides space |
| --- | --- | --- |
| `read` | `GET /api/v1/spaces/{space}/file` | query: `path`, optional `revision`, `startLine`, `lineCount` |
| `search` | `GET /api/v1/spaces/{space}/search` | query: `query`, optional `prefix`, `regex`, `ignoreCase`, `context`, `limit`, `revision` |
| `write` | `PUT /api/v1/spaces/{space}/file` | query: `path`; JSON: `text`, `ifInState`, optional `createOnly`, `strict` |
| `edit` | `POST /api/v1/spaces/{space}/edit` | JSON: `path`, `edits` (`oldText`, `newText`, optional `replaceAll`), optional `ifInState`, `strict` |
| `list` | `GET /api/v1/spaces/{space}/files` | query: optional `prefix`, `limit`, `cursor`, `blobs`, `after`, `since` |
| `move` | `POST /api/v1/spaces/{space}/move` | JSON: `from`, `to`, `ifInState`, optional `strict` |
| `delete` | `DELETE /api/v1/spaces/{space}/file` | query: `path`; JSON: `ifInState`, optional `strict` |
| `history` | `GET /api/v1/spaces/{space}/history` | query: `id`, optional `limit`, `cursor` or `offset` (REST) |

`search`, `edit` and read's line ranges (0.40.0) let an agent find, read and change part of a file without moving the whole text through its context: see [Search, line ranges and edit](#search-line-ranges-and-edit).

[tool-schema.json](../api/tool-schema.json) defines inputs and outputs and is served at `/api/v1/tool-schema.json` (public, no sign-in). Its `schemaVersion` changes with the schema, and `clients` names the oldest client versions the server works with (`mtsync.minVersion`, `slateViewer.minVersion`); desktop clients read it to update themselves before a server change breaks them. The MCP endpoints expose these tools through the official Go SDK v1.8.0 using stateless Streamable HTTP, supporting protocol versions 2025-11-25 and 2025-06-18. Successful results use `structuredContent` with a serialized JSON text fallback. Domain errors use `isError: true` and the error JSON in text content. REST returns the same domain objects and errors with HTTP statuses.

## Spaces and access

One global `public` space is fully open for reads and permitted mutations, within limits and protected-path rules. Every space has its own repository. Private spaces are either administrator-configured key spaces (below) or account-owned spaces reached through OAuth (next section). Space IDs contain lowercase letters, digits, and hyphens, start with a letter or digit, and are at most 48 characters.

For key spaces, REST and `/mcp` carry space-scoped bearer credentials through the transport, never tool arguments or URLs. A write key also permits reads. A read key cannot write or move. Private unknown spaces and invalid/missing keys return the same unauthorized response. Check access before disclosing files, revisions, IDs, or conflicts. Private HTTP responses use `Cache-Control: no-store`. Keys do not change backend configuration.

### OAuth agent access

Account-owned private spaces are reached only through OAuth, at the MCP endpoint `/mcp/account` and the REST routes under `/api/v1/account/` (each is its own protected resource; a token works only where it was issued). Requests carry `Authorization: Bearer <access token>`; without a valid token the response is 401 with `WWW-Authenticate: Bearer resource_metadata="…", scope="spaces:read spaces:write"` (plus `error="invalid_token"` for a bad or expired token). The MCP endpoint adds the `spaces`, `pull`, `pending` and `push` tools and REST adds `GET /api/v1/account/spaces`, `POST …/pull`, `GET …/pending?folder=` and `POST …/push`; both list the public space and the connected private spaces with `read_only` or `read_write` access. Other operations take the same arguments as the anonymous routes. `spaces` returns each private space's `space` name as `owner/slug` (the owner's username and the space slug, as in the website URL `/spaces/{owner}/{slug}/`) together with its immutable 32-hex `id`. The `space` argument accepts `public`, `owner/slug` (also the `owner/slug` a space had before its ownership was transferred) or a space ID; a bare slug returns `not_found` with a hint to use `owner/slug`. REST takes `owner/slug` as two path segments: `/api/v1/account/spaces/{owner}/{slug}/{file,files,search,edit,history,move,export}`. Results and cursors on these endpoints name private spaces by `owner/slug` whichever form was passed. Only spaces connected to the app are matched, and `not_found` tells the agent to call `spaces`.

Effective access is the user's consent for that app intersected with current ownership or active membership, the owner's per-member app permission and the token scope, checked once per operation before quotas and Git. Unconnected, unknown and key-protected spaces return `not_found`; suspended membership returns `forbidden`; a write without read/write access returns 403 `insufficient_scope` (REST adds an RFC 6750 challenge). The public space keeps its anonymous rules. Space keys are never accepted on OAuth routes, and `key` or `access_token` query parameters are refused on every route. See [oauth.md](oauth.md).

## Files, identity, and paths

A file has `id`, `path`, UTF-8 `bytes`, `protected` (a folder's `AGENTS.md` or `.metatrash.json`, below) and `deletable` (its opposite); read also returns `text`. Both are derived on every snapshot, not stored in the index (spaces from before 0.45.0 store `protected: true` on their README.md; it is ignored). Assign a random 32-character lowercase hexadecimal ID on creation. It survives edits and moves; a new file at a previously vacated path gets a new ID. IDs are unique within a space and never grant access.

Paths are case-sensitive, canonical relative ASCII paths, at most 240 characters and eight segments. Each segment starts with a letter, digit, underscore or dot and contains letters, digits, dots, underscores, or hyphens. A segment starting with a dot is allowed only inside a GitHub folder (a folder whose `.metatrash.json` has a github service), except a last segment of exactly `.metatrash.json` (folder configuration, below), which any folder may have; elsewhere writes and moves are refused with `invalid_request` and the reason. Never allowed: `.` and `..`, `.git` in any case, `.metatrash` as the first segment, empty segments, backslashes, leading/trailing slashes, symlinks, and file/directory collisions. A folder's github service cannot be removed while it still holds names starting with a dot. Decode HTTP query encoding exactly once. Folders are implicit; no mkdir operation.

README.md is an ordinary file (from 0.45.0; before, the root one was operator-managed and immutable). Hidden service metadata and Git internals are never exposed as files. Accept empty text; reject NUL and invalid Unicode; preserve text and line endings exactly.

## Delete

`delete` removes one file at `path`, with `ifInState` checked like write and move, and returns the removed file with old/new states. The commit records a `delete` operation for the file's ID. Earlier revisions stay readable with `read` and a `revision`, and `history` of the deleted ID still works and ends with the `delete` entry. A folder's `AGENTS.md` and `.metatrash.json` cannot be deleted (`protected_file`); deleting a folder's last other file removes them in the same commit. There are no directory deletes.

## Pull (GitHub folders)

`pull` (MCP on `/mcp/account`; REST `POST /api/v1/account/spaces/{space}/pull` with JSON `{"folder": "site/"}`) fills a GitHub folder from its repository and branch and records the commit it came from (the baseline). It needs write access and takes no `ifInState`: the service checks the folder itself.

- **First pull** (no baseline, or the folder's `repo` or `branch` changed since): the folder must be empty apart from `.metatrash.json` files and untouched `AGENTS.md` starters (the pull replaces those with the repository's copy or removes them), or hold files identical to the repository's. Otherwise `conflict`, naming up to ten files and why (differs, not in the repository, cannot be held). Identical files keep their IDs.
- **Later pulls** bring the folder to the branch's latest commit (create, write, delete; IDs kept for files that stay). Files changed in the folder since the last pull or push stay as they are and stay pending, as long as GitHub did not change the same files (`localChanges` counts the ones push would send). A file changed on both sides stops the pull with `conflict`, naming the files; the same change on both sides (same content, or both deleted) does not. No line-level merging. Nothing new on GitHub returns `upToDate: true` and writes nothing. Agents should pull at the start of each session.
- UTF-16 and UTF-32 files with a byte order mark are converted to UTF-8 with a UTF-8 byte order mark (lossless) and listed under `converted` (first 50; `convertedCount`), by path in the space. GitHub keeps the original: push sends the UTF-8 version only once the file is edited.
- Repository files Metatrash cannot hold are skipped and listed (`skipped`, first 50; `skippedCount`), by path in the repository: binary content, other encodings (Latin-1 and the like; the reason says to re-save as UTF-8), UTF-16/UTF-32 that does not decode cleanly, files over the space's file limit, names outside the path rules (spaces, other characters, a leading hyphen, more than 8 levels or 240 characters), symbolic links, submodules and `.metatrash.json` files. They stay on GitHub untouched.
- `.metatrash.json` files inside the folder are Metatrash's and are never touched by pull.
- One commit holds the whole pull: a summary line, then one line per file, so `history` shows each pulled file as `create`, `write` or `delete`.
- Limits: the space's file count and text bytes after the pull (else `storage_limit` with the totals), 10 pulls per space per 10 minutes, three minutes per pull.
- Errors: `invalid_request` (not a GitHub folder, unknown branch with the default branch named, empty repository), `forbidden` (no GitHub connection for the repository owner on the space owner's account, a suspended installation, or the app cannot reach the repository), `conflict` (also: a pull or push of the folder is already running), `storage_limit`, `github_unavailable` (502).

## Pending and push (GitHub folders)

`pending` (MCP; REST `GET /api/v1/account/spaces/{space}/pending?folder=site/`) compares a GitHub folder with its baseline (the last pull or push). Read access is enough. It returns:

- `changes`: files push would send, each `{path, change, note?, baseState?}` with `change` `added`, `modified` or `deleted`. `baseState` is the space revision holding the previous version (read the path at it to compare); absent for added files and for files that replace one pull skipped (the note says so). Edited converted files carry a note that GitHub's UTF-16 file will be replaced by UTF-8.
- `notPushed`: files `.gitignore` excludes. `.gitignore` applies only to files the repository does not hold, as in git; tracked files are pushed whatever the patterns say.
- `refused`: changes push refuses (files in `.github/workflows/`: workflows can reach the repository's CI secrets, so they are changed on GitHub directly).
- `converted`: unchanged files pull converted to UTF-8; push leaves GitHub's copy (some Windows files need UTF-16).
- `repo`, `branch`, `push` (the folder's mode), `baseCommit`, `baseState`, `state` (pass it to push as `ifInState`), `oldestChange` (when the oldest pending change was written), `upToDate` (nothing to push).
- `conflict` when the folder has not been pulled.

Write, edit, move and delete results inside a GitHub folder carry `hint` once the folder's oldest pending change is over 30 minutes old, at most once per 30 minutes per folder (not in `review` mode).

`push` (MCP: `{space, folder, message, ifInState?}`; REST `POST …/push` with the same fields as JSON) sends the pending changes as one commit on the folder's branch. It needs write access. All or nothing, never forced:

- `message` is required, at most 4,000 characters, several lines allowed. GitHub records the commit as the Metatrash app and signs it (Verified). The message gets the trailers `Co-authored-by: <username> <email>` (the caller's GitHub noreply address if they linked GitHub, else `<username>@users.noreply.metatrash.com`), `Metatrash-Space: owner/slug` and `Metatrash-Agent: <app name>`.
- Refused with nothing sent: no baseline (`conflict`, pull first); GitHub's branch moved past the baseline (`conflict`: pull, which merges, then push again); a change in `.github/workflows/` (`forbidden`); push mode `review` (`forbidden`); files in the folder changed since `ifInState` (`state_mismatch` with `currentState`; writes elsewhere in the space do not count); more than 1,000 files or 40 MB (`payload_too_large`); a local file standing where the repository holds a file pull skipped would make a folder of it (`invalid_request`); GitHub building another tree than expected (`github_unavailable`).
- Nothing pending: `upToDate: true`, no commit, nothing sent to GitHub.
- Success returns `commit`, `url`, `parent` (the baseline commit), counts `added`/`modified`/`deleted`, `files`, `notPushed`, `state` (the space revision whose files were pushed) and `newState` (after recording the new baseline). Changes written while a push runs stay pending. If recording the baseline fails after GitHub accepted the commit, `warning` says so; the next pull catches up without conflicts.
- Limits: 10 pushes per space per 10 minutes, three minutes per push, one pull or push per folder at a time.

## Folder files (`AGENTS.md` and `.metatrash.json`)

Every folder, the space root included, holds two files (0.45.0):

- **`AGENTS.md`**: instructions for agents working in that folder, in Markdown: what the folder holds, how it is organised, house rules. The root one starts from `docs/space-AGENTS.template.md`, other folders from one sentence saying nothing is written yet. Agents with write access rewrite it freely. Agents should read the root one first, and a folder's own before working in it.
- **`.metatrash.json`**: the folder's settings (below). The root starts with `{}`, other folders with `{"inherit": true}`.

The service adds both, in the same commit, when a write or move puts the first file in a folder (for every new folder on the path). They are `protected`: delete and move refuse them (`protected_file`), and when a folder's last other file is deleted or moved away, its folder files go in the same commit (deepest folder first, so emptied parents go too; the root keeps its own). A folder can be started by writing its `AGENTS.md`. An `AGENTS.md` in any letter case counts, so a folder with `agents.md` gets no second one. At startup (and when an owned space is first opened) the service adds missing folder files to every folder, as one commit per space; a space at its storage limit is skipped with a log line. Folder files count toward the space's file limit.

GitHub folders are the exception: everything in one belongs to its repository, so the service adds nothing inside it and `AGENTS.md` there is an ordinary repository file. The GitHub folder's own `.metatrash.json` stays protected; `.metatrash.json` files in its subfolders are deletable, as before.

## Folder settings (`.metatrash.json`)

A JSON object, validated on every write:

```json
{
  "inherit": true,
  "locked": { "services": "everyone", "purpose": "agents", "readOnly": "agents" },
  "readOnly": { "spec.md": "agents", "*.pdf": "everyone", "archive/": "agents" },
  "purpose": "Shared code for the bartco site",
  "children": { "notes/": "Working notes", "site/": "The website repo" },
  "services": [
    { "type": "github", "repo": "dave-zap/bartco-site", "branch": "main", "push": "agent", "pull": "auto" }
  ]
}
```

- `inherit: true` takes every setting from the parent folder's settings (themselves inherited when that folder inherits), except `services` (a repository attaches to one folder) and `inherit` itself; the folder's own keys override. Refused on the space root. A read of a `.metatrash.json` that inherits also returns `effective`: the settings with the inherited ones filled in.
- **Locks.** A lock has one of two levels: `"agents"` (agents can't change the thing; people can) or `"everyone"` (nobody can, people included, until a person lifts the lock). Anything not locked is read-write for everyone. A person is a write through Metatrash Sync (mtsync and Slate Viewer, the built-in OAuth client), by the owner or a member with write access; everything else (MCP, other apps, space keys, the anonymous `/mcp`) is an agent. `locked` and `readOnly` are maps of name to level; a list, as 0.45.0 wrote `locked`, locks each name for agents.
- `locked` locks keys of this file: `purpose`, `children`, `services`, `inherit`, `readOnly`, `locked`. Every write (or edit) is compared with the file as it was, and refused with `locked_setting` (403) when a key locked against the writer would change its value as the folder sees it with inherited settings. The locks are the folder's before the write, inherited ones included, so a child folder cannot override an inherited locked value. `locked` itself is always locked for agents and can't be locked for everyone, so a person can always lift a lock (lifting an `"everyone"` lock and changing the key take two writes). `inherit` is locked for agents while any lock applies, so a folder cannot shed its parent's locks. A new file counts as the one the folder would start with (`{"inherit": true}`), so new folders under a locked folder are locked too.
- `readOnly` locks files of this folder (up to 200 entries): a file name or `path.Match` pattern (`spec.md`, `*.pdf`, `n[0-9].md`) for files directly in the folder, or a subfolder name or pattern ending in `/` (`archive/`) for every file under it at any depth. A file locked against the writer can't be written, edited, moved (from or to), renamed or deleted, and a file can't be created under a locked name: `read_only_file` (403). It is never inherited (names are relative to the folder). A `.metatrash.json` is locked at most for agents, so its locks can be lifted. A change never removes a folder file locked against its writer along with an emptied folder. List, read and write results carry `readOnly: "agents"` or `"everyone"` on locked files. `readOnly` is an ordinary key: agents may change it unless `locked` locks it. GitHub pulls and pushes are not affected: a GitHub folder's files follow its repository. mtsync 0.46.0 handles a refused change to a file locked for everyone by keeping the local version as a conflict copy and putting the space's version back.
- `purpose` (up to 2,000 characters) and `children` (up to 200 entries, each key one file name or folder name ending in `/`, each text up to 1,000 characters) describe the folder; new descriptions belong in `AGENTS.md`.
- Every key optional; unknown keys and duplicate keys refused.
- `services` up to 8, at most one per type. Only `github` exists: `repo` is `owner/name`; `branch` defaults to `main`; `push` is `agent` (default: only the `push` tool sends), `auto` (accepted; behaves as `agent` until automatic pushing exists) or `review` (accepted; `push` is refused until a review screen exists); `pull` is `auto` (default). The `pull`, `pending` and `push` tools work on the folder (above).
- Services are allowed only in account-owned spaces, never on the space root, and a folder may not have a service of a type that a folder above or below it already has (no GitHub folder inside or around another). Different types may nest. The refusal names the clashing folder and suggests putting the repo beside it (for example under `dependencies/`).
- Files under a GitHub folder belong to that repository; files outside it are space-only and never synced.
- `actions` is reserved and refused until folder actions exist.
- `.metatrash.json` files cannot be moved (move source or destination). To remove a setting, rewrite the file.

Persist the ID-to-path mapping in a service-owned `.metatrash/files.json` in each repository. Commit metadata and file changes together. This small index preserves identity and history through moves without depending on Git rename guesses or a separate database. Exclude it from user listings, but include it in repository storage accounting. Other service files under `.metatrash/` (so far `github.json`, the GitHub baselines) are carried forward by every commit and are never visible to agents.

## Revision tokens and atomic mutations

Every read/list/search/history result returns `state`: the full Git HEAD hash of the selected snapshot. Every write, move and delete requires a non-null `ifInState` (edit takes one optionally). It is checked inside the write queue, against the current space HEAD, **per file** (0.40.0): when `ifInState` is older than HEAD, the change still goes ahead, on top of HEAD, if the files it touches (write, edit and delete: `path`; move: `from` and `to`) are the same at `ifInState` as at HEAD: the same ID and text, or absent at both. The result then carries `rebased: true`, since other files may have changed. Otherwise, or when `ifInState` is not an earlier state of the space, it is refused with `state_mismatch` (REST 409) and `currentState`. So agents working on different files no longer fail each other's writes. There is no force-write escape hatch.

`strict: true` keeps the whole-space rule: any change since `ifInState` refuses the operation. Use it when a change depends on other files that were read at that state. MCP defaults to `strict: false`. **REST write, move and delete default to `strict: true`** and take `strict: false` to opt in: sync clients (mtsync up to 0.39.0) treat a write's `newState` as a state whose every file they have, which a rebased write would break. (From 0.45.0 a write can also add or remove folder files; mtsync 0.45.0 therefore resumes from the state its pass started at, not from its last write's `newState`.) REST edit defaults to the per-file check like MCP. Rules that look at other files still check HEAD: `.metatrash.json` validation, dot-name rules, GitHub folders, path collisions, storage limits. Clients should re-read relevant content after a conflict, not blindly substitute a fresh token.

New spaces begin with a commit holding the root `AGENTS.md` and `.metatrash.json`, so even the first creation has a state token. List the space or read its `AGENTS.md` before creating a file. Mutation responses contain `oldState` and `newState`. Identical text is a no-op only after the state check; it returns equal states, `changed: false`, and consumes a write attempt.

Use one bounded mutation queue and compare-and-swap the repository branch reference. Read committed Git objects only. Create content, metadata, and commit before updating the reference; publication of that reference is the visibility boundary. One successful mutation creates one commit. Reads never observe a half-completed move. No shell interpolation of user input.

Use SHA-1 repositories initially and return full 40-character lowercase commit IDs. Historical revisions must be reachable from that space's current branch, not arbitrary Git expressions. Historical reads return the requested snapshot's state; it is not a valid write token unless still current.

## Tiny examples

List `public` first to obtain state `1111111111111111111111111111111111111111` (example hash). Call `write`:

```json
{"space":"public","path":"inbox/agent-b/message-001.txt","text":"Please review notes/design.txt.\n","ifInState":"1111111111111111111111111111111111111111","createOnly":true}
```

The response contains the new file ID and `newState`. `createOnly: true` rejects an existing destination with `destination_exists` (409), even when its text is identical. Default `createOnly: false` creates or replaces at that path after the state check.

After reading the message, its recipient calls `move`:

```json
{"space":"public","from":"inbox/agent-b/message-001.txt","to":"archive/agent-b/message-001.txt","ifInState":"2222222222222222222222222222222222222222"}
```

Move preserves ID and content. Source must exist; destination must be absent. Same-path moves are invalid. Do not overwrite a destination or support directory/cross-space moves. Failures leave both paths unchanged. A successful move returns the file at its new path plus old/new states.

## Messaging convention

Suggested paths: `inbox/{recipient}/{unique-message-id}.txt`, `processing/{recipient}/{unique-message-id}.txt`, and `archive/{recipient}/{unique-message-id}.txt`. The body is arbitrary text; optional sender, timestamp, and reply-path labels are conventions, not verified identity or a required envelope.

Senders list to obtain state, then create with `createOnly: true`. Recipients list their inbox prefix, read messages, and move them to processing or archive. Competing claims cannot both move the same source successfully. A claim may remain in processing after a crash; recovery is an agent convention.

No broker, subscriptions, receipts, exactly-once delivery, recipient ACLs, or trusted sender identity. All writers in a space can edit or move its ordinary files. Public inboxes remain public. This service stores shared state; agents decide what it means.

## Search, line ranges and edit

These save tokens: an agent finds the lines it needs, reads only those, and sends back only what changes.

- **Search** (`search`, `GET …/search`) finds lines containing `query` (1–500 characters, one line) in the files under `prefix`, at HEAD or `revision`. Plain text by default; `regex: true` uses Go's RE2 syntax (linear time, no backreferences); `ignoreCase: true` folds case. Each match has `path`, file `id`, 1-based `line` and the line's `text` without its line ending, cut to 400 bytes; `context` (0–10) adds that many lines `before` and `after`. Matches come in path then line order, up to `limit` (default 50, at most 200); `truncated: true` says there were more. It reads every file under the prefix with one `git cat-file --batch` and counts as one read. REST takes the same names as query parameters (`regex=true`, `ignoreCase=true`).
- **Line ranges** on `read`: `startLine` (1-based) and `lineCount` (1–5,000) return only those lines, each with its line ending, so the text joins back exactly; `lineCount` alone starts at line 1 and `startLine` alone runs to the end. The result adds `lines: {start, end, total}`. A line ends after `\n`; a last line without one still counts; an empty file has none. A `startLine` past the end is `invalid_request` naming the file's line count. Without them, read is unchanged.
- **Edit** (`edit`, `POST …/edit` with `{path, edits, ifInState?, strict?}`) changes an existing file: 1–50 `edits`, each `{oldText, newText, replaceAll?}`, applied in order to the evolving text. `oldText` must be non-empty and match exactly once (or at least once with `replaceAll`); otherwise it is refused with `conflict` (409) naming the edit and saying whether it matched nothing or several places. When the file uses CRLF line endings and an `oldText` with LF matches nothing, it is matched with CRLF and its `newText` converted the same way. All edits succeed or none are saved; the result is one commit with a `write` entry in history and the same response as write (`changed: false` and no commit when the text ends up the same). `ifInState` is optional: the exact match already refuses a change made over someone else's, and when given it is checked per file (or for the whole space with `strict`). Otherwise the rules of write apply: the file must exist (`not_found`), `.metatrash.json` is validated and its locked keys enforced, the result must fit the file limit (`payload_too_large`), and it counts as one write. To create a file or replace all of it, use write.

## Listings and history

Files sort by ASCII path. `prefix` is empty or a canonical folder prefix ending in slash, e.g. `inbox/agent-b/`; it selects descendants without listing internal metadata. `limit` defaults to 50 and is capped at 100. Both list and history return `state` and `nextCursor` (null at the end). REST list with `blobs=true` adds each file's `blob`, the Git blob hash of its text (`git hash-object`), so a sync client can find changed files without reading them; MCP results never carry it. REST list with `after=<state>` (not with `cursor`) waits while the space is still at that state, up to 25 seconds, and answers as soon as a write moves it, so a client hears of changes without polling; it then lists as usual, and an unchanged `state` means the wait ran out. It counts as one read. At most 256 lists wait at once service-wide; past that they answer at once. REST list with `since=<state>` (not with `cursor`) returns only what changed under `prefix` between that state and now: `files` holds the files added or changed (new text or a new ID), with `blob`, all in one response, and `removed` the paths deleted or moved away; with no changes both are empty and `state` is unchanged. A `since` that is not an earlier state of the space is `not_found` (list in full instead). `after` and `since` combine: wait for a change, then get it.

First-page requests capture a revision. Opaque validated continuation tokens bind space, operation, filters/ID, limit, revision, and offset; following pages use that snapshot. Tokens grant no access. Mismatched/malformed tokens return 400. Tokens are signed with a process-local secret and expire on service restart; restart listing if rejected. A reset that removes their snapshot returns 404 if the token is otherwise still valid.

History accepts the stable file ID from read/list and follows its identity through moves using the versioned index. Entries are newest first with `revision`, `path` at that revision, `operation` (create/write/move/delete), a server UTC `timestamp`, and `by`: who made the change, as `{name, app, kind}`. `name` is the account's username, or its email address before the `@` when it has none (usernames are public; absent for space keys), `app` the app the change came through (`Metatrash Sync` for mtsync and Slate Viewer, the app name an agent's connection was approved for, `space key`, or `anonymous`), and `kind` is `person` (a Metatrash Sync write) or `agent`. They are recorded as `Metatrash-By`, `Metatrash-App` and `Metatrash-Kind` trailers at the end of the commit message. Changes from before 0.47.0, and the service's own commits (new spaces, added folder files), have no `by`. Read old text using that entry's path and revision; a `delete` entry's revision no longer has the file, so read the next older entry. Deleted files keep their history, ending with the `delete` entry. IDs that never appear and unavailable historical files/revisions return 404. A file's old path can later belong to a different ID. REST history also takes `offset` (0–10000, not with `cursor`): it skips that many of the newest entries, so a client can page by number without keeping a cursor; past the end the page is empty. Entries shift when the file changes between pages, and `nextCursor` still pins a snapshot.

## Export

REST only: `GET /api/v1/spaces/{space}/export`, and `GET /api/v1/account/spaces/{owner}/{slug}/export` (or `{id}`) for connected spaces, returns the space at its current state as a zip, for backups. It takes no query parameters and needs read access (a read key, or a connection with read-only or read-write access). Every file is stored at its path, dated with the time of that state's commit, and `.metatrash-export.json` beside them records `space`, `state`, `committed` and each file's `path`, `id` and `bytes` (a space root cannot hold a dot file, so the name never collides). The response is `Content-Type: application/zip` with `Content-Disposition: attachment; filename="<owner>-<slug>-<date>-<state>.zip"`, `Cache-Control: no-store` and `X-Metatrash-State: <state>`. It counts as one read, plus its own allowance because it reads every file: 60 exports of a space and 10 per client of that space in each ten minutes (`rate_limited` past that). The archive is built in full before it is sent, so an error is an ordinary error response. The console's `metatrash spaces export` writes the same zip from the server's disk.

## Rate controls for everyone

[spaces.example.json](../config/spaces.example.json) defines initial backend defaults. All public and private clients are limited, including valid key holders, MCP clients, REST clients, and the read-only browser. These are provisional low-cost starting values, adjustable without changing the API:

| Counter | Default per 60 seconds |
| --- | ---: |
| Client reads within a space | 120 |
| Client writes within a space | 20 |
| All reads within a space | 600 |
| All writes within a space | 60 |
| Service-wide reads | 1,200 |
| Service-wide writes | 120 |

Use simple fixed windows. A client is the trusted source IP, scoped to the space; sharing or rotating keys does not bypass that bucket. Only use forwarded addresses from explicitly configured proxies. NAT users share a client bucket. Space and service counters constrain aggregate traffic. Window boundaries can allow adjacent bursts; this is acceptable for the initial low-cost version.

Apply the separate HTTP ingress limits before authentication. After space authentication and read/write authorization, reserve global, space, and client operation counters atomically: all are charged or none are. Unknown spaces, invalid keys, and read-only keys attempting writes do not consume operation allowance. Admitted operations retain their charge even if later validation, revision checks, or storage work fails. Write, edit, move and delete share the write counters. MCP and REST share the same counters, so changing transport does not reset an allowance. Reads include file lists, history, and browser document views; a browser view must not count the same underlying operation twice. MCP lifecycle/discovery requests, schema/static assets, and the private login route need a separate basic ingress allowance so they cannot bypass service protection.

On exhaustion, return 429 with `Retry-After` in integer seconds and a matching `retryAfterSeconds` field. Use the longest remaining wait among exhausted counters. Bound the in-memory counter map and discard expired entries. Counters reset on application restart for this version.

Only the site admin edits backend configuration. Merge each space's `overrides` into `defaults` at leaf level; omitted values inherit defaults. For example:

```json
{"visibility":"private","overrides":{"rates":{"clientWrites":40,"spaceWrites":120}}}
```

All limits must be positive integers. Rate windows are capped at 86400 seconds and the waiting queue at 10000. Invalid configuration stops startup with a clear error. Apply edits on service restart initially; no admin UI or reload mechanism is required. Global ceilings still apply when per-space limits increase. Keys belong in a separate secret store, never in this example file or a space repository. The initial HTTP ingress ceiling is 2400 requests/minute globally and 240/client, separate from per-space operation limits. These ingress counters are also reserved atomically, so a client already at its limit cannot drain the remaining global ingress allowance.

## Size and queue defaults

- 64 KiB per text file; 512 KiB per JSON request to allow JSON escaping overhead.
- 1,000 files and 16 MiB of current UTF-8 text per space, including folder files.
- 128 MiB repository budget per space, including history; reject further writes when the budget would be exceeded. Account for the candidate write before publishing its branch reference. This is a per-space policy cap; deployment also needs free disk headroom for temporary Git objects.
- 32 waiting writes service-wide, plus the active write. A full queue returns 503 with `Retry-After: 1`.

The site admin may override per-space storage limits as well as rates. Public content has no retention guarantee: an operator may reset it, making historical links unavailable. Private retention and backups are operator responsibilities.

## Errors

Every domain error is `{"error":{"code":"...","message":"..."}}`. Do not expose stack traces, secrets, or server paths.

| REST status | Code |
| --- | --- |
| 400 | `invalid_request` |
| 401 | `unauthorized` |
| 403 | `forbidden`, `protected_file` |
| 404 | `not_found` |
| 409 | `state_mismatch`, `destination_exists`, `conflict` |
| 413 | `payload_too_large` |
| 429 | `rate_limited` |
| 507 | `storage_limit` |
| 503 | `queue_full` |
| 502 | `github_unavailable` |
| 500 | `internal_error` |

State mismatches include `currentState` after authorization. Rate/queue errors include `retryAfterSeconds`; REST also sends `Retry-After`. REST creation returns 201; other successful operations return 200. Unknown fields and duplicate query parameters are rejected. Unsupported methods return 405 with `Allow`, unsupported mutation media types return 415; both use `invalid_request`.

## JMAP-inspired core, small adapters

Keep transport out of storage logic. Model a space as an account-like scope, a file as an object with stable identity and mutable path, and a Git commit as collection state. Use `ifInState`, `oldState`, and `newState` consistently. This follows the object/state approach in [JMAP Core, RFC 8620](https://www.rfc-editor.org/rfc/rfc8620.html#section-5), but deliberately requires a precondition on every mutation.

A later JMAP adapter can translate space to accountId and these objects to a custom File data type, with get/set/query/changes methods. Git snapshots and the identity index allow change calculation. Removed history would need a cannotCalculateChanges response. That adapter still needs capability discovery, method envelopes, batching, and other protocol behavior; this draft is not JMAP or JMAP Mail compatibility.

MCP discovery and tool results follow the [official MCP tools specification](https://modelcontextprotocol.io/specification/2025-06-18/server/tools). Pin the supported SDK/protocol version at implementation time. Do not build a second mutation engine for REST or future JMAP.
