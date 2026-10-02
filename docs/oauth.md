# OAuth agent access

Agents reach owned private spaces only through OAuth, served by Metatrash itself
(authorization server and resource server in one binary, reusing the email-code
sign-in). Built in 0.12.0–0.15.0; live since 2026-10-02. Everything here exists
only with accounts enabled and `"oauth": {"enabled": true}`; otherwise these
routes return 404.

## Decisions

- **In-house protocol core**, standard library only. `ory/fosite` (no release
  since December 2024, heavy dependencies, no `resource` or CIMD) and
  `zitadel/oidc` (Go 1.26 from v3.51.9, OIDC-first, owns routing) did not fit.
- **D1** `/mcp/account` always requires OAuth; `/mcp` stays anonymous and
  public-only. (Clients only start OAuth on a 401 at connect time, so one
  endpoint cannot serve both.)
- **D2** REST uses the same tokens under `/api/v1/account/`, as a separate
  resource (audience).
- **D3** Configured key spaces (`keys.json`) are untouched and never reachable
  through OAuth.
- **D4** Lifetimes: access token 1 hour, refresh token 30 days sliding and
  rotated on every use, code 60 seconds single-use, connection expires after 90
  idle days.
- **D5** Client ID Metadata Documents (CIMD) only, fetched from allow-listed
  hosts (`claude.ai`, `chatgpt.com`). No Dynamic Client Registration unless a
  client we need requires it.
- Public clients only (no secrets), PKCE S256 only, opaque tokens stored as
  SHA-256 digests, no credentials in URLs.
- One connection covers several spaces (Claude's free plan allows one custom
  connector).

## Endpoints

| Path | Purpose |
| --- | --- |
| `/.well-known/oauth-authorization-server` | RFC 8414 metadata; issuer is the `-public-url` |
| `/.well-known/oauth-protected-resource/mcp/account` | RFC 9728 metadata for the MCP resource |
| `/.well-known/oauth-protected-resource/api/v1/account` | RFC 9728 metadata for the REST resource |
| `/oauth/authorize` | Authorization (GET) |
| `/oauth/consent` | Sign-in continuation and consent (GET, POST) |
| `/oauth/token` | Code exchange and refresh |
| `/oauth/revoke` | RFC 7009 revocation |
| `/mcp/account` | MCP: the five tools plus `spaces` |
| `/api/v1/account/spaces` and `/api/v1/account/spaces/{space}/{file,files,history,move}` | REST |

Scopes are `spaces:read` and `spaces:write`; which spaces is stored with the
consent, not in the scope. Discovery, token and revoke endpoints allow
cross-origin reads without cookies.

## Flow

1. **Authorize.** Validate the `client_id` URL shape and host, fetch and check
   its metadata document, and match `redirect_uri` exactly (loopback ports
   ignored, RFC 8252). Failures here show an error page and never redirect;
   later failures redirect with `error`, `state` and `iss`.
2. **Continue.** The request is held in memory for ten minutes, bound to the
   browser by a `__Host-metatrash-oauth` cookie (SameSite=Lax). A short
   same-site "Continue" page then loads `/oauth/consent`, because the sign-in
   cookies are SameSite=Strict and are not sent on the cross-site arrival.
3. **Sign in** with the email code if needed; login returns to consent.
4. **Consent.** Shows the app name and host, the signed-in email, and every
   ready owned space and active joined space as *Not connected*, *Read only* or
   *Read and write* (the last only if the app asked for `spaces:write` and the
   user owns the space or the owner allows it). The public space is always
   included. Previous choices are preselected. Approval re-checks everything in
   one transaction and **replaces** the connection's space list.
5. **Token.** The code is bound to client, redirect, resource and PKCE, and is
   spent by its first use; reuse revokes the tokens issued with it. Tokens look
   like `mt_at_…` and `mt_rt_…`.
6. **Refresh.** Each refresh token works once and returns a new pair. Replaying
   a spent one revokes the whole connection. Scope can narrow, never widen.
7. **Revoke.** An access token deletes itself; a refresh token deletes all of
   that connection's tokens but keeps consent. Your account → Connected apps →
   Revoke deletes consent and tokens.

Expired tokens and idle connections are purged at most every ten minutes.

## Access check

One check per operation, before quotas, size checks and Git:

- `public`: the anonymous rules.
- Owned space: consent for this connection ∩ ready space ∩ ownership or active
  membership ∩ owner's member app permission ∩ token scope.
- Unconnected, unknown or key-protected space: `not_found`. Suspended member:
  `forbidden`. Removed member: their consent rows are deleted by cascade, so
  `not_found`, and re-invitation does not restore it without new consent.
- A write without read/write access: 403 `insufficient_scope` (REST also sends a
  `WWW-Authenticate` challenge).

Tokens are checked against the database on every request, so revocation and
permission changes apply to the next operation. A request without a valid token
gets 401 with `WWW-Authenticate: Bearer resource_metadata="…",
scope="spaces:read spaces:write"` (plus `error="invalid_token"` for a bad token).
`key` and `access_token` query parameters are refused with 400 on every route.

## CIMD fetching

Only `https://<allow-listed host>/<path>` client IDs, already in canonical form
(no port, query, fragment, credentials, encoded or dot segments). No proxy, no
redirects, public addresses on port 443 only (checked after DNS resolution),
five-second timeout, 16 KiB JSON cap, at most four fetches at once. The
document's `client_id` must equal the URL, contain no secret, and list 1–20
safe redirect URIs. Cached per `Cache-Control` up to 24 hours; failures for
one minute.

## Storage (schema v5)

- `metatrash_memberships.agent_permission`: `read_only` or `read_write`
  (default).
- `metatrash_oauth_grants`: one connection per account, client and resource.
- `metatrash_oauth_grant_spaces`: consented spaces; joined-space rows cascade
  when the membership is deleted.
- `metatrash_oauth_tokens`: token digests; spent refresh tokens are kept until
  expiry to detect replay.

Pending authorizations and codes are in memory: a restart mid-sign-in just
means the app retries.

## Testing

Unit tests cover every protocol rule. MariaDB integration tests run the full
flow, both protected endpoints and Connected apps when
`METATRASH_TEST_DB_CONFIG` is set (see [deployment](deployment.md#testing)).
Verified live: the real claude.ai and Claude Code client metadata documents,
and Claude connecting through `/mcp/account` and writing to a private space.

Owner checks still to do are listed in the [roadmap](roadmap.md).

## References

- [MCP authorization (2026-07-28)](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
  and [client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration)
- [Claude connector authentication](https://www.claude.com/docs/connectors/building/authentication),
  [ChatGPT apps authentication](https://developers.openai.com/apps-sdk/build/auth)
- [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414.html),
  [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728.html),
  [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707.html),
  [RFC 9207](https://www.rfc-editor.org/rfc/rfc9207.html),
  [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009.html),
  [RFC 8252](https://www.rfc-editor.org/rfc/rfc8252.html)
