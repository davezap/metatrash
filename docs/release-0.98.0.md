# Core 0.98.0: modules can add agent tools

Prepared 2026-11-16 by Claude (thread "Build isolated compute phase 3"), for
Company 0.96.54's combined release.

Tier: Core, as a small reusable hook. Core gains no compute code: it lets a
module (Company's isolated compute) offer agent tools, and a self-hoster
could plug their own tools in the same way.

## What changes

- **`extension.Hooks.Tools`** (`extension/tools.go`): a module lists tools,
  each with a name, an optional group, a description for apps, a shorter one
  for folder-action jobs, a JSON Schema and a `Call` function.
- **Apps** connected through `/mcp/account` (Claude, ChatGPT) see the tools
  in `tools/list`. The anonymous `/mcp` endpoint does not.
- **Folder actions** get a module's tools only when their `"tools"` list
  names them, or their group: `"tools": ["read", "write", "list", "compute"]`.
  An action without a `tools` list gets Core's tools as before, and none of a
  module's. A name that no module offers is refused like any unknown tool.
- **What a tool can do in a space**: `ToolCall.Open` checks the caller's
  access exactly as Core's own tools do (the app's connection and its
  read-only or read-write grant; a job's own space and folder). `Read` gives
  a snapshot of files with their blobs; `Apply` makes a set of changes as one
  commit, refusing all of them if any file changed since its blob was read
  (`conflict`), and holding them to the folder's locks, the write gates
  (billing), folder files for new folders and the usual name rules.
  `.metatrash.json` files and virtual files can't be changed this way, and a
  job can't write its `runner.md`.
- **Errors**: a tool returns `*extension.ToolError{Code, Message}` (or Core's
  own errors from `Open`, `Read` and `Apply`) to show the agent what went
  wrong; anything else shows as `internal_error` and is logged.

Files: `extension/extension.go`, `extension/tools.go` (new),
`internal/service/extension_tools.go` (new), `extensions.go`, `service.go`,
`mcp.go`, `agent_runner.go`, `actions.go`, `runner_tools.go`,
`extension_tools_test.go` (new), `assets.go`, `CHANGELOG.md`, this file.

No schema, grant, configuration, dependency or tool-schema change. Without a
module that adds tools, nothing changes.

Checked in the cloud: `go vet ./...` and the full `go test ./...` pass with
MariaDB (`METATRASH_TEST_DB_CONFIG`), including the new
`TestModuleToolRegistration`, `TestModuleToolsInJobs` and
`TestModuleToolsOnMCPAgainstDatabase`.

## Check after release

Nothing to check on its own: Company 0.96.54's compute tools use it, and its
release notes check them on ip-172-31-26-202. On ip-172-31-16-53, where
compute stays off, the account MCP tools list is unchanged.
