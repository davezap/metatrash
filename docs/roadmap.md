# Roadmap

Open work after 0.40.1. Done work is in [CHANGELOG.md](../CHANGELOG.md).

## Fewer tokens per change: later

Agreed with Dave 2026-10-16 as possible follow-ups to 0.40.0.

- **`append` tool.** Add text to the end of a file (creating it if absent)
  without reading it: logs, notes, agent-to-agent messages. No `ifInState`
  needed; same limits and rules as write.
- **Unchanged reads.** `read` takes the blob hash (or state) the agent already
  has and answers `unchanged: true` without the text when the file is the
  same. Needs read results to carry the file's blob, which MCP results do not
  today.

## Sign-in methods

Decided with Dave 2026-10-06 (`notes/auth-handoff.md` in the `dave-zap/metatrash`
space): passkeys are the primary sign-in, an authenticator app (TOTP) an optional
alternative, both opt-in. Email codes are demoted to a backup and may later be
turned off per account, or site-wide. Recovery must exist before anyone can turn
email off. The OAuth server for agents is unchanged.

Passkeys, the authenticator app, recovery codes and the per-account email
switch are done (0.22.0–0.25.0). Someone locked out emails Dave, who turns
email sign-in back on with `mt users email-login` (deployment.md, Recovery).
Open: whether to remove email codes site-wide (needs an answer for sign-up).

Possible extras: when a passkey that is not registered tries to sign in, tell
the browser to forget it (`PublicKeyCredential.signalUnknownCredential`, Chrome
132+), so removed passkeys stop being offered.

## Account lifecycle

Agreed with Dave 2026-10-06. Done: change email address (0.26.0), sign out
everywhere (0.27.0), transfer space ownership (0.30.0), export a space
(0.38.0: REST, `mt spaces export`, `mtsync export`). Lost-everything recovery
was dropped 2026-10-08 (Dave turns email sign-in back on by hand). Still to
build, in this order:

1. **Delete a space** (owner, step-up, type the name). Members and connected
   apps lose access; GitHub folders are unlinked, the repo is untouched.
2. **Download a space** from Your account (the zip REST and the console
   already make), offered before deleting; also answers access requests.
3. **Delete account.** Refused while the account owns any space (delete or
   transfer first). On confirm (step-up): the
   account is locked at once, sessions and OAuth grants revoked, and a notice
   is sent. **30-day grace period**: signing in during it cancels the
   deletion. Then purge: memberships, invitations, GitHub installations,
   passkeys, TOTP, recovery codes and the user row. Final notice email.
4. **Update the privacy policy** (`legal/privacy.md` in the
   `dave-zap/metatrash-com` space, live since 0.21.1) once deletion exists
   (sign-in sessions, device label and IP with 24-hour retention, added
   2026-10-09):
   what sign-in methods store (passkey public keys, sealed authenticator
   secrets, hashed recovery codes), how account deletion and the 30-day grace
   period work, and concrete retention for logs and backups.

## Folders: first version

From the design session of 2026-10-03 (notes in the `dave-zap/metatrash` space,
`notes/design-session-2026-10-03.md`). The folder is the unit; a folder's
`.metatrash.json` lists its services and actions.

1. **Repo folder, boundary only.** Done (0.16.0).
2. **One-way sync, space → GitHub.** Connect, dot names, `pull`, `pending`
   and `push` are done (0.17.0–0.20.0, see [github.md](github.md)): the
   Metatrash GitHub App (`metatrash-github`, App ID 5184697, on Dave's
   account), never force-push, repos never nest. Still open:
   - `auto` push mode (quiet-period batches; `push` still works).
   - Binary files (images and PDFs by content allow-list, base64 in
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
   auto), triggered by the push webhooks the app already receives.
4. **One folder action.** A write into an action folder triggers one process;
   the result lands as a file. Enables the reserved `actions` key.

## Owner checks still to run

From 0.30.0 (2026-10-09), schema v9 first:

- **Transfer:** on a test space, invite a second account, accept, confirm
  it's you, offer ownership. The second account gets the email and sees
  Ownership offered to you; accept. Check the address, that the first
  account is now a member, that its connected app still reads and writes
  using the old `owner/slug`, and that the old web address redirects. Try
  creating a space at the old address from the first account (refused).
  Transfer it back.

From 0.20.0 / 0.21.0 (2026-10-05), not yet tried live:

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
  lists a `push` delivery (needed for step 3).
- **Readable on the web (0.21.0):** apply schema v7 first. Make a space
  readable, read the warning, open it in a private window (no sign-in), make
  it private again (the private window gets the sign-in page). An agent
  connected by another account still cannot reach it.
- **Explorer navigation (0.21.0):** open folders, click between Markdown and
  text files (no reload, folders stay open), use back and forward, reload a
  nested file (its folders open).
- **Site docs (0.21.1):** set `"docsSpace": "dave-zap/metatrash-com"` in
  `accounts.json` and restart. Signed out (private window): the footer shows
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
- **Change email address (0.26.0):** run `deploy/account-grants.sql` first.
  On Security, confirm it's you, enter a second address you own under Email
  address and Send a code. Enter the code: the page shows the new address,
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
- **Passkeys (0.22.0):** apply schema v8 first. On Your account → Sign-in
  methods, add a passkey on your phone or computer (sign in fresh, or use
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

## mtsync

- **Desktop editor support.** Gaps G2–G8 in `developers/mtsync-editor-integration.md`
  in the `dave-zap/metatrash-com` space: `--json` on `spaces` and `list`,
  `whoami`, `resolve URL`, `files` and `status`. G9 (`history`, `show`) is
  done in 0.41.0; Slate Viewer's History panel uses it next. G1
  (downloads) is met by GitHub releases with `SHA256SUMS` and `mtsync.json`,
  except code signing.
- **Code signing** for the release binaries (Windows Authenticode, macOS
  Developer ID and notarization); needs certificates in the repository's
  Actions secrets. See deployment.md, Automated builds and releases.
- Deferred with Dave 2026-10-12: bulk first download, on-demand files like
  OneDrive, automatic three-way merge of conflicts.

## Desktop editor

- **History panel** for the open note: `mtsync history -json` newest first,
  10 at a time, and a click opens that version in Slate's Compare view
  (agreed with Dave 2026-10-17).
- **Editing core** (discussed 2026-10-16): keep Slate's toolbar, outline,
  embeds, wiki links and attachments, and replace its per-line live blocks
  and Source textarea with CodeMirror 6 for undo history, reloading files
  mtsync changed without moving the cursor, and its merge view for
  Compare/Resolve of conflict copies. Chart blocks (Mermaid first) as
  CodeMirror widgets and preview renderers.

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
- A focused security review of the account, sharing, OAuth and sign-in
  (passkey, TOTP, recovery) code, which the 2026-09-30 review predates.

## Later

- **Spaces:** rename, pagination of sharing lists over 200 entries.
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
