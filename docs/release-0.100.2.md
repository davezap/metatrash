# Core 0.100.2: clearer passkey message, higher example rate limits

Prepared 2026-11-22 by Claude (thread "Build the small ideas batch", slice 1),
released with Company 0.96.73 and Slate Viewer 0.47.2. Tier: Core.

1. `internal/service/web/passkey.js`: the sign-in page's "Sign in with a
   passkey" now explains a `NotAllowedError` (cancelled, timed out, or no
   passkey for this site on this device, including passkeys made for the
   site's old address) instead of only "Cancelled or timed out".
2. `config/spaces.example.json`: rate limits raised (per client per space:
   600 reads, 120 writes; per space: 3000 reads, 300 writes; whole server:
   30000 reads, 3000 writes a minute). A running server keeps its own
   spaces.json.

No schema, grant, configuration format, dependency or tool schema change.
No Go code changed, so no new tests; Core's tests that load the example
config check no rate limit it changes.
