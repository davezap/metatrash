# OAuth authorization server — 0.13.0

Second bite of the [OAuth agent access plan](oauth-agent-access-plan.md), on the
[schema v5 foundation](oauth-foundation.md). Metatrash now acts as an OAuth 2.1
authorization server for MCP clients. Tokens are issued but **no endpoint
accepts them yet**; the protected MCP endpoint arrives in 0.14.0. No new SQL.

Everything below exists only when accounts are enabled and the account
configuration has `"oauth": {"enabled": true}`. Otherwise these routes return
404 exactly as before.

## Endpoints

Paths are relative to the public URL (for example `https://metatrash.com`).

| Path | Purpose |
| --- | --- |
| `/.well-known/oauth-authorization-server` | RFC 8414 metadata; issuer is the public URL |
| `/.well-known/oauth-protected-resource/mcp/account` | RFC 9728 metadata for resource `…/mcp/account` |
| `/.well-known/oauth-protected-resource/api/v1/account` | RFC 9728 metadata for resource `…/api/v1/account` (REST, D2) |
| `/oauth/authorize` | Authorization endpoint (GET) |
| `/oauth/consent` | Sign-in continuation and consent page (GET, POST) |
| `/oauth/token` | Code exchange and refresh (POST, form) |
| `/oauth/revoke` | RFC 7009 revocation (POST, form) |

Authorization server metadata advertises `S256` only, `none` client
authentication only, `client_id_metadata_document_supported: true` and
`authorization_response_iss_parameter_supported: true`. Scopes are
`spaces:read` and `spaces:write`. Discovery documents and the token and
revocation endpoints allow cross-origin reads (`Access-Control-Allow-Origin: *`,
no cookies) for browser-based MCP tools.

## Flow

1. **Authorize.** The client is identified by its Client ID Metadata Document
   URL. Before anything is sent back to the client, Metatrash checks the URL
   shape and allow-listed host, fetches and validates the document, and requires
   the `redirect_uri` to match a registered one exactly (loopback `http` redirect
   URIs match with the port ignored, RFC 8252). Problems at this stage show an
   error page and never redirect. Later problems (response type, PKCE S256,
   unknown `resource`, `prompt=none`, request objects, repeated parameters)
   redirect with `error`, `state` and `iss`. A missing `resource` means the MCP
   resource; unknown scope values are ignored.
2. **Continuation.** The validated request is kept in service memory for ten
   minutes, bound to this browser by a random `__Host-metatrash-oauth` cookie
   (SameSite=Lax; one pending request per browser). The response is a short
   "Continue" page that refreshes to `/oauth/consent`. That extra same-site step
   is needed because sign-in cookies are SameSite=Strict and are not sent on the
   cross-site navigation from Claude or ChatGPT. Nothing about the request is in
   a URL the user could edit.
3. **Sign in.** If not signed in, the page names the app and links to the
   existing email-code login. After a successful code, login returns to the
   consent page instead of Your account.
4. **Consent.** The page shows the app name and its client host, the signed-in
   email (with a sign-out link), and every ready owned space and active joined
   space. Each space is *Not connected*, *Read only* or *Read and write*.
   Read and write is offered only when the app asked for `spaces:write` and the
   user owns the space or the owner allows read/write for members. The public
   space is always available, as it is anonymously. Previous choices for the same
   app are preselected. The form is protected by exact Origin, a CSRF value bound
   to both the session and this pending request, and a per-account rate limit.
5. **Approve or cancel.** Cancel redirects with `access_denied`. Approve re-checks
   ownership or active membership and member permissions inside one transaction
   (locking spaces in ID order like membership changes), then creates or replaces
   the single connection for this account, app and resource. A single-use code
   valid for `codeSeconds` (default 60) is returned with `state` and `iss`. The
   consent page's CSP allows the form to redirect only to that app's origin.
