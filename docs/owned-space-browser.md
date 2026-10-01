# Owned spaces in the browser — Stage 3, 0.9.0

The account page now lists owned private spaces and offers creation after a public
username is selected. A successful creation opens the space README. The list
shows used allowance, including unfinished preparation, and offers a retry button
for unfinished spaces. Names and URL slugs are entered separately; the slug stays
fixed. Retries use the original name/slug and reservation.

Private document addresses are:

```text
/spaces/{username}/{space-slug}/
/spaces/{username}/{space-slug}/README.md
/spaces/{username}/{space-slug}/folder/document.md
```

The root opens README.md. The slashless space address redirects to the root after
ownership is checked. All generated links, redirects, cookies, and form actions
respect the configured hosting prefix. Public document addresses are unchanged.

## Access and presentation

Every private GET/HEAD resolves the current human session, checks the URL username
against that account, and queries the database using the immutable owner ID and
slug. Only a ready space registered under its immutable space ID can be read.
Other accounts receive not-found; signed-out visitors go to login and can then
open their space from the account page. Database failures fail closed.

Private pages use no-store caching, no-referrer, and noindex headers. The file tree
and document are rendered together after authorization, without public JSON data
requests. Markdown uses the existing read-only Slate viewer and escaped fallback;
other files use plain text. External Markdown images are blocked on private pages.
Space names and all document text remain template-escaped.

Creation is a POST to `/account/spaces` with the existing exact Origin/Host checks,
bounded form parsing, action-specific session CSRF, and a ten-attempts-per-ten-minute
user limit. The owner ID comes only from the session. The storage layer enforces
allowance under an owner-row lock, including concurrent and unfinished requests.
Private document reads reserve global, space, and client quotas after authorization.

All humans browse read-only. Agent REST/MCP access stays blocked for owned IDs;
there is no new private history or supporting-data endpoint that bypasses human
authorization. Public recent activity still reads only the public repository.
Configured bearer-key spaces keep their existing behavior.

## Installation and scope

Version 0.9.0 uses the same schema v3 and grants as 0.8.0. There is no additional
SQL or Apache change. If upgrading from before 0.8.0, apply the
[owned-space storage upgrade](owned-space-storage.md) first. Build/install/restart
using the usual owner workflow, then use Your account to create and open a space.

This completes the planned Stage 3 human flow. Sharing/invitations (Stage 4),
agent population, renaming, deletion, key management, and exports remain deferred.
New spaces initially contain the service-provisioned README only.

Implementation checks were limited to formatting, source review, and patch
consistency. Builds, browser/runtime checks, and deployment are owner-run.
