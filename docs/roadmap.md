# Roadmap

Open work as of 0.58.0 (2026-10-26). Done work is in [CHANGELOG.md](../CHANGELOG.md).

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
2. **Binary files**: replaced by Binary files and conversion below; a pull
   will send a repository's binary files to B2 like any other. Until then
   pull skips them and push carries them through.
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
email) and members' own actions (`"member"`), schema v17. 0.57.0 adds the web
tools (`web_search` through Brave, `web_fetch` into a B2 downloads cache) and
`every` schedules in each space's own time zone, schema v18. 0.57.1 to
0.57.4 cut the tokens per job and mark a run cut off at its step limit as
failed. 0.58.0 lets you change an agent's name, daily cap and key without
removing it. (0.57.5 and 0.57.6 were printing and sign-in fixes, outside this
plan, which is why PDF and Word moved from 0.58.0 to 0.59.0.) PDF and Word
are now part of Binary files and conversion, below.

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
   `model` field, owner pays.~~ Done; changing a saved agent came in 0.58.0.
2. ~~**0.56.0**: bring your own key and members' own actions (opt-in,
   notices, adopt).~~ Done. Choices made while building it: the field is
   `"pays": "contributor"` and needs `model`; opting in picks one of your
   agents for that model, whose daily cap applies (no separate cap per
   action); the notice is an email at most once a day per action, linking to
   the action's page, and opting in there runs the skipped file; a member's
   action names them by username (`"member"`), so if an admin renames the
   account, its actions stop running until the name is fixed; files with no clear contributor
   (GitHub pull, space key) run on the owner's agent.
