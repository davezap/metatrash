# Roadmap

Open work as of 0.55.0 (2026-10-25). Done work is in [CHANGELOG.md](../CHANGELOG.md).

## Fewer tokens per change

`append`, blobs with `ifBlob` reads, MCP `list since` and shorter tool
descriptions shipped in 0.52.0. All were checked live through Claude Code on
2026-10-24 (append's cases are listed under 0.52.1 in CHANGELOG.md) and work as
documented; 0.52.1 only made append's refusals clearer. What is left needs
measuring first:

- **Which result copy the model reads.** Every MCP result is sent as
  `structuredContent` and again as JSON text, as the MCP spec asks. If a
  client (Claude.ai, Claude Code, ChatGPT) puts both into the model's
  context, a read costs its text twice. Check each client with a known file
  before changing anything; if one does, consider a smaller text copy (for
  read, the file's text alone after a short header).
- **Output schemas.** Tool definitions are about 15 KB without output schemas
  and 35 KB with them. If clients pass output schemas to the model, trim
  their descriptions too (they repeat the file object's fields for every
  tool).
- **Read several files at once.** One `read` with `paths` (and `ifBlob`
  each) would save a round of calls when an agent starts in a folder
  (AGENTS.md, .metatrash.json, README). Only worth it if agents are seen
  making those calls one by one.

## Account lifecycle

Agreed with Dave 2026-10-06. Done: change email address, sign out everywhere,
transfer ownership, space export (REST, `mt spaces export`, `mtsync export`).
Still to build, in this order:

1. **Delete a space** (owner, step-up, type the name). Members and connected
   apps lose access; GitHub folders are unlinked, the repo is untouched.
2. **Download a space** from Your account (the zip the REST export and the
   console already make), offered before deleting; also answers access
   requests.
3. **Delete account.** Refused while the account owns any space (delete or
   transfer first). On confirm (step-up): the account is locked at once,
   sessions and OAuth grants revoked, and a notice is sent. **30-day grace
   period**: signing in during it cancels the deletion. Then purge:
   memberships, invitations, GitHub installations, passkeys, TOTP, recovery
   codes and the user row. Final notice email.
4. **Update the privacy policy** (`legal/privacy.md` in the
   `dave-zap/metatrash-com` space) once deletion exists: what sign-in methods
   store (passkey public keys, sealed authenticator secrets, hashed recovery
   codes), how account deletion and the grace period work, and concrete
   retention for logs and backups (backups: 12 monthly snapshots, from 0.53.0).

## Sign-in

Passkeys, the authenticator app, recovery codes and the per-account email
switch are done; locked-out users email Dave, who runs `mt users email-login`.
Open:

- Whether to remove email codes site-wide (needs an answer for sign-up).
- When a passkey that is not registered tries to sign in, tell the browser to
  forget it (`PublicKeyCredential.signalUnknownCredential`, Chrome 132+).
- Rotating the authenticator-app key (`totpKeyFile`); today changing it
  breaks every authenticator app.

## Owner control of files and folders

Agreed with Dave 2026-10-18.

- **Owner-set read-only files:** done in 0.45.0–0.46.0. `locked` keys and
  `readOnly` files in a folder's `.metatrash.json`, each at level `agents`
  or `everyone`. Still open: whether members' sync clients count as the
  owner's (today any Metatrash Sync write counts as a person's).
- **Public space AGENTS.md.** Anyone can rewrite the public space's root
  `AGENTS.md`, and agents read it first, so anyone can plant instructions
  there. Lock it with `readOnly` at level `everyone` (or `agents`).
- **Folder configurator and folder selection in Slate Viewer:** done. The
  tree selects folders, and a folder's Properties (right-click, or Alt+Enter)
  writes its `.metatrash.json`: locks, read-only files, publishing and the
  acknowledge service. Still to add there: triggers (GitHub, and folder
  actions under Folder agent tasks).

## Forms and quizzes

Design: Dave's "Forms and quizzes: design proposal" doc
(https://claude.ai/artifact/WNBXHhfrc5Ukkvg4De7eU6, 2026-10-23), whose
"Decisions so far" section records Dave's answers. Syntax and behaviour of
what has shipped: docs/forms.md; release details: CHANGELOG.md.

**Done**

- **Forms (phase 1):** Metatrash 0.49.0–0.49.4, Slate 0.18.0–0.19.1, Slate
  Viewer 0.15.0–0.16.1. Form templates (`form`, `title`, `submitTo`,
  `{{type name=… …}}` fields), schema v13, `mtsync submit`/`submissions`,
  Viewer fill mode with drafts saved as you type, My Submissions and Switch
  Account. The server re-checks every answer and files a copy in the review
  space (same owner as the form's space). Sending a form in an
  `acknowledge` folder also confirms it as read.
- **Review (phase 2):** Metatrash 0.50.0, Slate Viewer 0.17.0. Every copy is
  `submitted`, then `approved` or `declined`, with `reviewedBy` and a note
  (schema v14). Reviewers (write access to the review space) review in
  Slate Viewer's review bar or with `mtsync review`; the sender sees the
  result in My Submissions. In a published folder the published text of a
  form is what's sent and checked.
- **Quizzes:** 0.50.0, reworked in Metatrash 0.51.0 and Slate Viewer 0.18.0
  (Dave, 2026-10-24). `quiz: true`, `passMark` (default 100), `retryAfter`
  (default 1h), `unmarked` (fields that aren't questions). `covers` is a
  comma list of files, `*` patterns, `folder/` and `/`-rooted paths (default
  `*.md`, across published folders). A quiz is its own item to pass, with
  its own red dot until passed; reading the covered policies is a separate
  I Have Read This, and `readFirst` (default on) refuses the quiz until they
  are read. The answer key is `<space slug>-<form>-answers.md` in the
  `submitTo` folder, made with **Use This as Answers**, valid only for the
  quiz text it came from; without one only reviewers can send. Choice-type
  answers are marked on send, free text waits for a reviewer's tick; people
  see their ticks, never the key. Changing the quiz text and publishing asks
  everyone again; changing only a policy doesn't.

Where the code is: server `internal/service/forms.go` (parsing, the filed
copy), `submissions.go` (sending, receipts, retries, read-first),
`quiz.go` (keys, covers, marking), `reviews.go`, `acknowledgements.go`
(`linkQuizzes`); mtsync `cmd/mtsync/forms.go` and `policies.go`; Slate
`src/forms.js` and fill mode in `src/slate.js`; Viewer `src/lib/forms.ts`,
`policies.ts`, `FormBanner.svelte`, `ReviewBanner.svelte`,
`PolicyBanner.svelte`, `SubmissionsDialog.svelte`. Server and Slate
parsers must stay in step.

Gotchas: front matter must start on line 1 (Viewer's New File adds a
`# name` heading first). A file with `submittedAt` is a filed copy, never a
form. A quiz must be published before it can be taken.

**Phase 3, notifications and more (later; Dave, 2026-10-23)**

- A third status, `needs-info`: bounces the copy back with a note; the
  sender reopens the form with the old answers and the resend is filed as
  a new copy linked to the first.
- Template keys: `notify: [sender, reviewers]` (email on each step, using
  the existing mail setup), optional `reviewers:` address for new-copy
  alerts (default: the review space's members).
- Open for Dave: are decisions final; multi-step approval (manager then
  HR; leave out at first); should approved copies move (e.g. `approved/`).
- Also parked here: pre-filled fields (`default=@name`), conditional
  questions (`show-if`), recurring forms (`repeat: yearly`), sending from
  the website.
- Groups and hidden folders (Dave, 2026-10-23) would let review folders live
  in the same space; parked, a separate review space works for now.

## Policies and permissions

Published versions and read acknowledgements shipped in 0.48.0 (Slate Viewer
0.13.0). Still open:

- **Open question: publishing folders inside folders** (Dave, 2026-10-24).
  A published version belongs to one folder: each file is published by the
  nearest folder above it with its own `publishedVersion`. Publishing a
  subfolder on its own (Dave did this with `policies/isg`, now v2 beside
  `policies/` at v5) makes it a separate unit, and the parent's versions
  stop including it. Good: sets of policies can be released on their own
  schedules, with their own history and owners. Bad: it surprises people
  (publishing the parent looks like it covers everything), each publishing
  subfolder needs its own acknowledge service because services aren't
  inherited, and two version numbers are harder to explain. To decide:
  keep this, or make publishing include subfolders by default. Either way,
  Slate Viewer could warn before publishing a folder inside one that is
  already published, and show which folder each file publishes with.
  Today, to fold a subfolder back in, remove its `publishedVersion`.

- **Per-folder permissions** (Dave, 2026-10-22: think more before building).
  Today access is per space: each member or connection is read-only or
  read-write for the whole space, and locks only tell agents from people.
  Wanted: some folders read-only for some people (policies for employees)
  and read-write elsewhere, and drafts hidden from readers. Idea discussed: a
  member role in the database (editor, reader) and per-folder rules in
  `.metatrash.json` naming roles, never people, such as `"readers":
  "published" | "read" | "write" | "none"`. Every read path (list, read at a
  revision, search, export, history, the website) would have to filter in
  one place, with a test per interface. Until then drafts are visible to
  everyone, which Dave accepts as transparent.
- Reminders (email to people with outstanding files) and due dates.
- Report on the website (account page) as well as in Slate Viewer and
  `mtsync readers`.
- An MCP tool for the report, so an agent can say who still has to read.

## GitHub folders

Connect, pull, `pending` and `push` are done ([github.md](github.md)). Still
open, from the 2026-10-03 design session:

1. **`auto` push mode**: quiet-period batches (`push` still works).
2. **Binary files**: images and PDFs by content allow-list, base64 in the
   API, image content on MCP reads. Until then pull skips them and push
   carries them through.
3. **GitHub → space**: repo changes appear in the folder (`pull` auto),
   triggered by the push webhooks the app already receives (today they are
   acknowledged and ignored).
4. **One folder action**: done in 0.54.0 as folder actions (see Folder agent
   tasks below).

Later: letting push send `.github/workflows/` changes (needs the app's
`workflows` permission; refused for now because workflows can reach CI
secrets); a member permission "write but not push", so a supervising agent
reviews `pending` and pushes; possibly a GitHub service on the space root;
the `review` push mode, which needs a review screen.

## Folder agent tasks

0.54.0 has the runner and text-file triggers (`actions` in `.metatrash.json`,
results in `runner.md`; [api-contract.md](api-contract.md#folder-actions)).
A tidy-draft run succeeded on the live server on 2026-10-25 (15 steps, about
95k tokens in). 0.54.2 lets only the owner, as a person, change `actions`.
0.55.0 replaces the server key with users' own agents (Your account →
Services → Agents, schema v16) and the action `model` field; the owner pays
for every job, and each agent has its own daily cap. It also tells the agent
to change only the files its instructions call for (the first live run
renamed `README.md`). 0.56.0 adds who pays per action (`"pays":
"contributor"`, with opting in on `/account/actions/<job>` and a notice
email) and members' own actions (`"member"`), schema v17.

### User agents and who pays (agreed with Dave 2026-10-25)

Users set up their own agents and keys; the server key in `accounts.json`
goes away. An owner can build an action as a service that other people use
with their own money.

- **Agents table.** One row per agent a user adds: owner, name ("MiniMax
  main"), provider, endpoint URL, model, most tokens per reply, the user's own
  daily token cap, and the API key, encrypted with a server key file (like
  `totp-key`), never shown again after saving, replaceable. Set up on the
  account page: Add agent, Test (one tiny call), Delete. Endpoints must be
  https and may not point into the server's own network. One agent is the
  user's default.
- **The owner controls the action**: instructions, provider and model
  (`"model": "minimax/MiniMax-M3"`, or the owner's default agent), steps, web.
  Nobody else can change them; the model never changes by who brings a file,
  so output stays the same.
- **Who pays, per action, set by the owner:**
  - `owner` (default): the owner's agent pays for every file.
  - bring your own key: whoever's file arrives pays, only once they have
    opted in to that action. Opting in means adding a key for the provider
    and model the action names (the notice says where to get one, e.g.
    MiniMax: platform.minimax.io → API keys) and setting their own daily
    cap. They see the action's settings but cannot change them.
  - Never silent: no fallback to another key or model. A file from someone
    not opted in runs nothing; they get a notice and `runner.md` records it.
  - A file brought by a GitHub pull has no clear contributor: the owner pays.
- **Members' own actions.** A member of a shared space can create an action
  backed by their own agent; it runs only for that member's files, on their
  key. The owner sees it as proposed and can delete it, adopt it (it becomes
  the owner's and the owner picks who pays), or leave it.
- **Identity.** A member's key only pays for the model; the agent still works
  within the owner's folder rules and locks, and changes are signed
  "MiniMax (agent) for <member>".
- `accounts.json` keeps only the on/off switch, the key-encryption file and
  server limits; `agents.owners` is removed. Dave's MiniMax key moves to his
  account.

### Build order

1. ~~**0.55.0**: user agents (table, account page, encrypted keys), the action
   `model` field, owner pays.~~ Done; replacing a saved agent's key means
   removing and adding it again for now.
2. ~~**0.56.0**: bring your own key and members' own actions (opt-in,
   notices, adopt).~~ Done. Choices made while building it: the field is
   `"pays": "contributor"` and needs `model`; opting in picks one of your
   agents for that model, whose daily cap applies (no separate cap per
   action); the notice is an email at most once a day per action, linking to
   the action's page, and opting in there runs the skipped file; a member's
   action names them by username (`"member"`), so if an admin renames the
   account, its actions stop running until the name is fixed; files with no clear contributor
   (GitHub pull, space key) run on the owner's agent.
3. **0.57.0**: web tools (Brave `web_search`, `web_fetch`, downloads cached in
   B2 as `web/<id>`) and `every` schedules.
4. **0.58.0**: PDF and Word files as triggers and input.
5. **Slate Viewer**: an Automation page for a folder's actions and its
   `runner.md`, opting in, and agents; make a refused `.metatrash.json` save
   obvious (an invalid file silently stayed local on 2026-10-25).

### Ideas (not scheduled)

- **Reply to the runner** (Dave, 2026-10-25): an input box under a
  `runner.md` entry (Slate Viewer, website) where the person who brought the
  file can answer the agent or ask for more ("use Australian law instead",
  "also add a summary"). The reply starts a follow-up job for the same action
  and file, with the earlier summary and the reply added to the prompt, under
  the same rules, pinning, limits and who-pays as the original job. To settle:
  who may reply (the owner only, or whoever pays), whether a reply can go
  beyond the action's instructions, and how a thread of replies shows in
  `runner.md`.

## mtsync and Slate Viewer

- **Editor gaps G2–G8** (`developers/mtsync-editor-integration.md` in the
  `dave-zap/metatrash-com` space): `--json` on `spaces` and `list`, `whoami`,
  `resolve URL`, `files` and `status`.
- **macOS code signing** (Developer ID and notarization) for mtsync and
  Slate Viewer; needs an Apple Developer account. Windows files are signed
  with Azure Artifact Signing since 0.43.2 (deployment.md, Code signing).
- **Slate**: a read-only Compare, so Slate Viewer's History panel works on
  documents it cannot edit. (Mermaid diagrams shipped in Slate 0.14, and the
  website uses Slate 0.19.0 since Metatrash 0.49.3.)
- **Slate Viewer as the space manager**: the desktop companion for managing
  Metatrash spaces (agreed direction 2026-10-17, not yet planned in detail).
- Deferred with Dave 2026-10-12: bulk first download, on-demand files like
  OneDrive, automatic three-way merge of conflicts.

## Maintenance

- **Old READMEs**: spaces made before 0.45.0 still hold the old agent
  README.md as an ordinary file; delete or rewrite them as needed.
- [Security review](security-review-2026-09-30.md) F3: cache recent activity
  by head hash, cap history scanned per request and bound concurrent Git
  readers. Hardening from the same review: systemd resource limits, an
  allowlist for Markdown link schemes, structured security-event logging, and
  a dependency/advisory scan.
- A focused security review of the account, sharing, OAuth and sign-in
  (passkey, TOTP, recovery) code, which the 2026-09-30 review predates.

## Owner checks still to run

Live checks not yet reported done. The server is at schema v14 with current
grants, so the schema and grant steps these once needed are behind us.

From 0.56.0 (2026-10-25):

- **Bring your own key:** with a second account (a member of a test space)
  that has a MiniMax agent, set an action in that space to `"model":
  "minimax/MiniMax-M3", "pays": "contributor"`. Drop a file in as the member:
  `runner.md` says skipped, "has not opted in", and the member gets the
  email. Open its link as the member, opt in: the file runs, `runner.md` ends
  with `- Paid by: <member>`, History shows "MiniMax (agent) for <member>",
  and Services lists it under Actions you pay for. Your own file in that
  folder still runs on your agent. Opt out, and the next file is skipped.
- **Member's own action:** as the member, in Slate Viewer, add an action with
  `"member": "<their username>"`: it runs for their files only, on their
  agent. As the owner, try to edit it (refused), then adopt it by deleting
  the `member` line.

From 0.53.0 (2026-10-25):

- **Off-site backups:** do the one-time setup in docs/deployment.md, Backups
  (B2 bucket with Object Lock, restic, settings, timer), run
  `sudo metatrash-backup` once and see it end `done`, then check the next
  nightly run in `journalctl -u metatrash-backup`. After the first Sunday,
  check that `forget --prune` worked with Object Lock on (old versions hidden,
  not refused). Do one test restore (rebuild a space from its bundle, load the
  dump into a scratch MariaDB) and note the date here.
- **Emailed configuration (0.53.1):** set up and run
  `sudo metatrash-backup-config`; the email reaches david@metatrash.com (not
  spam), the attachment opens with `gpg -d … | tar -xz` in Git Bash, and its
  `totp-key` and restic password match the password manager.

From 0.52.2 (2026-10-24):

- **Document viewer layout:** on a long document in `/docs/`, an owned space
  and the public explorer, the browser shows no page scroll bar at any window
  height; header and footer stay put; Slate's status bar sits on the footer;
  the document and the explorer tree each scroll on their own; a file deep in
  a long tree is scrolled into view on load. Check a phone width too (tree
  above the document, at most a quarter of the height).

From 0.30.0 (2026-10-09):

- **Transfer:** on a test space, invite a second account, accept, confirm
  it's you, offer ownership. The second account gets the email and sees
  Ownership offered to you; accept. Check the address, that the first
  account is now a member, that its connected app still reads and writes
  using the old `owner/slug`, and that the old web address redirects. Try
  creating a space at the old address from the first account (refused).
  Transfer it back.

From 0.20.0 to 0.27.0 (2026-10-05 to 2026-10-08):

- **Push merge:** edit a file on GitHub, have the agent push (expect "moved
  ahead"), pull (expect GitHub's change merged, local edits kept and still
  pending), push again.
- **Overlap:** change the same file on GitHub and in the space; pull should
  stop with `conflict` naming it.
- **Unpushed hint:** leave an edit unpushed for more than 30 minutes, then
  write, move or delete in that folder; the result should carry `hint`, and
  `pending` should show `oldestChange`.
- **Verified badge:** a pushed commit shows Verified on GitHub, with you as
  co-author (commit `400dd57` was co-authored; the badge was not checked).
- **Workflows permission dropped:** the app's installation page no longer
  lists Workflows; a pull of a repository with `.github/workflows/` still
  works, and a push changing one is refused.
- **Push webhook:** after a commit to panda-madness, the app's Advanced tab
  lists a `push` delivery (needed for GitHub → space).
- **Readable on the web (0.21.0):** make a space readable, read the warning, open it in a private window (no sign-in), make
  it private again (the private window gets the sign-in page). An agent
  connected by another account still cannot reach it.
- **Explorer navigation (0.21.0):** open folders, click between Markdown and
  text files (no reload, folders stay open), use back and forward, reload a
  nested file (its folders open).
- **Site docs (0.21.1):** with `docsSpace` set to `dave-zap/metatrash-com`,
  signed out (private window): the footer shows
  Privacy and Terms; both open under `/docs/legal/…` with no space header;
  clicking around the tree stays under `/docs/`. Make the space private: `/docs/`
  is not found (no sign-in page). Make it readable again.
- **About and links (0.21.2):** the footer About and `/about` open
  `/docs/about.md`. In it, the GitHub link opens a new tab, the email link
  opens your mail app, and a link to another docs page (try
  `[Terms](legal/terms.md)`) opens in the same tab without a reload.
- **Search indexing (0.21.1):** view source (or response headers) on a
  `/docs/` page: no `robots` meta. A web-readable or public space's top-level
  file: none either; a file in a subfolder: `noindex, nofollow`. Optionally
  ask Google Search Console to inspect `/docs/legal/privacy.md`.
- **Sign out everywhere (0.27.0):** sign in on two browsers (say your phone
  and a private window). On Security, Signed-in devices lists the other
  browser; Sign out everywhere else, then reload the other browser: it shows
  the sign-in page. Sign in there again, then remove a passkey (or turn off
  email sign-in, change your address, or sign in with a recovery code) in the
  first browser: the other one is signed out again. `journalctl` shows
  `account sessions-end … ended=1`.
- **Change email address (0.26.0):** on Security, confirm it's you, enter a
  second address you own under Email address and Send a code. Enter the code: the page shows the new address,
  and both addresses get a notice. Sign out and sign in with an emailed code
  to the new address (and the authenticator app or a recovery code with the
  new address). Try an address that already has an account: it gets a
  "already has its own Metatrash account" email and no code. Change back at
  the end if you like. `journalctl` shows `account email-change … result=ok`.
- **Recovery codes and email switch (0.25.0):** on Security, confirm it's
  you and Create recovery codes: ten codes show once (reload: gone, "10 of 10
  left"), with a notice email. Sign out; on the sign-in page open Use a
  recovery code and sign in with your email and one code (try it in capitals
  without hyphens); the notice says 9 are left, and the same code fails a
  second time. With the authenticator app or a passkey set up, Turn off
  email sign-in (notice email). Then Email me a code on the sign-in page
  should show Check your email but send only a "sign-in by email is turned
  off" notice, and `journalctl` shows `result=email_off`. Confirm it's you
  offers no email option, and removing your last passkey or app is refused.
  Turn email sign-in back on at the end (or keep it off, with your codes
  safe).
- **Authenticator app (0.24.0):** create the key file and add
  `"totpKeyFile": "/etc/metatrash/totp-key"` to `accounts.json`, then restart
  (see [deployment.md](deployment.md#authenticator-app)). On Your account →
  Security, confirm it's you, choose Set up an authenticator app, scan the QR
  code with your phone's app (also try typing the setup key into a second
  app), enter the code and Turn on. You should get a notice email. Sign out;
  on the sign-in page open Use an authenticator app and sign in with your
  email and a code. Try a wrong code and an address with no app: same
  message. Leave it more than 10 minutes, then confirm with the app (the
  code field in Confirm it's you) and remove it (notice email). `journalctl` shows `login totp … result=ok`.
- **Passkeys (0.22.0):** on Your account → Sign-in methods, add a passkey on your phone or computer (sign in fresh, or use
  Email me a code to confirm). You should get a notice email. Sign out, then
  sign in with the passkey button, and again by picking the passkey in the
  email field's suggestions. Leave it more than 10 minutes, then confirm with
  the passkey before removing it. Try a second device: a phone through the
  QR code (hybrid) and a password manager. `journalctl` shows
  `login passkey … result=ok`.

Earlier checks:

- Restart the service; Claude and ChatGPT reconnect by refreshing silently.
- With two accounts: set a member's app permission to read only; suspend,
  restore and remove a member who has a connected app; re-inviting does not
  restore the space until they consent again.
- Apache and service logs contain no `mt_at_`, `mt_rt_` or `mt_ac_` values.
- ChatGPT end to end. If it cannot use CIMD it will need Dynamic Client
  Registration, which [oauth.md](oauth.md) allows adding only if needed.


## Later

- **Spaces:** pagination of sharing lists over 200 entries. (Renaming stays a
  console job; there is no web rename.)
- **Remote Git:** start with authenticated HTTPS clone/fetch. Pushes would
  bypass stable IDs, protected files, path checks, quotas and conditional
  writes, so they need validated import through the service, never a plain
  receive-pack.
- **Agent features:** file locking; recording the calling app and account in
  commit history.
- **Folder pinning** (an attention aid, not a security boundary): a `start`
  tool returns a context token that later calls carry; the first read pins the
  context to that folder, and work in another folder asks the agent to confirm
  the change of focus before moving the pin.
- **Website:** search within a space, a space selector, a recent-activity tab,
  file revision view and download.
- **Compliance:** content-safety screening of writes (scores recorded per
  account and file for review rather than blocking) and secret scanning.
- **Protocol:** a JMAP adapter over the same object/state core.

Dave's working ideas are in the local `notes/` folder, which is not in git.
