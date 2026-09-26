# Go/REST bring-up - 0.1.0

Target: Amazon Linux 2023, existing Apache HTTPS virtual host, one Go process on loopback, persistent Git repositories. This records the original REST/storage installation. The 0.2.0 MCP implementation and upgrade instructions are in [MCP bring-up](mcp-bring-up.md); the read-only website remains pending.

## Confirmed public REST checkpoint - 2026-09-26

The owner confirmed Apache routing and supplied a successful eight-step public REST smoke report: list, protected README read, create/read/update, stale-state conflict, stable-ID move, history, and historical read. Final state: `33b3f0df6557ba34bdfe27f29425459c4921bbfa`. The remaining test file is `smoke-tests/d1e010e2-4dff-43b7-8456-0a4130bce3a0-moved.txt`, ID `a30d66f5a041c691ec7f933ffc640a28` (32 characters). No independent live test was run in this workspace.

## Historical installation record - 2026-09-24

The owner completed the following on the server. This is the original checkpoint, superseded by the public verification above:

- Amazon Linux `2023.12.20260918`, `aarch64` (ARM64).
- Installed Git and Go: `git version 2.50.1`, `go version go1.26.8-X:nodwarf5 linux/arm64`.
- Added a read-only GitHub deploy key for `davezap/metatrash` and cloned to `/home/ec2-user/metatrash`.
- Built version 0.1.0 and checked the executable's help output.
- Installed and enabled `metatrash.service`; systemd reports `active (running)`.
- `curl -fsS http://127.0.0.1:8080/healthz` returned `{"status":"ok"}`.
- Apache serves a manually installed `index.html` from `/var/www/metatrash.com`. A Metatrash certificate is configured in `/etc/httpd/conf/httpd-le-ssl.conf`.
- **Pending:** add the API proxy to the Metatrash HTTPS virtual host, validate/reload Apache, and check the public API. The commands below for that step have been supplied but are not confirmed executed.

Keep this checkpoint updated as each installation step is confirmed. The existing PHP sites share Apache; scope edits to the Metatrash virtual host. Source, private configuration, and application data stay outside the document root.

## Completed installation commands

These record the first installation; do not blindly rerun them on an existing deployment, particularly commands that create keys or replace configuration.

### Packages and repository access

```sh
sudo dnf install git golang
go version
git --version
```

As `ec2-user`, without sudo, create the repository-specific key. An empty passphrase was instructed for this deploy key; never overwrite an existing key. Only the `.pub` file is copied to GitHub:

```sh
mkdir -p ~/.ssh
chmod 700 ~/.ssh
ssh-keygen -t ed25519 -C "metatrash server deploy" -f ~/.ssh/metatrash_deploy
cat ~/.ssh/metatrash_deploy.pub
```

In <https://github.com/davezap/metatrash/settings/keys>, add a deploy key titled `Metatrash server`, paste the public key, and leave **Allow write access** unchecked. Keep the private key on the server.

```sh
cd ~
git clone --config core.sshCommand="ssh -i /home/ec2-user/.ssh/metatrash_deploy -o IdentitiesOnly=yes" git@github.com:davezap/metatrash.git
cd ~/metatrash
git status --short
```

On first connection, compare SSH's host fingerprint with [GitHub's published fingerprints](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints) before accepting it. The repository-local SSH setting persists for future pulls.

### Build and service installation

```sh
cd ~/metatrash
mkdir -p bin
go build -trimpath -o bin/metatrash ./cmd/metatrash
./bin/metatrash -help

sudo useradd --system --user-group --home-dir /var/lib/metatrash --shell /sbin/nologin metatrash
sudo install -d -o metatrash -g metatrash -m 0700 /var/lib/metatrash
sudo install -d -o root -g metatrash -m 0750 /etc/metatrash
sudo install -o root -g root -m 0755 bin/metatrash /usr/local/bin/metatrash
sudo install -o root -g metatrash -m 0640 config/spaces.example.json /etc/metatrash/spaces.json
printf '{}\n' | sudo tee /etc/metatrash/keys.json >/dev/null
sudo chown root:metatrash /etc/metatrash/keys.json
sudo chmod 0640 /etc/metatrash/keys.json
sudo install -o root -g root -m 0644 deploy/metatrash.service /etc/systemd/system/metatrash.service
sudo systemctl daemon-reload
sudo systemctl enable --now metatrash
sudo systemctl status metatrash --no-pager
curl -fsS http://127.0.0.1:8080/healthz
```

The installed configuration provisions one public space. The empty keys file is for this public-only setup. The service listens on `127.0.0.1:8080`; no public firewall opening for port 8080 is needed.

