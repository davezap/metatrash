# Changelog

## 0.59.1 - Markdown for agents (2026-10-27)

No schema, grant or configuration change. tool-schema.json is unchanged (0.15.1).

- **Every page's Markdown at its own address** (docs/roadmap.md, Binary files and conversion): add `?format=md` to a page in a space, the public space or `/docs/`, or ask for it with `Accept: text/markdown`, and the site sends the file as it is (`text/markdown; charset=utf-8`; other text files as `text/plain`) instead of the HTML page around it. The access rules are the page's own: a private page still needs its member signed in. Browsers never ask for Markdown, so they get the page as before. The Markdown copies are `noindex` and sandboxed; each HTML page names its Markdown in a `Link` header and a `<link rel="alternate" type="text/markdown">`.
- **`llms.txt`**: `/docs/llms.txt` (also at `/llms.txt`, where AI tools look first), `/spaces/public/llms.txt` and `llms.txt` at the top of any space you can read list its Markdown pages, top folder first and then one section per folder, each linking the page's `?format=md` address. A space with its own `llms.txt` file gets that file instead.
- **`web_fetch` asks for Markdown first** (`Accept: text/markdown, text/html;q=0.9, …`): a Metatrash page comes back as the document itself rather than the page with the Markdown inside a code block, and other sites that keep Markdown versions send those. Sites that don't still send HTML, converted as before.
- Tested: the Accept ranking (browsers' headers, Markdown first, Markdown ranked below HTML, `q=0`); llms.txt's layout and escaped links; against MariaDB, a private page's Markdown (signed out to sign in, a stranger not found, the owner by `?format=md` and by Accept, a browser's Accept still HTML), then readable on the web (anonymous, the space's top, the bare space address keeping `?format=md`, a JSON file as plain text, other queries and missing files not found, the `Link` header and head link, `llms.txt`), `/docs/` and `/llms.txt`, a space's own `llms.txt`, and `/llms.txt` without a docs space; the public space's Markdown, plain text and `llms.txt`.

## 0.59.0 - PDF and Word downloads as Markdown (2026-10-27)

No schema, grant or `accounts.json` change. tool-schema.json is unchanged (0.15.1). **Optional:** install `poppler-utils` on the server (`sudo dnf install poppler-utils`) for PDF text; `metatrash check` says whether the service found `pdftotext`.

- **Word files as Markdown** (docs/roadmap.md, Binary files and conversion): a `.docx` that `web_fetch` downloads gets `web/<ref>.md` beside the original, as web pages do. Headings (Title, Heading 1–6), bulleted and numbered lists, tables, bold, italic, links and line breaks come through; pictures, text boxes and tracked deletions are left out. The first heading is the reference's title. Read in Go, with each part limited to 64 MB unpacked and the Markdown to 4 MB.
- **PDF files as Markdown**: the text comes from `pdftotext` (60 seconds, 500 pages and 4 MB of text at most, run on a temporary copy without a shell), page by page, each page after the first starting with `<!-- page N -->` so an agent can say where something is. A scanned PDF has no text (no OCR) and the result's `note` says so; so does a server without `pdftotext`, which keeps PDFs without text as before.
- **The rest works unchanged**: `read` by line range, `search` with `path`, and `move` to keep the Markdown in the folder. Reading the original `.pdf` or `.docx` says to read its `.md`. A PDF or Word file fetched before 0.59.0 has no text; fetching it again downloads it anew (even when the site says it has not changed) and adds it.
- Tested: a Word file built in the test (title, headings, bold and italic runs, a link, bulleted, nested and numbered lists, a table with a `|` and two lines in a cell, a line break, a picture with a text box and a deleted run left out), a file that is not a Word file; PDF Markdown (pages, trailing spaces, blank lines, a `#` at a line start escaped, blank pages); a two-page PDF through the real `pdftotext` and a broken one refused; `web_fetch` of a `.docx` sent as `application/octet-stream`, reading its Markdown and its original, a PDF without `pdftotext` (kept, `note` says why), then with it (downloaded again despite the site's 304, page marker in the preview), and a plain 304 after that.

## 0.58.0 - Change an agent's key, name and daily cap (2026-10-26)

**Grants only**: run `deploy/account-grants.sql` again before this binary starts (`UPDATE (name, daily_cap, key_sealed, key_hint)` on `metatrash_agents`); `metatrash check` lists them as missing until then. No schema or `accounts.json` change. tool-schema.json is unchanged (0.15.1).

- **Change an agent** (the rest of "bring your own key", docs/roadmap.md): each agent on Your account → Services → Agents has a pencil that opens its name, its daily token cap and a new API key. Leave the key empty to keep the saved one; a new key is checked like a new agent's, sealed the same way and never shown again, and the row's "key ending" changes to it. Until now a new key or cap meant removing the agent and adding it again, which also dropped every action its owner had opted in to pay with it (Dave hit the 2,000,000 cap on 2026-10-25 and had to add a second agent).
- **What stays**: the provider, model and endpoint (an agent for another model is a new agent, so actions and opt-ins that chose this one keep the model they chose), whether it is the default, its opt-ins, and today's use, which counts toward the new cap straight away.
- Tested: the form's checks (name, cap, key) and that an empty key keeps the sealed one; against MariaDB with the runtime grants, renaming and changing the cap, a new key that Test then uses, the list showing the new key ending and "No cap", a bad cap refused, a clashing name refused, and another account's change not found.

## 0.57.6 - Back to the link after signing in; printout image setting (2026-10-26)

No schema, grant or configuration change. tool-schema.json is 0.15.1 (the write description mentions `print`). Deploy this before setting a printout image in Slate Viewer 0.19.0: older servers refuse the `print` key.

- **Signing in goes back to the page that asked for it** (Dave): a signed-out browser sent to `/login` from a space page or an account page (a link in a printed PDF or an email) is remembered in a short-lived `__Host-metatrash-return` cookie (20 minutes, Strict, HttpOnly), and every way of signing in (email code, passkey, authenticator app, recovery code) then goes back there instead of to Your account. Only `/spaces/...` and `/account/...` pages of this site are remembered or followed, so the cookie can't send anyone elsewhere; an app connection waiting to finish still comes first.
- **`print` in `.metatrash.json`** (Dave): `{"print": {"image": "https://example.com/logo.png"}}` names an image Slate Viewer prints centred above the title on the first page of the folder's documents. Only the address is stored; the files never change. The address must be `https` with a host and no credentials, up to 2,000 characters. Inherited like `purpose`, and lockable (`locked: {"print": ...}`). docs/api-contract.md, Folder settings.
- Tested: sign-in returns to the remembered page and forgets it, a plain sign-in still goes to Your account, a forged cookie naming another site, `//host`, `/login` or a backslash path goes to Your account; `print` accepts an https image and refuses http, a bare name, credentials, spaces and unknown fields.

## 0.57.5 - Space links from a PDF open the page (2026-10-26)

No schema, grant or configuration change.

- **Links clicked in a PDF or another site open the space page**: the sign-in cookie is `SameSite=Strict`, so a browser leaves it off a navigation that starts outside metatrash.com, and a private space page then sent a signed-in reader to the login page (Dave: links printed from Slate Viewer landed on `/account` or `/login`, while the same URL pasted into the address bar worked). A space page reached cross-site without the cookie now answers with a tiny page that reloads itself from metatrash.com, which does carry the cookie; a reader who isn't signed in still ends at the login page.
- **Slate 0.19.5** for printing on the site: Mermaid diagrams fit one page, a `##` section that would spill starts on a new page, headings stay with what follows, link-only lines and blank trailing lines are left out.
- Tested: a cross-site request without a cookie gets the reload page for a private page and a missing space; the cross-site sign-in and account-switching test still passes.

## 0.57.4 - A run cut off at its step limit is failed (2026-10-26)

No schema, grant or configuration change.

- **A job that used up its `steps` is recorded as failed**, even when the model then replies with a summary: its calls were refused, so it did not finish. A live news run ended "done" in `runner.md` with nothing written ("the write did not go through — the job hit its tool-call limit"). The model's summary is still recorded under the error.
- Tested: a job refused at its limit that then replies is failed, with its summary in `runner.md`.

## 0.57.3 - Tighter folder-action prompts (2026-10-25)

No schema, grant or configuration change. tool-schema.json is unchanged (0.15.0).

- **AGENTS.md only when asked** (Dave): a folder's `AGENTS.md` is written for agents doing general work there, so a job with a set task no longer gets it in its prompt, which saves its size on every step. `"agentsMd": true` on an action puts it back; the agent can still `read` it either way. Actions that relied on AGENTS.md for their instructions need `"agentsMd": true`, or those instructions moved into `do`.
- **Read only what the task needs**: the runner's instructions now say to follow the format the instructions give rather than reading other files to copy their style (a news run read the whole of `news.md` to match "the file's existing format").
- Tested: the prompt carries AGENTS.md with `agentsMd` and not without it.

## 0.57.2 - Leaner tools for folder actions (2026-10-25)

No schema, grant or configuration change. tool-schema.json is unchanged (0.15.0); MCP apps see the same tools as before.

- **Shorter tool definitions for jobs**: every step resends the tool definitions, about 3,000 tokens of the MCP descriptions and schemas, which was most of a 40-step job's 225,000 tokens. Jobs now get a one-line description per tool and argument, without `space`, `strict`, search's `revision` or the length and pattern limits: 4.2 KB for the file tools instead of 10 KB, 4.9 KB with the web tools instead of 11.2 KB. Every call is still checked against the full schema, so a refusal still says what was wrong.
- **`tools` on an action** lists the only tools its agent gets, such as `["read", "write", "web_search", "web_fetch"]` for a news digest. Unknown or repeated names are refused when the settings are saved, and a call to an unlisted tool is refused. Left out, the agent gets every tool as before.
- **A warning before the step limit**: with 3 tool calls left, the runner tells the agent to write its result now. A live scheduled run used all 40 calls looking for a fresher story and ended with nothing written.
- Tested: the definitions' size and content, the full schema still enforcing limits (including a web reference in search), an action's tool list (the model sees only those, the web preamble follows it, a delete is refused), the warning once at 3 calls left and the write after it, and the `tools` checks.

## 0.57.1 - Fewer tokens per folder-action job (2026-10-25)

No schema, grant or configuration change. tool-schema.json is unchanged (0.15.0).

- **Old tool results are shortened**: every step of a job sends the whole conversation again, so the first live scheduled web run (26 steps) used 341,000 input tokens, close to the 400,000 cap. Now, once a result is older than the newest two turns of results and over 600 bytes, the runner replaces it with its small fields (paths, states, blobs, titles, URLs, numbers, and how many items a list held) and a note that the agent can call the tool again for the rest. Long text, lists and images go. The model's own turns are never changed, and URLs the agent has seen stay fetchable.
- **Smaller web defaults**: `web_search` returns 5 results unless the agent asks for more (was 8), and a fetched page's preview is about 1,000 characters (was 1,800).
- Tested: shortening keeps a read's state, path and blob and a fetch's reference, drops an image, leaves small results, the instructions and the newest two turns alone, and never shortens twice.

## 0.57.0 - Web tools and schedules for folder actions (2026-10-25)

**Schema v18 and grants** (`deploy/account-schema-v18.sql`, then `deploy/account-grants.sql`) before this binary starts. **Optional `accounts.json` additions** for the web tools: `agents.search` (a Brave Search API key file) and `agents.downloads` (a Backblaze B2 bucket of its own with a 30-day lifecycle rule; docs/deployment.md, Folder actions). Without them actions run as before, without the web. tool-schema.json is unchanged (0.15.0). CI applies v1–v18: move the new `ci.yml` from the repository root into `.github/workflows/`. New dependency: `golang.org/x/net` (HTML parsing).

- **`web_search`** (Dave's design, docs/roadmap.md): an action's agent can search the web through Brave and gets titles, URLs and snippets. Metatrash runs it itself, so it works the same on MiniMax and Claude.
- **`web_fetch`**: downloads one URL into a reference the agent uses like a file, never into Git: `web/<ref>.md` is a page's text as Markdown (headings, lists, links and images with full addresses, tables, code; scripts, forms and hidden parts left out), `web/<ref>.html` the page itself; text files keep their own text, and reading an image shows it to the model. `read` (by line range), `search` (with `path`) and `move` (copies a page's text into the folder) work on references; `list` never shows them. PDF and Word files are kept but their text waits for 0.58.0.
- **Only URLs already in the conversation can be fetched** (in the instructions, `AGENTS.md`, a file read, search results or an earlier page), so a planted instruction cannot send data out through an invented address. GET only, public addresses only (checked on every connection, so after each redirect), standard ports, 25 MB and 30 seconds at most.
- **The downloads cache**: each download is stored once in the bucket (by its SHA-256), references belong to the account whose agent fetched them and last 29 days, and fetching a URL again asks the site whether it changed (ETag, Last-Modified) before downloading. Each payer's jobs may make 200 searches and 500 fetches a day unless `dailyLimit` says otherwise. `"web": false` on an action turns the tools off.
- **`every` schedules**: `hour`, `hour :30`, `day 06:00`, `weekday 08:00`, `monday 08:00` or `month 1 09:00`, instead of `on`. The prompt says it is a scheduled run; `runner.md` shows `- Run: scheduled (every day 06:00)`. The next run of each is kept in the database, so a restart neither skips nor doubles one (a run missed while the service was down happens once, on start). A scheduled action cannot say `"pays": "contributor"` (nobody brings a file); a member's scheduled action runs on their agent.
- **Each space's own time zone**: the clock icon on a space's row in Your account sets it (with every zone the browser knows suggested), and a new space takes its creator's browser zone. Schedules and `runner.md` times use it; spaces without one use `agents.timezone`. Changing it moves the next runs.
- **Schema v18**: `metatrash_action_schedules`, `metatrash_web_objects`, `metatrash_web_refs` and `timezone` on spaces. `metatrash check` reports v18 and checks the new key files are readable by the service.
- One choice differs from the design doc: a file kept from the web does not start the folder's `on` actions, because no change the runner makes ever does (the guard against jobs setting each other off).
- Tested with a fake model, a fake search and a local web server: search, a refused unseen URL, fetching through a redirect, the Markdown, reading by line range, searching a reference, a text file and an image (shown to the model), keeping a page's text in the folder (and refusing an image), an unknown reference, refetching (304, same reference), another account's references not found, `"web": false`; addresses and ports refused without a request; the S3 signature against the AWS example and put/get against a fake bucket; the settings; schedule parsing and next runs (weekdays, months, a zone change), a scheduled job end to end and its removal. Against MariaDB with the runtime grants: references and objects (another user's not found, expiry), schedule rows (a run taken once), and the time zone form and a new space taking the browser's zone.

## 0.56.0 - Bring your own key and members' own actions (2026-10-25)

**Schema v17 and grants** (`deploy/account-schema-v17.sql`, then `deploy/account-grants.sql`) before this binary starts. No `accounts.json` change. tool-schema.json is unchanged (0.15.0). CI applies v1–v17: move the new `ci.yml` from the repository root into `.github/workflows/`.

- **Who pays, per action** (Dave's design, docs/roadmap.md): `"pays": "owner"` (the default) runs every job on the space owner's agent, as before. `"pays": "contributor"` runs a job on the agent of whoever brought the file, but only once they have opted in to that action; it needs `model`, so the model is the same whoever pays. Nothing falls back to another key or model: a file from someone not opted in runs nothing, `runner.md` records it as skipped with the link to opt in, and they get an email (at most once a day per action). The owner's own files, and files nobody in particular brought (a GitHub pull, a space key), run on the owner's agent.
- **Opting in**: the email links to `/account/actions/<job>`, a page only the person whose file it was can open. It shows the action's model, steps and instructions (which only the owner can change) and lets them choose one of their agents for that provider and model; opting in runs the skipped file straight away. Their jobs count toward that agent's daily cap. Services → Agents lists Actions you pay for, with Opt out; removing the agent opts out of everything it paid for. If the chosen agent no longer runs the action's model, the job is skipped and they are told.
- **Members' own actions**: a member with a username can add, change and remove actions marked `"member": "<their username>"`, writing as themselves through Metatrash Sync or Slate Viewer, and no others. Such an action runs only for that member's files, on their agent. The owner can remove it or adopt it (take out `member`; it becomes theirs and they choose who pays), but cannot add or change one in a member's name. Agents still cannot change actions at all.
- **Signed by whoever pays**: changes made on someone else's agent are signed "MiniMax (agent) for alice", the runner's prompt says on whose behalf it runs, and `runner.md` adds `- Paid by: alice`. The agent still works within the owner's folder rules and locks.
- **Schema v17**: jobs record who brought the file (`contributor_id`); `metatrash_action_optins` holds the opt-ins. `metatrash check` reports v17.
- Tested with a fake model: not opted in (skipped, notice, link), opting in and the rerun on the contributor's key, the owner's own file and a space-key file on the owner's agent, a chosen agent that changed model, a member's own action (runs only for their files; skipped with a notice without an agent), GitHub pulls having no contributor, the `pays`/`member` checks, and who may change which action (member, owner, member without a username, agents). Against MariaDB with the runtime grants: the whole opt-in flow through the account pages (the page is only the contributor's, the wrong model's agent is refused, opt in, rerun, Actions you pay for, opt out), another user's agent refused, and removing an agent removing its opt-ins.

## 0.55.0 - Users' own agents for folder actions (2026-10-25)

**Schema v16 and grants** (`deploy/account-schema-v16.sql`, then `deploy/account-grants.sql`) before this binary starts. **`accounts.json` changes**: the `agents` section keeps only `enabled`, `timezone` and the new `keyFile` (64 hex digits, `openssl rand -hex 32`, root:metatrash 0640); `owners`, `provider`, `baseURL`, `model`, `apiKeyFile`, `maxTokens` and `dailyTokenCap` are refused with a message. Until the owner adds an agent, their actions are recorded as skipped. tool-schema.json is unchanged (0.15.0).

- **Agents in Your account → Services** (Dave's design, docs/roadmap.md): each person adds their own agents, each with a name, provider (MiniMax or Anthropic), model, API key, optional https endpoint and daily token cap (2,000,000 by default, 0 for none). They can have up to 10 and make one the default. Test makes one tiny call and shows the reply. The key is encrypted in the database (AES-256-GCM, bound to the user and the agent) and never shown again; the list shows only its last four characters and today's use.
- **The owner pays**: a job runs on the space owner's agent, whoever brought the file. This replaces the server's single key and `agents.owners`. Other people's spaces now run actions on their own agents, and an owner with none gets "skipped: … has no agent for this action" in `runner.md`.
- **Action `model`** (optional): `"model": "minimax/MiniMax-M3"` picks the owner's agent with that provider and model; left out, the default agent runs it. The model in `runner.md` comes from the agent.
- **Daily cap per agent** instead of per space, counted over all that agent's jobs; jobs record which agent ran them (`agent_id`).
- **Endpoints are checked against reaching the server's own network**: https only, public addresses only, checked when added and again on every connection, and redirects are not followed.
- **The agent changes only what it is asked to** (Dave's first live run renamed `README.md`): the runner preamble now says to change only the files the instructions call for, never `AGENTS.md` unless told, to put anything else in the summary, and to keep the summary to three to five sentences.
- Tested: sealing and opening keys (a key copied to another agent does not open), form validation, endpoint checks (loopback, private, http refused; a non-test client refuses loopback), adding, listing, default and remove against MariaDB (first is default, removing it promotes the next, at most 10, names unique per user), the Agents page and its forms, agent choice by `model` and default, skipped without an agent, the per-agent cap, refusal of the old settings.

## 0.54.2 - Only the owner sets folder actions (2026-10-25)

No database, grant or configuration change; tool-schema.json is unchanged (0.15.0). A missing `agents.dailyTokenCap` now means 2,000,000 tokens a day per space; set `0` to keep no limit.

- **A member could spend the owner's tokens** (Dave): anyone with write access could add an action to a folder's `.metatrash.json`, and an everyone-level lock could be lifted by any person, members included. Now only the space owner, writing as a person through Metatrash Sync or Slate Viewer, can add, change or remove `actions`. Members and every agent (the owner's own too, so a document cannot talk an agent into adding one) get `forbidden`; they can still change the file's other settings, and members' files still run the owner's actions.
- **Daily token cap by default**: 2,000,000 tokens (in and out) per space per day unless `dailyTokenCap` says otherwise, so a runaway action costs a known amount.
- Roadmap: the agreed design for user-owned agents, who pays, bring your own key and members' own actions (docs/roadmap.md, Folder agent tasks), replacing `agents.owners` later.
- Tested: owner as a person can set and clear actions; a member, the owner's agent and an anonymous write cannot, by write or edit; a member can change `purpose` while the actions stay; the default and zero caps.

## 0.54.1 - Folder actions run only for listed owners (2026-10-25)

No database or grant change. **`accounts.json` must gain `agents.owners`** (for example `"owners": ["dave-zap"]`) before this binary starts while `agents` is enabled. tool-schema.json is unchanged (0.15.0).

- **The provider key paid for every owned space** (Dave): any account's own space with an action would queue jobs on the server's key. `agents.owners` now lists the usernames whose spaces run actions, and is required. Other owners' actions are stored but queue no job (the service log says so), a job queued before a space changed hands is recorded as skipped, and an owner without a username never matches. Members' files in a listed owner's space still run on that owner's key, as designed. The configured spaces, the public space among them, already refused actions and still do.
- **`metatrash check` run with sudo checks the key file** ("agents key"): it fails when the `metatrash` account cannot read `apiKeyFile`, and prints the `chown`/`chmod` to fix it. Before, the check passed as root while the service exited at start (Dave). docs/deployment.md gives the commands.
- **`provider` ignores case and surrounding spaces**, and the error for a wrong value names the value it got.
- The account features line names the owners: "folder actions (MiniMax MiniMax-M3 for dave-zap)".
- Tested: other owners and owners without a username queue nothing and call no model; a queued job for an unlisted owner is skipped; settings validation; the key check as root against `nobody`. Live: 0.54.0 ran end to end on the server (trigger, queue, provider call, runner.md) and stopped at MiniMax's 402 insufficient balance.

## 0.54.0 - Folder actions: runner and text-file triggers (2026-10-25)

Needs account schema v15 (`deploy/account-schema-v15.sql`) and the new grants in `deploy/account-grants.sql` before the binary starts. tool-schema.json is 0.15.0 (the write description mentions `actions`). The runner is off until `agents` is enabled in `accounts.json`. Setup: docs/deployment.md, Folder actions (agents); rules: docs/api-contract.md, Folder actions.

- **`actions` in `.metatrash.json`** (Dave, design 2026-10-25): each action has a `name`, `on` (patterns relative to the folder; a trailing `/` matches a subfolder at any depth), optional `when` (`arrive`, the default, or `change`), `do` (instructions, or the path of a `.md` file holding them) and optional `steps` (default 40, up to 200) and `web`. Actions work only in your own spaces (connected through `/mcp/account`), are never inherited, and can be locked like other keys. `every` is reserved for 0.55.0 and refused for now.
- **The runner**: after each commit that is not its own, every file that arrived (or changed, for `when: change`) and matches an action queues one job. A job runs an agent pinned to the folder: it sees only the folder's files, through the same tools as MCP, cannot change `.metatrash.json` or `runner.md`, and reads the folder's `AGENTS.md` first. Its changes are signed by the space owner with App MiniMax or Claude. The outcome (status, file, what changed, steps, tokens, the agent's summary or the error) is appended to the folder's `runner.md`, signed "Metatrash runner".
- **Providers**: anything that speaks the Anthropic Messages API; `minimax` (https://api.minimax.io/anthropic, default model MiniMax-M3) and `anthropic` (default claude-sonnet-5-5). Retries twice on 408, 409, 429 and 5xx.
- **Limits**: one job per space at a time and two at once; 20 jobs per folder per hour (the 21st is recorded as skipped); 400k input tokens and 10 minutes per job; tool results cut at 100,000 bytes; optional `dailyTokenCap` per space. Jobs left running when the service stops are marked failed on the next start.
- **Account schema v15**: `metatrash_agent_jobs` holds the queue and every job's outcome. `metatrash check` reports v15.
- Tested with a fake model (tidy-draft example, limits and failures, the hourly cap, unpinning of results, the HTTP client against a TLS test server) and against MariaDB with the runtime grants (queue, claim, finish, restart). **Not yet run against MiniMax.**

## 0.53.2 - Emailed configuration works on Amazon Linux (2026-10-25)

No database, grant or configuration change, and the binary is unchanged apart from its version.

- **`metatrash-backup-config` failed on the live server** with "error running '/usr/bin/gpg-agent': probably not installed" (Dave): Amazon Linux 2023 ships `gnupg2-minimal`, whose gpg needs an agent it doesn't include, even to encrypt with a passphrase. The setup in docs/deployment.md (Backups → Emailed configuration) now starts with `sudo dnf swap -y gnupg2-minimal gnupg2-full`, and the script stops at once with that command when `gpg-agent` is missing.
- **Sending failed next** with curl's "Protocol "smtp" is disabled" (Dave): Amazon Linux's `curl-minimal` has no SMTP. The script now sends with Python's `smtplib` (STARTTLS on 587, TLS on 465, run as `python3 -I`), which needs nothing installed; the password is read from `smtpPasswordFile` inside Python. Checked with `smtplib` replaced by a stub: STARTTLS then login with a password holding `"` and `\`, the message parses with the attachment, and the archive decrypts.

## 0.53.1 - Emailed configuration (2026-10-25)

No database, grant or configuration change, and the binary is unchanged apart from its version. tool-schema.json is unchanged (0.14.0). Setup: docs/deployment.md, Backups → Emailed configuration.

- **`deploy/backup-config.sh`** (installed by `upgrade.sh` as `/usr/local/sbin/metatrash-backup-config`) emails the server's configuration every day to `CONFIG_MAIL_TO` (david@metatrash.com) (Dave): `/etc/metatrash` with `totp-key`, `/etc/metatrash-backup` without the passphrase file, and the service's unit and override, as one tar archive encrypted with gpg (AES-256) under the passphrase in `/etc/metatrash-backup/config-passphrase`. It sends through the SMTP settings in `accounts.json` with curl (STARTTLS on 587, TLS on 465); the password goes to curl on stdin, never on the command line. The body says how to open it and how the last B2 backup run ended.
- **`deploy/metatrash-backup-config.service` and `.timer`** run it daily at about 05:00, separately from the B2 backup so either keeps working without the other. `deploy/backup.env.example` gains `CONFIG_MAIL_TO`, `CONFIG_PASSPHRASE_FILE` and `CONFIG_MAIL_PATHS`.
- Checked in a scratch copy with curl replaced by a stub: the message parses as MIME with the attachment, the archive decrypts with the passphrase and lists `accounts.json`, `smtp-password`, `totp-key` and the backup settings without the passphrase file, and a password with `"` and `\` reaches curl's config intact. Not sent through Gmail yet.

## 0.53.0 - Off-site backups (2026-10-25)

No database, grant or configuration change, and the binary is unchanged apart from its version. tool-schema.json is unchanged (0.14.0); mtsync and Slate Viewer are unchanged. The backup needs a one-time setup (B2 bucket, restic, settings, timer): docs/deployment.md, Backups.

- **`deploy/backup.sh`** (installed by `upgrade.sh` as `/usr/local/sbin/metatrash-backup`) backs up everything needed to rebuild the server to an encrypted restic repository on Backblaze B2 (Dave). Each run dumps the account database (`mariadb-dump --single-transaction`), then makes a full `git bundle` of `main` for each space that changed since its last bundle (unchanged ones keep theirs, so every snapshot holds every space), copies `/etc/metatrash` without `totp-key` plus the service's unit and override, writes a `MANIFEST.txt` (time, version, schema, each space's commit) and uploads with `restic backup`. Dumping first means every space the dump lists as ready is in the same backup. Nothing writes to the live repositories and the service keeps running.
- **Retention**: 14 daily, 8 weekly and 12 monthly snapshots, forgotten, pruned and checked (5% of the data read back) on Sundays. B2 Object Lock (30 days) keeps old versions even if the server's key is used to delete them.
- **`deploy/metatrash-backup.service` and `.timer`** run it nightly at about 03:30; `deploy/backup.env.example` holds the settings (repository, B2 key, optional health-check URL, overrides).
- A space that fails to bundle is reported and the others still upload; the run then exits 1, and the optional `HEALTHCHECK_URL` is pinged at `/fail`.
- docs/deployment.md: Backups (what a run does, what to keep off the server, setup, EBS snapshots as a second layer, looking at backups, restoring one space or a whole server, signing everyone out after a restore, test restores); the export, upgrade and recovery notes point at it.
- Checked in a scratch copy with a stub `mariadb-dump`, restic 0.16 and a local repository: first run bundles every space, a second run bundles none, a changed space alone is bundled again, a removed repository's bundle leaves the staging folder, a repository without `main` is reported and the run exits 1; a space rebuilt from the restored snapshot has the same `main` and passes `git fsck --strict`. Not run against MariaDB or B2.

## 0.52.2 - Document pages fill the window (2026-10-24)

No database, grant or configuration change. Deploy the binary only. tool-schema.json is unchanged (0.14.0); mtsync and Slate Viewer are unchanged.

- **The page no longer scrolls on document pages** (`/docs/`, owned spaces, the public explorer; Dave): the page is exactly the window's height, so the browser shows no page scroll bar. Header, space bar and footer keep their height and stay in view, and the document (its tab and heading, then Slate's toolbar, text and status bar, or the plain-text view) takes all the height between, scrolling inside itself. Slate's status bar now sits on the footer instead of part way down with empty space below it.
- **The explorer tree scrolls on its own** when it is taller than the space between header and footer, and the open file's entry is scrolled into view when the page loads. On phone widths the tree sits above the document and takes at most a quarter of the window.
- The home, About and account pages are unchanged (the layout applies only to pages with the explorer, marked `class="viewer"` on `body`).
- Checked in Chromium at 1400×720, 760×900 and 390×780 with a long Markdown file, a JSON file and a tree longer than the window: no page scroll, wheel scrolling moves only the document or only the tree. The service tests pass.

## 0.52.1 - Clearer append refusals (2026-10-24)

No database, grant or configuration change. Deploy the binary only. tool-schema.json is unchanged (0.14.0).

- **append checked live** on 0.52.0 through Claude Code: a new file, adding at the end of a file without a final line break, before line 1 and line 2, one past the last line, several lines with and without a final break, `ifBlob` before and after, `list since`, history (`write`, by the agent), a new folder (its AGENTS.md and .metatrash.json came with it and went with its last file), a bad `.metatrash.json` and a `readOnly` file refused. All behaved as documented; the blob matched `git hash-object`.
- **Refusals say more**: `line` for a missing file now says the file does not exist and to leave `line` out to create it (was the generic "File or revision not found."); a `line` past the end of a one-line file says "1 line"; the `read_only_file` refusal lists append among what agents (or nobody) can do to the file.
- Tests: append to a read-only file and to a new name a `readOnly` pattern covers; the one-line wording.

## 0.52.0 - Fewer tokens per change: append, unchanged reads (2026-10-24)

No database, grant or configuration change. No new Go modules. Deploy the binary only. tool-schema.json is 0.14.0; mtsync and Slate Viewer are unchanged and keep working (results gain a `blob` field they ignore). Agents that connected before should reconnect, or remove and re-add the connector, to see the new tool and arguments.

- **`append`** (MCP tool, `POST /api/v1/spaces/{space}/append` and the account route): `{path, text, line?}` adds text to a file without reading it. Without `line` it goes at the end and a missing file is created; with `line` it goes before that line (1 = top), for files whose newest entries come first under a title, such as a changelog. The text starts on a line of its own and, before an existing line, ends with a line break; LF breaks become CRLF in a CRLF file. No `ifInState`: it runs in the write queue on the latest state. Otherwise write's rules and result; it counts as one write. A `line` past the end is `invalid_request`, a `line` for a missing file `not_found`.
- **Blobs on results**: read, write, edit, append, move and delete results give the file's `blob` (its Git blob hash).
- **Unchanged reads**: `read` with `ifBlob` answers `unchanged: true` with empty `text` when the file still has that blob, so an agent can re-check a file it already holds without reading it again (MCP, and REST `?ifBlob=`).
- **MCP `list` takes `since`**, as REST has since 0.37.0: only the files added or changed since a state (with `blob`) and the paths removed, so an agent coming back to a space need not list it all.
- **Shorter tool descriptions**: the tool definitions an agent loads per session went from about 19.5 KB to 15.3 KB (input schemas and descriptions, all 13 tools of `/mcp/account`, `append` included), and the account server instructions from 1.4 KB to 1.1 KB. The `.metatrash.json` rules in `write` are now a compact list of keys (refusals still say which rule), and the GitHub tools no longer list their errors. The instructions now point agents at `append`, `ifBlob` and `list since`.
- docs/api-contract.md (Operations, "Search, line ranges and edit", list), docs/architecture.md, README.md and docs/roadmap.md (what is left: measuring what clients pass to the model).
- Tests: `insertText` (end, top, middle, one past the end, past the end, empty file, no final line break, CRLF); append, `ifBlob` and the blob on results over REST and MCP, MCP `list since`, append's refusals. The service and mtsync tests pass (without MariaDB; nothing touches the database).

## 0.51.1 - mtsync show takes the revisions history prints (2026-10-24)

No server change. No schema, grant or configuration change. Only mtsync changes.

- **`mtsync show` accepts short revisions**: the 12 characters the plain `mtsync history` listing prints (or any 7 or more, any case), not only all 40 from `-json`. mtsync finds the revision in the file's history (its newest 1,000 changes) and refuses a prefix that matches two changes. A revision from the file's history is read at the path the file had then, so the file's current name works for revisions from before a rename; a delete entry says it has no text. Given a name the file no longer has (history's `file`), a short revision is refused with a hint and all 40 characters work as before.
- Tests: short, 7-character, upper-case and full revisions by the current name across a rename, the move itself, a short revision with the old name, one not in the history, and a full revision the path didn't exist at.

## 0.51.0 - Quizzes cover several policies (2026-10-24)

No schema, grant or configuration change. Deploy the binary only. mtsync 0.51.0 reads the new `policies` shape; Slate Viewer 0.18.0 needs both.

- **A quiz covers several policies**: `covers` is a comma-separated list of files (`wfh-policy.md`), patterns (`TG-ISG-*.md`, one folder level) and folders (`firmware/`, all of it), from the quiz's folder or, starting with `/`, the top of the space, in any published folder. Without `covers` a quiz covers the Markdown files of its folder.
- **Passing a quiz is separate from reading** (Dave): the quiz is now a tracked item of its own, outstanding until passed (`quiz` on the quiz file, with `covers`; covered files list it in `quizzes`). Covered files are confirmed with I Have Read This again; acknowledging a quiz itself is `quiz_required`. A new version of a policy asks to read it again, not for the quiz.
- **`readFirst`** (default `true`): the quiz is refused with `read_first` (409) until the person has confirmed reading every file it covers; `readFirst: false` allows it any time. The quiz's `toRead` lists what is left.
- mtsync `policies` prints a quiz's state, how many files it covers and how many to read first.
- Tests: covers entries, patterns, folders and `/`; against MariaDB, read-first, a quiz and its policy done separately, a policy republish (read again, quiz still passed), a changed quiz, and a quiz covering another published folder with `readFirst: false`.

## 0.50.0 - Reviews and quizzes (2026-10-23)

**Database schema v14** (`deploy/account-schema-v14.sql`, then `deploy/account-grants.sql`): apply both before the new binary starts; it refuses to start on v13. No configuration change. No new Go modules. Tool schema unchanged. mtsync 0.50.0 adds `review` and `submit -published N`, and `policies -json` passes on `covers` and `quiz`; Slate Viewer 0.17.0 uses them. **CI**: `.github/workflows/ci.yml` must apply schema v1–v14 and set `schema_version = 14`.

- **Every filed copy is reviewed**: copies and receipts have a `status`, `submitted` (waiting), `approved` or `declined`. A reviewer (read-write access to the review space) reviews a copy with `POST .../reviews` on that space, `mtsync review` or Slate Viewer: Approve or Decline with a note for a form. The receipt and the copy are updated (the copy is filled in again with `reviewedBy`, `reviewedAt` and `note`); My Submissions shows the status and note. New copies also carry `templateBlob` and `submission` (the receipt id).
- **Quizzes**: a form with `quiz: true`, `covers` (the policy file it tests), `passMark` (default 100), `retryAfter` (default `1h`) and `unmarked` (fields that are not questions), in a folder whose `acknowledge` service asks people to read it. The answer key is a reviewer's filled-in copy named `<space slug>-<form>-answers.md` in the quiz's `submitTo` folder, valid for the quiz text it was made from. The server marks choices, drop-downs, yes/no, numbers, dates and times on send; free text waits for a reviewer's tick. The copy shows ✓ and ✗, `marks` and `score`; at or over the pass mark it is approved. Without a key only reviewers can send the quiz (`no_answer_key`); a passed quiz, one waiting for review, or a fail within `retryAfter` can't be sent again (`quiz_passed`, `quiz_waiting`, `retry_later`).
- **Passing a quiz confirms the policy**: the quiz is not a file to read (no dot), and the file it covers counts as read while the person has passed the quiz as published now. A new version of the policy alone does not ask again; a changed quiz does. I Have Read This is refused for a covered file (`quiz_required`). Acknowledgements and the readers report show each person's quiz state.
- **Published forms are what's sent**: in a folder with a published version, the server checks answers against the published text (people see it in Slate Viewer), and the copy's `templateRevision` is the commit that published it. `"version": N` in the request names the version answered; an older Slate Viewer, which sent the draft, is refused with `template_changed` when the draft differs.
- Tests: quiz keys, marking, ticks in the copy and reading copies back; against MariaDB, a quiz from no key, through the reviewer's key, a fail with a reviewer's tick and note, the retry wait, a pass that clears the policy, a policy-only republish (still read) and a changed quiz (asks again, needs a new key), and a form approved with a note; mtsync review and `-published`.

## 0.49.4 - Sending a form counts as reading it (2026-10-23)

No schema, grant or configuration change. Deploy the binary only.

- **Forms in read-tracked folders**: when an `acknowledge` service asks people to read a form template, sending it also confirms the sender has read the folder's published version. The answer to sending has `acknowledged` with that version; Slate Viewer 0.16.1 updates the dot and the read bar.
- **Website**: documents no longer start with a large empty space. The site's stylesheet still laid out Slate's editor as two rows; Slate 0.17 and later have three (toolbar, document, status bar), so the empty toolbar row took the spare height. Slate 0.19.1 also drops the space above a first heading after hidden front matter.
- Tests: sending a form in an acknowledge folder confirms it and clears it from the caller's outstanding files.

## 0.49.3 - Forms on the website (2026-10-23)

No schema, grant or configuration change. Deploy the binary only.

- **Slate 0.19.0 on the website**, as in Slate Viewer 0.16.0: Preview hides front matter (the `---` block at the top), and a form template shows its fields as greyed-out boxes, with a note to fill it in and send it from Slate Viewer, instead of its `{{…}}` tokens.

## 0.49.2 - Documents render as in Slate Viewer (2026-10-23)

No schema, grant or configuration change. Deploy the binary only.

- **Slate 0.18.0 on the website** (was 0.14.1), the same Slate as Slate Viewer 0.15: HTML comments stay hidden, form fields show as blanks, the newer Markdown rendering, and the Mermaid entry for charts and diagrams (`web/mermaid/mermaid.js`; the chunks are unchanged).
- **Pictures from the space**: in owned spaces and `/docs/`, a document's relative images (`![](../pics/a.png)`) now show. `path?raw` sends an image file itself (png, jpg, gif, webp, avif, bmp, ico, svg) to anyone who may read the space, with `nosniff` and a sandboxing CSP; any other file or query is 404.
- **Images on the web**: owned-space and `/docs/` pages now show `https:` images, as the public explorer already did (`no-referrer` stays).
- **Wiki links** (`[[name]]`) open the matching file from the folder tree, the one nearest the current document, instead of doing nothing.
- Tests: `?raw` for an SVG (type, sandbox, nosniff), refused queries and non-images, and the page attribute that turns it on.

## 0.49.1 - Who is signed in (2026-10-23)

No schema, grant or configuration change.

- **REST**: `GET /api/v1/account/spaces` also returns `account`, the username the connection signs in as.
- **mtsync 0.49.1**: `whoami` prints the account this computer signs in as (`-json`: `server`, `signedIn`, `account`). Slate Viewer 0.15.1 shows it in Open Space, with Switch Account.
- The 0.48.0 and 0.49.0 notes below said `doctor`; the command is `mt check`.
- Tests: the listing names the account (against MariaDB); mtsync whoami signed in, as JSON and signed out.

## 0.49.0 - Forms (2026-10-23)

**Database schema v13** (`deploy/account-schema-v13.sql`, then `deploy/account-grants.sql`): apply both before the new binary starts; it refuses to start on v12. No configuration change. No new Go modules. Tool schema unchanged. mtsync minimum stays 0.45.0; mtsync 0.49.0 adds `submit`, `submissions` and `submission`, which Slate Viewer 0.15.0 uses. **CI**: `.github/workflows/ci.yml` must apply schema v1–v13 and set `schema_version = 13`.

- **Form templates**: a `.md` file whose front matter has `form`, `title` and `submitTo` (`owner/space/folder/`), with `{{type name=… …}}` fields: `text`, `textarea`, `number`, `date`, `time`, `email`, `phone`, `choice` (one, or `multi`), `select`, `yesno` and `sign`, with `required`, lengths, `pattern` (RE2), ranges and options from the list below a choice. Tokens in code and comments are text. docs/forms.md describes it all; Slate 0.18.0 parses the same syntax.
- **Submissions**: `POST .../submissions {"path", "answers", "revision"?, "blob"?, "today"?}` on the OAuth REST resource. Only a person (Metatrash Sync) can send a form, with read access to the template. The server re-reads the template, checks every answer (`invalid_answers` with a problem per field) and files the filled-in copy as a new file in the `submitTo` folder, written for the person (author trailers name them), who needs no access to that space. `submitTo` must be a space with the same owner as the template's space, outside GitHub folders. The copy is `<space slug>-<username>-<form>-<YYYY-MM-DDTHHMMZ>.md` with the answers in front matter and the comments left out. A template that changed since the person started (`revision` or `blob`) is `template_changed` (409). 60 per person per hour.
- **Receipts**: the new `metatrash_form_submissions` table keeps one row per submission with the copy, so `GET .../submissions` (the caller's, newest first) and `GET .../submissions?id=ID` (with the text) show people what they sent.
- **mtsync 0.49.0**: `submit FILE ANSWERS` (JSON file or `-` for stdin; sends the synced copy's blob hash; `-json` prints the receipt, or the refusal with its fields), `submissions PLACE`, `submission PLACE ID`.
- `mt check` and startup check schema v13 and the two new grants.
- Tests: template parsing and every mistake it reports, answer checks per type, the filed copy (escaping, choices, block quotes, comments gone, signature), and submissions end to end against MariaDB (agent refused, per-field refusals, read-only member sends, copy and folder files in the review space, author trailers, receipts per person, changed template, same-owner rule); mtsync submit and receipts. docs/forms.md (new), api-contract (Form submissions), deployment (schema v13), roadmap (forms and quizzes), cmd/mtsync/README.md.

## 0.48.0 - Published versions and read acknowledgements (2026-10-22)

**Database schema v12** (`deploy/account-schema-v12.sql`, then `deploy/account-grants.sql`): apply both before the new binary starts; it refuses to start on v11. No configuration change. No new Go modules. Tool schema 0.13.0. mtsync minimum stays 0.45.0; mtsync 0.48.0 adds `policies`, `ack` and `readers`, which Slate Viewer 0.13.0 uses.

- **`publishedVersion`** in a folder's `.metatrash.json`: a whole number, the folder's official version. Version N is the folder's files as they were in the commit that set N, so publishing is one settings write (Slate Viewer's Publish, or an agent asked to publish); edits after it are drafts until the next number. It only goes up (`invalid_request` otherwise), is never inherited, and `locked` can keep agents from changing it (`{"publishedVersion": "agents"}`). The commit is found from the settings file's history, so nothing is copied or tagged. Everyone can still read drafts; per-folder permissions are on the roadmap.
- **History marks published text**: an entry is `published: [N, ...]` when it is the file's text in version N (its newest change at or before the commit that set N). mtsync history prints `[published vN]`.
- **`acknowledge` service**: `{"services": [{"type": "acknowledge", "files": ["*.md"]}]}` asks everyone in the space (owner and active members) to read the folder's published files; `files` takes names, patterns and `sub/` like `readOnly`, by default every `.md` file but `AGENTS.md`. It may sit on the root and inside other acknowledge folders (the nearest folder with a `publishedVersion` owns a file). Owned spaces only.
- **Services registry**: service types are now entries in `serviceKinds` (`folder_config.go`) with their own checks, root and nesting rules, so the next service is one entry plus its code. GitHub's rules are unchanged.
- **Acknowledgements** are kept in the new `metatrash_acknowledgements` table, one row per person, file and published text, never in the space. REST on the OAuth resource: `GET .../acknowledgements` (the caller's published folders and files, `outstanding` per file and in total), `GET .../acknowledgements?report=true` (who has read what; needs read-write access) and `POST .../acknowledgements {"path", "version"}` (confirm reading; only Metatrash Sync, so only a person; `conflict` 409 when the folder has published a newer version). A new version asks again only for files whose text changed.
- **mtsync 0.48.0**: `policies PLACE` (published folders, files and what you still have to read; `-json` adds local file paths for `show`), `ack FILE VERSION`, `readers PLACE`.
- `mt check` and startup check schema v12 and the two new grants. The website's file browser labels a published folder `Published vN`.
- Tests: settings rules (versions only go up, not inherited, agent lock), publish points and history marks across drafts and versions, tracked-file rules, and acknowledgements end to end against MariaDB (agent refused, old version refused, report needs read-write, a new version re-asks only changed files); mtsync policies and ack. docs/api-contract.md (Folder settings, Acknowledgements), deployment (schema v12), roadmap, cmd/mtsync/README.md.

## 0.47.0 - History says who made each change (2026-10-21)

No database schema, configuration or grant change. No new Go modules. Tool schema 0.12.0. mtsync minimum stays 0.45.0.

- **Who made a change.** Every commit from a request now ends with `Metatrash-By` (the account's username, or its email address before the @ when it has none), `Metatrash-App` (the app: `Metatrash Sync` for mtsync and Slate Viewer, the approved app name such as Claude for an agent, `space key` or `anonymous`) and `Metatrash-Kind` (`person` or `agent`) trailers. History entries return them as `by: {name, app, kind}`. Usernames are public, so this shows in every space's history, the public space's included. Older commits and the service's own have none. The account is looked up once per request, and only when it commits.
- **mtsync**: `history` prints `by name (app, kind)` and `history -json` passes `by` through.

## 0.46.0 - Locks with levels, and read-only files (2026-10-21)

No database schema, configuration or grant change. No new Go modules. Tool schema 0.11.0. mtsync minimum stays 0.45.0; mtsync 0.46.0 handles files locked for everyone (older versions report the refusal and keep the local change).

- **Lock levels.** A lock in `.metatrash.json` is `"agents"` (agents can't change it, people writing through Metatrash Sync can) or `"everyone"` (nobody can until a person lifts the lock); anything not locked is read-write for everyone. `locked` is now a map of key to level (`{"purpose": "everyone"}`); the 0.45.0 list form still works and locks for agents. `locked` itself is always locked for agents and can't be locked for everyone, so a person can always lift a lock. People are now held to `"everyone"` locks on keys too.
- **`readOnly`** in `.metatrash.json` locks files of that folder: names or patterns (`spec.md`, `*.pdf`) of files in it, or subfolders ending in `/` (`archive/`) for everything under them, each with a level. A locked file can't be written, edited, moved, renamed or deleted, or created under a locked name, by a writer the lock covers: `read_only_file` (403). Not inherited. `readOnly` itself is an ordinary key, which `locked` can protect. A `.metatrash.json` is locked at most for agents. List, read and write results mark locked files with `readOnly`. An agent's delete or move never removes a locked `AGENTS.md` or `.metatrash.json` with its emptied folder.
- **mtsync**: a change or deletion of a file locked for everyone is refused by the space; mtsync keeps a changed local version as a conflict copy (`spec.conflict-<time>.md`) and puts the space's version back.

## 0.45.0 - AGENTS.md and .metatrash.json in every folder; README.md is ordinary (2026-10-21)

No database schema, configuration or grant change. No new Go modules. Tool schema 0.10.0. **mtsync 0.45.0 is required** (`clients.mtsync.minVersion`): older versions miss the folder files a write adds. Release this version (so `mtsync.json` offers 0.45.0) before or with deploying the server.

- **Every folder has an `AGENTS.md` and a `.metatrash.json`**, the space root included. `AGENTS.md` holds instructions for agents working in that folder; `.metatrash.json` holds its settings. The service adds both, in the same commit, when a write or move puts the first file in a folder, and removes them when the folder's last other file is deleted or moved away (emptied parent folders go too; the root keeps its own). Delete and move refuse them (`protected_file`); agents with write access rewrite them freely. A folder can be started by writing its `AGENTS.md`; an `agents.md` in another case counts. New root `AGENTS.md` files start from `docs/space-AGENTS.template.md` (replaces `docs/space-README.template.md`); other folders get one sentence; new root settings are `{}`, other folders' `{"inherit": true}`.
- **GitHub folders** get no folder files inside them: everything there belongs to the repository, and `AGENTS.md` there is an ordinary repository file. The GitHub folder's own `.metatrash.json` is protected. A first pull accepts an untouched starter `AGENTS.md` in the folder and replaces it with the repository's copy or removes it.
- **Existing spaces** get the folder files they lack when the service starts (owned spaces when first opened): one commit per space, logged; a space at its storage limit is skipped with a log line. Folder files count toward the 1,000-file limit.
- **README.md is an ordinary file**: no longer created, no longer immutable or undeletable (old spaces keep theirs, now deletable). `mt spaces readme` is removed. The `protected` field now marks folder files and is derived, not stored.
- **`inherit`** in `.metatrash.json`: `true` takes every setting from the parent folder's (itself inherited when that folder inherits) except `services`; the folder's own keys override. Refused on the root. Reading a `.metatrash.json` that inherits also returns `effective`, the settings with inherited ones filled in.
- **`locked`** in `.metatrash.json`: keys (`purpose`, `children`, `services`, `inherit`, `locked`) agents may not add, change or remove. The service compares the file before and after each write or edit and refuses a change to a locked key's effective value with `locked_setting` (403), inherited locks included. `locked` is always locked for agents, and `inherit` too while any lock applies. Writes through Metatrash Sync (mtsync and Slate Viewer, the built-in OAuth client) count as a person's and may change anything.
- **Website**: a space's address without a file shows `README.md`, else its first top-level Markdown file, else the root `AGENTS.md` (it was a 404 without a README).
- **Agent guidance**: the MCP server instructions and tool descriptions say to start with the root `AGENTS.md` and read each folder's own; the public home page prompt says AGENTS.md.
- **mtsync 0.45.0** resumes each pass from the state the last pass started at, so it hears of folder files its own writes added or removed; it treats `AGENTS.md` and `.metatrash.json` as undeletable (a local delete restores them; they go when the folder's other files are deleted).
- Tests: folder files on write, move, delete and in GitHub folders; backfill of an old space; `inherit`, `effective` and `locked` for agents and a person; first pull over a starter `AGENTS.md`. docs/api-contract.md (Folder files, Folder settings), architecture, deployment, roadmap, README, cmd/mtsync/README.md.

## 0.44.1 - Account page links to the moved Slate Viewer page (2026-10-20)

No schema, configuration or grant change. mtsync is unchanged.

- The account page's **Manage & Edit spaces from the desktop** link points to `/docs/tools/slate-viewer/README.md`. The Slate Viewer page moved there in the docs space, next to its change notes in `tools/slate-viewer/change-logs/`.

## 0.44.0 - Website documents use Slate 0.14.1, with diagrams (2026-10-19)

No schema, configuration or grant change. No new Go modules. mtsync is unchanged.

- **Document pages** render Markdown with Slate 0.14.1 (CodeMirror-based) in place of the old Slate 0.9.2, the same editor Slate Viewer uses, still read-only and still in `internal/service/web/slate.js` and `slate.css`.
- **Diagrams**: ` ```mermaid ` blocks draw as diagrams in public, docs and private space pages, following Mermaid's strict security level (no scripts, links or CSS from the diagram text). Mermaid is embedded from `internal/service/web/mermaid/` (`mermaid.js` plus a chunk per diagram type, about 5 MB in the binary) and served at `/assets/mermaid/…`; the browser fetches it only for a document that has a diagram (about 2.3 MB for a flowchart). The chunks carry a hash of their content in their names and are served with a one-year immutable `Cache-Control`; other assets keep `no-store`.
- Web addresses written bare (`https://…`) are links now, with a small ↗ after links to other sites.
- The pages' Content-Security-Policy is unchanged: Slate 0.14.1 applies Mermaid's styles through constructed stylesheets rather than `<style>` elements. CodeMirror's own `<style>` element is still refused by the policy, which shows as one console message per page; the read-only view doesn't use CodeMirror's styles.
- Tests: Slate and every Mermaid file it imports are served; other paths under `/assets/mermaid/` are 404.
- To update Slate later: copy `slate.js`, `slate.css` and the whole `mermaid/` folder from the Slate repository into `internal/service/web/` (chunk names change between Mermaid versions, so delete the old `mermaid/` first).

## 0.43.2 - Signed Windows downloads (2026-10-18)

No server or mtsync change. No schema, configuration or grant change. No new Go modules.

- **Release** signs `mtsync-windows-amd64.exe`, `mtsync-windows-arm64.exe`, the setup's uninstaller and `mtsync-windows-setup.exe` with Azure Artifact Signing (formerly Trusted Signing; account `zaptronicssigning`, East US), timestamped by Microsoft's timestamp server, before `SHA256SUMS` and `mtsync.json` are made. Signing runs on Linux with Jsign 7.5 (checked against its SHA-256). The job signs in to Azure with GitHub's OIDC token (`azure/login`, no client secret) from the repository's `release` environment, and fails before publishing if `AZURE_CLIENT_ID`, `AZURE_TENANT_ID` or `AZURE_SIGNING_PROFILE` is not set there. The macOS and Linux files are unchanged (Artifact Signing is Windows only).
- `deploy/installers/mtsync-setup.nsi` takes `-DSIGN=COMMAND` and signs the uninstaller and the setup with it (NSIS 3.08 or later). CI still builds it unsigned.
- docs/deployment.md (Code signing: the Azure and GitHub setup), README (the setup is signed), roadmap (macOS signing and notarization remain).

## 0.43.1 - Desktop tools link on the account page (2026-10-18)

No schema, configuration or grant change. No new Go modules. mtsync is unchanged.

- **Private spaces** no longer show the "Public space ↗" link in the header (a space readable on the web, such as the docs, keeps it).
- **Your account** replaces "Public space ↗" with **Manage & Edit spaces from the desktop**, which opens `/docs/tools/slate-viewer.md`.
- The docs space (`dave-zap/metatrash-com`) gains `tools/slate-viewer.md` (what Slate Viewer does, downloads, install and first space) and `tools/mtsync.md` (mtsync on its own, noting it comes with Slate Viewer), linked from introduction.md. These are space content, already live; they need no deploy.

## 0.43.0 - Reset a space's README from the console (2026-10-18)

No schema, configuration or grant change. No new Go modules. mtsync is unchanged.

- **`mt spaces readme <space>`** replaces a space's root `README.md` with the template built into this version (`docs/space-README.template.md`), keeping the file's ID and history: one commit with a `write` entry, as an agent's write would make. `<space>` is `owner/slug`, a space ID or a `spaces.json` name such as `public`; `-all` does every `spaces.json` space and every ready owned space, one line each (`updated` or `already current`). Agents still cannot change README.md, so this is how an existing space gets the current text; new spaces get the template when they are created, as before. It writes the repository under `-data` directly and can run beside the service (the branch only moves if nothing else committed meanwhile; a write that loses that race fails and can be retried). Space limits are not applied to the new text.
- After deploying, `mt spaces readme public` fixes the live public README, which still says the MCP endpoint is pending.
- The roadmap and docs drop stale items: per-version upgrade steps in docs/deployment.md are now one table, the README status is current, and the roadmap gains owner-set read-only files and a folder configurator in Slate Viewer.
- Tests: refresh keeps the README's ID and protection, writes one `write` commit, is a no-op when the text is already current, and refuses a missing repository.

## 0.42.0 - Client versions in the tool schema (2026-10-17)

No schema, configuration or grant change. No new Go modules. mtsync is unchanged.

- **`clients` in `/api/v1/tool-schema.json`** (`schemaVersion` 0.9.0) names the oldest mtsync and Slate Viewer this server works with: `{"mtsync": {"minVersion": "0.37.2"}, "slateViewer": {"minVersion": "0.2.0"}}`. Slate Viewer reads it in its update check (at launch and twice a day): a too-old mtsync is updated at once from the latest release's `mtsync.json`, and a too-old Slate Viewer asks to update. Raise these when a server change needs newer clients, and bump `schemaVersion` whenever the schema changes.
- docs/api-contract.md (Client versions).

## 0.41.2 - Installers replace an older running mtsync (2026-10-17)

No server or mtsync change. No schema, configuration or grant change. No new Go modules.

- **Windows setup:** an update now also stops an mtsync service run from another folder (a hand-built copy or an old download), so `mtsync install` starts the new one instead of finding the old one still running. Before, only a copy in `%LOCALAPPDATA%\Programs\mtsync` was stopped. The install folder now goes at the front of the user's `Path` rather than the end, and the setup warns, listing them, when other `mtsync.exe` copies are on the `Path` (machine or user), since terminals and Slate Viewer may still run those.
- **install.sh:** the downloaded program stops whichever service is running before it is replaced. An update whose systemd unit or LaunchAgent runs another copy is set up again with `mtsync install` (a LaunchAgent is unloaded first) instead of restarting the old one. It also lists other `mtsync` copies on `PATH`.

## 0.41.1 - mtsync installers (2026-10-17)

No server or mtsync change. No schema, configuration or grant change. No new Go modules.

- **Windows setup** (`mtsync-windows-setup.exe`, from `deploy/installers/mtsync-setup.nsi`, built with NSIS): installs for the current user without administrator rights in `%LOCALAPPDATA%\Programs\mtsync`, choosing the amd64 or arm64 build, adds the folder to the user's `Path` (kept as REG_EXPAND_SZ), runs `mtsync install`, registers in Settings → Apps, and offers `mtsync login` on the last page. Updating stops the running service first. Uninstalling runs `mtsync uninstall`, removes the program and the `Path` entry, and keeps the sign-in, registered folders and synced files. `/S` installs silently. Not code-signed yet.
- **macOS and Linux script** (`install.sh`, used as `curl -fsSL …/releases/latest/download/install.sh | sh`): picks the build, verifies it against `SHA256SUMS`, installs to `~/.local/bin`, runs `mtsync install` (or `mtsync start` on an update), and says when the folder is not on `PATH`. `MTSYNC_VERSION`, `MTSYNC_DIR` and `MTSYNC_NO_SERVICE` change what it does. A `curl` download is not quarantined, so macOS runs mtsync without a Gatekeeper exception.
- **Release** adds both to the GitHub Release (and to `SHA256SUMS`; `mtsync.json` still lists the programs only). **CI** builds the setup and runs shellcheck on every push, then runs the setup silently on Windows, checks the program and `Path`, and uninstalls.
- README "Install mtsync" leads with the installers.

## 0.41.0 - File history in mtsync (2026-10-17)

No schema, configuration or grant change. No new Go modules. Gap G9 of the editor integration: a desktop editor can show a note's history through mtsync, without holding tokens.

- **`mtsync history FILE`** lists the changes to a file in a registered folder, newest first (time, revision, operation, path then), following it through renames. It shows 10; `-limit N` (1–100) and `-offset N` page back, and `-json` answers for editors with `space`, `folder`, `id`, `entries` (`revision`, `path`, `operation`, `timestamp`, and `file`, the local path of that revision's path) and `nextOffset` (null at the end). The file's ID comes from the folder's `.mtsync/state.json`, so it must have synced once.
- **`mtsync show FILE REVISION`** prints the file's text at a revision, exactly as stored. For a revision from before a rename, pass the entry's `file`.
- **REST history takes `offset`** (0–10000, not with `cursor`), skipping that many of the newest entries; past the end the page is empty. mtsync sends it only for `-offset` above 0, so plain `mtsync history` works with older servers. MCP history is unchanged.
- cmd/mtsync/README.md (Use, commands and flags), docs/api-contract.md (history), docs/roadmap.md (G9 done; the Slate Viewer History panel is next; Open Recent with spaces is done in Slate Viewer).
- Tests: history paging with limit and offset, past the end, JSON fields, show of each revision, history and show across a rename, plain listing with the next page, and refusals (file not yet synced, outside registered folders, bad limit, short revision, path not there at a revision); REST offset, offset past the end, negative offset, offset with cursor, offset on list. The full suite passes (without MariaDB; no database code changed).

## 0.40.1 - Automated builds and releases (2026-10-16)

No server or mtsync change. No schema, configuration or grant change. No new Go modules.

- **CI** (`.github/workflows/ci.yml`) on every push and pull request: `gofmt`, `go vet`, `go test ./...` on Linux including the MariaDB integration tests (a disposable MariaDB 11.4 container with schema v1–v11 and `account-grants.sql`), a cross-compile of mtsync and metatrash for Windows, Linux and macOS on amd64 and arm64, and the mtsync tests on Windows and macOS.
- **Releases** (`.github/workflows/release.yml`): pushing a tag `vX.Y.Z` that matches `Version` checks the changelog has that section, runs CI, and publishes a GitHub Release with mtsync for Windows, Linux and macOS (amd64, arm64), the metatrash service for Linux (amd64, arm64), `SHA256SUMS`, and `mtsync.json` (version, URL, SHA-256 and size per platform, for the editor's installer). The changelog section is the release notes. Binaries are not code-signed yet.
- The mtsync log test closes its log file, so Windows can delete its temporary folder (the first Windows run failed on that cleanup, not on mtsync).
- `upgrade.sh` is unchanged (it still builds on the server); docs/deployment.md shows how it could install the release binary instead.
- docs/deployment.md ("Automated builds and releases", Testing) and cmd/mtsync/README.md (Build).

## 0.40.0 - Edit, search, line ranges and per-file write checks (2026-10-16)

No schema, configuration or grant change. No new Go modules. Agents spend fewer tokens per change: they find text with `search`, read only the lines they need, and send back only what changes with `edit`. mtsync is unchanged and keeps working as before.

- **`edit`** (MCP tool, `POST /api/v1/spaces/{space}/edit` and the account route) changes part of an existing file: `{path, edits: [{oldText, newText, replaceAll?}], ifInState?, strict?}`, up to 50 edits applied in order. Each `oldText` must match the current text exactly once (or with `replaceAll`, at least once); otherwise `conflict` (409) names the edit and whether it matched nothing or several places, so the agent reads or searches again. All edits are saved or none, as one `write` in the file's history, with write's response. No `ifInState` is needed, since the exact match refuses a change over someone else's; when given it is checked for this file only. LF `oldText` matches files with CRLF line endings. README.md, `.metatrash.json` validation and the file limit apply as for write. It counts as one write.
- **`search`** (MCP tool, `GET …/search`) finds lines like grep: `query` (plain text, or RE2 with `regex`), `ignoreCase`, `prefix`, `context` (0–10 lines), `limit` (default 50, at most 200), `revision`. Each match gives `path`, file `id`, `line` and the line's text (cut to 400 bytes); `truncated` says there were more. One `git cat-file --batch` reads the files; it counts as one read.
- **Line ranges on `read`**: `startLine` and `lineCount` (up to 5,000) return only those lines, with line endings, plus `lines: {start, end, total}`. Reads without them are unchanged.
- **Per-file write checks** (the roadmap priority agreed 2026-10-08). `ifInState` on write, move, delete and edit no longer has to be the space's latest state: when it is older, the change goes ahead on top of the latest state if the files it touches (write, edit, delete: `path`; move: `from` and `to`) are unchanged since, and the result says `rebased: true`. Agents working on different files stop failing each other's writes with `state_mismatch`. A file changed since, a move into a path created since, or a state that is not an earlier state of the space is still refused. `strict: true` keeps the whole-space check. Rules that look at other files (folder configuration, dot names, GitHub folders, collisions, storage) still check the latest state.
- **REST write, move and delete default to `strict: true`** (send `strict: false` for the per-file check), because mtsync 0.33.0–0.39.0 takes a write's `newState` as a state it has every file of; a rebased write would make it miss other changes. MCP and REST edit default to the per-file check.
- The account MCP server instructions, tool descriptions and the README new spaces start with tell agents to search, read by line range and edit instead of rewriting files. tool-schema.json is 0.8.0 (`search`, `edit`, read's `startLine`/`lineCount`/`lines`, `strict` and `rebased`).
- docs/api-contract.md (Operations, Revision tokens, new "Search, line ranges and edit"), docs/architecture.md, docs/oauth.md, README.md and docs/roadmap.md (the per-file priority is done; `append` and unchanged reads are listed for later; the public guides need updating after deploy).
- Tests: line ranges (middle, tail, past the end, too many lines); search by text, prefix, case, regex, context and limit, bad regex and parameters; edit applied, ambiguous, missing, all-or-none, replaceAll, no-op, CRLF, README, missing file, empty oldText, invalid `.metatrash.json`, over the file limit; per-file checks for write, move, delete and edit, including strict, unknown states and the REST default; edit and search over REST and MCP. The full suite passes (without MariaDB; the change touches no database code).

## 0.39.0 - Rename an account from the console (2026-10-15)

No schema, configuration or grant change (the v2 grant `UPDATE (username)` on `metatrash_users` covers it). No new Go modules.

- **`mt users rename <who> <new-username>`** sets an account's username, whether or not it had one, under the same rules as Your account (3–32 lowercase letters or numbers with single hyphens, reserved names refused, case ignored). A name another account has is refused. Every table refers to accounts by user ID, so the account's spaces, memberships, invitations, connected apps, GitHub installations and sign-ins follow at once, with the service running. Addresses with the old name stop working: `/spaces/<old>/<slug>/` links, and apps or mtsync folders that name a space as `<old>/<slug>` (space IDs keep working); earlier addresses of spaces transferred away follow the new name. The old name is free for anyone to choose. The command says how many owned spaces moved, and warns when `accounts.json` `docsSpace` names the old username. The account is emailed a notice with the old and new names through the service's SMTP settings (`-no-notice` skips it); if the email fails the rename still stands and the command says so. Git history in spaces keeps the name commits were made under.
- Metatrash has no separate display name: the username is the name shown on the site, in member lists and to passkey managers. A passkey keeps the name it was created with on the device.
- Tests: against MariaDB, a first username set on an account without one; too short, double hyphen, reserved, taken and taken-in-capitals names refused; the same name reported as no change; a rename stored, the old name no longer finding the account, and the old name free for another account to choose. The full suite passes against MariaDB. Checked by hand: each message, including the moved spaces, the docsSpace warning and a failed notice.

## 0.38.0 - Space export to zip (2026-10-12)

No schema, configuration or grant change. No new Go modules.

- **REST export.** `GET /api/v1/spaces/{space}/export` and `GET /api/v1/account/spaces/{owner}/{slug}/export` return the space at its current state as a zip, for backups: every file at its path, dated with that state's commit time, plus `.metatrash-export.json` with the space, state and each file's path, ID and size. Read access is enough (a read key, or a read-only connection). It counts as one read, plus its own allowance since it reads every file: 60 exports of a space and 10 per client of that space in each ten minutes. The space's files are read with one `git cat-file --batch` process. MCP is unchanged.
- **`mt spaces export <space> [FILE]`** writes the same zip on the server, straight from the data directory (`-data`, default `/var/lib/metatrash`), with the service running and without rate limits. `<space>` is `owner/slug`, a space ID or a `spaces.json` name; `FILE` defaults to `<owner>-<slug>-<date>-<state>.zip` here, and `-` writes to standard output. **`mt spaces export -all DIR`** exports every ready owned space and every `spaces.json` space into `DIR`, for a nightly cron job. Exports are written readable only by the service user.
- **`mtsync export SPACE [FILE]`** saves a space to a zip on your computer through the same sign-in as syncing (or `-key-env` for key spaces), whether or not it is synced.
- docs/api-contract.md (Export), docs/deployment.md (console commands) and cmd/mtsync/README.md describe them.

## 0.37.3 - mtsync retries refused renames (2026-10-12)

No server change. No new Go modules.

- mtsync retries a rename for up to two seconds when it fails. On Windows, replacing `.mtsync/state.json` or a downloaded file failed with "Access is denied" while OneDrive, an antivirus scanner or an editor briefly had the file open, which failed the pass (it was retried 5 seconds later, so nothing was lost).

## 0.37.2 - mtsync start (2026-10-12)

No server change. No new Go modules.

- New `mtsync start` starts the service in the background and waits until it is running. On Windows it launches `mtsync run` with no console window (and outside the terminal's job, so closing the terminal does not stop it); the login entry is now `"mtsync.exe" start` instead of `conhost.exe --headless`, which could exit without starting the service. On Linux and macOS it uses the systemd unit or LaunchAgent when installed.
- `mtsync install` checks that the service actually started and, if not, says to look at `mtsync log` or run `mtsync run`. A service that cannot start writes the reason to the log.
- `mtsync add` of a folder already registered for the same space says so and succeeds, instead of reporting that the folder overlaps itself; for another space it says to `mtsync remove` it first.

## 0.37.1 - mtsync sign-in page (2026-10-12)

No server change. No new Go modules.

- The page the browser shows after `mtsync login` now looks like the site: a "This computer is connected." page with the next three commands (`mtsync spaces`, `mtsync add`, `mtsync install`) and a link to Services in the account, or a clear "That didn't work." page with the reason. It is self-contained and served with a strict Content-Security-Policy.
- cmd/mtsync/README.md: a Windows build line with `-o mtsync.exe`, and a note to clear `GOOS` after cross-compiling.

## 0.37.0 - Change lists: mtsync fetches only what changed (2026-10-12)

No schema, configuration or grant change. No new Go modules.

- **REST list takes `since=<state>`** and answers with only what changed under `prefix` since that state: `files` holds the added or changed files (new text or a new ID) with `blob`, all in one response, and the new `removed` field the paths that went away. Nothing changed: both empty, same `state`. A `since` that is not an earlier state of the space (after a reset) is `not_found`. It combines with `after=` (wait, then the changes). One read. MCP is unchanged.
- **mtsync** now asks for the changes since its last agreed state instead of listing the whole space: a quiet check and any change both cost one listing request, plus one download per changed file. Full listings happen only on the first sync, after an unfinished pass, after a reset, or against a server before 0.37.0 (which it detects and then lists as before).
- mtsync skips `.git` folders entirely (a space can never hold them), so a synced folder can be its own local Git repository without a refusal logged per file.
- Tests: changes since a state (added, changed, removed), none, outside the prefix, combined with a wait, an unknown state and bad parameters; with 122 files, one changed file costs two requests and one removal one; syncing against a server without `since=`.

## 0.36.0 - mtsync can sync one folder of a space (2026-10-12)

No server change. No new Go modules.

- `mtsync add owner/slug/folder/sub FOLDER` (and a one-off `mtsync owner/slug/folder FOLDER`) syncs just that folder of the space: the local folder holds its contents with paths relative to it, new files and renames stay inside it, and the rest of the space is untouched. `public/...` and space IDs work the same way; key spaces take `name/folder`. Inside such a folder README.md and `.metatrash.json` are ordinary files (only the space root's can't be deleted). `add` checks the folder path with the server. Several folders of one space can go to different local folders.
- Tests: splitting `owner/slug/path`, `public/path`, IDs and key spaces; a subfolder downloads only its files, uploads and moves stay inside it, its README.md can be deleted, and files elsewhere in the space are untouched.

## 0.35.0 - mtsync runs as one background service (2026-10-12)

No server change: this release only changes `mtsync`. No new Go modules.

- **One service syncs every registered folder.** `mtsync add SPACE FOLDER` registers a folder (checking that this computer can reach the space and that it doesn't overlap a registered folder; it creates the folder), `mtsync remove FOLDER|SPACE` unregisters one (its files stay) and `mtsync list` shows each folder with its state: in sync and when last checked, the last error, or service not running. The running service picks up changes to the list within 3 seconds; a folder that stops with an error (signed out, space no longer connected) is retried every minute. Folders on the same site share one sign-in.
- **`mtsync install`** starts the service at every login and now, as the user (never a system account, no administrator rights): a systemd user unit on Linux, a LaunchAgent on macOS, a Run key entry on Windows started without a window (`conhost.exe --headless`). `mtsync uninstall` stops it and removes the entry. `mtsync run` runs the service in the foreground, `mtsync stop` stops a running one (through a stop file, so it exits cleanly on every system), `mtsync log [N]` shows the service log (`mtsync.log` in the config folder, rotated at 5 MB).
- **Locks**: one service per user, and one mtsync per folder (the service or a one-off `mtsync SPACE FOLDER`, which still works). Lock files are refreshed every 30 seconds and taken over two minutes after a crash.
- Files: `sync-folders.json`, `sync-status.json`, `mtsync.log` and `mtsync-service.lock` beside `sync-credentials.json`; `.mtsync/lock` in each folder.
- Tests: the service syncs folders registered while it runs, downloads an agent's write, writes status that `list` reports, holds each folder's lock, refuses a second service, stops a removed folder and keeps its files, and stops cleanly on `mtsync stop`, releasing its locks; overlap checks and remove; log tail and rotation; the systemd unit, LaunchAgent and Windows command text. Race detector clean. `install` itself is not exercised here (no systemd, launchd or Windows in the test environment).

## 0.34.0 - mtsync hears changes at once (2026-10-12)

No schema, configuration or grant change. No new Go modules.

- **REST list takes `after=<state>`**: while the space is still at that state the request waits, up to 25 seconds, and answers as soon as a write moves the branch (each repository wakes its waiters when it publishes a commit). It then lists as usual; an unchanged `state` means the wait ran out. Not with `cursor`. One read each. At most 256 waiting lists service-wide; past that, lists answer at once. 25 seconds stays under the server's 45-second write timeout and the example Apache `ProxyTimeout 60`. MCP is unchanged.
- **mtsync downloads agents' changes as they happen.** It keeps one `after=` request waiting and starts a pass when it returns with a new state; an idle folder now costs about two requests a minute instead of twelve. The folder is scanned every `-interval` (5 seconds) with no request unless something changed. Against a server before 0.34.0 it says so once and checks every interval, as before.
- Tests: a waiting list ends unchanged after the wait, is woken by a write with the new state and blobs, answers at once for an older state, and refuses a bad `after` or one with `cursor`; mtsync with a one-minute interval downloads an agent's write within seconds and makes no requests while idle beyond the waiting one; local saves upload; the fallback against a server that refuses `after=`. Race detector clean. Full suite passes against MariaDB.

## 0.33.0 - Sync a folder with mtsync (2026-10-12)

No schema, configuration or grant change. No new Go modules. The server change is needed before `mtsync` can sign in.

- **New `mtsync` command** (`cmd/mtsync`, standard library only) keeps a folder on Windows, Linux or macOS in sync with a space, so any editor or tool can work on its files. `mtsync login` connects through the browser like any app (choose spaces and read-only or read and write on the consent page); `mtsync spaces` lists them; `mtsync SPACE FOLDER` syncs every 5 seconds until stopped (`-once`, `-interval`). See README → Sync a folder.
- How it syncs: a three-way comparison of the folder, the space and the last agreed version (kept in `.mtsync/state.json`). Saves upload, agents' changes download, deletions go both ways, a local rename becomes a `move` (keeps the file ID and history), and a file changed on both sides keeps the space version under its name and saves the local one as `name.conflict-<UTC time>.ext`, which is uploaded too. Every write carries `ifInState`; a change in the space mid-pass restarts the pass. A quiet check is one request. Files are hashed like Git blobs so only changed files are read or sent; content a local file already has (a copy or move in the space) is not downloaded again. Files saved within the last second wait for the next pass. Editor and system files (`*.swp`, `*~`, `.DS_Store`, `Thumbs.db`, …) are skipped; files the space refuses are reported once and retried when they change. README.md and the root `.metatrash.json` are restored if deleted locally. Read-only access downloads only. 429 and 503 wait for `Retry-After`.
- Sign-in: tokens are stored per site in the user's config folder (`metatrash/sync-credentials.json`, mode 0600), refreshed before expiry and on a 401, under a lock file so two `mtsync` processes never spend the same rotating refresh token. Administrator key spaces work with `-key-env VAR`.
- **Built-in OAuth client for mtsync**: `client_id` `https://metatrash.com/oauth/clients/sync`, loopback redirect `http://127.0.0.1/callback` (any port, RFC 8252). The site serves its metadata document at that URL but never fetches it, so no `clientHosts` change is needed; it is shown as **Metatrash Sync** on the consent page and in Services. Access rules are unchanged: consent, ownership or membership, the owner's per-member app permission and the token scope.
- REST list takes `blobs=true` and then adds each file's `blob` (Git blob hash). MCP results are unchanged.
- Tests: the built-in client (document, authorize with a loopback port, other redirects and another site's path refused) and, against MariaDB, the full sign-in, token, REST list with blobs, an unchosen space refused, refresh and the Services entry; list blobs on and off. `mtsync` against a real service: first download, a quiet pass costs one request, uploads, an agent's write and delete, rename as move, local delete, empty folders removed, README restored, a conflict, a refused file reported once, read-only key, resuming in a new process, a folder refused for a second space; login and token refresh against a fake authorization server. Cross-compiled for Windows, Linux and macOS. Full suite passes against MariaDB.

## 0.32.0 - Signed-in devices (2026-10-09)

**Needs schema v11 before the new binary starts** (`deploy/account-schema-v11.sql`; see deployment.md). No new grants, no configuration change, no new Go modules. Sessions survive the upgrade.

- **Security → Signed-in devices** replaces the Signed-in browsers card near the top with a table at the bottom: one row per session with the browser and system ("Chrome on Windows", "Safari on iPhone"), signed in and last used ("3 hours ago"; relative, so no time zones), and the address it signed in from. This browser comes first, marked **this device**, then the most recently used. Each other row has an **✕** that signs that one out (no confirmation needed, as for Sign out everywhere else, which stays above the table). Rows name sessions by an opaque ID, never the stored digest. Narrow screens label each value.
- The browser label is made from the User-Agent at sign-in and only the label is stored; the address comes from the trusted proxy's `X-Forwarded-For`. Both are deleted with the session. Sessions from 0.31.0 show "Unknown browser" and "unknown".
- `mt users show` lists the account's sessions under **Signed in** (device, address, since, last use).
- Schema v11: `device` and `ip` columns on `metatrash_sessions`. `mt check` reports v11.
- Tests: browser labels for Chrome, Edge, Safari, Firefox, Opera, Samsung Internet on Windows, Mac, iPhone, iPad, Android, Linux and ChromeOS; relative times; the devices table (order, this device without a button, others with one, no other account's sessions, no digests in the page); signing one out (and not this browser, another account's, unknown IDs or with another form's CSRF token), kept after a restart; device and address stored and restored; an authenticator sign-in records its address. Full suite passes against MariaDB. Checked the console output and the table in headless Chromium at 1300px and 400px.

## 0.31.0 - Stay signed in across restarts (2026-10-09)

**Needs schema v10 and the grants before the new binary starts** (`deploy/account-schema-v10.sql`, then `deploy/account-grants.sql`; see deployment.md). No configuration change. No new Go modules.

- **A restart or deploy no longer signs everyone out.** Sessions are now also stored in the new `metatrash_sessions` table and loaded at startup (`account sessions-loaded count=N` in the journal). The upgrade to 0.31.0 itself still signs everyone out once.
- What is stored per session: the SHA-256 of the cookie's random token (the token itself is never stored), the user, created and expiry times, when the session last signed in or confirmed (so the 10-minute step-up window survives a restart too) and when it was last used (written at most every 5 minutes, for the signed-in devices table that comes next).
- A session is stored before its cookie is set; if the database can't store it, sign-in fails with "temporarily unavailable" rather than giving a session that would vanish on restart. Sign out, replacing a browser's session, Sign out everywhere else, the automatic sign-outs (after removing a sign-in method, changing the email address, a recovery code sign-in) and the per-account limit of 8 delete the rows. If Sign out everywhere else can't delete them, the other browsers are still signed out in the running service and the page says it couldn't be saved. Expired rows are swept at startup and at most hourly on sign-in. Sign-in codes, pending app connections and other short-lived state stay in memory.
- Schema v10: `metatrash_sessions`. New grants: `SELECT, INSERT, DELETE` and `UPDATE (auth_at, last_used_at)` on it. `mt check` reports v10.
- Tests (new `TestSessionsSurviveRestart`, `TestSignOutSurvivesRestart`, against MariaDB): only the digest is stored; sessions, their accounts and the step-up window come back in a fresh process; a stale step-up stays stale; signing out (through the web form), Sign out everywhere else and the ninth sign-in's eviction stay done after a restart; last use is written; expired rows are neither loaded nor kept. Full suite passes against MariaDB. Smoke-tested the binary: a stored session signs in after start, an expired one is swept.

## 0.30.0 - Transfer space ownership (2026-10-09)

**Needs schema v9 and the grants before the new binary starts** (`deploy/account-schema-v9.sql`, then `deploy/account-grants.sql`; see deployment.md). No configuration change. No new Go modules.

- **Transfer ownership** on a space's Manage sharing page offers it to one of its active members who has a public username. Offering needs a sign-in or confirmation within the last 10 minutes (as changing sign-in methods does); the page says so and links to Security. One offer per space, valid seven days; the owner can cancel it, and suspending or removing the member withdraws it. The member gets an email (five offer emails per owner a day, one per space and address an hour; the offer stands if the email fails, and the notice says so).
- The member sees **Ownership offered to you** at the top of Spaces (All and Shared with me), counted in the navigation badge, with the new address, **Accept ownership** (a panel explains what changes) and **Decline**. Acceptance is refused if the offer expired, the member is no longer active, has no username, already owns a space at the same slug, or has no allowance left.
- **What accepting changes:** the space's owner, and its address becomes `newowner/slug`. The previous owner stays as an active member whose apps may read and write, and gets an email. Other members, their app permissions and pending invitations stay. Every app connection keeps working in its person's new role (consent rows move between owner and member form, so later suspensions and removals apply to the previous owner's apps). GitHub folders pull and push through the new owner's GitHub connections. The space ID never changes.
- **The old address keeps working and stays reserved.** Website links to `/spaces/oldowner/slug/…` get a 301 to the new address for anyone who may see the space (everyone else gets what any private address gives: sign-in when signed out, not found otherwise). Agents may keep passing the old `owner/slug`; results use the new name. A docs space configured by its old address keeps serving `/docs/`. The old owner cannot create a space at that slug ("reserved") or accept another space onto it; if the space comes back to them, it takes the address back.
- Schema v9: `metatrash_space_transfers` and `metatrash_space_aliases`. New grants: `UPDATE (owner_user_id)` on `metatrash_spaces`, `UPDATE (member_user_id)` on `metatrash_oauth_grant_spaces`, `SELECT, INSERT, DELETE` on both new tables. `mt check` reports v9.
- The repository's in-memory owner is now read and changed under the owned-space lock, so a transfer takes effect for GitHub folders without a restart.
- Tests (new `space_transfer_test.go`, against MariaDB): offer refused without a fresh session, to suspended, nameless, unknown members and by a member, and with another action's CSRF token; the form lists only eligible members; the email; the member's view under each filter and the badge; accept refused for someone else, onto an address in use, over the allowance, after decline, cancel, suspension and expiry; accept and a repeated accept; owner, memberships and repository owner afterwards; both apps keep working, through the old name too, and the previous owner's app follows her new member permission; web redirect for a member and a signed-out visitor of a web-readable space, sign-in or not found for others; docs by the old address; the old address refused for a new space (create form error) and for accepting another space; a transfer back; removing the former owner disconnects his app. Full suite passes against MariaDB. Checked the sharing page (stale, fresh, pending, expired) and Spaces with an offer in headless Chromium at 1300px and 400px.

## 0.29.1 - Links from email keep you signed in; switching accounts by code (2026-10-09)

No schema, configuration or grant change. No new Go modules.

- **Following a link from another site no longer looks signed out.** The session cookie is SameSite=Strict, so a link opened from webmail (the invitation email's `/account` link) arrived without it and went to sign-in even in a signed-in browser. Account pages and `/login`, reached from another site without a session cookie (`Sec-Fetch-Site: cross-site`), now answer with a "Continue to Metatrash" page that reloads itself from this site at once, which sends the cookie: signed-in browsers land on their account, signed-out ones go to sign-in as before. The cookie stays Strict; this is the same reload the app-connection flow already uses. Browsers that don't send `Sec-Fetch-Site` behave as before.
- **Asking for a code for another address in a signed-in browser shows where to enter it.** Before, the code was emailed but the page went straight back to the account already signed in. Now the code page says "This browser is signed in as A. Entering the code signs it in as B instead", with a link to stay signed in as A. Entering the code switches accounts and ends A's session in this browser (as sign-in already did). With no code waiting, `/login` in a signed-in browser still goes to the account.
- Tests (new `account_switch_test.go`): cross-site visits to `/account`, `/account/shared` and `/login` get the reload page; the same-origin reload reaches the account with the cookie and sign-in without it; no reload when the cookie is present; a signed-in browser asking for a second address's code sees the switch notice, the code signs it in as the second account and the first session ends. Full suite passes against MariaDB. Checked both pages in headless Chromium.

## 0.29.0 - One Spaces page (2026-10-09)

No schema, configuration or grant change. No new Go modules.

- **My spaces and Shared with me are now one page, Spaces** (`/account`): one table of the spaces you own and the spaces you joined, with filters **All**, **Mine** (with the allowance, `2/3`) and **Shared with me**. The filters are links (`/account`, `/account/mine`, `/account/shared`), so they need no JavaScript and old links to Shared with me still work. The Access column says `owner` (plus `public` when readable on the web) or `member` (or `suspended`); the address shows whose space it is. Owned rows keep their open, sharing and globe actions; joined rows have open.
- **Invitations sit at the top of Spaces** under All and Shared with me, in a highlighted box with **Accept invitation** and a line saying no username or space of your own is needed. Before, someone invited landed on My spaces, which was about creating spaces, with the invitation only a count in the navigation (off screen on phones), and some thought they had to create a space to accept.
- The navigation has one **Spaces** entry, showing waiting invitations as a badge, otherwise the number of spaces. "Choose your public username to create a space" is now a plain hint under the table instead of a highlighted box, and Create a private space is not shown under Shared with me.
- The invitation email says to open `/account` and choose Accept invitation at the top of Spaces, and that no username or space is needed. The sign-in page hint tells invited people to sign in with the invited address, instead of saying to choose a username and create a space.
- Accepting still returns to Shared with me, where the joined space now appears. Either half of the page being unavailable shows its own message; the page answers 503 only when the filter shown depends on it.
- README and deployment docs name Spaces instead of My spaces / Shared with me.
- Tests: the sections test covers the three filters (one filter marked, the Spaces nav entry marked on all three, no create form under Shared with me); against MariaDB, an invited guest sees Accept invitation under All and Shared with me but not Mine, an account without invitations sees no Invitations box; the email's wording. Full suite passes against MariaDB. Checked in headless Chromium at 1300px and 400px with owned, public, joined and suspended spaces and an invitation, and for an invited account with no username.

## 0.28.1 - Scroll position per document (2026-10-08)

No schema, configuration or grant change. No new Go modules.

- **The document viewer keeps a scroll position for each file** instead of one for all of them. Following a link or a tree entry to a file not yet read in this tab now starts at its top; a file read before comes back where it was left, including with Back and Forward. Before, the preview pane stayed wherever the previous file had been scrolled to.
- **Links with an anchor** to another file in the space (`guide.md#install`) open that file in the page and scroll to the heading, matched by its id or GitHub-style by its text. Before, those links reloaded the whole page and started at the top.
- Positions are kept for the tab only and forgotten on reload.
- Checked in headless Chromium: scrolling one file, opening another (top), returning (same place), Back (same place), and a link to a heading in a third file.

## 0.28.0 - Console commands (2026-10-08)

No schema or configuration change. **Run `deploy/account-grants.sql` again** for one new grant, `UPDATE (max_private_spaces)` on `metatrash_users`, used only by `users set-limit` (without it that command says so; everything else works). No new Go modules.

- **`metatrash help`** lists the commands. Any first argument that is not a flag is now a command, and an unknown one is an error (before, `metatrash anything` started the server). With no command, or only flags, the server starts exactly as before. `version`, `keygen` and `accounts-migrate` are unchanged; their code moved to `cmd/metatrash/console.go`. Command errors print as `metatrash: …` without a timestamp. Flags may go before or after the arguments.
- **`mt`** shortcut (`deploy/mt`): `mt users list` runs `sudo -u metatrash /usr/local/bin/metatrash users list`. `upgrade.sh` installs it as `/usr/local/bin/mt`.
- **`check`** runs the service's startup checks without starting it or taking the data lock: `spaces.json` and keys, `accounts.json` and the files it names (SMTP password, authenticator-app key, OAuth and GitHub settings), the schema, the database login's grants compared with `deploy/account-grants.sql` (missing fail, extra are listed), and the repository of every ready owned space. It says whether the service lock is held, and exits 1 when a check fails. Defaults are the unit file's paths. `EnableAccounts` now reads its configuration through the same function (`loadAccountSettings`), unchanged in behaviour.
- **`users list`**: every account, oldest first, with username, email, created date, owned spaces against the limit (`2/3`), active memberships and sign-in methods (`email` or `email off`, passkeys, `app`, recovery codes left).
- **`users show <who>`** (email in any case, username or user ID): the account in full, with passkey names, when added and last used and whether synced; the authenticator app or an unfinished setup; recovery codes left and when made; owned spaces; memberships with status and app permission; invitations waiting; connected apps that have not expired; GitHub installations.
- **`users email-login on|off <who>`**: the Security switch for administrators, replacing the SQL in deployment.md's Recovery section. Turning it off needs what Security needs. The account is emailed a notice ("A Metatrash administrator turned sign-in by emailed code back on …") through the service's SMTP settings; `-no-notice` skips it, and a failed email is reported without undoing the change.
- **`users set-limit <who> <n>`**: how many private spaces the account may own (0–1000). Spaces already owned stay when the limit is lower.
- **`spaces list`**: every owned space with owner, name, visibility, state, active members and invitations waiting.
- `list`, `show` and `spaces list` take `-json`. Commands read `accounts.json` from `-accounts-config`, else `METATRASH_ACCOUNTS_CONFIG`, else `/etc/metatrash/accounts.json`, use the service's database login and need schema v8. Names chosen by users are printed without control characters.
- Docs: deployment (Console commands section, Recovery uses `mt users email-login on`, upgrade note).
- Tests: a missing or unknown-field configuration refused; `accountGrants` in Go matches `deploy/account-grants.sql`. Against MariaDB: an account listed with its details; found by email in capitals, username with spaces and user ID, not by an unknown name; details of a new account; email sign-in refused off without another method, already on reported as no change, turned back on and then accepted by the service's own check; a limit over the cap refused and a new one stored; the test login's grants exactly as the file. The full suite passes against MariaDB. Checked by hand with seeded accounts: every command's output, `check` with a missing SMTP password file, missing repositories, a held lock, a missing grant and an extra one, and `set-limit` without the new grant.

## 0.27.0 - Sign out everywhere (2026-10-08)

No schema, configuration or grant change. No new Go modules.

- **Signed-in browsers** card on Your account → Security: "You’re signed in here and in 2 other browsers" (or "and nowhere else"), with **Sign out everywhere else** when there are others. It needs no Confirm it's you, since it can only sign people out; this browser stays signed in. The journal gets `account sessions-end user=<id> ended=<n>`.
- **Signed out automatically** everywhere but the browser making the change after removing a passkey, removing the authenticator app (not cancelling an unfinished setup), turning off email sign-in, and changing the email address; their confirmations now say so. **Signing in with a recovery code** signs out every other session too, since it usually means a lost device.
- Connected apps (OAuth) are not affected: revoke them under Services. Sessions are still in memory only, so a restart still signs everyone out.
- Docs: deployment's fresh-install grant step took the first 8 lines of `account-grants.sql`, which since 0.26.0's longer header misses the `metatrash_account_meta` grant (the import then fails with "both account schema tables must exist"); it now takes the v1 block by its comment.
- Tests: counting and ending other sessions (expired ones not counted, other accounts untouched). Against MariaDB, over HTTP: the card's count, Sign out everywhere else ending the other two sessions and keeping this one and another account's, the card afterwards, and a signed-out browser sent to sign in; other sessions signed out after removing a passkey, removing the authenticator app, turning off email sign-in, changing the email address and a recovery code sign-in, with the acting session kept.

## 0.26.0 - Change email address (2026-10-08)

No schema or configuration change. **Run `deploy/account-grants.sql` again** before starting the new binary: the service now needs `UPDATE (email)` on `metatrash_users` (without it, entering the code fails with "temporarily unavailable" and nothing changes). No new Go modules.

- **Change email address** on Your account → Security, in a new Email address card (Profile links to it). After Confirm it's you, enter the new address and Send a code: a six-digit code goes to the new address, and entering it moves the account there. The code waits for ten minutes, only for the session that asked (another session of the same account cannot use it), five wrong tries discard it, and Cancel forgets it. Entering the code does not need a fresh confirmation, so the ten-minute confirmation window does not race the email.
- **Notices to both addresses** once it changes: the old address is told the account moved, to which address, and what to do if it was not the owner (sign in with a passkey, authenticator app or recovery code and change it back, or reply to the email); the new address is told it is now the account's address.
- **An address that already has an account** gets a notice ("this address already has its own Metatrash account") instead of a code, and the page looks the same as for a free address, so the form does not reveal which addresses have accounts. The database's unique key is the final check; a clash at that point is refused without changing anything.
- After the change, codes still waiting for either address are dropped (a login code for the old address would otherwise create a new account there). Sessions, passkeys, the authenticator app, recovery codes, spaces and memberships stay with the account; the authenticator app and recovery codes now sign in with the new address. Pending invitations sent to the old address stay addressed to it (owners invite the new address again); invitations already sent to the new address now show under Shared with me. Passkeys saved in password managers may still be labelled with the old address.
- Limits: five changes started per account a day, plus the usual mail limits on the new address and the IP. The journal gets `account email-change user=<id> result=ok` (no addresses).
- Confirm it's you and Ready for changes now mention the email address.
- Tests: against MariaDB, over HTTP: refused before confirming, an invalid address and the current address (in capitals) refused, a taken address gets a notice and no code, Cancel, the code from another session refused, a wrong code, the change itself with the confirmation window expired (stored address, lookup by the new address, the old one free, both notices, an old-address login code dropped), the code refused a second time, and the database refusing a stale old address or a taken new one. Without the new grant the change fails safely. Checked in headless Chromium.

## 0.25.1 - Try another way (2026-10-08)

No schema or configuration change. No new Go modules.

- **Try another way** on the Check your email page: it forgets the code waiting in this browser and goes back to the sign-in page with every method, so someone who asked for an emailed code but can't reach their email can still use a passkey, the authenticator app or a recovery code. Found by Dave testing the locked-out-of-email case. "Send a new code or use another email" is now just Send a new code (another address is under Try another way).
- Tests: the code page offers Try another way, a bad CSRF is refused, and afterwards the sign-in page no longer waits for a code and offers recovery codes. Checked in headless Chromium.

## 0.25.0 - Recovery codes and turning off email sign-in (2026-10-07)

No schema or configuration change (uses the `metatrash_recovery_codes` table and `email_login` column from v8). No new Go modules.

- **Recovery codes** on Your account → Security (after Confirm it's you): Create recovery codes makes ten single-use codes like `abcd-efgh-jkmn`, shown once on the next page load and never again; the section then says how many are left and when they were made. Create new codes replaces the whole set. Creating codes emails the account a notice.
- **Sign in with a recovery code**: on the sign-in page, Use a recovery code takes the email address and a code (any case, with or without hyphens). It works like the authenticator app: every failure gets the same answer, five attempts per address per 15 minutes plus the per-IP verify limit, and the session counts as freshly confirmed. Each code works once (the row is deleted as the check, so two requests cannot both use it), and the account is emailed a notice saying how many codes are left.
- **Turn off email sign-in**: the Email codes card shows on or off. It can be turned off only with a passkey or an authenticator app and unused recovery codes, after Confirm it's you, and back on the same way; both email a notice. While it is off:
  - Email me a code on the sign-in page shows the usual Check your email page but sends a notice ("sign-in by email is turned off") instead of a code, so the page does not reveal how an address signs in. A code sent before the switch no longer signs in.
  - Confirm it's you no longer offers or accepts emailed codes.
  - The last passkey or authenticator app cannot be removed (checked in the same transaction as the removal, under the account row's lock).
  - With three or fewer recovery codes left, Security warns to create new ones.
- Codes have 12 characters from an alphabet without 0, 1, i, l and o (about 59 bits each) and are stored as SHA-256 salted with the user ID, never in clear. New codes wait in memory for at most ten minutes to be shown once, only to the session that created them.
- Login log: `login recovery …` lines, and `result=email_off` on a send line when a notice went out instead of a code.
- Docs: architecture, deployment (log lines, upgrade and rollback note, how an administrator turns email sign-in back on for a locked-out account), roadmap (step 4 done, owner check added).
- Tests: code format and normalisation, salted digests. Against MariaDB, over HTTP: creating codes refused without confirming, shown once and only to that session, stored only as digests, the switch refused without a strong method or without codes and from a stale session, turning it off (notice, page state, confirm by email refused, last authenticator app kept, `/login/send` sends a notice not a code with the same page, an earlier code refused at verify), recovery sign-in (wrong code, malformed code, an account without codes and an unknown address all the same; right code typed in capitals without hyphens signs in fresh with a notice of 9 left; reuse refused; the sixth attempt 429), new codes replacing old ones, and turning it back on. Pages checked in headless Chromium at desktop and phone widths.

## 0.24.0 - Authenticator app (2026-10-07)

No schema change (uses the `metatrash_totp` table from v8). No new Go modules. New optional setting `"totpKeyFile"` in `accounts.json`; without it nothing changes (see [docs/deployment.md](docs/deployment.md#authenticator-app)).

- **Set up an authenticator app** on Your account → Security (after Confirm it's you): the page shows a QR code and the setup key in groups of four, and the app is turned on by entering the code it shows. Starting again before finishing replaces the key; Cancel setup removes it without confirming. One authenticator app per account. Adding or removing it emails the account a notice.
- **Sign in with an authenticator app**: on the sign-in page, Use an authenticator app takes the email address and the code in one form. A wrong code, an address without an authenticator app and an address without an account all get the same answer ("That code did not work for that email address"), and every attempt counts against that address: five per 15 minutes, plus the existing per-IP verify limit. A successful sign-in is fresh for Confirm it's you, like a passkey or an emailed code.
- **Confirm it's you** also takes a code from the authenticator app (five attempts per account per 15 minutes); Email me a code becomes the quieter button when another method is available.
- **TOTP** in `totp.go`: RFC 6238 with SHA-1, six digits, 30-second steps, the current step and one either side. Each step is accepted once per account (`last_used_step`, checked again in the `UPDATE`, so two requests with one code cannot both succeed). Secrets are 20 random bytes, sealed with AES-256-GCM under the key file's 32-byte key, with the user ID as additional data. The key never reaches the database.
- **QR codes** in `qrcode.go`, standard library only: byte mode, error correction level M, versions 1–40, mask chosen by the standard penalty rules, drawn as one inline SVG path, dark on white in both colour schemes. No image request and no script.
- Login log lines for this sign-in read `login totp ip=… email=… account=existing result=ok`; failures log `invalid_code` or `rate_limited` and the same `account` field as the other lines.
- Docs: deployment (key file, upgrade note, schema loop now runs to v8), architecture, roadmap (step 3 done, owner check added). README and roadmap version references brought up to date.
- Tests: RFC 6238 vectors, the ±1 step window and spent steps, sealing (other account, tampering, version, short value, fresh nonces), the key file, the `otpauth://` URI and grouped key, QR version choice and fixed patterns. Against MariaDB, over HTTP: setup refused without confirming, start and restart (new key, secret not stored in clear, page `no-store`), wrong and right first code, enabling from a stale session, second setup refused, sign-in with a spent code, an address without the app and an unknown address (same answer), sign-in with the next code (fresh session), bad CSRF, invalid address, the per-address limit (429 on the sixth attempt), confirming with the app (wrong code, right code, an account without the app), removal (notice, codes refused afterwards), cancelling an unfinished setup without a notice, and everything off without the key file. The encoder's output was decoded by zxing-cpp for 1 to 2000 bytes (versions 1–38), and the setup page's QR code decoded from headless Chromium screenshots at desktop and phone widths.

## 0.23.0 - Account pages laid out like a space (2026-10-07)

No schema or configuration change. No new Go modules.

- **Your account is now one page per section**, with the sections listed down the left like a space's explorer (a row of tabs on phones):
  - **My spaces** (`/account`, where you land), **Shared with me** (`/account/shared`: invitations and joined spaces), **Security** (`/account/security`: confirm it's you, email codes, passkeys), **Services** (`/account/services`: connected apps and folder services, GitHub for now) and **Profile** (`/account/profile`: username and email). Sign out sits at the bottom of the list.
  - The navigation shows spaces used against your allowance and a badge with the number of pending invitations.
  - A space's sharing page and a connected app's spaces page open inside the same layout, under My spaces and Services.
  - Each page loads only what it shows: GitHub connections and connected apps only on Services, sign-in methods only on Security. Spaces and memberships load on every page for the navigation counts. A section's own data being unavailable still answers 503; another section's does not.
- **Tables instead of stacked boxes.** Spaces, invitations, joined spaces, members, passkeys, connected apps and GitHub installations are one row each, with actions as small icon buttons (with tooltips and accessible names) on the right. On phones each row stacks with its icons top right.
  - Making a space readable on the web is the globe icon: it opens the full warning in a small panel with the confirm button. Making it private again is one click on the highlighted globe.
  - "Readable on the web" is explained once, in a note above the spaces table, shown only when at least one space is public; each public space has a **public** pill.
  - Removing a member or passkey, revoking an app and disconnecting GitHub ask in the same kind of panel first. Renaming a passkey happens in a panel too. No JavaScript needed for any of them.
  - With JavaScript (new `/assets/account.js`, loaded on every signed-in account page), only one panel is open at a time, and a click outside it or Escape closes it (Escape returns focus to its icon). Without it, the icon opens and closes its panel.
  - Confirm with a passkey and Email me a code sit side by side; Confirm it's you and Email codes are cards side by side. Buttons are smaller.
- **Redirects** after a change go to that change's section: sign-in methods to `/account/security`, connected apps and GitHub to `/account/services`, space visibility to `/account`, accepting an invitation to `/account/shared`. The confirmation-code email points at `/account/security`. A failed form is shown again on its own section.
- **Explorer tree:** folders line up with the files beside them, with the open/closed arrow in the same column as the file glyph. A folder's contents indent together one step, with a faint guide line. Before, a folder looked like a child of the file above it.
- Tests: every section over HTTP against MariaDB (its content, only its own data, exactly one current navigation entry, sign-in redirect), the sharing page under My spaces, an unknown section not served; existing tests moved to the section that now shows their content and the new redirect targets. Checked in headless Chromium at desktop and phone widths, including the panels.

## 0.22.0 - Passkeys (2026-10-06)

**Needs schema v8** before the new binary starts: stop the service, back up, apply `deploy/account-schema-v8.sql`, then `deploy/account-grants.sql` again (see [docs/deployment.md](docs/deployment.md#upgrades-backups-and-rollback)). No configuration change. No new Go modules.

- **Sign in with a passkey.** The sign-in page has a **Sign in with a passkey** button above the email form, and needs no email address (discoverable credentials). Browsers that support it also offer passkeys in the email field's autofill (conditional UI). Without JavaScript or passkey support the button stays hidden and the email form works as before. Signing in this way never says which methods an email address has.
- **Sign-in methods** on Your account: add a passkey (with a name, such as "Pixel phone"), rename it, remove it. The list shows when each was added and last used, and whether it is synced.
  - **Confirm it's you.** Adding or removing a passkey needs a sign-in or confirmation within the last 10 minutes. Confirm with one of your passkeys or with a code emailed to the account's address (same mail limits as sign-in codes). Renaming needs no confirmation.
  - **Notices.** Adding or removing a passkey emails the account a notice (best effort; it never blocks the change).
  - Email codes stay on for everyone. Schema v8 already holds the authenticator-app (TOTP) and recovery-code tables and an `email_login` flag on accounts, all unused in this version, so the next steps need no schema change.
- **WebAuthn**, implemented with the standard library only (like the OAuth server), in `webauthn.go`: a small CBOR reader, COSE keys ES256, EdDSA (Ed25519) and RS256 (2048–4096 bits), attestation `none` (any attestation statement is ignored), user verification required, origin and RP ID (`metatrash.com`, from the account origin) checked. Challenges are stateless and signed with the service secret for one purpose (sign in, add, confirm), bound to the browser's login cookie or the session, valid 10 minutes and accepted once. Signature counters must move forward unless they stay at zero (synced passkeys); a change in backup eligibility is refused. Up to 20 passkeys per account.
- Sessions record when they last proved who the user is (`AuthAt`); a sign-in by code or passkey sets it. Sessions are still in memory, so a restart signs everyone out as before.
- Login log lines for passkey sign-ins read `login passkey ip=… email=… account=existing result=ok`; failures log the code, such as `unknown_passkey` or `invalid_passkey`.
- Account pages load the new `/assets/passkey.js`.
- Tests: CBOR edge cases, COSE keys (off-curve point, 1024-bit RSA, unsupported algorithm), registration and sign-in with software ES256, Ed25519 and RS256 authenticators, and refusals (no user verification, wrong origin, RP ID, type, cross-origin, forged, expired, replayed or wrong-purpose challenge, bad signature, other user handle, counter going back, backup eligibility change). Against MariaDB: the whole flow over HTTP (confirm required, email confirmation with wrong code and from another session, add, replay, duplicate, sign in, replay, unknown passkey, challenge from another browser, confirm with passkey, another account's passkey, rename, remove, removed passkey refused, notices). In headless Chromium with a virtual authenticator on `https://metatrash.com` (real TLS): email sign-in, add, sign-in through autofill and through the button, confirm with a passkey, the browser refusing a second passkey on the same authenticator, remove, removed passkey refused, no unexpected console errors.

## 0.21.2 - About page from the docs space; links in documents (2026-10-06)

No schema or configuration change.

- **About from the docs space.** With `docsSpace` set, `/about` redirects (302, temporary) to `/docs/about.md` and every footer's About link points there. Without it, the built-in About page is unchanged. The About text now lives as `about.md` in dave-zap/metatrash-com.
- **Links in documents** (explorer and `/docs/`). Slate opens every link in a new tab; now links to this site open in the same tab: files listed in the tree without a reload (relative links like `terms.md` or `../about.md` and absolute ones like `/docs/legal/terms.md`), other pages of the site with a normal load. Links to other sites still open in a new tab. `mailto:` and other non-web links open normally, without an empty tab. Ctrl/Cmd/Shift/Alt-clicks are left to the browser. In `web/document.js`; Slate is unchanged.
- Tests: built-in `/about` without a docs space, the redirect and footer About links with one. In headless Chromium: relative, `../` and absolute links between docs pages without reloads, back button, a link to `/`, an external link in a new tab, `mailto:` without `target`, no console errors.

## 0.21.1 - Site docs under /docs/; privacy and terms links; search indexing (2026-10-06)

No schema change. Optional setting `"docsSpace": "owner/slug"` in `accounts.json` (see [docs/deployment.md](docs/deployment.md#site-docs)).

- **`/docs/`** shows the configured space as part of the website: the same read-only explorer (in-page navigation, Markdown preview) at `/docs/…` instead of `/spaces/owner/slug/…`, without the space bar (owner, space name, visibility) and without GitHub folder labels; the explorer heading reads DOCS. `/docs` redirects to `/docs/`, which shows `README.md`.
  - Only while the space is readable on the web. Otherwise, and for any missing file, `/docs/…` is not found for everyone, the owner and members included, and never sends anyone to sign in. The docs pages never look at the visitor's session.
  - Search engines may index every `/docs/` page (no `noindex`). Still `no-store`. The space's own `/spaces/…` address keeps working as before.
  - Anyone with write access to that space (the owner, members whose apps may write) changes what the site shows under `/docs/`.
- **Footer links** to Privacy (`/docs/legal/privacy.md`) and Terms (`/docs/legal/terms.md`) on every page (home, about, explorers, account and sign-in, app consent, GitHub return), shown only when `docsSpace` is set.
- **Search engines under `/spaces/`:** in spaces anyone can read (the public space and spaces readable on the web), only files in the space's top folder are indexable; files in subfolders get `noindex, nofollow` (meta tag and `X-Robots-Tag`). Members-only pages, errors and sign-in redirects stay `noindex`. The warning before making a space readable on the web now says so. Before, the public space was indexable throughout and web-readable spaces not at all.
- The owned-space explorer is now one handler shared by `/spaces/…` and `/docs/`; behaviour of `/spaces/…` is unchanged.
- Tests: `/docs/` against MariaDB (not configured, private space for signed out and owner, web-readable page without space chrome and with `/docs/` tree links, `/docs` redirect, missing file, query, POST, back to private) and the footer links on home, about, sign-in and public explorer; indexing of `/docs/` files, a web-readable space's top folder and subfolder, a members-only page, and the public space's top folder and subfolder. In headless Chromium: tree clicks and back stay under `/docs/` without reloads; the footer Terms link opens the page.

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
