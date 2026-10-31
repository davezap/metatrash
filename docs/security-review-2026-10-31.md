# Security review of sign-in, OAuth and sharing (2026-10-31)

**Status (0.76.0):** F1, F2, F4 and F5 fixed. F3 is an Apache change on the
server (deployment.md, Apache). F6 and F7 are on the roadmap under
Maintenance.

Reviewed: Metatrash 0.75.0 source in the laptop checkout. A source review only,
like the [2026-09-30 review](security-review-2026-09-30.md): no live attacks,
no deploy settings inspected beyond the examples in `deploy/`. It covers what
that review predates: accounts and sessions, email codes, passkeys, the
authenticator app, recovery codes, email change, the OAuth server and token
checks, memberships and invitations, transfer, and the cookie site API (0.72.0)
behind Slate Viewer's UI on the site.

**Summary.** No way in without a sign-in method was found, and no way for one
account to reach another's spaces. The building blocks are careful: single-use
signed challenges bound to the browser, `__Host-` SameSite=Strict cookies plus
exact Origin and per-form CSRF tokens, digests instead of secrets in the
database, step-up for sign-in method changes, PKCE-only public OAuth clients
from an allow-list, refresh-token rotation with replay revocation, and access
rechecked against live membership on every request. The findings are about
budgets and availability: how many guesses an attacker gets, a mail budget an
attacker can spend for everyone, rate limits that may see Cloudflare instead of
people, and one lock that every signed-in request queues on.

| # | Severity | Finding |
|---|----------|---------|
| F1 | Medium | Authenticator-app sign-in allows about 480 guesses a day per address |
| F2 | Medium | One 100-a-day mail budget can be spent by anyone, which also silences security notices |
| F3 | Medium (check) | Behind Cloudflare, every per-address limit is per Cloudflare edge |
| F4 | Medium (availability) | Every signed-in request holds the accounts lock across a database query |
| F5 | Low | A stolen session can be turned into lasting access without confirming or any notice |
| F6 | Low | A repeated refresh ends the whole app connection |
| F7 | Low | Attempt budgets live in memory and reset on restart |

## F1. Authenticator-app sign-in allows about 480 guesses a day per address

`totp.go` `totpSignIn`, `totpAttempts`: `/login/totp` takes an email address
and a six-digit code. The only per-address budget is 5 attempts per 15 minutes
(`totp:email:…`, 5 per 900 s), so 480 a day, from any number of addresses.
Each guess matches one of three codes (the current step and one either side)
out of a million: about 0.14% a day, or roughly 40% over a year of quiet,
steady guessing against an address known to have the app. Nothing tells the
owner: wrong codes send no notice. The authenticator app on its own signs in,
so this is a full account takeover when it succeeds. (Emailed codes are
bounded much harder by the mail budgets, and every guess there emails the
owner. Recovery codes are 12 characters from 31, far out of reach.)

**Fix:** add a daily cap per address and per account (10 a day brings the
chance to about 1% a year), and email the owner once a day when wrong codes
reach it, saying the app's sign-in is paused until tomorrow and how to sign in
otherwise. Optionally persist the daily counts (F7).

## F2. One small mail budget can be spent by anyone, and it silences security notices

`account_http.go` `/login/send`, `signin_http.go` `sendSecurityNotice`: every
email the site sends (sign-in codes, confirmation codes, email-change codes and
security notices) draws from `mail:global:day`, 100 a day. Since the
2026-09-30 fix only requests that pass every narrower check spend it, but each
client address may still send 10 an hour after an 18-bit proof of work (about
a second in a browser, less in native code). Ten addresses for an hour spend
the day's budget: no one can sign in by email until UTC midnight. Worse,
`sendSecurityNotice` skips silently when the budget is gone, so an attacker who
spends it first and then succeeds at F1, uses a recovery code, or adds a
passkey with a stolen session leaves the owner without the "sign-in method
added" or "recovery code used" email that would have warned them.

**Fix:** give security notices their own budget, so codes can't use it up, and
log when any budget stops a mail. Consider a higher, configurable code budget
sized to the SMTP provider's own limit.

## F3. Behind Cloudflare, every per-address limit is per Cloudflare edge (check)

`http.go` `client`, `deploy/apache-metatrash.conf.example`: the service trusts
`X-Forwarded-For` only from `-trusted-proxies` (Apache on 127.0.0.1), and
Apache drops whatever the client sent and adds the address that connected to
it. The 2026-10-25 note about Cloudflare's analytics script being injected
means the site is proxied through Cloudflare, so the address that connects to
Apache is a Cloudflare edge, not the person. Then every per-address limit
(240 requests a minute, 10 mails an hour, 30 code checks per 10 minutes, the
OAuth token and authorize limits, exports) is shared by everyone who reaches
that edge, the sign-in journal logs Cloudflare addresses, and Signed-in devices
shows them. An attacker's requests spread over many edges; real people in one
city can throttle each other.

**Check:** look at `ip=` on a recent `login` line in `journalctl -u metatrash`.
If it is a Cloudflare address (104.x, 172.64–71.x, 162.158.x and so on), this
applies.

