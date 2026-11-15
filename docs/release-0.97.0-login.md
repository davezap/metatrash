# Core 0.97.0 (sign-in part): the login email's link signs in; old sign-in pages refresh

Prepared 2026-11-15 by Claude (thread "Fix email login link and expired
login page"), released in Core 0.97.0 with search and email receiving,
through Company 0.96.50's manifest.

Tier: Core (the sign-in pages are Core's; every instance needs them).

1. **Login email link.** 0.95.31's fix never ran: the Continue page asked the
   browser to go to the address it was already on, `#code=` and all, which
   browsers treat as a jump within the page, not a load. The page stayed on
   "Continue", and its button led to the empty code card. The Continue page
   now keeps the `#code=` in the tab's sessionStorage for a minute and loads
   the bare `/login`; the next page puts it back and signs in. Checked in
   Chromium with a cross-site link and a SameSite=Strict cookie.
2. **Expired sign-in page.** After 20 minutes the page's sign-in cookie runs
   out, and sending a form showed a JSON error. The server now shows the
   sign-in page again with a note ("This page had been open a while, so it
   expired. We’ve refreshed it: please try again."), keeping the email
   typed. Coming back to a tab open more than 15 minutes reloads it first.
   Signing out after the session ended goes to the sign-in page.

Files: `internal/service/account_http.go`, `internal/service/web/login.js`,
`internal/service/login_expired_test.go`, `assets.go`, `CHANGELOG.md`, this
file. No schema, grant or configuration change. Rollback: the old binary
starts with the same config.

Check after release: ask for a code, open the email's link from webmail in
the same browser. The page should sign in by itself. Then leave a sign-in
page open for more than 20 minutes (or clear the site's cookies) and send
the email form: you should see the sign-in page with the note, not JSON.
