# Next action: OAuth agent access

Status: implemented locally (2026-10-02), ready for owner testing. D1–D5
accepted as recommended. Bites: 0.12.0 [schema v5 and configuration](oauth-foundation.md),
0.13.0 [authorization server](oauth-authorization.md), 0.14.0
[protected MCP and REST](oauth-protected-endpoints.md) and 0.15.0
[connected apps and member app permissions](oauth-account-management.md),
which includes the deployment checklist. The live server runs 0.10.0.

This plan replaces the earlier URL-key work (person/space keys in MCP URLs,
recoverable encrypted keys, schema v5 credentials). That work was never deployed
and is not on `main`; it is kept for reference on the local
`archive/url-key-0030-0034` branch. Nothing in this plan migrates from it.

## Intended experience

Human login stays email address plus six-digit code. Agents never receive a key.

1. A user adds `https://metatrash.com/mcp/account` (final path: decision D1) as a
   custom connector in Claude, ChatGPT or another MCP client.
2. The client is sent to Metatrash, the user signs in with the existing email-code
   login if needed, and a consent page lists their owned and joined spaces.
3. The user ticks the spaces to connect and chooses read-only or read/write.
4. The client receives tokens and renews them silently. The user can see and
   revoke the connection on Your account.

`https://metatrash.com/mcp` stays anonymous and public-only, so the public space
keeps working with no login.

## Decisions recorded

- **Authorization server in the Go service.** Metatrash is both the OAuth
  authorization server and the MCP/REST resource server, in one binary and one
  database, reusing the email-code login and session. No external provider.
- **Protocol core: focused in-house implementation** (owner decision in bite 1,
  superseding the earlier "use a maintained library" note). Bite 1 evaluated
  `github.com/ory/fosite` and the `op` package of `github.com/zitadel/oidc`
  against Go 1.25 and MCP SDK v1.8.0 compatibility, dependency weight, storage
  glue, PKCE S256, refresh rotation with replay detection and `resource`
  enforcement; neither fitted. See [the foundation notes](oauth-foundation.md).
- **Client registration: Client ID Metadata Documents (CIMD) only.** The current
  MCP authorization spec (2026-07-28) recommends CIMD and deprecates Dynamic
  Client Registration. Claude uses CIMD when metadata advertises
  `client_id_metadata_document_supported: true` and `none` in
  `token_endpoint_auth_methods_supported`; ChatGPT prefers CIMD when available.
  Add DCR later only if a client we need cannot use CIMD.
- **Public clients only**: `token_endpoint_auth_method` `none`, PKCE S256
  mandatory, authorization code grant plus refresh tokens. No client secrets,
  no implicit or password grants.
- **Opaque tokens** stored only as SHA-256 digests. Every request re-checks the
  database, so JWTs would add nothing; membership must be checked live anyway.
- **One grant may cover several spaces.** Claude's free plan allows one custom
  connector, so one connection must reach every space the user chooses. New
  spaces, or raising a space to read/write, need fresh consent.
- **Effective access = consent ∩ current membership.** Owners always have
  read/write on their spaces; members have an owner-set `read_only` or
  `read_write` agent permission. Suspension, removal or a permission reduction
  takes effect on the next operation. Removal deletes that space from all of the
  member's grants, so re-invitation never revives old consent.
- **No credentials in URLs.** Tokens only in `Authorization: Bearer`. Any `key`
  query parameter is rejected, as the MCP spec requires.

## Owner decisions (accepted as recommended, 2026-10-02)

- **D1 – Authenticated endpoint path.** Recommended: keep `/mcp` anonymous and
  add `/mcp/account` (name open) that always requires OAuth and serves the public
  space plus granted spaces. An endpoint that mixes anonymous and OAuth access
  never prompts clients to sign in, because clients only start OAuth on an HTTP
  401 at connect time.
- **D2 – REST.** Recommended: same tokens and checks under
  `/api/v1/account/...` with its own resource identifier, delivered after MCP.
  Alternative: MCP only for now.
- **D3 – Existing administrator-configured bearer-key spaces** (`config/keys.json`,
  header only). Recommended: leave them unchanged as an operator feature and
  never accept those keys on the OAuth endpoint. Alternative: retire them.
- **D4 – Lifetimes.** Proposed defaults: access token 1 hour, refresh token
  30 days sliding and rotated on every use, authorization code 60 seconds,
  single use. A grant unused for 90 days expires.
- **D5 – Client trust.** Proposed: start with an allow-list of CIMD client hosts
  (`claude.ai`, `chatgpt.com`, plus any the owner adds) in config. This limits
  server-side fetches and phishing-style look-alike clients. Can be opened up later.

## Protocol profile

Follows the MCP authorization spec 2026-07-28, OAuth 2.1, RFC 9728, RFC 8414,
RFC 8707, RFC 9207 and the CIMD draft. Recheck these at implementation time.

- **Protected resource metadata** at
  `/.well-known/oauth-protected-resource/mcp/account` (RFC 9728 path insertion),
  naming the resource exactly as the user types the connector URL and listing
  Metatrash as the authorization server.
