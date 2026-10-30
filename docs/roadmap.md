# Roadmap

Open work as of Metatrash 0.61.1 and Slate Viewer 0.20.2 (2026-10-28). Finished plans and the decisions behind them are in [roadmap-archive.md](roadmap-archive.md); release details in [CHANGELOG.md](../CHANGELOG.md).

## Fewer tokens per change

Shipped work is in [roadmap-archive.md](roadmap-archive.md#fewer-tokens-per-change).
What is left needs measuring first:

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

Agreed with Dave 2026-10-06 (done parts in the archive). Still to build, in
this order:

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

Passkeys, the authenticator app and recovery codes are done (archive).
Locked-out users email Dave, who runs `mt users email-login`. Open:

- Whether to remove email codes site-wide (needs an answer for sign-up).
- Rotating the authenticator-app key (`totpKeyFile`); today changing it
  breaks every authenticator app.

## Owner control of files and folders

Agreed with Dave 2026-10-18.

- **Members' sync clients:** whether they count as the owner's for locks
  (today any Metatrash Sync write counts as a person's).
- **Public space AGENTS.md.** Anyone can rewrite the public space's root
  `AGENTS.md`, and agents read it first, so anyone can plant instructions
  there. Lock it with `readOnly` at level `everyone` (or `agents`).
- **Folder Properties in Slate Viewer:** still to add there: triggers
  (GitHub, and folder actions under Folder agent tasks).

## Forms and quizzes

Design: Dave's "Forms and quizzes: design proposal" doc
(https://claude.ai/artifact/WNBXHhfrc5Ukkvg4De7eU6, 2026-10-23), whose
"Decisions so far" section records Dave's answers. Syntax and behaviour of
what has shipped: docs/forms.md; release details: CHANGELOG.md.

Phases 1 and 2 and quizzes are done (summary in the archive).

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

Published versions and read acknowledgements are done (archive). Still open:

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

Connect, pull, `pending` and `push` are done ([github.md](github.md)), and
binaries travel as virtual files (archive). Still open, from the 2026-10-03
design session:

1. **`auto` push mode**: quiet-period batches (`push` still works).
2. **GitHub → space**: repo changes appear in the folder (`pull` auto),
   triggered by the push webhooks the app already receives (today they are
   acknowledged and ignored). Until then pull and push happen only when an
   agent or a REST call asks for them.

Also wanted (Dave, 2026-10-28): **Pull and Push in Slate Viewer**, a button
on a GitHub folder that shows `pending` and pushes with a message, so a
person can do it without asking an agent. mtsync has `pull`, `pending` and
`push` (with `-json`) since 0.64.0; the Viewer part is still to build.

Later: letting push send `.github/workflows/` changes (needs the app's
`workflows` permission; refused for now because workflows can reach CI
secrets); a member permission "write but not push", so a supervising agent
reviews `pending` and pushes; possibly a GitHub service on the space root;
the `review` push mode, which needs a review screen.

## Folder agent tasks

Folder actions (runner, users' own agents, who pays, members' actions, web
tools, schedules, changing an agent) are done through 0.58.0; the history,
the agreed design and the choices made while building are in
[roadmap-archive.md](roadmap-archive.md#folder-agent-tasks). Rules:
[api-contract.md](api-contract.md#folder-actions).

### Next

1. **Slate Viewer**: an Automation page for a folder's actions and its
   `runner.md`, opting in, and agents; make a refused `.metatrash.json` save
   obvious in both Slate Viewer and mtsync (on 2026-10-25 an invalid file
   stayed local while `mtsync list` still said "in sync"); and open
   the folder's settings when someone names a new file `.metatrash.json`
   (on 2026-10-25 that made `metatrash.json.md` instead).

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
- **Show running jobs in Slate Viewer** (Dave, 2026-10-25): while an action
  is queued or running for a folder, show it (a small animation on the
  folder, and on the file that started it) so people can see it's working,
  and clear it when the `runner.md` entry lands. Needs the server to expose
  a folder's queued and running jobs (REST, alongside the long-poll list).
- **Prompt caching for MiniMax**: check what its Anthropic-compatible API
  supports (Anthropic agents already mark cache breakpoints, though
  shortening an old result changes the cached prefix from that point). Token
  history of web jobs is in the archive.

## Binary files and conversion (Dave, 2026-10-26)

Virtual files are done through Metatrash 0.61.1 and Slate Viewer 0.20.2:
binaries live in B2 and never enter Git, PDF and Word read as Markdown, and
mtsync, Slate Viewer and GitHub folders handle them. The plan, the build
history and the settled decisions are in
[roadmap-archive.md](roadmap-archive.md#binary-files-and-conversion); rules in
[api-contract.md](api-contract.md#virtual-files). Still open:

- **Uploads hold one file in memory** (up to 100 MB); stream them instead.
- **`md.metatrash.com`** (Dave, 2026-10-27), if wanted, on top of
  `?format=md`: DNS, TLS and Apache only.
- **Raw bytes for MCP agents** (Dave's Q&A, 2026-10-29): today `read` gives
  text, or the image for a picture, and only REST `…/content` or `?raw`
  gives the original. Options: base64 under a size cap, or a short-lived
  download link.
- **Later: a blob service for compute output.** An action, or another
  service, stores a result as a virtual file (or a bare blob with a
  reference) for later retrieval, under the same ownership and retention
  rules.

### Text files

Spaces already accept any UTF-8 file whatever its extension (`.json`, `.c`,
`.go`, `.js`, `.css`, and so on); only "UTF-8 without NUL" is checked.
Native support means showing them properly: a code view with highlighting
on the site and in Slate Viewer, and HTML shown either raw or as filtered
Markdown (the same converter `web_fetch` uses), chosen per file or folder.
Check every client (mtsync, Slate Viewer, the site explorer, MCP) with a set
of such files first.

## Hidden system files (Dave, 2026-10-25)

Metatrash's own files should be hidden files with an `.mt.` prefix, as other
tools do (`.gitignore`, `.vscode/`), so a folder shows the person's files
first: `.metatrash.json` becomes `.mt.json`, `runner.md` becomes
`.mt.runner.md`, and later system files follow the same pattern. Not
scheduled yet. To settle when it is:

- **AGENTS.md** stays visible: it is the shared convention agents look for.
- **Dot names.** Today names starting with a dot are allowed only inside
  GitHub folders. `.mt.*` must be allowed everywhere and stay reserved to
  Metatrash (protected as `.metatrash.json` and `runner.md` are now), while
  other dot names keep today's rule. Space exports and the root-level dot
  refusal need the same exception.
- **Migration.** Existing spaces hold `.metatrash.json` and `runner.md` in
  every folder: a one-off rename on upgrade (one commit per space, file IDs
  kept, so history follows), with the server reading the old names for a
  release while mtsync and Slate Viewer catch up.
- **Clients.** mtsync, Slate Viewer, Slate and the website explorer read
  `.metatrash.json` by name and need the new name; decide whether they hide
  `.mt.*` by default with a "show system files" switch. The MCP tool
  descriptions, the agent preamble, docs and the docs space change with it.
- **GitHub folders.** Whether `.mt.*` files are pushed to the repository or
  kept on the Metatrash side only.

## mtsync and Slate Viewer

- **Editor gaps** (`developers/mtsync-editor-integration.md` in the
  `dave-zap/metatrash-com` space): `resolve URL`, `files` and `status`
  (`whoami -json` is done, and `-json` on `spaces` and `list` since 0.62.0).
- **macOS code signing** (Developer ID and notarization) for mtsync and
  Slate Viewer; needs an Apple Developer account. Windows files are signed
  with Azure Artifact Signing since 0.43.2 (deployment.md, Code signing).
- **Slate**: a read-only Compare, so Slate Viewer's History panel works on
  documents it cannot edit. (Mermaid diagrams shipped in Slate 0.14, and the
  website uses Slate 0.19.5 since Metatrash 0.57.5.)
- **Slate Viewer as the space manager**: the desktop companion for managing
  Metatrash spaces (agreed direction 2026-10-17, not yet planned in detail).
- **Open pictures in Slate Viewer** (Dave, 2026-10-28): clicking a virtual
  picture (and perhaps a PDF) shows it in the Viewer instead of handing it
  to the system's app. Planned as Slate Viewer 0.21.0.
- **Create folder** (Dave, 2026-10-25): a folder only exists in Git once it
  holds a file, so a new empty folder has no `.metatrash.json` or `AGENTS.md`
  until a file arrives, and people make them by hand (one became
  `metatrash.json.md`). Add a create-folder operation (REST and MCP) that
  commits the starter `AGENTS.md` and `.metatrash.json` at once; Slate
  Viewer's New Folder calls it, and mtsync calls it when it finds a new empty
  local folder. Folders stay paths, not objects.
- **Slate Viewer folder tree is hard to read** (Dave, 2026-10-25): with
  hidden files shown, the indentation doesn't make clear which folder a
  `.metatrash.json` belongs to. Stronger indent guides (a line per level),
  folders' own files grouped first, and perhaps the folder's settings shown
  as an icon on the folder row instead of a separate file.
- **Activity icon** (Dave, 2026-10-28): Slate Viewer shows when something is
  happening: files syncing (mtsync uploading or downloading, which would need
  mtsync to report its progress, perhaps in `mtsync status`), and agents
  running actions on folders (the runner's jobs for the space, which the
  server would need to expose). Perhaps on the folder rows as well as one
  icon for the whole space.
- Deferred with Dave 2026-10-12: bulk first download, on-demand files like
  OneDrive, automatic three-way merge of conflicts.

## Maintenance

- **Cloudflare beacon** (2026-10-25): Cloudflare Web Analytics injects
  `static.cloudflareinsights.com/beacon.min.js`, which the pages' CSP
  (`script-src 'self'`) blocks, so every page logs a console error. Turn off
  its automatic setup in the Cloudflare dashboard (or allow the host in the
  CSP if the analytics are wanted). It also rewrote HTML downloads of
  virtual files until 0.61.1 sent them with `no-transform`.
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

Live checks not yet reported done. The server is at schema v19 with current
grants (0.60.2), so the schema and grant steps these once needed are behind
us. Checks already passed are listed in the archive.

From 0.60.2 (2026-10-27):

- **Kept up to date from a URL:** ask Claude to configure
  `forms-guide.md` in a folder with source
  `https://metatrash.com/docs/guides/forms.md?format=md` and refresh
  `hour`. The file appears (a virtual file; its page shows "refreshed every
  hour"), and reading it gives the guide. After the next hour, History
  shows no new version unless the guide changed; change a docs page you
  control and see a new version by "Metatrash refresh" within the hour.
- **Stale:** put a `test.md` in a web-readable space and configure a file
  elsewhere with source `https://metatrash.com/spaces/<owner>/<slug>/test.md?format=md`
  and refresh `hour`. Delete `test.md`: after the next run, reading the
  file says refreshing has failed since … and its page shows "Stale", and
  History has no new version. Write `test.md` again: after the next run
  the stale note is gone and the new text is a new version.

From 0.60.1 (2026-10-27):

- **Pictures:** have a folder action fetch a PNG with `path`, then ask Claude
  to read it and describe it.
- **Export:** `mtsync export` a space with a virtual file: the zip holds
  the file itself and it opens, and `.metatrash-export.json` says
  `"virtualFiles": "content"`. Over REST, `…/export?virtual=list` holds the
  pointer text instead.
- **Sweep:** add the `files/` lifecycle rule (docs/deployment.md). In the B2
  browser, upload any small file to `files/<space ID>/` named with 64 hex
  digits (`aaaa…`). About a day later (the sweep runs an hour after start,
  then daily, and skips content under a day old) the journal says
  `virtual files sweep: space … removed 1 of N objects`, the file is
  hidden in B2, and every virtual file in that space still reads,
  old versions too (History).

From 0.60.0 (2026-10-27):

- **Virtual files:** give a folder action the instruction "fetch <the URL of
  a PDF> with web_fetch, with path policy.pdf, then summarise it in
  summary.md". The folder then shows `policy.pdf`;
  its page on the site says "Virtual file" with a Download link and shows
  the text; `read` of it (from Claude) gives the Markdown; asking Claude to
  write to it is refused with `virtual_file`. Download it from the page and
  check it opens. In B2 the bucket has a `files/<space ID>/` folder.

From 0.59.1 (2026-10-27):

- **Markdown for agents:** open `https://metatrash.com/docs/README.md?format=md`
  (plain Markdown, no page around it), then `https://metatrash.com/llms.txt`
  (a list of the docs pages with `?format=md` links). Signed out, a private
  space page with `?format=md` goes to sign in. Run a folder action that
  fetches a metatrash.com docs page: its `web/<ref>.md` is the document
  itself, without the page's heading and code block.
- **Folder addresses (0.59.2):** open `https://metatrash.com/docs/legal/`
  (it goes to the folder's page), then `https://metatrash.com/docs/legal/?format=md`
  and `https://metatrash.com/docs/legal/llms.txt` (lists of that folder's pages).

From 0.58.0 (2026-10-26):

- **Change an agent:** run `deploy/account-grants.sql`, deploy, and see
  `metatrash check` report no missing grants. On Services → Agents, open
  the pencil on an agent: change its daily cap (the row shows "N of
  <new cap> tokens today"), rename it, and give it a new key (the row shows
  the new key ending; Test works with it). Renaming it to another agent's
  name is refused. An action you opted in to pay with it still lists it under
  Actions you pay for.

From 0.57.5 and 0.57.6 (2026-10-26):

- **Links from a PDF:** print a document with a link to another page in a
  private space from Slate Viewer, open the PDF while signed in, and click
  the link: the page opens, not `/account` or `/login`.
- **Back after signing in:** signed out, open a private space page: after
  signing in (email code, passkey or authenticator app) you land on that
  page, not Your account.
- **Printout image:** with 0.57.6 deployed, set Properties → Publishing →
  Printed copies in Slate Viewer 0.19.0 to an https image; it prints above
  the title on the first page.

From 0.57.0 (2026-10-25):

- **Web tools:** set up the Brave key and the B2 bucket (docs/deployment.md,
  Folder actions), add `agents.search` and `agents.downloads`, and see
  `metatrash check` list "web search, web downloads" and both key files as
  readable. Give an action instructions with a real question and a URL (or
  none, so it searches): `runner.md` lists the steps, the summary uses the
  page, and a kept page lands in the folder as Markdown. Check the bucket
  holds `web/<hash>` files and its lifecycle rule. A fetch of a URL the
  agent made up should come back `url_not_seen`.
- **Schedules:** set a test action to `"every": "hour"` (or a time a few
  minutes ahead with `day HH:MM`), check it runs at that time in the space's
  zone, with `- Run: scheduled` in `runner.md`. Change the space's zone on
  Your account and see the next run move; restart the service across a
  run time and see it run once, not twice.

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

- **Backup test restore:** rebuild a space from its restic bundle and load
  the dump into a scratch MariaDB, then note the date here. If not yet seen,
  also check that the Sunday `forget --prune` worked with Object Lock on (old
  versions hidden, not refused). Setup, nightly runs and the emailed
  configuration are done.

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
