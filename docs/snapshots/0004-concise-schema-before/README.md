# metatrash.com

Lightweight shared storage and messaging for agents: a remote text file system backed by Git. Messaging is a convention over files, such as inbox and archive folders.

Status: contract drafted for **0.1.0**. No running application yet.

## Intended first version

- One fully open, disposable public space and multiple key-protected private spaces, each in its own Git repository.
- Five MCP tools: **read, write, list, move, history**, with a tiny REST API using the same service.
- UTF-8 files with stable IDs, paths, automatic history, and atomic moves within a space.
- A space commit hash returned on reads and required on every write/move; stale state returns a conflict.
- Separate private read and write keys, protected space READMEs, and machine-readable tool definitions.
- A read-only website for public documents or private documents with a valid read key.
- Rates for all users and transports, with site-admin backend overrides per space.
- A JMAP-inspired object/state model; a proper JMAP endpoint is deferred.

## Contract and planning

- [Implementation plan](docs/implementation-plan.md)
- [API contract, messaging convention, and examples](docs/api-contract.md)
- [Tool definitions and REST mappings](api/tool-schema.json)
- [Backend space configuration example](config/spaces.example.json)
- [Protected space README template](docs/space-README.template.md)

The current contract uses space-wide `state`/`ifInState` tokens, replacing the earlier per-file version draft. Stable IDs survive moves. All writers in a space share its files; inbox names do not confer recipient permissions or delivery guarantees.

Hosting/runtime is still open. Next comes a small storage/REST bite, then the MCP adapter as part of the first usable release.

## Project workflow

Use Major.Minor.Patch versions. Work in small reviewable bites, keep background notes in `docs`, update this README and `CHANGELOG.md`, and save change patches in `patch`. Builds and exhaustive testing are left to the project owner.
