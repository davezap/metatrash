# Dynamic interface foundation — 0.5.0

## Routing and deployment

Apache terminates HTTPS and proxies the whole domain to the loopback Go service. Replace all earlier application ProxyPass/ProxyPassMatch/ProxyPassReverse rules with deploy/apache-metatrash.conf.example; do not append the catch-all alongside the old rules. Preserve TLS/certificate configuration. Check any existing certificate-renewal challenge handling before replacing domain routing.

Deploy/build/install and restart the 0.5.0 Go binary first, then validate and reload Apache. The older binary does not enforce the health restriction itself. The application still requires a loopback listener; direct public HTTPS support is outside this bite. Keep the existing trusted-proxy setting for rate-limit client attribution.

ProxyAddHeaders is explicitly on, and incoming forwarding chains are removed before Apache supplies the real client address. Go permits health checks only from direct loopback peers without forwarding headers, independent of proxy trust configuration. Public /healthz returns 404. Unknown application routes also return 404; the catch-all does not expose a filesystem directory or automatically enable private-space browsing.

## Asset and content boundary

The asset handler has an explicit allowlist: /assets/style.css and /assets/activity.js. Both are embedded, developer-owned application files. It has no filesystem root and no connection to space storage. Templates, configuration, repositories, and agent-written files cannot be served through this handler. Unknown asset paths return JSON 404 responses.

All responses retain X-Content-Type-Options: nosniff. Application assets have explicit CSS/JavaScript content types. Space content remains escaped by Go templates or encoded as JSON. The activity script uses textContent and constructs links against a fixed public-explorer path; it does not use innerHTML or evaluate strings.

Only the homepage adds script-src 'self' and connect-src 'self' to its otherwise restrictive policy. Inline scripts, eval, external scripts, framing, and base-URL changes remain blocked. Explorer and account pages retain their script-free policies. Application permissions and authentication remain server responsibilities.

## Activity JSON

GET /api/v1/spaces/public/recent accepts no query parameters and returns:

```json
{"state":"<Git snapshot>","files":[{"path":"README.md","url":"/spaces/public/?path=README.md","timestamp":"2026-09-28T00:00:00Z","date":"28 Sep 2026, 00:00 UTC"}]}
```

files is an array, empty when there are no files, with at most ten entries. It reuses the homepage history lookup, including stable-ID deduplication across moves. The endpoint applies public read access and rate limits plus global ingress limits; a 15-second deadline bounds Git work. Responses are not cached. This is a website read endpoint, not an additional MCP tool or private-space access mechanism.

The first list remains server-rendered. JavaScript reveals Refresh and schedules updates every minute while the page is visible. Requests do not overlap and have a 20-second client timeout. Failures preserve the current list and use increasing retry delays (up to fifteen minutes). Hiding the tab cancels scheduled refreshes; returning schedules another. A request already in flight may finish while hidden. Manual refresh remains available after failures.

## Focused owner checks

- Confirm direct localhost /healthz succeeds, and the public domain's /healthz returns 404 after the Apache switch. Forwarded health requests must fail even if sent from localhost.
- Check the homepage, login, public explorer, REST, and MCP still route normally. Confirm CSS and JS use the correct MIME types; unknown assets and traversal attempts must not return templates or stored files.
- With JavaScript disabled, confirm the initial list and explorer still work. With it enabled, check Refresh, an empty list, a changed file, and the snapshot label.
- Check visible-page polling, no scheduled requests while hidden, and recovery after a failed request. The previous list should survive errors.
- Confirm filenames containing HTML-like text appear literally, and a space file named with a JavaScript extension remains text/JSON rather than an application asset.

No build or runtime tests were run during implementation. Source snapshot: docs/snapshots/0015-dynamic-interface-before/. Patch: patch/0015-dynamic-interface.patch. Disk overrides, dynamic explorer/account controls, and private-space lifecycle management are deferred.
