# Core 0.95.16: Docling pairing fixture recovery

Prepared 2026-11-09. Completed: fixture source, version and changelog. Pending
David: focused rerun, full checks/builds, staged review, commit, tag/push and
Actions. Codex has not run tests, builds, release commands or deployments.

David's full Windows run failed at the initial PDF pairing wait; a focused
rerun progressed to DOCX pairing and failed at its same ten-second wait after
113 seconds total. That total is the whole test, not the duration of the DOCX
pair operation. The production background pair permits two minutes. The test
was cancelling the queue during teardown, then logging that cancellation as
a second error. It also uploaded the invalid Word placeholder `PK word`.

The fixture now waits for the existing two-minute pairing budget plus five
seconds of scheduling/teardown grace. Conversion uses its configured timeout
plus the implementation's 30-second backstop and five seconds of grace. Pair
callbacks send count/error together; the test goroutine fails on an actual
error. It uses the existing valid `testDocx` fixture. All move/identity/content,
permission/keep-in-place and no-extra-pair assertions remain. No runtime code,
deadline, schema, grants, dependency, configuration or mtsync behavior changes.
Passing remains pending; slow Git is plausible, not a measured root cause.

## David's focused check, before the full release retry

```powershell
Set-Location 'C:\Claude\zt\metatrash'
go test -v ./internal/service -run '^TestDoclingPairing$' -count=1 -timeout=10m
if ($LASTEXITCODE) { throw 'Pairing regression still fails: retain output; stop release' }
```

Accept only a successful full pairing test, including PDF and DOCX moves and
contents. A real pairing error or internal two-minute timeout still fails.
Retain failure output; do not skip this regression or infer success from syntax.

## Ordered combined release

The private sibling Company `docs/release-0.96.25.md` is the complete release
guide, including normal build/install commands for both hosts, exact tag/SHA
checks, selectors, bootstrap, backups, health, journals and rollback. Runnable
`scripts/release-laptop.ps1 -Version 0.96.25` selects these four Core files:
`assets.go`, `CHANGELOG.md`, `internal/service/docling_pair_test.go`, this guide.

After the focused check succeeds, David runs:

```powershell
Set-Location 'C:\Claude\zt\metatrash.com'
.\scripts\release-laptop.ps1 -Version 0.96.25 -CoreTestsTimeoutMinutes 30 -CompanyTestsTimeoutMinutes 20
```

The helper reuses Viewer v0.34.3 only if its tag matches HEAD and intended files;
otherwise it prepares that release first. It runs the prescribed Core checks,
stages all intended Core files, displays the staged diff for David's review,
commits, verifies committed HEAD contents and a clean Core tree, then creates
new **v0.95.16**, pushes main and tag, waits for both main CI and tagged Release,
and records the actual accepted Core revision in Company's build inputs/go.mod.
It then builds/checks Company, stages/reviews/commits/verifies, creates/pushes
v0.96.25 and waits for Company Actions before staging deployment. Do not pin an
invented commit or a dirty tree. Leave v0.95.15 and Viewer v0.34.3 intact.

The helper's explicit tag/push steps, for review only, not another procedure:

```powershell
git -C 'C:\Claude\zt\metatrash' show --stat --oneline HEAD
git -C 'C:\Claude\zt\metatrash' show HEAD:assets.go
git -C 'C:\Claude\zt\metatrash' tag -a v0.95.16 -m 'Metatrash v0.95.16'
git -C 'C:\Claude\zt\metatrash' push origin main
git -C 'C:\Claude\zt\metatrash' push origin refs/tags/v0.95.16
```

No Docker is required on the laptop; disposable database checks stay in Actions.
Core's signed tagged Release must pass as well as main CI. Hosted Company uses
the actual recorded Core 0.95.16 pin. David executes deployments and staging's
real-account procedure; no direct standalone Core server install is needed.

## Recovery

A failed check stops release before tagging this source. Retain output and
staged files; never move tags, bypass failed guards or accept a partial test.
For an installed executable regression David uses Company's saved staging
backup/production checkpoint rollback. No data/config restore is required.
Before release, reverting only these four changes restores the prior fixture
and metadata; preserve unrelated work. Later corrections require new tags.
