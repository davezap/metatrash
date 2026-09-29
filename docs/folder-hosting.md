# Hosting under a folder — 0.5.1

Add this option to the existing Go service command (preserve its other arguments):

```text
-public-url https://mydomain.com/metatrash/
```

The default is https://metatrash.com at the domain root. The option accepts an HTTPS origin with an optional path; prefix segments support letters, digits, hyphens and underscores. A trailing slash is optional in this setting. Queries, fragments, credentials, encoded paths and dot segments are rejected at startup. It is explicit configuration, never inferred from untrusted forwarding headers.

Inside the existing HTTPS Apache VirtualHost, preserve the proxy/header settings in the main example, but replace its two root proxy directives with:

```apache
RedirectMatch 308 ^/metatrash$ /metatrash/
ProxyPass        /metatrash/ http://127.0.0.1:8080/
ProxyPassReverse /metatrash/ http://127.0.0.1:8080/
```

Use matching trailing slashes. The redirect handles the bare folder address. Do not retain the catch-all root proxy when serving other applications on the domain. RedirectMatch requires mod_alias in addition to the modules listed in the main example.

Apache removes /metatrash/ from incoming paths; Go still receives /, /login, /mcp, /api/v1/... and so on. Go adds the prefix to outward links and redirects. Do not configure the proxy to preserve the prefix too. Direct loopback page rendering will contain the configured external prefix; use the public URL for browser checks. Direct loopback health checks remain at /healthz.

The external MCP endpoint is https://mydomain.com/metatrash/mcp and REST endpoints begin https://mydomain.com/metatrash/api/v1/. MCP permits the configured public host and origin, plus the existing local development hosts. Host preservation remains required. The public prefix does not change space paths or API arguments.

If email accounts are enabled, set their origin to https://mydomain.com, WITHOUT /metatrash/. Browsers send an origin without a path. Startup rejects a mismatch with public-url. Existing Origin and CSRF validation stay enabled. Account forms and redirects use the prefix.

Account cookies retain __Host- protection, Secure, HttpOnly, SameSite=Strict, and Path=/. Prefix installations use separate cookie names derived from the prefix so login state does not collide with another Metatrash installation. Changing the prefix requires signing in again. Cookies still travel to the whole hostname: path hosting is not an isolation boundary from other applications on that hostname. Use separate hostnames when applications need separate trust boundaries.

## Owner deployment and checks

Build/install 0.5.1, add public-url to the service command, restart Go, then validate/reload the matching Apache configuration. No build or runtime checks were run during implementation.

- Visit /metatrash and confirm it redirects to /metatrash/.
- Check styles, the two instruction tabs, the displayed MCP endpoint, Refresh, file-tree/file links, and navigation remain under the folder.
- Check login, code submission, account and logout redirects under the prefix; account origin remains path-free.
- Connect MCP at the prefixed URL and check a REST read at the prefixed API URL.
- Confirm unrelated domain paths remain served by their existing application and public /metatrash/healthz stays inaccessible.
- For root hosting, omit the option or set it to the site's HTTPS origin and retain the root proxy example.

Before snapshot: docs/snapshots/0016-public-url-before/. Incremental patch: patch/0016-public-url.patch, applied after patch 0015. Application assets remain an embedded allowlist, entirely separate from space storage.
