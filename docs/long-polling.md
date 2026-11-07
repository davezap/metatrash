# HTTP sync waiting and retry guidance

Core/mtsync 0.95.3 adds the first B3 capacity/backoff slice. File reads and writes
remain ordinary HTTP requests. Change watching remains a sequence of bounded
HTTP long polls; there is no WebSocket, permanent subscription service or browser
transport change in this slice.

## Server configuration

The optional `global.maxListWaiters` integer in `spaces.json` limits requests
currently held by `list?after=STATE`, across all spaces in one server process.
It defaults to **256** when omitted or zero. Explicit values **1–65536** are
accepted; negative or larger values fail configuration loading. Zero selects the
default, rather than disabling waiting. A restart applies a changed value.
Older Core versions reject this new JSON key: remove it or restore the previous
configuration before rolling back the executable.

```json
{
  "global": {
    "rates": {"windowSeconds": 60, "reads": 1200, "writes": 120},
    "maxQueuedWrites": 32,
    "maxListWaiters": 256
  }
}
```

This is the global section of the existing complete configuration, not a standalone
config file. Preserve other settings and files. Held requests use connections,
goroutines and proxy resources; the accepted numeric range is not a proven safe
capacity. Write concurrency, ingress/space rate allowances and proxy limits are
independent budgets. Multiple server processes do not share this counter.

## Response compatibility

A held request returns when the revision changes, after at most 25 seconds, on
client cancellation, or when the service stops. Slot accounting releases on every
exit, including failed HEAD probes. The server still authenticates/rate-limits
the caller and validates its query before waiting.

When capacity is full, list still returns an ordinary successful result. If its
state remains equal to `after=`, the response additionally contains
`"retryAfterSeconds": 5`, `Retry-After: 5` and `Cache-Control: no-store`. New
clients should delay their next unchanged watch request by at least five seconds
plus jitter. They can apply changes immediately when a response has a new state;
the server omits advice in that case. Ordinary lists and history are unaffected.
The advice is a backoff hint, not a guarantee of capacity at the next attempt.

`after=STATE&since=STATE` retains the existing empty-change fast path: unchanged
responses need no index load. Earlier clients ignore the optional JSON field and
retain their existing fallback. REST list responses carry this optional field;
the MCP tool inputs/outputs are unchanged because MCP does not expose `after=`.

## Mtsync behavior

Mtsync sends `after=` and `since=` together when watching an observed state. On an
old server's `invalid_request`, it retries without `since=` and remembers that
compatibility choice. Servers that also lack `after=` still use the existing
ordinary polling fallback.

- An unchanged response that arrived immediately, or includes saturation advice,
  waits at least the configured sync interval or advice, whichever is larger,
  plus up to 25% positive jitter. Advisory JSON delays are bounded at 300 seconds.
- A changed revision signals the sync loop immediately; healthy long polls do
  not add a backoff delay or discard the last observed revision.
- Repeated watch/sync failures use exponential backoff with jitter, a five-second
  minimum and a two-minute ceiling for ordinary intervals. A configured interval
  above two minutes retains its own minimum. A successful watch resets failures.
- Existing 429/503 HTTP retries keep the 20-retry budget, add jitter, and accept
  either seconds or an HTTP date in Retry-After. Missing hints use increasing
  delays up to 64 seconds plus 25% jitter. Server floors are bounded at 24 hours;
  waits are cancelable and the final delay is at most 24 hours. The permanent
  virtual_unavailable exception and one-time token refresh are unchanged.

This does not require a new minimum client/server version. Use the new mtsync to
benefit from advice/jitter. Browser retry/lifecycle behavior is a separate shared
Viewer 0.27.4 slice bundled by Company 0.96.10; it retains ordinary HTTP polling.
Multiplexed watches, fleet-wide reconnect/connection capacity and proxy-memory
measurements remain subsequent work. Tests and capacity measurements are separate
release gates.
