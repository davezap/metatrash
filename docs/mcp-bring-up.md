# MCP adapter - 0.2.0

Implementation is ready for owner build and deployment. No build or tests were run during this change. The existing 0.1.0 public REST service passed the owner-supplied smoke test on 2026-09-26.

## Transport

- Endpoint: `https://metatrash.com/mcp` (exact path, no trailing slash).
- Official [Go MCP SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/releases/tag/v1.8.0). Go 1.25+ is required; the server's recorded Go 1.26.8 is sufficient.
- Stateless Streamable HTTP with JSON responses; supported protocol versions are explicitly limited to `2025-11-25` and `2025-06-18` for this bite.
- Standard initialize / initialized / tools/list / tools/call lifecycle. No persistent sessions, SSE subscriptions, OAuth discovery, or browser UI. GET and DELETE on `/mcp` return 405.
- Send `Content-Type: application/json` and `Accept: application/json, text/event-stream`. After initialization send the negotiated `MCP-Protocol-Version` header.
- Public tools need no credentials. Private calls require a manually configured `Authorization: Bearer <key>` header on every request. Clients requiring OAuth-only connections are outside this bite.
- Tool arguments and output schemas come directly from the embedded tool catalog. Successful calls return both structuredContent and JSON text. Domain failures return isError with the error object in text; inspect this even when HTTP is 200.
- The same access checks, counters, write queue, and storage service handle REST and MCP. MCP envelopes are capped at 512 KiB; arguments must also fit the configured per-space request limit. Rate/queue domain errors include retryAfterSeconds in tool error text.
- Host is restricted to metatrash.com (optionally :443) and localhost/loopback on :8080. Changing the public hostname or local MCP port requires updating this allowlist. Origin may be absent, otherwise it must be exactly https://metatrash.com. No cross-origin browser API is enabled.

## Owner upgrade

First get this change onto the server through your usual repository workflow. Do not rerun the initial provisioning commands or replace the installed configuration/keys.

From `~/metatrash`, build and check the version:

```sh
go version
go build -trimpath -o bin/metatrash ./cmd/metatrash
./bin/metatrash version
```

Expect `0.2.0`. The first build downloads the pinned SDK dependencies.

Optional focused test (temporary repositories only):

```sh
go test ./internal/service -run '^TestMCPSmoke$' -count=1
```

It checks initialization, five-tool discovery, create/read/move/history, a stale-state conflict, schema rejection, and private read/write permissions across requests. It does not touch live storage.

After a successful build, preserve the installed binary, stop the service before replacement, then install and start:

```sh
sudo cp -a /usr/local/bin/metatrash /usr/local/bin/metatrash-0.1.0.backup
sudo systemctl stop metatrash
sudo install -o root -g root -m 0755 bin/metatrash /usr/local/bin/metatrash
sudo systemctl start metatrash
sudo systemctl status metatrash --no-pager
curl -fsS http://127.0.0.1:8080/healthz
```

Back up the Apache HTTPS configuration as in the existing bring-up guide. Inside only the Metatrash HTTPS virtual host, retain the existing API directives and add:

```apache
ProxyPassMatch   ^/mcp$ http://127.0.0.1:8080/mcp
ProxyPassReverse /mcp http://127.0.0.1:8080/mcp
```

Keep `ProxyPreserveHost On` and the existing forwarding-header cleanup. Run `sudo apachectl configtest`; reload with `sudo systemctl reload httpd` only after `Syntax OK`.

## Small live check

On the server, initialize:

```sh
curl -fsS https://metatrash.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"owner-check","version":"1.0.0"}}}'
```

Expect serverInfo.version `0.2.0` and protocolVersion `2025-11-25`. Then:

```sh
curl -fsS https://metatrash.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","method":"notifications/initialized"}'

curl -fsS https://metatrash.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'

curl -fsS https://metatrash.com/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"space":"public","path":"README.md"}}}'
```

The initialized notification returns an empty 202 response. Discovery must show read, write, list, move, and history. The read must contain the protected README in structuredContent and JSON text. Finally connect one MCP-capable agent to the endpoint and repeat the earlier single-file smoke flow through tools/call; leave its unique test file in place.

For rollback, stop the service, restore `/usr/local/bin/metatrash-0.1.0.backup` to `/usr/local/bin/metatrash`, and start the service. Remove the two MCP Apache directives, configtest, then reload. Storage format and existing configuration are unchanged.

## Validation record

SDK API/source review, Go formatting/syntax parsing via gofmt, JSON parsing, and patch whitespace checks only. MCP smoke test supplied but not run. Deployment, client interoperability, and runtime verification remain with the owner.
