# MCP adapter - 0.2.0

The owner has deployed 0.2.0 and reported a successful public MCP smoke test covering discovery, list/read, create/update, stale-state rejection, move with stable identity, history, and historical reads. The existing REST service passed the owner-supplied smoke test on 2026-09-26.

Use the Linux section for server installation and Bash testing. Use the Windows PowerShell section for remote testing from your PC (Windows PowerShell 5.1 or PowerShell 7). A prompt starting with `PS C:\` is PowerShell; `C:\Users\David>` is Command Prompt. Open PowerShell for the Windows examples below. Copy code blocks using their copy button; URLs inside commands must be plain URLs, without Markdown link brackets.

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

## Owner upgrade - Linux server only

This section is retained for installation or upgrade; skip it when 0.2.0 is already running. First get this change onto the server through your usual repository workflow. Do not rerun the initial provisioning commands or replace the installed configuration/keys.

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

## Live check - Windows PowerShell

These checks only initialize MCP, discover tools, and read the public README. Run the blocks in order in the same PowerShell window. They use `Invoke-RestMethod` so JSON and headers do not depend on native curl quoting. In Windows PowerShell, `curl` can be an alias for `Invoke-WebRequest`; Linux curl examples do not work with that alias.

### 1. Set up the request helper

```powershell
$mcpUrl = 'https://metatrash.com/mcp'
$mcpHeaders = @{
    Accept = 'application/json, text/event-stream'
}

function Invoke-MetatrashMcp {
    param([hashtable]$Message)

    $json = $Message | ConvertTo-Json -Depth 20 -Compress
    $request = @{
        Uri         = $mcpUrl
        Method      = 'Post'
        ContentType = 'application/json'
        Headers     = $mcpHeaders
        Body        = [System.Text.Encoding]::UTF8.GetBytes($json)
        TimeoutSec  = 60
        ErrorAction = 'Stop'
    }
    $reply = Invoke-RestMethod @request
    if ($null -ne $reply.error) {
        throw ($reply.error | ConvertTo-Json -Depth 20 -Compress)
    }
    if ($reply.result.isError -eq $true) {
        throw ($reply.result.content | ConvertTo-Json -Depth 20 -Compress)
    }
    return $reply
}
```

### 2. Initialize

```powershell
$init = Invoke-MetatrashMcp @{
    jsonrpc = '2.0'
    id = 1
    method = 'initialize'
    params = @{
        protocolVersion = '2025-11-25'
        capabilities = @{}
        clientInfo = @{ name = 'owner-check'; version = '1.0.0' }
    }
}
$init.result | ConvertTo-Json -Depth 20
$mcpHeaders['MCP-Protocol-Version'] = $init.result.protocolVersion
```

Expect `serverInfo.version` to be `0.2.0` and `protocolVersion` to be `2025-11-25`.

### 3. Send the initialized notification

```powershell
Invoke-MetatrashMcp @{
    jsonrpc = '2.0'
    method = 'notifications/initialized'
}
```

An empty response is expected (HTTP 202). Notifications have no `id`.

### 4. Discover the tools

```powershell
$discovery = Invoke-MetatrashMcp @{
    jsonrpc = '2.0'
    id = 2
    method = 'tools/list'
    params = @{}
}
$discovery.result.tools | Select-Object name, description | Format-Table -Wrap
```

Expect exactly `read`, `write`, `list`, `move`, and `history`; their order may differ. To inspect the full schemas, run `$discovery | ConvertTo-Json -Depth 40`.

### 5. Read the protected README

```powershell
$read = Invoke-MetatrashMcp @{
    jsonrpc = '2.0'
    id = 3
    method = 'tools/call'
    params = @{
        name = 'read'
        arguments = @{ space = 'public'; path = 'README.md' }
    }
}
$read.result.structuredContent | ConvertTo-Json -Depth 20
```

Expect `space: public`, a state hash, and a file with `path: README.md`, `protected: true`, and readable `text`. The helper checks both JSON-RPC errors and tool `isError`; an HTTP 200 alone does not prove a tool call succeeded.

## Live check - Linux Bash

Run these blocks in order in one Bash session on the server or another Linux computer. The public URL checks Apache and TLS. To isolate the Go service on the server, change only the first assignment to `mcp_url='http://127.0.0.1:8080/mcp'`. That loopback address does not reach the remote server from your Windows PC.

### 1. Initialize

```sh
mcp_url='https://metatrash.com/mcp'

