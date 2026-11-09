# Core 0.95.18: isolate the CI database between test processes

Prepared 2026-11-09. Completed: CI correction, version/changelog and release
instructions. Pending David: reviewed commit/tag/push and both Actions gates.
Codex ran no tests, builds, release commands, SQL or deployments.

## Cause and correction

Core v0.95.17 is already tagged at
`b3fe0fe0de7f7743f3d0b198879de49104fe1446`. David passed the focused admission
checks and the helper's laptop Core checks before committing it. Linux's focused
race/SQL test then created ready owned-space records. The following full-suite
`go test` was a separate process, with a different `integrationDataDir`, but
reused those database rows. `loadOwnedSpaces` correctly refused to open their
missing repositories, causing many later fixtures to fail on the same space ID.

The workflow now recreates only its disposable MariaDB service database between
the focused process and full-suite process, reapplying the fresh schema/index
and runtime grants. Both test invocations stay. No tests, startup guard or
runtime policy is removed. This is a CI/metadata correction to 0.95.17, with no
runtime/test source, schema definition, dependency or configuration changes.

## Guarded resume without repeating the long laptop suite

Private Company's prepared `scripts/releases/0.96.26.json` selects Core
0.95.18 and names the previously tested v0.95.17 tag/full SHA as its laptop
baseline. Its helper validates that exact tag/commit, ancestry, unchanged
runtime/test files and an assets.go change containing only the version bump.
Only workflow, changelog and this new guide may differ otherwise. Additional
changes stop reuse. This narrow baseline applies only to this recovery; it is
not a generic skip-tests option. Corrected main CI and signed tagged Release
must still pass before Company records the new Core pin.

David resumes directly, without rerunning the focused or full Core laptop tests:

```powershell
Set-Location 'C:\Claude\zt\metatrash.com'
.\scripts\release-laptop.ps1 -Version 0.96.26 -CoreTestsTimeoutMinutes 30 -CompanyTestsTimeoutMinutes 20
```

The helper prints its baseline verification, stages all intended Core files,
requires review of the staged diff, commits, verifies committed HEAD contents,
creates **new v0.95.18**, pushes branch/tag and waits for main CI and tagged
Release. It then records the actual accepted Core revision, runs the Company
checks/build that have not completed yet, stages/reviews/commits/verifies,
creates/pushes v0.96.26, and waits for Company Actions. No tag is replaced.

The new Core tag/push steps performed by the helper, shown for review only:

```powershell
git -C 'C:\Claude\zt\metatrash' show --stat --oneline HEAD
git -C 'C:\Claude\zt\metatrash' show HEAD:assets.go
git -C 'C:\Claude\zt\metatrash' tag -a v0.95.18 -m 'Metatrash v0.95.18'
git -C 'C:\Claude\zt\metatrash' push origin main
git -C 'C:\Claude\zt\metatrash' push origin refs/tags/v0.95.18
```

## Acceptance, deployment and recovery

Require a passing focused race/SQL step, successful disposable DB recreation,
passing full Linux/MariaDB suite, other platform checks and signed publication.
A repeated failure is not accepted by retrying around the guard. No laptop
Docker or production database cleanup is required. Recreating a database is
restricted to the workflow's disposable service container, never server data.

Use private Company `docs/release-0.96.26.md` for the complete exact-tag/SHA
build/install sequence, separate staging/production selectors, active-executable
bootstrap, additive index backup/migration, health/journals and executable
rollback. That guide and the existing runnable wrappers now select Core
0.95.18. The original action-discovery release still needs its additive index
before installation; this CI repair adds no further schema requirement.

Retain the existing v0.95.17 tag and failed CI evidence. Do not weaken the
missing-repository guard, modify database rows on a live server, or move a tag
to fix a disposable-test failure. Runtime rollback follows Company's saved
checkpoint while keeping the compatible additive index. Future source fixes
require another new tag.