3. ~~**0.57.0**: web tools (Brave `web_search`, `web_fetch`, downloads cached in
   B2 as `web/<id>`) and `every` schedules.~~ Done. Choices made while
   building it: references are `web/<16 hex>.md` (a page's Markdown) and
   `web/<16 hex>.<ext>` (the original), belong to whoever paid for the job
   and last 29 days against the bucket's 30-day lifecycle rule; only URLs
   already in the conversation can be fetched; each payer gets 200 searches
   and 500 fetches a day by default; keeping a download copies its text into
   the folder and, like every runner change, starts no actions (the design
   doc said it would); text that is not UTF-8 is read as Windows-1252;
   schedules are `hour`, `hour :MM`, `day`, `weekday`, a day's name or
   `month D` (1 to 28) with a time, and cannot use `"pays": "contributor"`;
   each space's time zone is set with the clock icon on Your account.
4. ~~**0.58.0**: change an agent (name, daily cap, a new key) on Services →
   Agents, which the design called "replaceable".~~ Done. The provider,
   model and endpoint stay fixed (another model is another agent, so the
   actions and opt-ins that chose it keep their model); today's use counts
   toward a new cap at once.
5. **Slate Viewer**: an Automation page for a folder's actions and its
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
- **Fewer tokens per web job**: the first live news run (2026-10-25) took
  26 steps and 341,000 input tokens, close to the 400,000 cap, because every
  step resends the whole conversation. 0.57.1 shortens old tool results and
  lowers the web defaults (5 results, 1,000-character previews); compare a
  rerun (it used 195,000 tokens over 34 steps, against 341,000 over 26).
  0.57.2 sends jobs short tool definitions (4.9 KB instead of 11.2 KB per
  step), lets an action list its `tools`, and warns the agent with 3 calls
  left. Still open: prompt caching for MiniMax (check what its
  Anthropic-compatible API supports; Anthropic agents already mark cache
  breakpoints, though shortening a result changes the cached prefix from
  that point).

## Binary files and conversion (Dave, 2026-10-26)

Spaces stay plain UTF-8 text in Git, so they stay small, diffable and
friendly to agents. Binary files (PDF, Word, images, archives, compute
output) live in B2, the way `web_fetch` downloads already do, and never
enter a space's Git history. Clients handle the split for people: mtsync,
Slate Viewer and the server send a UTF-8 file to the space and anything else
to B2, so a person just drops files into a folder. What agents mostly need
from a binary is its content as Markdown, so converting is a first-class
step.

### Build order

1. ~~**0.59.0: convert to Markdown in the downloads store.**~~ Done: Word
   is read in Go, PDF with `pdftotext` (poppler-utils) when the server has
   it, pages marked `<!-- page N -->`; a reference fetched before 0.59.0
   gets its text on the next fetch. Planned: A PDF or Word
   (`.docx`) file that `web_fetch` downloads gets a `web/<ref>.md` with its
   text as Markdown, as HTML pages already do (headings, lists and tables
   where the format has them). `read`, `search` and `move` (which copies
   the Markdown into the folder) work on it unchanged. Word is zipped XML,
   read in Go; PDF needs a text extractor on the server (a Go library or
   `pdftotext`, to be chosen). Done on the server so it works the same for
   MiniMax and Claude.
2. ~~**0.59.1: Markdown for agents.**~~ Done: `?format=md` and
   `Accept: text/markdown` on every space, public and `/docs/` page,
   `llms.txt` per space plus `/llms.txt` for the docs (a space's own
   `llms.txt` file wins), and `web_fetch` asking for Markdown first.
   Planned: Every space page and `/docs/` page
   is a Markdown file already, so the site sends that file as it is when a
   client asks for Markdown: `Accept: text/markdown` or `?format=md` on the
   same URL (`Content-Type: text/markdown; charset=utf-8`, the same access
   rules as the page, so a private page still needs the sign-in). An
   `llms.txt` at the root of `/docs/` (and of each web-readable space) lists
   the pages with one line each, the usual way to point AI tools at a site's
   Markdown. `web_fetch` asks for Markdown first, so fetching a Metatrash
   page gives the document itself instead of the page around it. Today a
   fetch gets the HTML page, whose `<main>` carries the source in a `<pre>`,
   so the agent sees the Markdown inside a code block under the page's
   heading: usable, but larger and less clean. A separate
   `md.metatrash.com` (Dave, 2026-10-27) can come later on top of this if
   wanted; it would need DNS, TLS and Apache changes and nothing more from
   the server.
3. **0.60.0: virtual files** (Dave's design, 2026-10-27; replaces the
   earlier plan of a database record per path). A virtual file is content
   kept outside Git, uploaded or fetched from the internet, that appears in
   the space as a normal file:
   - **In Git, a small pointer at the file's own path** (as Git LFS does):
     `report.pdf` holds a UTF-8 record, starting with a `metatrash-virtual 1`
     line, then the content's SHA-256, size and type, an optional source URL
     and its refresh and retention policy. The same format serves binary and
     text content. Because the pointer is an ordinary Git file, history,
     moves, deletes, export, locks and the `on` triggers of folder actions all
     work unchanged, and each pointer revision is a version of the content.
   - **Tools resolve it.** `list` marks the file `virtual` (with size, type,
     source, stale); `read` returns the content (text as text, an image as an
     image, a PDF or Word file as its Markdown with a link to the original).
     Content writes are refused (`virtual_file`); a separate `configure`
     operation changes the source and policies, under the usual locks. The
     website and Slate Viewer show it as virtual and read-only.
   - **Creating one.** An upload route stores a file and writes its pointer;
     `web_fetch` takes an optional destination path in the folder, so
     Metatrash fetches, stores and creates the virtual file there, and the
     agent then uses the normal file tools without handling the URL.
   - **Refresh.** A policy pins an exact version or refreshes from the
     source on an `every` schedule (the folder-actions schedules), asking
     the site whether it changed first. A new hash is a new commit of the
     pointer, so History shows it and actions fire; an unchanged source makes
     no commit. A failed refresh keeps the previous content and marks it
     stale in the database (not in Git, so no commit noise), and reads say
     so. Refreshes are made for the space owner and count toward their daily
     fetch limit; the `web_fetch` address rules apply.
   - **Storage and retention.** Content goes to the space-files bucket (no
     lifecycle rule) by hash, once. A sweep keeps every hash a pointer in any
     commit of the space still names, so old revisions always resolve; the
     30-day `web/` cache stays separate, and fetching to a path copies the
     content into the space-files bucket.
   - **Uses.** Attachments, embedded images, PDF and Word files with their
     Markdown, and documents kept up to date from a URL, such as the forms
     guide an action needs (`forms-guide.md`, refreshed daily from
     `/docs/guides/forms.md?format=md`), which the agent then reads like any
     file in its folder.
   - To settle while building: whether `read` of a PDF or Word file returns
     its Markdown by default or needs `as: "markdown"`; what a GitHub folder
     pushes for a virtual file (the pointer, the content, or nothing); and
     how old mtsync releases, which see pointer text, are kept from
     overwriting one (refuse writes over pointers and raise `minVersion`).
4. **0.61.0: mtsync and Slate Viewer handle it transparently.** mtsync
   downloads a virtual file's content to the local path, puts it back when
   it is edited locally (as for everyone-locked files), and turns a new
   local binary into a virtual file; Slate Viewer shows virtual files, opens
   them in the system's app and offers "Convert to Markdown". GitHub folders
   store a repository's binary files as virtual files on pull.
5. **Later: a blob service for compute output.** An action, or another
   service, stores a result as a virtual file (or a bare blob with a
   reference) for later retrieval, under the same ownership and retention
   rules.

### Settled with Dave (2026-10-27)

- **History.** A binary keeps its old versions; with virtual files these
  are the pointer's revisions in Git, and B2 holds each version once.
- **Retention and backups.** Space binaries go in their own bucket (or a
  prefix with no 30-day lifecycle rule, unlike `web/`). A sweep deletes
  objects that no pointer in any commit, and no download, still uses. The bucket is backed up by restic or B2's own
  versioning with Object Lock.
- **Limits.** 100 MB per file and 5 GB per space to start, counted toward
  the owner.
- **Export.** The space zip export includes binaries, with an option to
  list them only.
- **Which files are binary.** Anything that is not UTF-8 without NUL, as the
  write check already decides, so no list of extensions.
- **B2 files never change.** Reading a byte range works, but there is no
  append or partial rewrite, so binaries are written whole, once, by hash;
  anything that grows (logs, `runner.md`) stays in Git or the database.

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
- Deferred with Dave 2026-10-12: bulk first download, on-demand files like
  OneDrive, automatic three-way merge of conflicts.

## Maintenance

- **Cloudflare beacon** (2026-10-25): Cloudflare Web Analytics injects
  `static.cloudflareinsights.com/beacon.min.js`, which the pages' CSP
  (`script-src 'self'`) blocks, so every page logs a console error. Turn off
  its automatic setup in the Cloudflare dashboard (or allow the host in the
  CSP if the analytics are wanted).
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

From 0.59.1 (2026-10-27):

- **Markdown for agents:** open `https://metatrash.com/docs/README.md?format=md`
  (plain Markdown, no page around it), then `https://metatrash.com/llms.txt`
  (a list of the docs pages with `?format=md` links). Signed out, a private
  space page with `?format=md` goes to sign in. Run a folder action that
  fetches a metatrash.com docs page: its `web/<ref>.md` is the document
  itself, without the page's heading and code block.

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