**Fix (Apache only):** `mod_remoteip` with `RemoteIPHeader CF-Connecting-IP`
and a `RemoteIPTrustedProxy` line per Cloudflare range
(https://www.cloudflare.com/ips/). The header is then believed only from
Cloudflare, Apache's own address for the request becomes the person's, and
`mod_proxy` passes that on in `X-Forwarded-For`. The deployment guide now shows
this.

## F4. Every signed-in request holds the accounts lock across a database query

`sessions.go` `currentUser`: it takes `accounts.mu`, then calls
`a.store.ByID` (a MariaDB query) and sometimes `updateSessionTimes` (a write)
before releasing it. That lock is shared by every page, the site API, the
OAuth consent page and every sign-in, so all signed-in traffic runs one
database round trip at a time, and a slow or stuck database (5-second
timeouts) stalls the whole site for everyone, signed out too where a page
checks for a session. Slate Viewer's UI on the site makes this matter more:
each open tab polls the site API every 5 seconds. `cleanup` also walks every
in-memory map (up to about 20,000 entries) on each of these calls.

**Fix:** read the session under the lock, release it, then query; sweep
expired entries at most once a minute. The sign-in paths (`verify`, passkey,
authenticator app and recovery sign-in) still hold the lock across their
database writes; they are rare, so they can follow later.

## F5. A stolen session can become lasting access without confirming

Adding or removing sign-in methods, changing the address, deleting and
transferring all need a sign-in or confirmation in the last 10 minutes. Three
things that keep access after the session ends don't, and send the owner no
notice:

- connecting an app (OAuth consent) with read-write access to every space;
- making a private space readable on the web;
- inviting another account (the attacker's own) as a read-write member.

A session cookie taken from an unlocked computer lasts a day; any of these
keeps the attacker in for months. Severity is low because it needs the
session first.

**Fix:** email the owner when an app is connected and when a space is made
readable on the web (done in 0.76.0). Asking to confirm first is a stronger
option for later, at the cost of one more step when connecting Claude.

## F6. A repeated refresh ends the whole app connection

`oauth_store.go` `refreshTokens`: when a used refresh token comes back, the
whole grant is deleted, consent included. That is the recommended reaction to
a stolen refresh token, but it also hits an honest client that retries after
its first answer was lost on the network. If Claude or ChatGPT is seen asking
to reconnect for no clear reason, consider accepting the previous refresh
token once within a few seconds of its rotation. No change for now.

## F7. Attempt budgets live in memory

`rate.go`: the limiter, spent proof-of-work and WebAuthn challenges, and code
attempts are kept in memory, so a restart gives every address a fresh budget
for the authenticator app and recovery codes. Restarts are only on deploy, so
an attacker can't trigger them; worth noting while F1's daily cap is in
memory too.

## Checked and found sound

- **Sessions:** 32-byte random tokens, stored as SHA-256 digests;
  `__Host-` cookies, HttpOnly, Secure, SameSite=Strict; a fixed 24-hour life;
  8 per account; a new token at every sign-in; Sign out everywhere, and the
  others signed out when a method is removed or the address changes, and after
  a recovery code.
- **Email codes:** bound to the browser's login cookie, five tries a code,
  consumed before the session starts, every outstanding code for the address
  dropped on sign-in; the turned-off email switch is checked again at verify.
  Proof of work is signed, bound to the browser and single-use.
- **Passkeys:** challenges signed per purpose and bound to the login cookie or
  session, single-use, ten minutes; origin, type, `crossOrigin` and
  `topOrigin` checked; RP ID hash; user presence and verification always
  required; backup-eligible flag pinned; sign counter checked; the CBOR parser
  bounds depth, lengths and duplicate keys; RSA keys 2048 to 4096 bits.
- **Authenticator app:** secrets sealed with AES-GCM bound to the user ID;
  each step used once (checked in the database, so two requests can't share
  a code); comparisons in constant time; setup needs a fresh session.
- **Recovery codes:** about 59 bits each, stored salted per user, deleted on
  use, the rest of the sessions signed out and a notice sent.
- **Email change:** step-up first, a code to the new address, the old address
  told, the unique key the final check, codes for either address dropped.
- **Account forms:** exact Origin, form-encoded only, field allow-lists,
  per-route CSRF tokens derived from the session, no query strings; the return
  cookie only goes back to `/spaces/`, `/app/` or `/account/` paths.
- **OAuth:** clients only from allow-listed hosts by metadata document,
  canonical URL checks, no secrets accepted; redirect URIs matched before
  anything is sent there; PKCE S256 required; the pending request bound to a
  browser cookie and consumed once; codes single-use with reuse revoking the
  grant; consent re-checks ownership, active membership and the member's write
  permission inside a transaction; access tokens re-checked against the grant,
  its resource and expiry, and against live membership on every operation.
- **Invitations:** accepted only by the account whose stored address matches
  the invitation, never a submitted address.
- **Site API:** host and Origin checked, `Sec-Fetch-Site` same-origin (or a
  typed address for reads), CSRF header on writes, JSON bodies only; access is
  the person's own (owner, active member and their permission, or a space
  readable on the web) with suspended members refused; virtual-file content as
  `application/octet-stream`, text as `text/plain`, both with `nosniff` and a
  sandbox CSP, so nothing a space holds can run as the site; the app page's
  CSP allows scripts from the site only.

## For Microsoft and Google sign-in (next on the roadmap)

These follow from the review and belong in that design:

- **Never trust Microsoft's `email` claim.** In Entra ID it is whatever the
  tenant's admin typed and isn't verified (the "nOAuth" problem). Invitations
  and account lookup here are matched by email, so keying on it would let an
  admin of any tenant sign in as anyone. Key accounts on `tid` + `oid`; for
  Google on `sub`, using `email` only when `email_verified` is true.
- **Link a provider to an existing account only from a signed-in, freshly
  confirmed session,** never because the email matches.
- **Validate the ID token fully:** issuer for the tenant, audience, signature
  from the provider's published keys, expiry, and a nonce bound to the login
  cookie like the passkey challenges.
- **Group-based access goes stale** until the person's next sign-in. Session
  life (24 hours) bounds that for the site; OAuth refresh tokens would need the
  same re-check, or a shorter connection life for these accounts.

## Suggested order

F4 and F2 first (small, and they protect everything else), then F1, then F5
notices. F3 is an Apache change to check and make on the server. F6 and F7 are
for later.
