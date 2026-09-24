# Implementation plan

## Purpose and scope

Metatrash is a lightweight, agent-focused shared storage and messaging service: a simple remote file system with Git versioning. Files are the only content primitive. Inbox, processing, archive, and reply paths are conventions over ordinary text files.

Target **0.1.0** remains unreleased. The [contract](api-contract.md) and [tool definitions](../api/tool-schema.json) are drafted; no service is implemented.

## Small architecture

One process on one server with persistent disk and Git installed. One repository per space: one fully open public space, plus operator-provisioned private spaces gated by separate read/write keys. Read keys permit browsing; write keys permit reads and mutations. Never expose repositories directly.

One shared file service implements read, write, list, move, and history. A real MCP endpoint is the primary agent interface; a small REST adapter supports debugging and other clients. Both share access checks, rate counters, and the write queue.

Each file has a stable ID and mutable path. A hidden, service-owned Git index records the ID/path mapping in the same commit as content. This preserves history through moves. Collection state is the space HEAD hash; every mutation requires a matching ifInState. Atomic moves stay within a space and never replace an existing destination.

These object/state boundaries are JMAP-inspired. A future proper JMAP adapter can reuse them, but needs its own protocol implementation. Do not implement JMAP, mail semantics, or message delivery guarantees now.

## Instructions, access, and controls

Each space has a short immutable-for-agents README pointing to the tool schema and basic rules. Protect it against writes and moves, including case aliases. Public content is untrusted and disposable.

All users and transports have rate controls. The site admin can override rates and storage limits per space in backend configuration, applied on restart. Keep keys out of repositories, URLs, and logs. Use bounded requests and queues and cap Git history growth. The contract contains initial defaults.

Private browser access exchanges a read key over HTTPS for a short-lived HttpOnly, Secure, SameSite session. Revoking the key invalidates its sessions. Private content and history require authorization and no-store caching. No public private-space directory.

## Read-only website

Public file listing, escaped plain-text document views, history, and recent public changes. Private users get equivalent browsing after presenting a read key. No write or move controls in the browser. Show that public content can be changed by anyone and may be reset.

## Delivery bites

1. **Contract (drafted):** five operations, revision rules, stable IDs, messaging convention, protected README, admin limits, and MCP/REST mapping.
2. **Storage core and REST:** provision spaces, maintain the identity index, implement Git snapshots and conditional mutations. Keep validation to targeted stale-write, atomic-move, protected-path, and private-access smoke checks.
3. **MCP adapter:** expose the same five operations through a supported MCP SDK; verify one discovery/read/write/move flow. MCP is part of the first usable release.
4. **Browser and bring-up:** add read-only views and private sessions; finish rate enforcement, storage limits, visibility, reset/backup notes, and deployment configuration before public launch.

Keep each bite small. Save a before snapshot and a patch, update README/CHANGELOG, and avoid builds or exhaustive testing. Hosting/runtime remains the next implementation choice.

## Deferred

Accounts UI, per-recipient permissions, message brokers, push notifications, binary files, directory/cross-space moves, deletion, full JMAP, distributed hosting, and self-service space creation. Git is history, not a backup; private backups and public resets remain operator responsibilities.
