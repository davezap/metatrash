# Roadmap

Open work after 0.43.0. Done work is in [CHANGELOG.md](../CHANGELOG.md).

## Fewer tokens per change

Follow-ups to 0.40.0 (`edit`, `search`, read line ranges), agreed with Dave
2026-10-16 and 2026-10-18.

- **`append` and `insert` tools.** Add text to a file without reading it.
  `append` adds to the end (creating the file if absent): logs, notes,
  agent-to-agent messages. `insert` puts text before a given line number
  (1 = top), for files whose newest entries come first under a title block,
  such as a changelog. Neither needs `ifInState`; same limits and rules as
  write. Possibly one tool with an optional line (absent = end). Today `write`
  replaces the whole file, and `edit` can already insert after known text
  (`oldText` the title block, `newText` the title block plus the new entry),
  which is safer when other agents may have shifted the lines; a line number
  suits text with no unique anchor.
- **Unchanged reads.** `read` takes the blob hash (or state) the agent already
  has and answers `unchanged: true` without the text when the file is the
  same. Needs read results to carry the file's blob, which MCP results do not
  today.

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
   retention for logs and backups.

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

- **Owner-set read-only files** (private spaces). The owner marks files or
  folders read-only: agents can read them but not write, edit, move or delete
  them. The owner still edits them through their own copy, with mtsync and
  Slate Viewer, so the rule applies to connected agent apps and not to the
  owner's sync client (mtsync signs in through the built-in client
  `{issuer}/oauth/clients/sync`, which the service can tell apart). README.md
  becomes one of these: read-only by default, editable if the owner chooses.
  Open: where the setting lives (likely the folder's `.metatrash.json`, which
  then itself needs protecting from agents), whether members' sync clients
  count as the owner's, and `mt spaces readme -all` skipping READMEs an owner
  has taken over. The public space and key spaces keep a locked README, reset
  with `mt spaces readme`: anyone can write there, and agents read the README
  first, so an editable one would let anyone plant instructions for them.
- **Folder configurator in Slate Viewer.** A `…` button beside the selected
  file or folder opens its settings, written to the folder's
  `.metatrash.json`: read-only (owner only), purpose and child descriptions,
  and later services and triggers (GitHub, folder actions).
- **Folder selection in Slate Viewer.** The file tree selects folders as well
  as files, so the configurator has something to act on.

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
4. **One folder action**: a write into an action folder triggers one process;
   the result lands as a file. Enables the reserved `actions` key.

Later: letting push send `.github/workflows/` changes (needs the app's
`workflows` permission; refused for now because workflows can reach CI
secrets); a member permission "write but not push", so a supervising agent
reviews `pending` and pushes; possibly a GitHub service on the space root;
the `review` push mode, which needs a review screen.

## mtsync and Slate Viewer

- **Editor gaps G2–G8** (`developers/mtsync-editor-integration.md` in the
  `dave-zap/metatrash-com` space): `--json` on `spaces` and `list`, `whoami`,
  `resolve URL`, `files` and `status`.
- **macOS code signing** (Developer ID and notarization) for mtsync and
  Slate Viewer; needs an Apple Developer account. Windows files are signed
  with Azure Artifact Signing since 0.43.2 (deployment.md, Code signing).
- **Slate**: chart blocks (Mermaid first) as CodeMirror widgets and preview
  renderers; a read-only Compare, so Slate Viewer's History panel works on
  documents it cannot edit.
- **Website viewer**: the site still bundles Slate 0.9.2 for read-only
  viewing; upgrading it was deferred with Dave (2026-10-17).
- **Slate Viewer as the space manager**: the desktop companion for managing
  Metatrash spaces (agreed direction 2026-10-17, not yet planned in detail).
- Deferred with Dave 2026-10-12: bulk first download, on-demand files like
  OneDrive, automatic three-way merge of conflicts.

## Maintenance

- **Public space README**: after deploying 0.43.0, run
  `mt spaces readme public` (it still says "MCP endpoint `/mcp` is pending",
  checked 2026-10-18), and consider `mt spaces readme -all` for owned spaces
  made with older templates.
- [Security review](security-review-2026-09-30.md) F3: cache recent activity
  by head hash, cap history scanned per request and bound concurrent Git
  readers. Hardening from the same review: systemd resource limits, an
  allowlist for Markdown link schemes, structured security-event logging, and
  a dependency/advisory scan.
- A focused security review of the account, sharing, OAuth and sign-in
  (passkey, TOTP, recovery) code, which the 2026-09-30 review predates.

## Owner checks still to run

Live checks not yet reported done. The server is at schema v11 with current
grants, so the schema and grant steps these once needed are behind us.

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
