# Human sharing — Stage 4, 0.11.0

This completes the local human-facing Stage 4 implementation on the 0.10.0
invitation and membership backend. Owner build and two-account validation are
pending. Use the existing schema v4 and grants; no additional SQL or Apache
changes are needed. Upgrades from before 0.10.0 still require the
[schema v4 upgrade](human-membership-storage.md).

## Human flow

- Open Your account, then Manage sharing beside a ready owned space. The owner
  is shown separately. Invite by email; no invitation email is sent. Ask the
  recipient to sign in with that email and check Your account.
- Pending invitations expire after seven days. Creating the same live invitation
  preserves its expiry. Cancel from Manage sharing; invite again to renew an
  expired invitation. Cancelled/accepted invitations are omitted from that list.
- The recipient accepts from Your account, even without a public username.
  Acceptance uses the authoritative verified email and binds membership to user ID.
- Joined spaces appear separately and do not use the member's owned-space
  allowance. Active members browse the owner's existing URL read-only.
- The owner can suspend, restore or remove invited members. Removal requires a
  new invitation and acceptance to return. Old acceptance forms cannot restore
  suspended or removed access. Changes apply only to the selected space.

Sharing lists each show at most 200 entries, with explicit labels and deterministic
ordering. Queries use five-second deadlines. Pagination is outside this bite.
Errors show reload/retry guidance; successful forms redirect to refreshed lists.
New private-space READMEs mention human sharing; existing repository content is
not rewritten.

## Security boundaries

Forms reuse account Host/Origin checks, POST-only handling, 2 KiB bounded and
allowlisted form fields, secure session cookies, and action-specific session CSRF.
Membership mutations share a 30-attempt per-user ten-minute limit and call the
existing owner/acceptance transactions. Sharing details require the current owner;
request fields never establish actor identity. Private browsing checks current
ownership or active membership on every request before read-quota reservation or
Git access. Failures deny access; no session-level membership cache is introduced.
Private no-store/noindex headers, read-only viewer behavior and hosting prefixes
remain in place. REST/MCP and agent integration are unchanged.

## Focused two-account checks for the owner

1. With account A, open Manage sharing and invite account B. Confirm no email is
   promised; B sees the invitation only when signed in with the matching email.
2. With B, accept and open Joined spaces. Confirm A's URL, document navigation,
   read-only viewing and unchanged owned-space allowance. Acceptance needs no
   public username. B cannot open A's sharing page or submit owner actions.
3. Keep B signed in. Suspend from A, then request a document again from B: it
   must be denied. Restore and reload to regain access. Remove B and confirm
   denial; invite again and accept to regain access.
4. Cancel a pending invitation and try its old acceptance form. Also try an old
   acceptance form after suspension/removal: neither should restore membership.
   Missing/wrong CSRF or Origin must reject mutations. Owner access remains intact.

Only brief source/formatting and patch checks were performed locally. No build,
test execution, database changes or deployment were performed.