## Apache API routing (completed; retained for reference)

Back up the active HTTPS configuration before editing:

```sh
sudo cp -a /etc/httpd/conf/httpd-le-ssl.conf "/etc/httpd/conf/httpd-le-ssl.conf.backup-$(date +%Y%m%d-%H%M%S)"
sudo nano /etc/httpd/conf/httpd-le-ssl.conf
```

Inside only the Metatrash HTTPS `<VirtualHost>` block, before `</VirtualHost>`, add the contents of `deploy/apache-metatrash.conf.example`:

```apache
ProxyRequests Off
ProxyPreserveHost On
ProxyTimeout 60
RequestHeader unset X-Forwarded-For
ProxyPass        /api/ http://127.0.0.1:8080/api/
ProxyPassReverse /api/ http://127.0.0.1:8080/api/
```

This keeps `/` and website files served by Apache and forwards `/api/` to Go on the same domain. Do not add a catch-all proxy. `/healthz` stays local and `/mcp` is not implemented yet. The public space permits unauthenticated reads and writes once exposed.

```sh
sudo apachectl configtest
```

Only after `Syntax OK`:

```sh
sudo systemctl reload httpd
curl -fsS https://metatrash.com/api/v1/spaces/public/files
```

Confirm the existing website still loads. If configuration validation fails, stop before reloading. For proxy failures, inspect Apache errors and any SELinux denial before changing policy.

## Build and run when ready

Requirements for current 0.2.0 source: Git and Go 1.25 or newer to build, with pinned Go modules downloaded on first build. The executable needs Git at runtime, but no Go runtime, database, or Node. Build on the target server (or for its architecture). The owner completed the server build recorded above; the commands below remain a general reference.

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

Add the Apache fragment inside the existing HTTPS virtual host; it proxies `/api/`, `/mcp`, and (from 0.3.0) the human home page, public explorer, and stylesheet. Keep the existing TLS setup. See [human interface deployment notes](human-interface.md). Confirm the required proxy/header modules and check Apache configuration before reloading. If SELinux denies the localhost proxy connection, review its audit message and permit the appropriate HTTP proxy connection according to the server's policy.

Forwarded client addresses are ignored unless `-trusted-proxies` explicitly names Apache's address range. The unit trusts only `127.0.0.1/32`; Apache clears inbound X-Forwarded-For before adding the actual address. If another proxy/CDN is in front of Apache, establish its real-client-IP configuration first. Otherwise rate limiting will see that proxy as one client.

The separate ingress ceiling is 2,400 requests/minute service-wide and 240/minute per client, covering schema, liveness, and malformed requests. Per-space rates still apply to file operations. Larger deployments may need a later configurable ingress allowance; increasing a space's limits never bypasses service ceilings.

## Operational limits

- Ordinary reads fetch the current commit/index and requested blob; they do not scan history. Writes batch paths into a temporary Git index. History requests and the 0.3.0 home page recent-files list traverse commit history.
- Repository size is checked on writes by summing file sizes, including history and candidate objects, with 4 KiB reserved for metadata. This is a conservative logical-byte cap, not filesystem allocated blocks. Leave disk space for temporary objects and backups.
- Git auto-maintenance is disabled during service operations. For this small bring-up, do not run external writers, pruning, or Git maintenance against live repositories.
- Failed publication after object promotion can leave unreachable objects, which still count against the cap. Budget-rejected candidates are discarded before promotion. A crash can leave temporary `.objects-*`/`.provision-*` directories; inspect these only with the service stopped.
- A `.service-lock` directory prevents a second process using the same data. After a forced kill or machine crash, verify the service is stopped before manually removing that lock; its `pid` file is a clue, not proof. Ordinary shutdown removes it.
- Pagination tokens and rate counters reset on restart. Start a fresh list/history request if a saved cursor is rejected.
- No delete API or automatic retention cleanup. For a public reset, stop the service, move the public repository to an operator-controlled backup location, then restart to provision a fresh one. Old tokens and revisions become unavailable. Keep the backup private.
- Back up private repositories and protected configuration separately while stopped. Git history is not a backup. Replacing the embedded README affects newly provisioned spaces only.

## Validation performed in this bite

During initial implementation: Go formatting/syntax parsing and a source review only. An optional `go list ./...` check could not complete because the local Go cache denied access. Subsequently, the owner built and installed the executable on the server and confirmed systemd startup and local HTTP health, as recorded above. No smoke-test execution, exhaustive testing, or performance measurements have been reported. Public Apache API routing and the REST smoke flow were subsequently confirmed by the owner on 2026-09-26. MCP runtime validation remains pending.