curl -fsS "$mcp_url" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  --data '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"owner-check","version":"1.0.0"}}}'
```

Expect serverInfo.version `0.2.0` and protocolVersion `2025-11-25`. If initialization fails or returns a different protocol version, stop and inspect the response before continuing.

### 2. Send the initialized notification

```sh
curl -fsS "$mcp_url" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","method":"notifications/initialized"}'
```

An empty response is expected (HTTP 202).

### 3. Discover the tools

```sh
curl -fsS "$mcp_url" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
```

Expect exactly read, write, list, move, and history, in any order. The response includes their schemas and is fairly long.

### 4. Read the protected README

```sh
curl -fsS "$mcp_url" \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2025-11-25' \
  --data '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"space":"public","path":"README.md"}}}'
```

Expect the protected README in structuredContent and JSON text, with `space: public`, a state hash, and `protected: true`. Check for a top-level `error` or `result.isError: true`: curl's `-f` detects HTTP failures, but MCP tool failures can return HTTP 200.

## If a check fails

- Windows errors such as `Could not resolve host: application` or `text`, or HTTP 415 after pasting a Linux command, indicate quoting/header problems. Use the PowerShell blocks above in PowerShell.
- If PowerShell reports an HTTP error, stop at that block and share the status/error message. On Linux, repeat the failed curl command with `-sS -i` instead of `-fsS` to see the status and response body.
- If server-local initialization works but the public URL fails, check the active Metatrash HTTPS virtual host and Apache error log. The configured filename varies; edit the active vhost file, then configtest before reloading.
- A plain `ProxyPass /mcp http://127.0.0.1:8080/mcp` with the corresponding ProxyPassReverse works for the exact `/mcp` request too. If that is already working, do not add a second competing MCP mapping. The template above uses an exact-path ProxyPassMatch.

## Next test and rollback

After discovery and README reading succeed, connect one MCP-capable agent to the endpoint and repeat the earlier single-file smoke flow through tools/call; leave its unique test file in place. Those mutation checks are separate from the read-only commands above.

For rollback, stop the service, restore `/usr/local/bin/metatrash-0.1.0.backup` to `/usr/local/bin/metatrash`, and start the service. Remove the two MCP Apache directives, configtest, then reload. Storage format and existing configuration are unchanged.

## Validation record

During implementation: SDK API/source review, Go formatting/syntax parsing via gofmt, JSON parsing, and patch whitespace checks only; no build/test execution by the coding agent. Subsequently the owner confirmed local/public initialization with server version 0.2.0 and supplied a public tools/list response containing all five tools. The owner subsequently supplied a successful MCP file-operation smoke report. The optional automated smoke test remains unconfirmed. The shell-guide revision is documentation only; PowerShell blocks were syntax-checked locally without making live requests.


## Public MCP smoke result and README correction

The owner-reported test left `smoke-tests/5ff97841-ae47-4ee5-9a84-6bda8397db4a-moved.txt` containing version 2. Expected tool failures used HTTP 200 with `isError: true`; stale writes preserved the current content, and moves preserved identity. No independent live test was run for this documentation update.

The public README still says MCP is pending because it was copied into the space during initial provisioning. The source template has been corrected. Rebuilding or restarting does not replace existing space READMEs. The live README correction remains pending an administrator maintenance update that preserves its file ID, metadata, and Git history. Do not reset the public repository or bypass the protected-file rule through the API just to change this wording.
