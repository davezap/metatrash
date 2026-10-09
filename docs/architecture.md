# Architecture

How Metatrash 0.16.0 works. The wire contract is in [api-contract.md](api-contract.md),
agent sign-in in [oauth.md](oauth.md), the GitHub App in [github.md](github.md)
(0.17.0), installation in [deployment.md](deployment.md).

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
| Website (read-only) | `/`, `/spaces/public/…`, spaces their owner made readable on the web, `/docs/…` (the configured docs space, while readable on the web) | `/spaces/{username}/{slug}/…`, `/account` |

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
service-owned `.metatrash/files.json` index committed with the content (GitHub
baselines sit beside it in `.metatrash/github.json`). Every
read returns the space `state` (HEAD hash) and every write or move must pass it
back as `ifInState`. Messaging is a folder convention (inbox, processing,
archive), not a broker. Files can be deleted (except `README.md` and the root
`.metatrash.json`).

Each space has a root `.metatrash.json`, and any folder may have one: it
describes the folder (purpose, children) and the services attached to it,
validated on write. The only service so far is `github`, which designates a
folder as a repository: files under it belong to the repo, files outside it
are space-only. Services are allowed only in owned spaces, never on the root,
and never nested inside or around a folder with the same service type. Agents
sync a GitHub folder with `pull`, `pending` and `push` (see
[github.md](github.md)). The explorer labels GitHub folders. Format in
[api-contract.md](api-contract.md).

## Accounts

- **Sign-in**: email address plus a six-digit code sent through Gmail SMTP
  (STARTTLS required). Codes expire after ten minutes, allow five attempts and
  are kept as keyed digests in memory. Sessions last 24 hours and are kept
  in memory and in `metatrash_sessions` (SHA-256 of the token, never the
  token), so restarts keep people signed in (0.31.0); Security → Signed-in
  devices lists them with browser, sign-in address and last use, and signs
  out any one (0.32.0); they use `__Host-` cookies with Secure,
  HttpOnly and SameSite=Strict. Sign out everywhere else (0.27.0) ends the
  account's other sessions; the same happens
  after removing a passkey or the authenticator app, turning off email
  sign-in, changing the email address and signing in with a recovery code.
