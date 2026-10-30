# Roadmap archive

Finished roadmap work, moved out of [roadmap.md](roadmap.md) on 2026-10-29
with the context still useful later: what was agreed, the choices made while
building, and live checks that passed. Release details are in
[CHANGELOG.md](../CHANGELOG.md); the rules as they stand are in
[api-contract.md](api-contract.md).

## Fewer tokens per change

`append`, blobs with `ifBlob` reads, MCP `list since` and shorter tool
descriptions shipped in 0.52.0. All were checked live through Claude Code on
2026-10-24 (append's cases are listed under 0.52.1 in CHANGELOG.md) and work as
documented; 0.52.1 only made append's refusals clearer.

## Account lifecycle

Agreed with Dave 2026-10-06. Done: change email address, sign out everywhere,
transfer ownership, space export (REST, `mt spaces export`, `mtsync export`),
and Download on Your account (0.65.0: `/spaces/<owner>/<slug>/?export`, owner
and members only).

**Delete a space** (0.67.0, T006, 2026-10-30). Decisions (Dave, 2026-10-30):
a deleted space is hidden from everyone at once and kept 30 days, restorable
only by the operator (`mt spaces restore`), then purged; its virtual files in
B2 stay the same 30 days, then go; its address is held for the 30 days, then
freed. It leaves the allowance count at once. The delete is at the bottom of
Manage sharing, needs a recent sign-in and the typed name, and offers the
Download link first. GitHub repositories are never touched. The docs space
can't be deleted while `docsSpace` names it.

**Delete account** (0.71.0, 2026-10-30), as planned on 2026-10-06: refused
while the account owns a space, a step-up and the typed email, then signed
out everywhere with connected apps and opt-ins removed and a notice emailed;
signing in within 30 days cancels; then the daily housekeeping purges it and
sends a final email. One change from the plan: the user row is anonymised
(email `deleted-<id>@deleted.invalid`, no username, `deleted_at`) instead of
deleted, because other people's acknowledgements and form submissions and
purged spaces reference it. Invitations the account accepted become
cancelled; the inviter's list keeps the address they typed.

## Create folder

Done in 0.66.0 (T004, 2026-10-30) without a new operation: the space already
starts a folder when its `AGENTS.md` or `.metatrash.json` is written (the
write tool says so since tool-schema 0.17.3), and mtsync writes
`.metatrash.json` into a new empty local folder. A separate create-folder
tool would only have added tool-definition tokens.

## Sign-in

Passkeys, the authenticator app, recovery codes and the per-account email
switch are done.

## Owner control of files and folders

Agreed with Dave 2026-10-18.

- **Owner-set read-only files:** done in 0.45.0–0.46.0. `locked` keys and
  `readOnly` files in a folder's `.metatrash.json`, each at level `agents`
  or `everyone`.
- **Folder configurator and folder selection in Slate Viewer:** done. The
  tree selects folders, and a folder's Properties (right-click, or Alt+Enter)
  writes its `.metatrash.json`: locks, read-only files, publishing and the
  acknowledge service.

## Forms and quizzes

Design: Dave's "Forms and quizzes: design proposal" doc
(https://claude.ai/artifact/WNBXHhfrc5Ukkvg4De7eU6, 2026-10-23). The code
map, gotchas and phase 3 stay in the roadmap.

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

## Policies

Published versions and read acknowledgements shipped in 0.48.0 (Slate Viewer
0.13.0).

## GitHub folders

- **Binary files**: done in 0.61.0. Pull keeps a repository's binaries
  and files over the file limit as virtual files, and push sends them as
  blobs (Binary files and conversion below).
- **One folder action**: done in 0.54.0 as folder actions (see Folder agent
  tasks below).

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

### Build order (done)

1. **0.55.0**: user agents (table, account page, encrypted keys), the action
   `model` field, owner pays. Done; changing a saved agent came in 0.58.0.
2. **0.56.0**: bring your own key and members' own actions (opt-in,
   notices, adopt). Done. Choices made while building it: the field is
   `"pays": "contributor"` and needs `model`; opting in picks one of your
   agents for that model, whose daily cap applies (no separate cap per
   action); the notice is an email at most once a day per action, linking to
   the action's page, and opting in there runs the skipped file; a member's
   action names them by username (`"member"`), so if an admin renames the
   account, its actions stop running until the name is fixed; files with no clear contributor
   (GitHub pull, space key) run on the owner's agent.
3. **0.57.0**: web tools (Brave `web_search`, `web_fetch`, downloads cached in
   B2 as `web/<id>`) and `every` schedules. Done. Choices made while
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
4. **0.58.0**: change an agent (name, daily cap, a new key) on Services →
   Agents, which the design called "replaceable". Done. The provider,
   model and endpoint stay fixed (another model is another agent, so the
   actions and opt-ins that chose it keep their model); today's use counts
   toward a new cap at once.

### Fewer tokens per web job

The first live news run (2026-10-25) took 26 steps and 341,000 input
tokens, close to the 400,000 cap, because every step resends the whole
conversation. 0.57.1 shortens old tool results and lowers the web defaults
(5 results, 1,000-character previews); a rerun used 195,000 tokens over 34
steps. 0.57.2 sends jobs short tool definitions (4.9 KB instead of 11.2 KB
per step), lets an action list its `tools`, and warns the agent with 3 calls
left; with 0.57.1 to 0.57.4 a news job went from 341,000 to about 37,000
tokens. Prompt caching for MiniMax is still open (roadmap).

## GitHub → space

Done in 0.69.0 (T011, 2026-10-30): the app's `push` deliveries pull every
folder that mirrors the repository and branch with `pull` `auto` (the
default), in spaces whose owner linked the installation, in the background
and as the owner (app `GitHub auto-pull`). Conflicts stop the pull and are
logged. `pull` `agent` opts a folder out. Not built: telling anyone about a
stopped auto-pull other than the log (the next `pending` or pull shows it).

## Binary files and conversion

Streamed uploads (0.68.0, T005, 2026-10-30): `PUT …/content` spools the
body to a temporary file beside the repositories while hashing it and sends
the file to the bucket, so an upload no longer holds up to 100 MB in memory.

Dave's direction, 2026-10-26:

Spaces stay plain UTF-8 text in Git, so they stay small, diffable and
friendly to agents. Binary files (PDF, Word, images, archives, compute
output) live in B2, the way `web_fetch` downloads already do, and never
enter a space's Git history. Clients handle the split for people: mtsync,
Slate Viewer and the server send a UTF-8 file to the space and anything else
to B2, so a person just drops files into a folder. What agents mostly need
from a binary is its content as Markdown, so converting is a first-class
step.

### Build order (done)

1. **0.59.0: convert to Markdown in the downloads store.** Done: Word
   is read in Go, PDF with `pdftotext` (poppler-utils) when the server has
   it, pages marked `<!-- page N -->`; a reference fetched before 0.59.0
   gets its text on the next fetch.
2. **0.59.1: Markdown for agents.** Done: `?format=md` and
   `Accept: text/markdown` on every space, public and `/docs/` page,
   `llms.txt` per space plus `/llms.txt` for the docs (a space's own
   `llms.txt` file wins), and `web_fetch` asking for Markdown first.
   0.59.2 added folder addresses (`/docs/legal/` opens the folder's page,
   or gives its README.md or page list as Markdown) and an `llms.txt` per
   folder. A separate `md.metatrash.com` (Dave, 2026-10-27) can come later
   on top of this if wanted; it would need DNS, TLS and Apache changes and
   nothing more from the server.
3. **0.60.0 to 0.60.2: virtual files.** Done (Dave's design,
   2026-10-27; format and rules in
   [api-contract.md](api-contract.md#virtual-files)). A virtual file is
   content kept outside Git that appears in the space as an ordinary file:
   Git holds a small pointer at its path (`metatrash-virtual 1`, SHA-256,
   size, type, source, refresh), so history, moves, deletes, export, locks
   and folder-action triggers work unchanged. 0.60.0: the pointer, `read`
   resolving (PDF and Word as Markdown by default), content writes refused
   (`virtual_file`), the upload and download route (`…/content`),
   `web_fetch` with `path`, and website pages with Download. 0.60.1: search
   inside virtual text, pictures shown to models, export with binaries
   (`?virtual=list` for pointers), streamed downloads and the daily sweep.
   0.60.2: `configure` (source and refresh; MCP and REST, not for folder
   actions' agents) and refresh on an `every` schedule with the stale flag
   (schema v19). A retention policy was dropped: every version stays.
   Still open: stale marks in `list`, and uploads hold one file in memory
   (up to 100 MB).
4. **0.61.0: mtsync and Slate Viewer handle it transparently.** Done
   and checked live 2026-10-28: mtsync downloads a virtual file's content,
   uploads a new local binary (or text over the file limit) as one, and
   compares by content, so a pointer-only change (configure, refresh) makes
   no conflict copy; a file with a source is put back when edited locally.
   `minVersion` is 0.61.0. GitHub folders pull binaries and files over the
   file limit as virtual files and push them as blobs. mtsync's `export`
   streams to the file. Slate Viewer 0.20.0 lists virtual files, opens them
   in the system's app and offers "Convert to Markdown". A signed-out
   Markdown or `?raw` request to a private space answers 401, so
   `configure` can't store a sign-in page. Fixes since: 0.61.1 sends
   downloads as `application/octet-stream` with `no-transform` (Cloudflare
   was adding its script to HTML content), Slate Viewer 0.20.1 stops
   offering to merge a conflict copy of a linked or locked file, and 0.20.2
   shows an upload as a virtual file as soon as Sync finishes.

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

## Owner checks that passed

- **0.61.0** (2026-10-28): mtsync and virtual files, big files, GitHub
  binaries (a picture pushed in panda-madness/local-repo), sign-in page
  refused.
- **0.60.0 to 0.60.2 basics** (2026-10-27, `vtest/` in dave-zap/metatrash):
  configure, read, history, `not_virtual`, an empty source, search inside a
  PDF.
- **0.52.0** (2026-10-24): append, `ifBlob` and `list since` through Claude
  Code (cases under 0.52.1 in CHANGELOG.md).
- **0.53.0 and 0.53.1** (by 2026-10-25): off-site backups with restic to B2
  set up and running nightly, and the emailed encrypted configuration
  received and opened. The test restore (and the Object Lock prune check, if
  not yet seen) is still open (roadmap).
