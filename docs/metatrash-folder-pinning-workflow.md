# MetaTrash Folder Pinning Workflow

## Purpose

Folder pinning is an **attention-focusing mechanism for AI agents**, not a security boundary.

Once an agent begins working in a folder, MetaTrash keeps that agent focused on the same folder. If the agent later attempts to work elsewhere, MetaTrash can explicitly ask it to confirm that it intends to change focus.

## Authentication vs Agent Context

MetaTrash uses an **access key** for authentication. However, the access key alone cannot identify an individual agent/session because multiple agents may use the same key concurrently.

Therefore, MetaTrash also issues a separate **context token** for each agent session.

- **Access key** — identifies/authenticates the MetaTrash account or space.
- **Context token** — identifies an individual agent's working context and stores its folder pin.

## Session Start

Before using normal MetaTrash tools, an agent must call a dedicated initialization tool, for example:

`start`

The `start` tool creates a new agent context and returns a context token.

Example conceptual response:

```json
{
  "context_token": "ctx_xxxxxxxxx"
}
```

The agent must retain this token and provide it with subsequent MetaTrash tool calls.

This is deliberately separate from `login`: authentication has already been performed using the access key.

## Normal Tool Calls

Tools such as:

- `read`
- `write`
- `list`
- `move`
- `history`

accept the context token as an argument.

For example:

```json
{
  "context_token": "ctx_xxxxxxxxx",
  "path": "/project/design/spec.md"
}
```

If an agent calls one of these tools without a valid context token, MetaTrash rejects the request and returns an actionable error telling the agent to call `start` first.

For example:

```text
Context required. Call the `start` tool to create an agent context,
then include the returned context_token in subsequent tool calls.
```

This provides a recovery mechanism for MCP clients or agents that did not initially follow the tool instructions.

## Folder Pinning

A newly created context initially has no folder pin.

On the agent's **first file read**, MetaTrash determines the containing folder and pins that context to it.

Example:

```text
read /project/electronics/power.md
```

causes:

```text
context_token -> /project/electronics/
```

Subsequent operations are therefore associated with that folder context.

## Changing Focus

If the agent attempts to read or operate in another folder, MetaTrash should not silently move the pin.

Instead, it should tell the agent that the requested operation is outside its current pinned folder and require explicit confirmation before changing focus.

Conceptually:

```text
Current folder pin: /project/electronics/
Requested folder:   /project/software/

This operation changes the current working focus.
Confirm the folder change before continuing.
```

Once confirmed, MetaTrash updates the folder associated with that context token.

## MCP Portability

The design does not depend on Anthropic, ChatGPT, OpenAI API, or another MCP client providing a particular session mechanism.

The context token is simply part of the MetaTrash MCP tool interface.

Tool descriptions should clearly instruct agents that:

1. `start` must be called before other MetaTrash tools.
2. The returned `context_token` must be retained.
3. The same token must be supplied with subsequent calls in that working session.
4. A folder pin may be established automatically by the first read.
5. Changing to another folder requires an explicit focus change/confirmation.

This keeps the mechanism under MetaTrash's control and makes it portable across MCP implementations.

## Overall Flow

```text
Agent connects to MetaTrash MCP
          |
          v
       start()
          |
          v
MetaTrash creates context
          |
          v
Returns context_token
          |
          v
Agent performs first read
          |
          v
Context is pinned to containing folder
          |
          v
Agent continues using context_token
          |
          +---- operation inside pinned folder ----> allow
          |
          +---- operation outside pinned folder ---> request confirmation
                                                       |
                                                       v
                                               update folder pin
```

## Key Principle

**Authentication identifies who may access MetaTrash.  
The context token identifies which agent working context is making the request.  
The folder pin identifies where that agent is currently focused.**

These are intentionally separate concepts.
