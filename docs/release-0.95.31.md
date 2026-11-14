# Core 0.95.31: the login email's link works from webmail

Prepared 2026-11-14 by Claude, driven by Company 0.96.47's manifest.

Tier: Core (the sign-in pages are Core's).

Opening the login email's link (`/login#code=123456`) from webmail flashed
the Continue page and then showed the code page with nothing filled in. A
link from another site arrives without the SameSite=Strict cookies, so the
server answers with a Continue page that reloads from this site; that
reload went to `/login` without the `#code=` fragment. The Continue page now
loads `login.js`, which reloads with the fragment kept; the `<meta refresh>`
is inside `<noscript>` for browsers without scripts.

Files: `internal/service/web/login.html`, `internal/service/web/login.js`,
`internal/service/account_switch_test.go`, `assets.go`, `CHANGELOG.md`, this
file. No schema, grant or configuration change.

Check after release: ask for a code in one browser, open the email's link
in webmail in the same browser. The page should sign in by itself (or, in
another browser, say the link only works where the code was asked for).