- **401 challenge** on the authenticated endpoint without a valid token:
  `WWW-Authenticate: Bearer resource_metadata="…", scope="spaces:read"`.
  Insufficient scope or a read-only space on a write returns 403
  `insufficient_scope`.
- **Authorization server metadata** at `/.well-known/oauth-authorization-server`
  with issuer, authorization/token/revocation endpoints,
  `code_challenge_methods_supported: ["S256"]`,
  `token_endpoint_auth_methods_supported: ["none"]`,
  `client_id_metadata_document_supported: true` and
  `authorization_response_iss_parameter_supported: true`.
- **Authorization endpoint** validates client, exact redirect URI (loopback
  redirects match with the port ignored, per RFC 8252), PKCE and `resource`
  before showing anything. If not signed in, it sends the user through the
  existing email-code login and returns to consent through a server-side
  continuation record, not a URL the user can tamper with. Responses include
  `iss`.
- **Token endpoint** handles code exchange (bound to client, redirect URI,
  resource and PKCE challenge) and refresh. Refresh tokens rotate; reuse of a
  spent refresh token revokes the whole grant. Invalid refresh returns
  `invalid_grant`.
- **Revocation endpoint** (RFC 7009) for clients; Your account revokes whole
  connections.
- **Audience**: tokens are bound to the resource they were issued for and are
  rejected anywhere else. No third-party tokens are accepted or passed through.
- **Scopes**: `spaces:read` and `spaces:write`. Which spaces is stored in the
  grant from the consent page, not encoded in scopes. Tool space IDs choose a
  resource; they never grant authority.

## CIMD fetching safeguards

Fetch `client_id` URLs only over HTTPS with a path, from allow-listed hosts (D5).
Resolve DNS and refuse loopback, private, link-local and metadata addresses at
connection time. No redirects, 5-second timeout, 16 KiB response cap, JSON only,
`client_id` must match the URL exactly. Cache per HTTP headers, capped at 24 hours.
The consent page shows the client name and the client_id host.

## Delivery bites

Each bite: before snapshot, incremental patch in `patch/`, README and CHANGELOG
updates, version bump. Brief source, formatting and patch checks; builds and
runtime testing stay with the owner.

1. **0.12.0 – Foundation (no new endpoints).** Settle D1–D5 and the library.
   Account schema v5: `agent_permission` (`read_only`/`read_write`, default
   `read_write`) on memberships; OAuth tables for client cache, grants, granted
   spaces (cascading on membership removal), pending authorizations, codes and
   token digests. Config: issuer, OAuth enabled flag (default off), lifetimes,
   client host allow-list. Upgrade, grant and recovery notes. Bring forward the
   repository-bound dispatch refactor and per-operation owned-space quota from
   the archive branch.
2. **0.13.0 – Authorization flow.** Discovery documents, CIMD client lookup,
   authorize with login continuation and consent page, token exchange with PKCE,
   refresh rotation and replay detection, revocation, `iss` responses. Session,
   CSRF, exact Host/Origin and rate limits on the browser pages;
   protocol-appropriate validation on token requests. Tokens are issued but not
   yet accepted by any endpoint.
3. **0.14.0 – Protected MCP (and REST if D2).** Authenticated endpoint with 401
   challenge, token and audience validation, and one shared access check per
   operation (grant ∩ current ownership/membership ∩ agent permission) before
   quotas or Git. Public `/mcp` unchanged.
4. **0.15.0 – Management and instructions.** Your account: Connected apps with
   client name, spaces, access and last use, plus revoke. Manage sharing: owner
   sets each member's agent permission. Replace the README connection notes for
   Claude and ChatGPT private spaces.

## Owner-run checks

- Public `/mcp` works with no login, exactly as today.
- Claude and ChatGPT discover OAuth from the authenticated URL, sign in by email
  code, show consent, connect, and reconnect after restarts.
- Wrong, reused or expired codes; wrong PKCE, client, redirect or resource; and
  invalid or expired tokens fail with the expected protocol errors.
- Read-only consent or read-only membership blocks writes with 403 before quota
  is charged. Unconsented spaces are invisible.
- Refresh rotation, replay detection, revoke from Your account, suspension,
  removal and re-invitation behave correctly, including on live connections.
- No tokens, codes or verifiers appear in Apache or service logs.
- Root and folder hosting discovery URLs resolve through Apache.

## Deployment notes

The server runs 0.10.0 (schema v4). 0.11.0 needs no SQL. 0.12.0 adds schema v5;
upgrade order, backup and rollback notes ship with that bite. Apache must pass
`/.well-known/oauth-*` and `/oauth/*` to the service and must not log request
bodies.

## References

- [MCP authorization, 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
  and [client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration)
- [Claude connector authentication](https://www.claude.com/docs/connectors/building/authentication)
- [ChatGPT apps authentication](https://developers.openai.com/apps-sdk/build/auth)
- [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728.html),
  [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707.html),
  [RFC 9207](https://www.rfc-editor.org/rfc/rfc9207.html)
- MCP Go SDK v1.8.0 `auth` package: bearer-token middleware and protected
  resource metadata handler only; it has no authorization server.
