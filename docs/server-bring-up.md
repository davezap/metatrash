# Go/REST bring-up - 0.1.0

Target: Amazon Linux 2023, existing Apache HTTPS virtual host, one Go process on loopback, persistent Git repositories. This bite implements REST and storage. MCP and the read-only website are still pending. Nothing has been installed or changed on the server.

## Build and run when ready

Requirements: Git and Go 1.23 or newer to build. The executable needs Git at runtime, but no Go runtime, database, Node, or downloaded Go modules. Build on the target server (or for its architecture). These commands are for the project owner; they were not run during implementation.

```sh
go version
git --version
mkdir -p bin
go build -trimpath -o bin/metatrash ./cmd/metatrash
./bin/metatrash -config config/spaces.example.json -data ./data
```

The service listens on `127.0.0.1:8080`, provisions missing configured spaces, and embeds its schema and initial space README. `GET /healthz` checks HTTP liveness. Data is outside the binary and survives restart. Keep the data directory outside Apache's document root and accessible only to the service user. Run just one service against it.

Quick read:

```sh
curl -fsS http://127.0.0.1:8080/api/v1/spaces/public/files
curl -fsS 'http://127.0.0.1:8080/api/v1/spaces/public/file?path=README.md'
```

Copy the returned `state` into a write request. Paths are URL-encoded query parameters; text is a JSON string. See [the API contract](api-contract.md) for write/move examples and history.

For one optional smoke check covering creation, stale writes, moves, history, protected README, and private keys:

```sh
go test ./internal/service -run TestSmoke -count=1
```

It creates temporary repositories and does not use live data. No broad suite or benchmark is needed for initial bring-up.

## Private spaces and admin settings

Copy `config/spaces.example.json` to your deployment configuration. Add a space under `spaces`, for example:

```json
"team": {
  "visibility": "private",
  "overrides": {"rates": {"clientWrites": 40, "spaceWrites": 120}}
}
```

Defaults are merged with per-space overrides. Reads and writes are limited for all users. Write and move share the write allowance. Windows must be 1..86400 seconds; the waiting queue must be 1..10000. Invalid limits fail startup. Changes apply on restart.

Generate each credential with:

```sh
./bin/metatrash keygen
```

This prints a random bearer `key` and its `sha256` digest. Give the key to the client; store only the digest server-side. Do not commit either live configuration or keys. The separate keys file is a map of private-space names to digest arrays:

```json
{"team":{"readHashes":["REPLACE_WITH_READ_KEY_SHA256"],"writeHashes":["REPLACE_WITH_WRITE_KEY_SHA256"]}}
```

Each private space needs at least one write digest; read digests are optional. Multiple digests allow key rotation. Remove a digest and restart to revoke it. Start with `-keys /path/to/keys.json`; use `{}` for a public-only deployment with the supplied systemd unit. Requests use `Authorization: Bearer <key>`. The service never logs authorization headers or file bodies.

## Apache and systemd

Use [the unit template](../deploy/metatrash.service) and [Apache fragment](../deploy/apache-metatrash.conf.example). Suggested locations:

| Item | Location |
| --- | --- |
| Executable | `/usr/local/bin/metatrash` |
| Space configuration | `/etc/metatrash/spaces.json` |
| Key digests | `/etc/metatrash/keys.json` |
| Persistent storage | `/var/lib/metatrash` |
| Unit | `/etc/systemd/system/metatrash.service` |

Create an unprivileged `metatrash` user/group. It needs read access to configuration and digests, and ownership of the data directory. Suggested modes: configuration directory 0750 owned by root:metatrash, config files 0640, data directory 0700 owned by metatrash. Install the executable with mode 0755. Enable/start the unit only after paths and permissions are ready.

Add the Apache fragment inside the existing HTTPS virtual host; it proxies `/api/` only and preserves the existing site/TLS setup. Confirm the required proxy/header modules and check Apache configuration before reloading. If SELinux denies the localhost proxy connection, review its audit message and permit the appropriate HTTP proxy connection according to the server's policy.

Forwarded client addresses are ignored unless `-trusted-proxies` explicitly names Apache's address range. The unit trusts only `127.0.0.1/32`; Apache clears inbound X-Forwarded-For before adding the actual address. If another proxy/CDN is in front of Apache, establish its real-client-IP configuration first. Otherwise rate limiting will see that proxy as one client.

The separate ingress ceiling is 2,400 requests/minute service-wide and 240/minute per client, covering schema, liveness, and malformed requests. Per-space rates still apply to file operations. Larger deployments may need a later configurable ingress allowance; increasing a space's limits never bypasses service ceilings.

## Operational limits

- Ordinary reads fetch the current commit/index and requested blob; they do not scan history. Writes batch paths into a temporary Git index. Only explicit history requests traverse commit history.
- Repository size is checked on writes by summing file sizes, including history and candidate objects, with 4 KiB reserved for metadata. This is a conservative logical-byte cap, not filesystem allocated blocks. Leave disk space for temporary objects and backups.
- Git auto-maintenance is disabled during service operations. For this small bring-up, do not run external writers, pruning, or Git maintenance against live repositories.
- Failed publication after object promotion can leave unreachable objects, which still count against the cap. Budget-rejected candidates are discarded before promotion. A crash can leave temporary `.objects-*`/`.provision-*` directories; inspect these only with the service stopped.
- A `.service-lock` directory prevents a second process using the same data. After a forced kill or machine crash, verify the service is stopped before manually removing that lock; its `pid` file is a clue, not proof. Ordinary shutdown removes it.
- Pagination tokens and rate counters reset on restart. Start a fresh list/history request if a saved cursor is rejected.
- No delete API or automatic retention cleanup. For a public reset, stop the service, move the public repository to an operator-controlled backup location, then restart to provision a fresh one. Old tokens and revisions become unavailable. Keep the backup private.
- Back up private repositories and protected configuration separately while stopped. Git history is not a backup. Replacing the embedded README affects newly provisioned spaces only.

## Validation performed in this bite

Go formatting/syntax parsing and a source review only. An optional `go list ./...` check could not complete because the local Go cache denied access. No executable build, smoke-test execution, server deployment, or performance measurements. The smoke test is supplied for the owner to run during bring-up.