6. **Token.** Code exchange requires the same `client_id` and `redirect_uri`, a
   `code_verifier` matching the S256 challenge, and (if sent) the same `resource`.
   A code is spent by its first use, successful or not; presenting it again
   revokes the tokens issued with it. Responses carry `access_token` (`mt_at_…`),
   `refresh_token` (`mt_rt_…`), `expires_in`, `scope` and `Cache-Control: no-store`.
   Only SHA-256 digests are stored.
7. **Refresh.** Each refresh token works once. It returns a new pair and extends
   the connection's idle expiry. A spent refresh token presented again revokes
   the whole connection (consent and all tokens). A refresh may narrow the scope,
   never widen it, and is limited by the connection's current consent.
8. **Revoke.** Revoking an access token deletes it; revoking a refresh token
   deletes every token of that connection but keeps consent. Unknown tokens and
   other clients' tokens are ignored (RFC 7009 always answers 200).

Public clients only: requests with an `Authorization` header or `client_secret`
get `invalid_client`. Unsupported grants get `unsupported_grant_type`.
Expired tokens and idle-expired connections are purged at most every ten minutes.

## Client metadata fetching (D5)

Only `https://<allow-listed host>/<path>` client IDs are fetched: no port, query,
fragment, credentials, encoded or dot segments, and the URL must already be in
that exact form. The fetch uses no proxy, connects only to public addresses on
port 443 (loopback, private, link-local and metadata addresses, shared and
documentation ranges are refused when connecting, after DNS resolution), follows
no redirects, times out after five seconds and reads at most 16 KiB of
`application/json`. The document's `client_id` must equal the URL; it must not
contain a secret, must use `none` authentication if it says, must allow the code
grant and code responses if it lists them, and must list 1–20 redirect URIs
(https, loopback http, or a private-use scheme; never `javascript:`, `data:` and
similar). The display name falls back to the host if missing or containing
control characters. Results are cached per `Cache-Control` for at most 24 hours;
failures are cached for one minute; at most four fetches run at once.

## Apache

Root hosting needs no change: the catch-all proxy already passes
`/.well-known/oauth-*` and `/oauth/*` to the service. Apache's default access
log records request lines only; authorization codes travel in the `Location`
header and tokens in POST bodies, so neither is logged. Do not enable body or
header logging (for example `mod_dumpio` or `mod_log_forensic`) on this host.

Folder hosting (for example `-public-url https://mydomain.com/metatrash/`) makes
the issuer `https://mydomain.com/metatrash`. Clients find its metadata by RFC 8414
path insertion at the domain root, so add these lines beside the folder's
ProxyPass rules:

```apache
ProxyPass        /.well-known/oauth-authorization-server/metatrash http://127.0.0.1:8080/.well-known/oauth-authorization-server
ProxyPass        /.well-known/oauth-protected-resource/metatrash/ http://127.0.0.1:8080/.well-known/oauth-protected-resource/
```

## Checks

Built, vetted and tested with Go 1.25.1. Unit tests cover client ID shape,
document validation, redirect matching, the address filter (including real
dials to refused addresses), cache lifetimes, resource and scope handling,
discovery, authorization request errors (error page versus redirect, `state`
and `iss`), the continuation cookie, and token request validation.

An integration test against MariaDB 10.11 with the documented runtime grants
runs the whole flow: consent listing, approval, wrong redirect URI, spent and
reused codes, wrong PKCE verifier, resource binding, refresh rotation, narrowed
and widened scope, replay revocation, client revocation, removal of a member
deleting their consent, refusal to consent to a removed membership, cancel, and
sign-in continuation back to consent. Run it with
`METATRASH_TEST_DB_CONFIG=<database config> go test ./internal/service` against a
freshly created, disposable database.

Owner checks after deploying with `oauth.enabled`:

- `curl https://metatrash.com/.well-known/oauth-authorization-server` returns the
  metadata above; with OAuth disabled it returns 404.
- An `/oauth/authorize` URL with an unknown `client_id` or redirect shows an
  error page and does not redirect.
- End-to-end checks with Claude and ChatGPT start with 0.14.0, whose protected
  endpoint is what makes those apps begin this flow.
