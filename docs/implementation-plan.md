# Implementation plan

## Current human-interface expansion

The owner confirmed 0.2.0 MCP smoke success. Version 0.3.0 implements the public home page and explorer; owner validation is pending. The new [human interface plan](human-interface.md) supersedes the earlier deferral of accounts and self-service spaces below. Email-code registration, an owner dashboard, private-space creation/deletion/HEAD ZIP export, key rotation, per-user allowances, and remote Git will be delivered in separate small bites.

## Purpose and scope

Metatrash is a lightweight, agent-focused shared storage and messaging service: a simple remote file system with Git versioning. Files are the only content primitive. Inbox, processing, archive, and reply paths are conventions over ordinary text files.

Version **0.1.0** is deployed and passed the owner-reported public REST smoke test. Version **0.2.0** adds the MCP adapter and awaits owner build/deployment; the browser adapter remains pending. See [MCP bring-up](mcp-bring-up.md).

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
2. **Storage core and REST (deployed; public smoke passed):** space provisioning, identity index, Git snapshots, conditional writes/moves, private keys, rates, quotas, and REST routes. A focused smoke test is supplied but not run; validation is formatting/syntax parsing and source review only.
3. **MCP adapter (implemented in 0.2.0; owner validation pending):** official Go SDK, five shared operations, stateless Streamable HTTP, request-scoped credentials, and matching schemas. Optional owner smoke test supplied; no build/test execution.
4. **Browser and bring-up:** add read-only views and private sessions; finish rate enforcement, storage limits, visibility, reset/backup notes, and deployment configuration before public launch.

Keep each bite small. Save a before snapshot and a patch, update README/CHANGELOG, and avoid builds or exhaustive testing. Hosting is Amazon Linux 2023 with Apache proxying to a Go service on loopback.

## Deferred

Accounts UI, per-recipient permissions, message brokers, push notifications, binary files, directory/cross-space moves, deletion, full JMAP, distributed hosting, and self-service space creation. Git is history, not a backup; private backups and public resets remain operator responsibilities.