- **Passkeys** (0.22.0, `webauthn.go`, `signin_*.go`, `web/passkey.js`):
  WebAuthn with the standard library only. Sign-in needs no email
  (discoverable credentials; a button and the email field's autofill).
  Attestation `none`, user verification required, ES256, EdDSA and RS256,
  origin and RP ID checked, counters must move forward unless zero. Challenges
  are stateless, signed for one purpose, bound to the login cookie or the
  session, valid ten minutes and accepted once. Stored in
  `metatrash_passkeys` (schema v8), at most 20 per account.
- **Authenticator app** (0.24.0, `totp.go`, `qrcode.go`): TOTP (RFC 6238,
  SHA-1, six digits, 30 seconds, one step either side, each step accepted
  once). One per account, in `metatrash_totp`; the secret is sealed with
  AES-256-GCM under the `totpKeyFile` key, bound to the user ID. Setup shows a
  QR code drawn server-side as inline SVG (byte mode, level M, standard
  library) and the base32 key, and is finished by entering a code. Sign-in
  takes the email address and a code together; every failure, including an
  unknown address or an account without the app, gives the same answer, and
  each address (and each account when confirming) has five attempts per 15
  minutes.
- **Recovery codes and the email switch** (0.25.0, `recovery.go`): ten
  single-use codes per account (12 characters from a 31-letter alphabet,
  about 59 bits each), shown once and stored in `metatrash_recovery_codes` as
  SHA-256 salted with the user ID; creating new ones replaces the old. A code
  signs in with the email address, like the authenticator app (same answer
  for every failure, five attempts per address per 15 minutes), and the
  account is emailed how many are left. `metatrash_users.email_login` turns
  emailed codes off per account, allowed only with a passkey or authenticator
  app and unused recovery codes. While it is off, `/login/send` emails a
  notice instead of a code (the page looks the same), `/login/verify` and
  emailed confirmation refuse, and the last passkey or authenticator app
  cannot be removed (checked in the same transaction, under the user row's
  lock).
- **Confirm it's you** (step-up): each session records when it last proved
  who the user is. Adding or removing a sign-in method needs that within ten
  minutes; otherwise the user confirms with a passkey, the authenticator app
  or an emailed code (same mail limits as sign-in; not while email sign-in is
  off). Signing in with a recovery code counts as recent. Changes email the account
  a notice.
- **Change email address** (0.26.0, `email_change.go`): after confirming,
  a six-digit code goes to the new address and waits under the session (ten
  minutes, five tries); entering it updates `metatrash_users.email` under the
  user row's lock, drops codes waiting for either address, and emails a
  notice to both. An address that already has an account gets a notice
  instead of a code (same page), and the unique key refuses a late clash.
  Five changes started per account a day, plus the send limits below.
- **Send limits**: per email 1/minute and 3/hour, per IP 10/hour, 100/day in
  total, plus a separate 30-attempts-per-IP-per-10-minutes limit. All delivery
  limits are reserved together, so a rejected request does not consume the
  shared budget.
- **Bot checks** (`login_guard.go`, `web/login.js`): before a code is sent the
  form must leave a hidden honeypot field (`website`) empty and carry a proof
  of work: a nonce so that SHA-256(challenge ":" nonce) starts with 18 zero
  bits (a fraction of a second to a couple of seconds in a browser). The
  challenge is signed, bound to the browser's login cookie, valid for 20
  minutes and accepted once. Blocked requests count towards the attempt limit
  but not the mail budgets, and the message does not say which check failed.
  Without JavaScript no code can be sent.
- **Login log**: every `/login/send`, `/login/verify`, `/login/passkey`, `/login/totp` and `/login/recovery` request writes one
  journal line with IP, the full email address, whether an account uses it,
  both check results, the challenge age, the result and the user agent. No
  codes or cookies. Full addresses are in the journal by the owner's choice;
  they stay as long as journald keeps logs.
- **Users** are keyed by an immutable `user_id`. Email is lowercased and can
  be changed by the user (above). Each user
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
- **Transfer** (0.30.0, schema v9). The owner offers the space to an active
  member with a username (step-up required); one offer per space in
  `metatrash_space_transfers`, seven days, cancelled by the owner, declined
  by the member, and withdrawn if the member is suspended or removed. The
  member accepts from Spaces. Acceptance, in one transaction with the space
  row locked first and then the new owner's row: rechecks the offer, active
  membership, username, no own space at the slug, no alias of theirs at the
  slug for another space, and the allowance; moves `owner_user_id`; deletes
  the new owner's membership and inserts the old owner's (active,
  read_write); moves `member_user_id` on consent rows (NULL for the new
  owner, the old owner's ID for theirs) so later membership changes apply;
  records the old owner/slug in `metatrash_space_aliases`. The open
  repository's owner is updated after commit, so GitHub folders use the new
  owner's installations.
- **Old addresses** stay with the space: the website answers
  `/spaces/{old}/{slug}/…` with 301 to the current address for anyone who may
  see the space (others get what any private address gives), `/docs/` keeps
  serving a docs space configured by its old address, and agents may pass
  the old owner/slug (results use the current name). The old owner cannot
  create or accept another space at that slug. If the space comes back to
  them it takes its address back and that alias is deleted.

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
| Anyone, space readable on the web | read (website only) | not found |
| Anyone else | not found (signed out: sign-in page) | not found |

Only the owner can make a space readable on the web (Your account → My
spaces, `/account`), behind a warning, and make it private again; it changes the website
only. Human browsing is read-only for everyone, owners included. Access is checked on
every request, before quotas are charged or Git is touched.

## Website

- Home page: introduction, Claude/ChatGPT connection tabs, and the ten most
  recently touched public files, refreshed by JSON from
  `/api/v1/spaces/public/recent` once a minute while the tab is visible.
- Explorer: a folder tree (with recursive counts; the folders leading to the
  current file open) beside the document. Clicking a file fetches it and
  swaps only the document, with the address bar and history updated, so the
  tree keeps its open folders (`web/document.js`; whole pages without
  JavaScript or when a fetch fails). Markdown renders with the embedded Slate
  0.9.2 viewer in read-only mode, mounted once per page; other files show as
  escaped text, which is also the fallback without JavaScript.
- Legacy `?path=` links redirect (308) to clean document URLs.
- Assets come only from an explicit embedded allowlist, never from space
  storage. CSP allows same-origin scripts only where needed; space text is
  always escaped. Private pages (including spaces readable on the web) are
  no-store and no-referrer, and block external images.
- Search engines: the site's pages and all of `/docs/` are indexable. Under
  `/spaces/`, in spaces anyone can read (public, or readable on the web), only
  files in the space's top folder are; subfolder files, members-only pages,
  errors and redirects are `noindex, nofollow`.
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
- Counters, sign-in codes, pending OAuth requests and pagination cursors are
  in memory and reset on restart. Sessions survive restarts (0.31.0).

## Security notes

Git runs with an argument array and no shell, with hooks, signing and global
configuration disabled. Paths are validated strictly, content is stored as
blobs and never checked out or executed. Tokens, keys and login codes are
stored only as digests and never logged. Open findings and hardening
suggestions are in [the security review](security-review-2026-09-30.md).
