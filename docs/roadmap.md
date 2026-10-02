# Roadmap

Open work after 0.15.1. Done work is in [CHANGELOG.md](../CHANGELOG.md).

## Done in 0.15.1: Change spaces

Found in live use:

- The only way to add a space to an existing connection is Revoke (Your
  account → Connected apps) and reconnect. Deleting and re-adding the connector
  in Claude does not restart OAuth: Claude reuses its cached tokens and
  Metatrash is never told the connector was removed.
- Approving consent replaces the connection's space list instead of adding to
  it (after a reconnect, bartco dropped off because only metatrash was ticked).

Proposal: a **Change spaces** action on each Connected apps entry, reusing the
consent page's space chooser pre-filled with the current choices, editing the
live connection in place with the same checks as consent and applying to the
app's next operation. Add a hint that new spaces are added there.

Done (see CHANGELOG). Consent still replaces the list it shows, which is
correct for a pre-filled form; the bartco loss came from Revoke clearing the
previous choices, and Change spaces removes the need to revoke.

Also in 0.15.1:

- **Agents don't need the 32-hex ID.** `owner/slug` names, IDs still
  accepted. Bare slugs were dropped on purpose: their meaning changes as
  spaces are connected.

## Next: 0.15.2

- **One field when creating a space.** The user enters only a name and the slug
  is derived from it. Both columns stay, so separate slugs can return later. A
  name that gives an invalid or already used slug is reported on the name field.

## Owner checks still to run

- A read-only space refuses writes from a connected app.
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

- **Spaces:** rename, delete, ZIP export of HEAD, ownership transfer, invitation
  emails, pagination of sharing lists over 200 entries.
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
