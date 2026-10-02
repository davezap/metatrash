# OAuth-protected MCP and REST — 0.14.0

Third bite of the [OAuth agent access plan](oauth-agent-access-plan.md). Agents
can now use private spaces through OAuth. Requires the
[schema v5 foundation](oauth-foundation.md) and the
[authorization server](oauth-authorization.md) with `oauth.enabled`. No new SQL.

## Endpoints

| Endpoint | Resource identifier | Discovery |
| --- | --- | --- |
| `POST /mcp/account` (MCP, D1) | `https://metatrash.com/mcp/account` | `/.well-known/oauth-protected-resource/mcp/account` |
| `/api/v1/account/…` (REST, D2) | `https://metatrash.com/api/v1/account` | `/.well-known/oauth-protected-resource/api/v1/account` |

`/mcp` stays anonymous and public-only, unchanged. With OAuth disabled the
account endpoints return 404.

Every request to an account endpoint must carry exactly one
`Authorization: Bearer <access token>` issued for that resource. Without one the
response is 401 with

```text
WWW-Authenticate: Bearer resource_metadata="https://metatrash.com/.well-known/oauth-protected-resource/mcp/account", scope="spaces:read spaces:write"
```

which is what makes Claude, ChatGPT and other MCP clients start OAuth. An
invalid, expired, revoked or wrong-audience token adds `error="invalid_token"`.
Tokens are checked against the database on every HTTP request, so revocation,
idle expiry and consent changes apply to the next request.

## Tools and routes

The MCP server on `/mcp/account` has the usual five tools plus `spaces`, and
instructions telling the agent to call `spaces` first. `spaces` (and
`GET /api/v1/account/spaces`) returns:

```json
{"spaces": [
  {"space": "public", "name": "Public space", "owner": "", "access": "read_write"},
  {"space": "<32-hex space ID>", "name": "Team notes", "owner": "dave", "access": "read_only"}
]}
```

Pass the `space` value to `read`, `list`, `history`, `write` and `move`. REST
operations are `/api/v1/account/spaces/{space}/{file,files,history,move}` with
the same methods, queries and bodies as the anonymous REST routes.

## One access check per operation

Each operation runs one check before quotas, request-size checks and Git:

- `public`: the same anonymous rules and quotas as `/mcp`.
- Owned private space: there must be consent for this connection, the space
  must be ready, and the account must own it or be an active member.
  Access is the consent (`read_only` or `read_write`) narrowed by the owner's
  member app permission for members, and by the token scope.
- Anything else, including key-protected spaces from `config/keys.json` (D3) and
  spaces the user did not connect: `not_found`, indistinguishable from a missing
  space.
- Suspended membership: `forbidden`. Removed membership: the consent row is gone,
  so `not_found`; re-invitation does not bring it back.
- Writes without read/write access: 403 `insufficient_scope` (an MCP tool error;
  REST also sends `WWW-Authenticate: Bearer error="insufficient_scope", …`).

Owned-space operations charge the owned-space read or write quotas only after
access is granted, so rejected requests consume nothing but ingress allowance.
Space IDs only select a repository; they never grant authority.

## Credentials in URLs

`key` and `access_token` query parameters are refused with 400 on every route,
including `/mcp` and the anonymous REST routes. Space keys keep working only as
`Authorization` headers on `/mcp` and `/api/v1/spaces/…` and are never accepted
on account endpoints.

## Connecting

Claude: Settings → Connectors → Add custom connector, URL
`https://metatrash.com/mcp/account`, no client ID or secret. Claude discovers
OAuth, sends you to Metatrash to sign in with your email code, and shows the
consent page. ChatGPT (developer mode): create a connector with the same URL and
choose OAuth. Keep the anonymous `https://metatrash.com/mcp` connector for the
public space only, if you like; the account connector includes the public space.

## Checks

Built, vetted and tested with Go 1.25.1. An integration test against MariaDB
runs MCP and REST end to end through real OAuth tokens: missing and invalid
token challenges, space keys refused as tokens, URL credentials refused,
`initialize` instructions and six tools, `spaces` results, owned-space
read/write/move/list/history, public access, key-protected and unconnected
spaces hidden, the anonymous endpoint still closed to owned spaces, the owner
lowering a member's app permission, suspension and restoration, read-only
consent, MCP tokens refused by the REST resource, REST listing, reads and
`insufficient_scope` challenges, and membership removal ending access.

Owner checks after deploying:

- `curl -i -X POST https://metatrash.com/mcp/account` returns 401 with the
  `WWW-Authenticate` header above.
- Add the connector in Claude and in ChatGPT: sign in by email code, consent,
  call `spaces`, read and write a read/write space, and confirm a read-only
  space refuses writes. Restart the service and confirm the apps reconnect by
  refreshing silently.
- Suspend, then remove, a member who connected an app; their next operation
  fails, and re-inviting them does not restore the space until they consent again.
- Confirm Apache and service logs contain no `mt_at_`, `mt_rt_` or `mt_ac_` values.
