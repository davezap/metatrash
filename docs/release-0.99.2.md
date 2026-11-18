# Core 0.99.2: dashboards for visitors

Prepared 2026-11-19 by Claude (thread "Build Collumii dashboards") for
Company 0.96.59. Tier: Core (a published space's dashboards are part of
publishing, for any instance).

## What changes

- Dave opened the docs space's example dashboards on collumii.com signed out:
  most cards showed an error. The site API refused every signed-out POST
  ("Sign in to change files.") before looking at the route, and a card's
  query is a POST only to carry its JSON body. `serveSiteAPI` now lets a
  signed-out `POST .../query` through to `siteSpaceAccess(write=false)`, the
  same check as reading a file: answered in a space shared on the web and the
  public space, 401 in a private one. The Origin check still applies (a POST
  must carry the site's own Origin); there is no session, so no CSRF token to
  check. A query reads only what a visitor could already read file by file.
- `GET .../notices` signed out answers with every notice and form not done
  (it was 404, so notice cards errored). `noticeStatus` skips the database
  lookups when there is no person. A signed-out acknowledge is 401 "Sign in
  to acknowledge this notice."
- docs/dashboards.md: "Who sees what".

No schema, grant, configuration, dependency or tool schema change. Cost: a
visitor's queries count against the site API's 600 a minute per address and
the ingress limits, as file reads do; each query is capped at 5,000 rows and
30 seconds, with rows cached by blob.

## Checked in the cloud

`gofmt`, `go vet`, and `go test ./...` with MariaDB.
`TestSiteAPIAgainstDatabase` adds: signed-out query refused while the space
is private, answered once it is shared on the web, refused from another
site's Origin and without one; notices not done signed out; acknowledging
and writing signed out refused.

## Checks after release

Signed out (a private window), open
https://collumii.com/docs/examples/dashboards/team-jobs/dashboard.md and the
other examples: every card draws; the notice card shows Acknowledge, and
pressing it asks you to sign in. A private space's dashboard signed out still
asks you to sign in.
