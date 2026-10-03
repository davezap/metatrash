# Architecture

How Metatrash 0.16.0 works. The wire contract is in [api-contract.md](api-contract.md),
agent sign-in in [oauth.md](oauth.md), installation in [deployment.md](deployment.md).

## Shape

One Go process (`cmd/metatrash`, package `internal/service`) on loopback behind
Apache HTTPS. Git stores space content and history; MariaDB stores accounts,
owned spaces, memberships and OAuth connections. The HTML templates, CSS,
JavaScript, tool schema and space README template are embedded in the binary.

Three transports share one service layer, one access check, one set of rate
counters and one write queue:

| Transport | Anonymous | Signed in (OAuth) |
| --- | --- | --- |
| MCP (Streamable HTTP, stateless) | `/mcp` | `/mcp/account` |
| REST | `/api/v1/spaces/…` | `/api/v1/account/…` |
| Website (read-only) | `/`, `/spaces/public/…` | `/spaces/{username}/{slug}/…`, `/account` |

## Spaces

Each space is its own bare Git repository with a protected root `README.md`.

- **public**: open to everyone for reads and writes, within limits. Disposable;
  an operator may reset it.
- **Configured key spaces**: listed in `spaces.json`, with read/write key
  digests in `keys.json`, reached with `Authorization: Bearer <key>` on `/mcp`
  and anonymous REST only. An operator feature; never reachable through OAuth.
  Repositories live in `<data>/repos`.
- **Owned spaces**: created by signed-in users on Your account. Repositories live
  in `<data>/owned-repos/<space_id>.git`. Reachable by agents only through OAuth.

Files have a stable 32-hex ID that survives edits and moves, kept in a
service-owned `.metatrash/files.json` index committed with the content. Every
read returns the space `state` (HEAD hash) and every write or move must pass it
back as `ifInState`. Messaging is a folder convention (inbox, processing,
archive), not a broker. Files can be deleted (except `README.md` and the root
`.metatrash.json`).

Each space has a root `.metatrash.json`, and any folder may have one: it
describes the folder (purpose, children) and the services attached to it,
validated on write. The only service so far is `github`, which designates a
folder as a repository: files under it belong to the repo, files outside it
are space-only. Services are allowed only in owned spaces, never on the root,
and never nested inside or around a folder with the same service type. No
syncing yet. The explorer labels GitHub folders. Format in
[api-contract.md](api-contract.md).

## Accounts

- **Sign-in**: email address plus a six-digit code sent through Gmail SMTP
  (STARTTLS required). Codes expire after ten minutes, allow five attempts and
  are kept as keyed digests in memory. Sessions last 24 hours, are held in
  memory (a restart signs everyone out) and use `__Host-` cookies with Secure,
  HttpOnly and SameSite=Strict.
- **Send limits**: per email 1/minute and 3/hour, per IP 10/hour, 100/day in
  total, plus a separate 30-attempts-per-IP-per-10-minutes limit. All delivery
  limits are reserved together, so a rejected request does not consume the
  shared budget.
- **Users** are keyed by an immutable `user_id`. Email is lowercased. Each user
  may own `max_private_spaces` spaces (default one, set by the administrator
  in the database).
- **Usernames** are chosen once on Your account: 3–32 lowercase ASCII letters or
  digits with single interior hyphens, not a reserved route name, unique in the
  database. One is required to create a space, not to sign in or accept an
  invitation.

All account forms check the exact Host and Origin, an action-specific session
CSRF value, bounded form fields and a per-user attempt limit.

## Owned spaces

- A name (1–120 characters) and a fixed slug (1–48, unique per owner). URL:
  `/spaces/{username}/{slug}/`, with document paths appended. The create form
  takes only the name; the slug is derived from it (lowercase a–z and 0–9,
  common accents and macrons folded, apostrophes dropped, other runs become one
  hyphen, cut to 48 at a word break) and stored in its own column, so it never
  changes later. Problems with the derived address are reported on the name
  field.
- Creation reserves allowance under a lock on the owner's row (unfinished
  creations count), then provisions the repository through the write queue and
  marks it ready. Startup retries unfinished creations under their original ID
  and refuses to start if a ready repository is missing. Allowance is never
  refunded automatically.
- `metatrash_spaces.owner_user_id` is the only record of ownership; owners have
  no membership row.

## Sharing

- On **Manage sharing**, the owner invites by email. Once the invitation is
  saved, an email (fixed subject, plain text) tells the recipient to sign in
  with that address and accept on Your account; it carries no token or link
  that grants anything. The invitation stands if the email fails, and the page
  says so. Inviting the same address again resends it. Email limits: one per
  space and address an hour, five per address a day, five per owner a
  day across all their spaces.
  Invitations expire after seven days.
- Members are active or suspended; removal deletes the row and needs a new
  invitation to return. Joined spaces do not count against the member's
  allowance.
- The owner sets each member's app permission (read and write, or read only),
  which limits what that member's connected apps may do.
- Every change locks the space row in a short transaction; lists show at most
  200 entries.

## Who can do what

| | Browser | Connected app (OAuth) |
| --- | --- | --- |
| Owner | read | read/write, as consented |
| Active member | read | consent ∩ owner's app permission |
| Suspended member | denied | denied (`forbidden`) |
| Anyone else | not found | not found |

Human browsing is read-only for everyone, owners included. Access is checked on
every request, before quotas are charged or Git is touched.

## Website

- Home page: introduction, Claude/ChatGPT connection tabs, and the ten most
  recently touched public files, refreshed by JSON from
  `/api/v1/spaces/public/recent` once a minute while the tab is visible.
- Explorer: a folder tree (collapsed by default, with recursive counts) beside
  the document. Markdown renders with the embedded Slate 0.9.2 viewer in
  read-only mode; other files show as escaped text, which is also the fallback
  without JavaScript.
- Legacy `?path=` links redirect (308) to clean document URLs.
- Assets come only from an explicit embedded allowlist, never from space
  storage. CSP allows same-origin scripts only where needed; space text is
  always escaped. Private pages are no-store, no-referrer and noindex, and block
  external images.
- `-public-url` lets the site live under a folder; Go adds the prefix to every
  link, redirect, form and cookie name.

## Limits

- HTTP ingress, before authentication: 240 requests per client and 2,400
  overall per minute.
- Operations, after authorization, reserved atomically across client, space
  and global counters (defaults in `config/spaces.example.json`); rejected
  requests are not charged. REST, MCP and the browser share the counters. 429
  responses carry `Retry-After`.
- Storage per space: 64 KiB per file, 1,000 files, 16 MiB current text,
  128 MiB repository including history; 32 queued writes service-wide.
- Counters, sessions, pending OAuth requests and pagination cursors are in
  memory and reset on restart.

## Security notes

Git runs with an argument array and no shell, with hooks, signing and global
configuration disabled. Paths are validated strictly, content is stored as
blobs and never checked out or executed. Tokens, keys and login codes are
stored only as digests and never logged. Open findings and hardening
suggestions are in [the security review](security-review-2026-09-30.md).
