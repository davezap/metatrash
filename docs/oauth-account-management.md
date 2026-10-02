# Connected apps and member app permissions — 0.15.0

Fourth and last bite of the [OAuth agent access plan](oauth-agent-access-plan.md).
No new SQL: it uses schema v5 from [the foundation](oauth-foundation.md).

## Your account: Connected apps

With `oauth.enabled`, Your account has a **Connected apps** section:

- The connector address to paste into Claude, ChatGPT or another MCP app
  (`https://metatrash.com/mcp/account`).
- Each live connection: the app's name and client host, MCP or REST, when it was
  connected and last used, and each consented space with its current access
  (*read and write*, *read only*, or *suspended by owner*). Access shown is the
  consent narrowed by membership status and the owner's member permission.
- **Revoke access** deletes the connection with its consent and tokens at once.
  The app's next request gets 401; connecting again asks for consent again.
  The form uses the account page's exact Origin and a session-bound CSRF value,
  and only deletes connections belonging to the signed-in account.

Last use is recorded at most once an hour. Connections unused for
`grantIdleDays` (default 90) expire and disappear from the list.

## Manage sharing: member app permission

On an owned space's **Manage sharing** page, each member has *Their connected
apps may: Read and write / Read only* (default read and write). Only the owner can
change it; the change locks the space like other membership changes and applies
to the member's next app operation. The consent page offers members read and
write only where the owner allows it. Owners always have full access to their
own spaces. Suspension, restoration and removal work as before and also apply to
apps: suspension blocks app access, removal deletes the space from the member's
connections.

## Other changes

- README connection instructions now cover private spaces from Claude and
  ChatGPT through `/mcp/account`.
- The My Spaces note points to Connected apps instead of saying agent access is
  unavailable.

## Deploying 0.15.0 from the live 0.10.0

1. Stop the service, back up the database and data directory.
2. Apply `deploy/account-schema-v5.sql` and the grants in
   [the foundation notes](oauth-foundation.md#owner-upgrade). 0.11.0 needed no SQL.
3. Build and install 0.15.0. Start once with OAuth still disabled and check
   that browsing, sharing and the public `/mcp` behave as before.
4. Add `"oauth": {"enabled": true}` to the account configuration (defaults:
   `claude.ai` and `chatgpt.com` client hosts, 1-hour access tokens, 30-day
   rotating refresh tokens, 60-second codes, 90-day idle expiry) and restart.
5. Root hosting needs no Apache change. Folder hosting needs the two discovery
   lines in [the authorization notes](oauth-authorization.md#apache).
6. Run the owner checks in [protected endpoints](oauth-protected-endpoints.md#checks)
   and below.

## Checks

Built, vetted and tested with Go 1.25.1. A MariaDB integration test covers the
Connected apps listing, a non-owner and a forged CSRF value failing to change
member app permission, an invalid permission, the owner lowering it (the
member's app then gets `insufficient_scope` and the listing shows read only),
another account failing to revoke a connection, revocation ending the token
immediately, and the empty listing afterwards.

Owner checks:

- Connect Claude, then see it under Connected apps with the chosen spaces.
  Revoke it; Claude's next tool call fails and reconnecting shows consent again.
- With two accounts: as owner set a member to Read only; the member's connected
  app can read but not write that space, and their consent page no longer offers
  read and write for it.
- Suspend the member; their listing shows *suspended by owner* and the app is
  refused. Restore, then remove; the space disappears from their connection.
