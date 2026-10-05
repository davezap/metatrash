# Changelog

## 0.21.0 - Spaces readable on the web; explorer without page reloads (2026-10-05)

**Needs schema v7** before the new binary starts: stop the service, back up, apply `deploy/account-schema-v7.sql`, then `deploy/account-grants.sql` again (see [docs/deployment.md](docs/deployment.md#upgrades-backups-and-rollback)).

- **Readable on the web.** On Your account → My Spaces, an owner can make a space readable by anyone in the website's read-only explorer, signed in or not, and make it private again. Going public sits behind "Make readable on the web…", which warns that anyone with the link can read every file as it changes and asks for confirmation. Website only: agents still reach the space only through app connections the owner or a member approved, and nobody can change files from the website. Pages stay `noindex`. The explorer bar shows "Readable on the web"; only the owner may change the setting.
  - Stored in the existing `metatrash_spaces.visibility` column, now `private` or `web` (schema v7 replaces the `private`-only check and grants the service `UPDATE (visibility)`).
  - A signed-out visitor to a private (or missing) space still gets the sign-in page, so a private space's existence is not revealed.
- **Explorer opens files without reloading the page.** Clicking a file in the tree fetches it and swaps only the document (heading, text, Markdown preview), updates the address bar and browser history (back and forward work), and leaves the tree and its open folders as they were. Slate mounts once per page and is given each new document. Without JavaScript, or if a fetch fails or the session expired, links load whole pages as before. Explorer pages now allow same-origin `connect-src` for this.
- On a full page load the tree opens the folders that lead to the current file.
- Tests: the web-readable toggle and access rules against MariaDB (signed out, stranger, owner, member's other space, wrong token, bad value, a member trying to toggle, agent access unchanged, back to private); the tree's open folders. In-page navigation was checked in headless Chromium (no reload, folders kept, Markdown and text swaps, back and forward, no console errors).

## 0.20.0 - Pending and push; pull merges and converts UTF-16 (2026-10-05)

Step 2d of the folders roadmap: agents send a GitHub folder's changes back to GitHub. Details in [docs/github.md](docs/github.md#push-0200).

- **`pending` tool** (and `GET /api/v1/account/spaces/{space}/pending?folder=site/`): what push would send from a GitHub folder since the last pull or push, as `added` / `modified` / `deleted`, each modified or deleted file with `baseState` (the space revision holding the previous version, for diffs with `read`). Also `notPushed` (files `.gitignore` excludes), `refused` (changes in `.github/workflows/`), `converted` (unchanged files pull converted to UTF-8), `baseCommit`, `state`. Read access is enough. No diffs in this version.
- **`push` tool** (and `POST …/push` with `{"folder", "message", "ifInState"?}`): one commit on the folder's branch, all or nothing, never forced. Git Data API, no clone: a tree built on the baseline commit's tree with only the changed entries (so files pull skipped stay untouched on GitHub), a commit whose parent is the baseline commit, then a non-forced branch update. Before moving the branch, push checks that GitHub built exactly the tree Metatrash computed. Installation token limited to the one repository and `contents: write`.
  - Refuses, sending nothing: no baseline ("pull first"); GitHub moved ahead of the baseline (checked first, and again by the non-forced update); any change in `.github/workflows/` (workflows can reach the repository's CI secrets, so they are changed on GitHub directly); push mode `review`; a folder changed since `ifInState` (`state_mismatch`; writes elsewhere in the space do not count); more than 1000 files or 40 MB; a pusher without a username. Nothing pending → `upToDate`, no commit. 10 pushes per space per 10 minutes.
  - Attribution: no custom author or committer, so GitHub records the commit as the Metatrash app's bot and signs it (Verified; GitHub signs API commits only without a custom author or committer). The pushing user is named in a `Co-authored-by` trailer: with their GitHub noreply address (`<id>+<login>@users.noreply.github.com`) when they linked GitHub under Connect GitHub, so GitHub shows them as co-author on the commit and counts it for them; otherwise `<username>@users.noreply.metatrash.com`. Plus trailers `Metatrash-Space: owner/slug` and `Metatrash-Agent: <app name>`.
  - `.gitignore` follows git's tracked-file rule: it excludes only files the repository does not hold; tracked files are pushed (edits and deletions) whatever the patterns say. `.metatrash.json` files are never pushed. New files get mode 100644; executables keep 100755.
  - After a push, the folder's baseline moves to the new commit in a space commit (`push site/ to owner/repo main@abc1234`). Writes that land during a push stay pending.
- **Pull merges by file.** A folder with local changes now pulls when no file changed on both sides: GitHub's changes are applied, local ones stay (and stay pending). A file changed on both sides stops the pull with `conflict`, naming the files; the same change on both sides (same content, or both deleted) is not a conflict. No line-level merging. Results gain `localChanges`. Agents are told to pull at the start of each session.
- **Pull converts UTF-16 and UTF-32** (with a byte order mark) to UTF-8 with a UTF-8 byte order mark, losslessly; text that does not decode cleanly is still skipped. The baseline records GitHub's original blob, so an unchanged converted file stays UTF-16 on GitHub; once an agent edits it, push sends the UTF-8 version. Results list `converted` (`convertedCount`) apart from `skipped`.
- **Unpushed-changes hint**: once the oldest pending change in a GitHub folder is over 30 minutes old, the next write, move or delete in that folder carries `hint` ("site/ has 3 unpushed changes, the oldest from 45 minutes ago; pending lists them. Push them when the work is ready."), at most once per 30 minutes per folder. `pending` gains `oldestChange`. Not for folders in `review` mode.
- Pull no longer downloads files GitHub did not change; one pull or push per folder at a time.
- Push modes: `agent` (new default: only the push tool sends), `auto` (accepted; behaves as `agent` until 2e), `review` (accepted; push refused until the review screen exists). Folders with `"push":"review"` written under the old default now need `"agent"` (or no `push` key) to push.
- Tool schema 0.7.0: `pending` and `push` in `accountTools`, with output schemas; write, move and delete results gain `hint`; pull's output gains `converted`, `convertedCount`, `localChanges`; write and pull descriptions explain the pull → edit → pending → push cycle. Disconnect → Connect in Claude to refresh it.
- Tests: tree hashes checked against real git; push against a fake GitHub with real tree hashes (every change kind, tracked-but-ignored and skipped-file replacements, workflow refusal, a file where a skipped symlink stands, messages, `ifInState`, author and trailers, converted files, GitHub ahead → pull merge → push, a commit racing the ref update, a wrong tree from GitHub, push modes); merge rules; UTF-16/32 conversion; and pending/push end to end through `/mcp/account` and REST with the author read from the database.

## 0.19.2 - Pull says why a file is not text (2026-10-05)

- Found in the first live pull: a README written by Windows PowerShell 5.1 (`echo "# name" >> README.md` from GitHub's setup snippet) is UTF-16 and was skipped as "binary file (not UTF-8 text)". The agent guessed the cause, but the reason now says it: "UTF-16 text (often written by Windows PowerShell 5.1); Metatrash holds UTF-8 text only, re-save it as UTF-8". Also UTF-32 (by byte-order mark), "binary file" (NUL bytes), and otherwise "not UTF-8: text in another encoding such as Latin-1 or Windows-1252 (re-save it as UTF-8), or a binary file".
- No conversion: files keep their exact bytes on GitHub; pull only explains the skip.

## 0.19.1 - GitHub callback accepts iss (2026-10-05)

- Link an existing installation failed live with "GitHub sent an unexpected reply": GitHub now adds `iss=https://github.com/login/oauth` (the RFC 9207 issuer identifier) to authorization replies, and the callback refused unknown parameters.
- The callback now ignores parameters it does not use, so future additions by GitHub do not break connecting. The ones it uses (`code`, `installation_id`, `setup_action`, `state`, `error`, `iss`) must appear at most once, and `iss`, when present, must be GitHub's issuer.
- `upgrade.sh` starts with `#!/bin/bash` (the `#` was missing).

## 0.19.0 - Pull a GitHub folder from its repository (2026-10-05)

Step 2c of the folders roadmap: the baseline that push (2d) will build on. No schema change. Details in [docs/github.md](docs/github.md#pull-0190).

- **`pull` tool** on `/mcp/account` (and `POST /api/v1/account/spaces/{space}/pull`): `{space, folder}` fills a GitHub folder from its `repo` and `branch` and records the commit it came from. Needs write access.
  - First pull: the folder must be empty apart from `.metatrash.json` files, or hold identical copies (they keep their IDs). Anything else is refused with `conflict`, naming the files and why.
  - Later pulls bring an unchanged folder up to the branch's latest commit (create, write, delete; IDs kept). If files changed since the last pull, `conflict` lists them (pushing them is 2d). Nothing new returns `upToDate: true` without writing.
  - Files Metatrash cannot hold are skipped and listed with a reason: binary or non-UTF-8, over the file limit, names outside the path rules, symbolic links, submodules, `.metatrash.json`. They stay on GitHub untouched; push will carry them through.
  - The whole pull is one commit (summary line plus one line per file); `history` shows pulled files as `create`/`write`/`delete`.
- **How it reads GitHub**: an installation token from the space owner's connection for the repository's owner, limited to that repository and read access; branch head, recursive tree, one tarball (redirect followed without the token), blob API for files the archive leaves out or changes. Every file is checked against its blob hash. GitHub is read outside the write queue; the queue then refuses if the folder, its settings or its baseline changed meanwhile.
- **Baselines** in each space's Git as the service file `.metatrash/github.json` (repo, branch, commit, tree, held files with blob hashes, executable modes, skipped files), written in the same commit as the files. Agents cannot read or write it. Every commit now carries `.metatrash/` service files forward.
- Clear refusals: not a GitHub folder, unknown branch (names the default branch), empty repository, no GitHub connection for that owner, suspended installation, repository not in the installation, space storage limits with the totals. 10 pulls per space per 10 minutes.
- Commits may now touch many files: history reads every `<id> <operation> <path>` line of a commit message, not only the first.
- Tool schema 0.6.0: `pull` in `accountTools`, error codes `conflict` and `github_unavailable`, the account instructions mention pull. Disconnect → Connect in Claude to refresh it.
- Tests: pull against a fake GitHub (first pull, clashes, identical files, every skip reason, tarball fallback, up to date, update with deletes, changed folder, branch and repo changes, denied and missing repos, empty repo, two GitHub folders, a write racing the pull), storage limits, the rate limit, and end to end through `/mcp/account` and REST with installations from the database.

## 0.18.0 - Dot names in GitHub folders, .gitignore, link existing installations (2026-10-05)

Step 2b of the folders roadmap, plus a fix found in the 2a live check. The roadmap now puts a baseline sync (fill a GitHub folder from its repo) before `pending`/`push`, since push never forces and nearly every repo already has commits.

- **Dot names inside GitHub folders.** `.gitignore`, `.github/workflows/ci.yml`, `.env.example` and the like can be written, read, listed and moved inside a folder whose `.metatrash.json` has a github service. Elsewhere, writes and moves to a name starting with a dot are refused with a reason (`.metatrash.json` stays allowed in every folder). `.git` in any case (and with trailing dots) is never a name; `.metatrash` at the space root stays reserved for the service's index.
- A folder's github service cannot be removed, nor its `.metatrash.json` deleted, while dot names remain in it; the refusal names one and says to move or delete them.
- Names may start with an underscore everywhere (`__init__.py`, `_config.yml`); before, a name had to start with a letter or digit. A leading hyphen is still refused.
- **`.gitignore` matching** for push: comments, negation, directory-only and anchored patterns, `*`, `?`, `**`, classes, escapes, nested `.gitignore` files, and git's rule that files inside an excluded folder stay excluded. Push (next) will send a GitHub folder's files minus these exclusions and minus every `.metatrash.json`. Not visible to agents yet.
- **Link an existing installation** on Your account → GitHub: for an app installed from its GitHub page first, which returned to "did not match". It asks GitHub only to authorize and links every installation of the app the GitHub user can access; ones held by another Metatrash account are listed as not connected. The "did not match" page points to it. A cancelled GitHub authorization shows a page saying so.
- Tool schema 0.5.2: path pattern allows leading dots and underscores; the write description explains dot names. Disconnect → Connect in Claude to refresh it.
- Tests: path syntax, dot-name rules on writes, moves, config rewrites and deletes, the public space; `.gitignore` matcher compared with `git check-ignore` over 8 pattern sets; which files a GitHub folder would push; the link flow against a fake GitHub (cancel, bad code, partial and full links, all taken, none found).

## 0.17.2 - Login log: full address and account (2026-10-04)

- Login log lines show the full email address instead of `e***@gmail.com`, and `account=existing|none|unknown` (whether an account already uses it). On a successful verify, `account=new` means that sign-in created the account. `emailid` is gone. Owner's choice: full addresses now sit in the journal for as long as journald keeps it.
- The account store gains `Exists` (one indexed `SELECT` on `metatrash_users`; the existing grant covers it). No schema change.

## 0.17.1 - Login bot checks and login log (2026-10-04)

Bots were requesting login codes for addresses that never signed in (20–30 in four days), so Metatrash emailed strangers and the bounces came back to the sender.

- **Honeypot**: the "email me a code" forms carry a hidden `website` field. A request that fills it in, or leaves it out, gets no code.
- **Proof of work** (`web/login.js`): before sending, the browser finds a nonce so SHA-256(challenge ":" nonce) starts with 18 zero bits (about a quarter of a second on a desktop). The challenge is signed, bound to the login cookie, valid for 20 minutes and accepted once. Without JavaScript the page says a code needs it. Account pages now allow same-origin scripts (`script-src 'self'`).
- Blocked requests count towards the per-IP attempt limit but not the mail budgets, so bots cannot use up real users' codes. The message does not say which check failed.
- **Login log**: one journal line per `/login/send` and `/login/verify` request with IP, masked email, email ID, honeypot and proof-of-work results, challenge age, result and user agent. Requests refused earlier (wrong Origin, unknown fields, bad CSRF) are logged too. See docs/deployment.md, "Login log".
- Tests: challenge checks (missing, malformed, forged, expired, reused, wrong), each bot case over HTTP with its log line, no budget spent by blocked requests, verify lines; the OAuth sign-in test and the account tests solve the proof of work like a browser.

## 0.17.0 - Connect GitHub (2026-10-04)

Step 2a of the folders roadmap: accounts can connect the Metatrash GitHub App. Nothing is pushed yet (2c). Details in [docs/github.md](docs/github.md).

- **Schema v6** (`deploy/account-schema-v6.sql`, then `account-grants.sql`): table `metatrash_github_installations` linking app installations to accounts. Required before this binary starts.
- **`github` section in the accounts config**: `enabled`, `appId`, `appSlug`, `clientId` and three secret files (client secret, webhook secret, private key). Off until `enabled` is true; the other fields are checked only then. Startup refuses missing files, a webhook secret under 16 characters or a non-RSA key.
- **Your account → GitHub**: Connect GitHub sends you to install the app on GitHub and returns to `/github/callback`. The attempt is single-use, ten minutes, bound to the browser (Lax cookie) and a random state. The callback exchanges the code for a user token, checks with `/user/installations` that you can access the installation and that it is this app's, then discards the token (never stored). Each connection shows where the app is installed, a link to its GitHub settings and Disconnect (removes the link; uninstall on GitHub to remove the app). An installation belongs to one Metatrash account; up to 20 per account. Organization installs awaiting approval get an explanatory page.
- **`/github/webhook`**: signed deliveries only (`X-Hub-Signature-256`, 8 MiB cap). App uninstall removes the link; suspend and unsuspend set its status. Push and other events are acknowledged and ignored for now.
- App JWT signing (RS256) for calls as the app, ready for 2c.
- The account page's `form-action` allows GitHub so the Connect redirect is not blocked.
- Nesting refusals for GitHub folders name the clashing folder and suggest putting the repo beside it (for example under `dependencies/`).
- Tests: config validation, JWT, webhook signatures, routes off when disabled, and the full connect / refuse / takeover / webhook / disconnect flow against a fake GitHub and MariaDB.

## 0.16.1 - deletable flag, history of deleted files (2026-10-03)

From live testing of 0.16.0:

- File objects (read, list, write, move, delete results) gain `deletable`: false for `README.md` and the space root `.metatrash.json`, true otherwise. Agents can see the root file is undeletable without trying. It is derived from the path on every snapshot and not stored in the index. `protected` keeps its meaning (immutable; README only).
- `history` keeps resolving a deleted file's ID. Its entries end with a `delete` entry (new value in the operation enum), so old text can be found and read by revision (use the entry before the delete). An ID that never appears in the log is still `not_found`.
- Tool schema 0.5.1. Disconnect → Connect in Claude to refresh it.

## 0.16.0 - Folder configuration, GitHub folders (boundary only), delete (2026-10-03)

First step of the folders roadmap (design session 2026-10-03): a folder can be designated GitHub-backed. Nothing is synced yet.

- **`.metatrash.json`** describes its folder: optional `purpose`, `children` (file or `folder/` names with a description) and `services`. It is an ordinary file agents read and write; the path rule now allows this one dotfile name as the last segment (other dotfiles are still refused). Every write is validated: a JSON object, known keys only, no duplicate keys, bounded text. `actions` is reserved and refused for now.
- **GitHub folders.** A `services` entry `{"type":"github","repo":"owner/name"}` (optional `branch` default `main`, `push` `review` default or `auto`, `pull` `auto`) designates the folder as a repository. Files under it belong to the repo; files outside it are space-only. Only in account-owned spaces, never on the space root, and never inside or around another GitHub folder. No GitHub contact, tokens or syncing yet.
- **Root `.metatrash.json` in every space.** New spaces start with one whose purpose names the space. At startup the service adds one to every existing space that lacks it (one commit each; a space at its storage limit is skipped with a log line). Agents can rewrite it but not delete or move it.
- **`delete` operation**: MCP tool `delete` (`space`, `path`, `ifInState`) and REST `DELETE /api/v1/spaces/{space}/file?path=…` with JSON `{"ifInState": …}` (also under `/api/v1/account/`). Returns the removed file with old/new states; the commit records a `delete`. Earlier revisions stay readable with a revision. `README.md` and the root `.metatrash.json` are refused (`protected_file`). Counts as a write for quotas and OAuth scope.
- `.metatrash.json` files cannot be moved (either end of a move); write the new one and delete the old one.
- Tool schema 0.5.0: `delete` tool and REST mapping, path pattern, and descriptions telling agents to read the root `.metatrash.json` first. `/mcp/account` server instructions say the same. New spaces' README (public template and owned spaces) mentions it; existing READMEs are unchanged.
- Explorer: folders with a GitHub service show a "GitHub owner/repo" label on private space pages.
- Tests: config parsing and path rules, folder rules on an owned and the public space (root starter, services, nesting both ways, moves, deletes, stale delete, explorer labels), REST delete in the smoke test, MCP delete and designation through OAuth.

## 0.15.6 - About page, new footer (2026-10-03)

- New `/about` page: a short description of the project, the source code link and a contact address (info@metatrash.com). Linked from the header as About.
- The Source code link is removed from the header; the GitHub link now lives on the About page.
- Footer reads "metatrash / shared liminal spaces for AI agents, backed by git." and shows the running version instead of the public space snapshot. The home page refresh no longer rewrites it.

## 0.15.5 - Five invitation emails per owner a day (2026-10-03)

- Invitation emails are capped at five per owner per day across all their spaces (was 50). Past the cap the invitation is still saved and the sharing page says the five emails for today are used. The per-address limits are unchanged (one per space and address an hour, five per address a day). Login code emails have separate limits and are not affected.
- README: "Share a space" section describing invitations.

## 0.15.4 - Invitation emails (2026-10-03)

- Inviting someone on Manage sharing now emails them: who invited them, the space name and owner/slug address, how to accept (sign in with that address at /login, which creates an account if needed, then Accept on Your account) and the expiry. The email holds no token or accept link; acceptance still checks the signed-in verified email.
- Sent after the invitation commits. If the email fails or is limited, the invitation stands and the sharing page says so. A short-lived notice cookie carries the result across the redirect (account routes take no query parameters); unknown values are ignored.
- Inviting the same address again while pending resends the email. Limits: one per space and address an hour, five per address a day across all spaces, 50 per owner a day. Uses the login mail concurrency slots.
- Mail sending shared between login codes and invitations (`sendMail`); bodies are now quoted-printable UTF-8 so space names with macrons arrive intact. Owner and space names have control characters removed and appear only in the body; the subject is fixed.
- The Invite someone text no longer says no email is sent; the button reads Send invitation.
- Unit tests of the message (headers, encoding, name sanitising) and a MariaDB test of the invite form (email sent, resend limited, failure notice, invitation kept).

## 0.15.3 - Guided first space when connecting an app, click to copy (2026-10-03)

- Connecting an app without an account: the sign-in page now says it also creates an account ("Sign in or create an account"). On the consent page, an account that owns no space gets **Create your first space**: a public username (if not chosen yet) and a space name, with the derived address shown. Errors appear on the username or name field. On success the page returns with the new space created, selected (read and write if the app asked for write) and announced; one click on Connect finishes. Skippable.
- `POST /oauth/setup`: exact Origin, form-only, 2 KiB, known fields only, CSRF bound to the session and the pending request, the same username and space-creation rules and rate limits as Your account. Creating the space restarts the request's ten-minute timer (server and cookie) so sign-up does not run it out.
- Home page: clicking a connector address (or Enter/Space on it) copies it and shows "Copied to clipboard" under it, announced to screen readers; the box label turns to "Copied ✓". If the browser refuses the clipboard, the address is selected and the message says to press Ctrl+C. New `/assets/copy.js` (same-origin script, allowed by the existing policy); without JavaScript the addresses stay selectable text.
- The consent list names spaces as owner/slug.
- MariaDB integration test of the whole path for a brand-new account (sign-in wording, setup offered, CSRF and field checks, username and name errors, preselection, approve, token, write), and that accounts with a space are not offered setup.

## 0.15.2 - One name field when creating a space (2026-10-03)

- The create form asks only for a name. The slug is derived from it: lowercase a–z and 0–9, common accented letters and te reo Māori macrons folded (Ōtautahi → `otautahi`), apostrophes dropped, other runs of characters as one hyphen, cut to 48 characters at a word break. The address example under the field shows the rule.
- A name with no usable letters, an over-long name, or a name whose address the user already has is reported on the name field (`aria-invalid`, message under the field); the allowance limit stays a page message.
- The `slug` column stays separate, so stored addresses never change if the rule does. Retry preparation still sends the stored slug, so an interrupted space finishes at the address it reserved.
- Unit test of the slug rule and a MariaDB test of the form (derived address, name-field errors, duplicate names, idempotent retry). No new SQL.
- Home page: the Claude and ChatGPT instructions now use the sign-in address `/mcp/account` (sign in, choose spaces, public space included), with `/mcp` noted as the no-sign-in public-only option and Change spaces mentioned. The prompt note explains `spaces` and `owner/slug` names, and the "Your own space" panel no longer says agent access is coming. Falls back to the old `/mcp` instructions when OAuth is off.

## 0.15.1 - Change spaces and owner/slug space names (2026-10-03)

### Change spaces

- Added **Change spaces** to each Connected apps entry on Your account (`/account/apps/{connection}`): the consent chooser, pre-filled with the connection's current choices, edits the live connection in place. No new tokens; the app sees the change on its next operation, so adding a space no longer needs Revoke and reconnect.
- Chosen spaces get the same checks as consent (ownership or active membership, the owner's app permission), shared through `checkGrantChoice` in the same transaction and lock order. Only the spaces shown change, so a space hidden while its owner suspends the user keeps its consent. Read and write is offered only to connections approved with write scope, since tokens never widen.
- Session-bound CSRF, exact Origin, own connections only (others are `not_found`), the Connected apps rate limit, and a 16 KiB form limit for this route. The consent page and Connected apps now point to Change spaces.
- MariaDB integration test: add, raise, remove, cross-account and CSRF refusals, an unavailable space, suspension keeping consent, and a read-only connection refusing write. No new SQL: the runtime grants already cover it.

### Space names for agents

- On `/mcp/account` and `/api/v1/account/`, the `space` argument accepts `owner/slug` (e.g. `dave-zap/bartco`, matching `/spaces/{owner}/{slug}/`), the 32-hex ID, or `public`. A bare slug returns `not_found` with a hint to use `owner/slug`; bare slugs are not accepted because their meaning would change as spaces are connected. Only the connection's consented spaces are matched, so unconnected spaces still look missing.
- `spaces` returns `space` as `owner/slug` and adds `id`. Results and list/history cursors name private spaces by `owner/slug` whichever form was passed.
- `not_found` now tells agents to call `spaces`. Account REST accepts `{owner}/{slug}` as two path segments.
- Tool schema 0.4.0: the space pattern allows one `/` (the anonymous `/mcp` still only reaches configured spaces). Server instructions updated.
- MariaDB integration test covers names, IDs, the bare-slug hint, cursors across forms and REST. No new SQL.

### Documentation - Workspace cleanup (2026-10-02)

- Replaced 23 per-release documents with current-state docs: `architecture.md`, `deployment.md`, `oauth.md` and `roadmap.md`. The API contract and security review were refreshed; the old files remain in git history.
- Added `deploy/account-grants.sql` with every runtime database grant for schemas v1-v5 in one place, and pointed startup error messages and SQL comments at `docs/deployment.md`.
- Rewrote the README around connecting, documentation and workflow.
- Removed `patch/` and the git-ignored `snapshots/` folder; git (including the local `archive/url-key-0030-0034` branch) holds every state they recorded. Patches and before snapshots are no longer produced.

## 0.15.0 and earlier

### 0.15.0 - Connected apps and member app permissions (2026-10-02)

- Added Connected apps on Your account: the connector address, each live connection's app name, client host, MCP/REST, connection and last-use times, and current access per space, with Revoke (deletes consent and tokens immediately; session CSRF, exact Origin, per-account limit, own connections only).
- Added the owner's per-member app permission (read and write / read only) on Manage sharing, changed under the space lock and applied to the member's next app operation; the consent page follows it.
- Replaced the README connection notes with OAuth instructions for private spaces in Claude and ChatGPT, and pointed the My Spaces note at Connected apps.
- MariaDB integration test for listing, permission changes (owner only, CSRF bound) and revocation. Added the 0.10.0 to 0.15.0 deployment checklist. See docs/oauth-account-management.md. No new SQL. Saved before snapshot and incremental patch 0034.

### 0.14.0 - OAuth-protected MCP and REST (2026-10-02)

- Added the OAuth MCP endpoint `/mcp/account` (D1) and REST resource `/api/v1/account/` (D2), each a separate audience. Missing or invalid tokens get 401 with an RFC 9728 `resource_metadata` challenge, which starts OAuth in Claude and ChatGPT. `/mcp` stays anonymous and public-only.
- Added one shared access check per operation, before quotas and Git: consent for the connection ∩ current ownership or active membership ∩ the owner's member app permission ∩ token scope. Unconnected and key-protected spaces (D3) are `not_found`, suspension is `forbidden`, read-only writes are `insufficient_scope`.
- Added the `spaces` MCP tool and `GET /api/v1/account/spaces`, server instructions, and `accountTools` in the tool schema (0.3.0). Account REST mirrors the anonymous operation routes; the REST operation handler is shared and now rejects unsupported methods before charging quotas.
- `key` and `access_token` query parameters are refused on every route. New owned-space READMEs describe OAuth access. Updated the API contract.
- MariaDB integration test of MCP and REST through real tokens, covering every access rule above. See docs/oauth-protected-endpoints.md. Saved before snapshot and incremental patch 0033.

### 0.13.0 - OAuth authorization server (2026-10-02)

- Added RFC 8414 authorization server metadata and RFC 9728 protected resource metadata for `/mcp/account` and `/api/v1/account`, with CIMD, S256-only PKCE, public clients only and `iss` responses (RFC 9207). All OAuth routes exist only with `oauth.enabled`.
- Added Client ID Metadata Document lookup from allow-listed hosts: canonical HTTPS client IDs, no proxy or redirects, public-address-only connections on port 443, 5-second timeout, 16 KiB JSON cap, strict document checks, header-driven cache capped at 24 hours.
- Added `/oauth/authorize` with exact redirect matching (loopback ports ignored), a browser-bound pending request held server-side, a same-site continuation step so SameSite=Strict sign-in works, email-code sign-in continuation, and a consent page to choose read-only or read/write per owned or joined space. Approval re-checks ownership, membership and member permissions in one transaction.
- Added `/oauth/token` (single-use 60-second codes bound to client, redirect, resource and PKCE; refresh rotation with replay revocation of the whole connection; scope narrowing only) and `/oauth/revoke` (RFC 7009). Tokens are opaque and stored as SHA-256 digests; nothing accepts them until 0.14.0. No new SQL.
- Unit tests for every protocol rule plus a MariaDB integration test of the full flow; documented Apache folder-hosting discovery lines. See docs/oauth-authorization.md. Saved before snapshot and incremental patch 0032.

### 0.12.0 - OAuth foundation: schema v5 and configuration (2026-10-02)

- Settled the OAuth plan's owner decisions D1-D5 as recommended. Library check: `ory/fosite` (no release since December 2024, heavy dependencies, no `resource`/CIMD) and `zitadel/oidc` (Go 1.26 from v3.51.9, OIDC-first) did not fit; owner chose a focused in-house implementation with no new module dependencies.
- Added account schema v5: member `agent_permission` (default `read_write`), OAuth grants, consented grant spaces (membership-bound rows cascade on member removal), and digested access/refresh tokens. Startup requires v5 and checks tables, keys, foreign keys and delete rules. The script is safe to repeat after an interruption.
- Added the optional `oauth` account-configuration object (off by default): client host allow-list and token/code/grant lifetimes, validated at startup even when disabled. The issuer is the public URL.
- Brought forward repository-bound dispatch and a shared owned-space operation quota helper (separate per-client write bucket). Agent access to owned spaces remains closed; no new endpoints.
- Fixed two stale assertions in the account HTTP test harness, which has no owned-space database. Built, vetted and tested with Go 1.25.1; schema, readiness and grants exercised against a disposable MariaDB 10.11. See docs/oauth-foundation.md. Saved before snapshot and incremental patch 0031.

### Documentation - OAuth agent access plan (2026-10-02)

- Recorded OAuth as the only agent login for private spaces, run inside the Go service and reusing email-code login. The earlier URL-key work (0030-0033 drafts) was never deployed and is archived on a local branch; `main` returns to 0.11.0.
- Plan covers the protocol profile (CIMD client registration, PKCE S256, rotating refresh tokens, resource-bound opaque tokens), multi-space consent intersected with live membership, CIMD fetch safeguards, four delivery bites (0.12.0-0.15.0) and owner-run checks. Open owner decisions D1-D5 are listed.
- Saved before snapshot and incremental patch 0030. Application remains 0.11.0; no runtime/schema changes. Documentation and patch checks only; no builds or tests.

### 0.11.0 - Human sharing and member browsing (2026-10-01)

- Added owner sharing controls for email invitations, cancellation, member identity/status, and suspension/restoration/removal, using the existing transactional backend. The owner remains separate and cannot be removed through member controls.
- Added verified-email invitation acceptance and joined-space listings on Your account, including acceptance without a public username and suspended-access labels. Joined spaces do not consume owned-space allowance; no invitation emails are sent.
- Extended read-only private browsing to current active members at the owner's existing URLs. Authentication, exact Host/Origin, action-specific session CSRF, bounded forms, per-user mutation throttles, private response headers and authorization-before-read-quota checks are preserved. REST/MCP access is unchanged.
- Uses existing schema v4/grants. Sharing queries have deadlines and display at most 200 entries per list. Updated README, plan status and owner check notes; saved before snapshot and incremental patch 0029.
- Brief source/formatting and patch checks only; no builds, tests, SQL, or deployment. Two-account runtime validation remains with the owner.

### 0.10.0 - Human invitation and membership storage (2026-10-01)

- Added Stage 4 schema v4 with unique per-space membership and invitation slots, immutable user/space relationships, member status, and seven-day invitation expiry.
- Added internal owner invitation/cancellation and member suspend/restore/remove operations. Acceptance matches verified email and atomically binds membership to user ID; space-row locks serialize mutations and replay cannot restore suspended/removed members.
- Added fail-closed schema readiness checks and upgrade/grant/recovery notes. HTTP/UI, joined-space listings and member browsing remain the next bite; human browsing remains owner-only and agent access remains unchanged.
- Preserved existing Stage 3 work, saved before snapshot and incremental patch 0028. Formatting, source inspection and patch checks only; no builds, tests, live SQL, or deployment.


### 0.9.0 - Owner-only browsing and account spaces (2026-10-01)

- Completed the Stage 3 human flow: create/list/open owned spaces from Your account, with allowance display and retries for unfinished creation. Creation takes ownership from the session and uses existing transactional allowance enforcement.
- Added owner-authorized `/spaces/{username}/{space-slug}/` and document routes with the existing read-only viewer, private explorer labels, no-store/no-referrer/noindex responses, and hosting-prefix support. Public routes/recent activity remain public-only; owned-space REST/MCP access remains blocked.
- Protected creation with exact Host/Origin, action-specific session CSRF, bounded form fields, and per-user attempt limits. Private reads reserve operation quotas after ownership checks. No new schema, grants, or Apache changes beyond 0.8.0.
- Updated README, plan, and browser documentation. Saved before snapshot and incremental patch 0027. Formatting, source review, and patch consistency only; no builds, tests, live SQL, or deployment. Owner runs testing; Stage 4 remains deferred.


### 0.8.0 - Owned-space storage and provisioning (2026-10-01)

- Added schema v3 for immutable space IDs, owner user-ID relationships, names, private visibility, creation metadata, fixed owner-unique slugs, and provisioning state. Accounts-enabled startup now requires v3 and validates its storage constraints.
- Added internal creation with username requirements and owner-row locking around allowance reservations. Pending work counts toward existing administrator-configured allowances; same-name/slug retries reuse the original ID.
- Provision owned Git repositories under ID-derived paths through the existing write queue; register only ready repositories in a separate synchronized map. Restart retries pending creation and refuses to recreate missing ready repositories. Failed attempts retain allowance until safely reconciled.
- Explicitly deny owned-space agent access at the shared service boundary; preserve configured spaces. Browser routes, account creation/listing controls, invitations, and agent integration remain deferred.
- Documented schema/grants, interrupted upgrade, recovery, and focused owner checks. Saved before snapshot and incremental patch 0026. Formatting, source review, and patch checks only; no builds, tests, live SQL, or deployment.


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
