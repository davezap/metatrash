# Core 0.98.1: the Spaces page knows when a space needs a plan; signing in goes back to your work

Prepared 2026-11-17 by Claude (thread "Fix first-visit problems from Adam's
feedback"), for Company 0.96.55's combined release.

Tier: Core. The billing gate is Core's generic hook
(`extension.Hooks.SpaceCreateGate`), so any instance with a gate gets the
same answer; Company only supplies the words of its offer (Slate Viewer
`Brand.spaceOffer`).

## Why

A visitor without a plan saw "Your spaces 0 of 1" and a Create space form in
Settings → Spaces. Submitting it was refused with "Creating a space needs a
membership". The page offered something the server then refused.

## What changes

- `GET /api/v1/site/account` asks the space-create gate when the account
  has room for another space. If the gate refuses with
  `subscription_required`, `canCreate` is false and the new `createNeeds`
  field holds the gate's message. Any other answer (allowed, no gate,
  billing state unreadable) leaves `canCreate` as before and omits
  `createNeeds`.
- Slate Viewer 0.43.0 shows the edition's offer (Collumii: Start your
  30-day free trial) in place of the count and the form when `createNeeds`
  is there.
- **After signing in**, a browser that no page sent to sign in goes to the
  site's home (`/`), where the site app reopens the space and document it had
  open last, instead of Settings → Spaces (Dave, 2026-11-17). On a site
  without a docs space there is no home page, so it goes to `/spaces/`.
  Pages that sent the browser to sign in (a link from an email or a printed
  PDF) still get it back, app connections still continue to their consent
  page, and the old `/account` addresses still open Settings.
  `account_http.go` `signedInHome`.
- Also in this release: the Bartco clean-up (example names in README.md,
  cmd/mtsync/README.md, the CHANGELOG and five tests now say `acme`).

Files: `internal/service/site_account.go`,
`internal/service/billing_gates_test.go`, `internal/service/account_http.go`,
`internal/service/signin_test.go`, `internal/service/totp_test.go`,
`internal/service/accounts_test.go`, `internal/service/site_api_test.go`,
`assets.go`, `CHANGELOG.md`, this
file; Bartco clean-up: `README.md`, `cmd/mtsync/README.md`,
`cmd/mtsync/forms_test.go`, `cmd/mtsync/prefix_test.go`,
`internal/service/account_switch_test.go`,
`internal/service/folder_config_test.go`,
`internal/service/space_slug_test.go`.

No schema, grant, configuration, dependency or tool-schema change.

Checked in the cloud: `gofmt`, `go vet` and the full `go test ./...` pass
with MariaDB (`METATRASH_TEST_DB_CONFIG`). `TestBillingGatesAgainstDatabase`
fails without the change and passes with it.

## Check after release

Signed in as an account without a plan on collumii.com, Settings → Spaces
shows the trial offer and no "0 of 1". A paid account still sees its count
and the Create space form.

Signed out, open a guide in the docs (or a file in the public space), then
click Sign In in the header and sign in: you land back on that page, not in
Settings. (Sign out forgets the last page, so test from a signed-out
browser that has looked at something since.)
