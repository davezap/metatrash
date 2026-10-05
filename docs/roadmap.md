# Roadmap

Open work after 0.20.0. Done work is in [CHANGELOG.md](../CHANGELOG.md).

## Folders: first version

From the design session of 2026-10-03 (notes in the `dave-zap/metatrash` space,
`notes/design-session-2026-10-03.md`). The folder is the unit; a folder's
`.metatrash.json` lists its services and actions.

1. **Repo folder, boundary only.** Done in 0.16.0 and 0.16.1: `.metatrash.json`,
   GitHub designation, nesting rules, root file in every space, `delete`,
   `deletable`, history of deleted files. Checked live by Dave 2026-10-03.
2. **One-way sync, space → GitHub.** Agreed 2026-10-03:
   - Credentials through a **Metatrash GitHub App** (installed on the repo,
     short-lived tokens, webhooks reused by step 3). Owned by Dave's account
     for now, possibly a dedicated Metatrash GitHub account later (apps can be
     transferred).
   - Agent commands: `pending` (files changed in a GitHub folder since the
     last push, with the last-pushed and current revisions) and `push`
     (space, folder, message; all or nothing, never a partial push). An agent
     that objects to a change restores the file from history, moves it out of
     the folder, deletes it or adds it to `.gitignore`.
   - Push modes in `.metatrash.json`: `agent` (default; only `push` sends),
     `auto` (quiet-period batch; `push` still works) and `review` (parked; no
     push until the review UI exists).
   - Dotfiles allowed only inside GitHub folders (`.git` never); push follows
     the folder's `.gitignore` (common patterns).
   - Never force-push: if GitHub moved ahead, `push` stops and says so.
   - Chunks, in this order (reordered 2026-10-05 with Dave: push needs a
     baseline, because nearly every repo already has commits and push never
     forces):
     - 2a connect GitHub — done in 0.17.0, see [github.md](github.md). Checked
       live 2026-10-04: app `metatrash-github` (App ID 5184697) on Dave's
       account; connect and signed webhook delivery work.
     - 2b dot names and `.gitignore` — done in 0.18.0, with Link an existing
       installation (for apps installed from GitHub's page first).
     - 2c baseline — done in 0.19.0: the `pull` tool fills a GitHub folder
       from the repo and records the commit it came from in
       `.metatrash/github.json` (installation token from the app JWT, GitHub
       API and one tarball, no clone). Later pulls fast-forward an unchanged
       folder. Files Metatrash cannot hold are skipped and will be carried
       through by push.
     - 2d `pending` and `push` — done in 0.20.0: Git Data API push on the
       baseline tree with a tree-hash check, never forced; GitHub moved past
       the baseline → stop. Pull merges by file (no file changed on both
       sides) and converts UTF-16/UTF-32 to UTF-8. Push author is the
       app (Verified) with the user as `Co-authored-by` (GitHub account when
       linked). Hint in write/move/delete results after 30 minutes unpushed.
     - 2e `auto` mode.
     - Step 3 (ongoing pull) reuses the baseline fetch, triggered by push
       webhooks.
   - Repos never nest (decided 2026-10-03, kept for simplicity and so agents
     have one rule to learn): no GitHub folder inside or around another.
     Related repos sit side by side, e.g. under `dependencies/`. The refusal
     names the clashing folder and suggests that.
   - After 2d: binary files (images and PDFs by content allow-list, base64 in
     the API, image content on MCP reads). Until then pull skips them and push
     carries them through.
   - Later: consider letting push send `.github/workflows/` changes (needs
     the app's `workflows` permission; refused for now because workflows can
     reach CI secrets, so anyone who can push could reach them).
   - Later: a member permission "write but not push", so working agents edit
     and a supervising agent reviews `pending` and pushes. Possibly a GitHub
     service on the space root (the whole space as one repo, so no other
     GitHub folders in it).
3. **One-way sync, GitHub → space.** Repo changes appear in the folder (`pull`
   auto).
4. **One folder action.** A write into an action folder triggers one process;
   the result lands as a file. Enables the reserved `actions` key.

## Done in 0.15.1: Change spaces

Found in live use:

- The only way to add a space to an existing connection is Revoke (Your
  account → Connected apps) and reconnect. Deleting and re-adding the connector
  in Claude does not restart OAuth: Claude reuses its cached tokens and
  Metatrash is never told the connector was removed. (Checked 2026-10-03:
  **Disconnect** then Connect on the connector in Claude does restart OAuth,
  shows the consent page with the previous choices, and refreshes Claude's
  copy of the tool schema.)
- Approving consent replaces the connection's space list instead of adding to
  it (after a reconnect, bartco dropped off because only metatrash was ticked).

Proposal: a **Change spaces** action on each Connected apps entry, reusing the
consent page's space chooser pre-filled with the current choices, editing the
live connection in place with the same checks as consent and applying to the
app's next operation. Add a hint that new spaces are added there.

Done (see CHANGELOG). Consent still replaces the list it shows, which is
correct for a pre-filled form; the bartco loss came from Revoke clearing the
previous choices, and Change spaces removes the need to revoke.

Checked live 2026-10-03 with Claude: removing metatrash and adding bartco
read-only through Change spaces applied to Claude's next calls without a
reconnect (`spaces` updated, metatrash `not_found`, bartco listed), and a write
to bartco was refused with `insufficient_scope`. Adding metatrash back worked
the same way.

Also in 0.15.1:

- **Agents don't need the 32-hex ID.** `owner/slug` names, IDs still
  accepted. Bare slugs were dropped on purpose: their meaning changes as
  spaces are connected.

## Owner checks still to run

- Restart the service; Claude and ChatGPT reconnect by refreshing silently.
- With two accounts: set a member's app permission to read only; suspend,
  restore and remove a member who has a connected app; re-inviting does not
  restore the space until they consent again.
- Apache and service logs contain no `mt_at_`, `mt_rt_` or `mt_ac_` values.
- ChatGPT end to end. If it cannot use CIMD it will need Dynamic Client
  Registration, which [oauth.md](oauth.md) allows adding only if needed.

## Maintenance

- The live public space README still says "MCP endpoint `/mcp` is pending". The
  service will not let agents edit it; an administrator needs to update it in
  the repository while preserving its file ID and history (the current text is
  `docs/space-README.template.md`).
- [Security review](security-review-2026-09-30.md) F3: cache recent activity by
  head hash, cap history scanned per request and bound concurrent Git readers.
  Hardening items from the same review: systemd resource limits, an allowlist
  for Markdown link schemes, structured security-event logging, and a
  dependency/advisory scan.
- A focused security review of the account, sharing and OAuth code, which the
  2026-09-30 review predates.

## Later

- **Spaces:** rename, delete, ZIP export of HEAD, ownership transfer,
  pagination of sharing lists over 200 entries.
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
