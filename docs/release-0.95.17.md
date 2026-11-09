# Core 0.95.17: bounded action-job discovery

Prepared 2026-11-09. Source, tests, additive index and release metadata prepared.
David's tests/builds, staged review, commit/tag/push and Actions are pending.
Codex has not run tests, builds, release commands, SQL or deployments.

David's first focused run exposed a compile-time collision between the new
admission-test callback field and the existing `execute` method. The callback
is now named `runAdmitted`; the production `execute(context.Context,*agentJob)`
method is unchanged. The corrected focused run remains pending David. No tag
exists yet for this preparation, so the correction stays in this version.

One busy space could own the oldest 100 queued jobs, hiding another space's job
while the second action slot sat idle. Discovery now selects one oldest job per
eligible space, excluding running spaces before limiting the result. It rotates
through immutable space IDs using one cursor, at most two eight-space pages
per pass, the existing ten-second pass context and a one-second continuation
when more discovery is needed. Cross-space global oldest-first ordering changes
to space rotation; within-space `(queued_at, job_id)` ordering stays intact.

Keep two global action jobs, one per space, the existing per-folder hourly
allowance, payer/permissions/provider selection and interruption handling. This
is a single-process queue-discovery improvement; account-wide fairness,
leases, fenced takeover, replay and multi-process workers remain future work.

## Index and compatibility

`deploy/account-agent-queue-index.sql` adds
`metatrash_agent_jobs_queue_space(status,space_id,queued_at,job_id)` to existing
MariaDB installations. The fresh public schema includes it too. Company ships
the identical SQL with its guarded installation scripts. Back up first; use
the private Company procedure to apply/verify it before the new executable.
The index is additive, changes no row format or component marker, and remains
compatible with the previous executable. Keep it when rolling back. Building
the index uses database CPU/disk and may wait on metadata locks; inspect its
actual plan and migration outcome before promotion. No online/constant-cost
index-build guarantee is made.

## David's focused checks (no Docker on the laptop)

```powershell
Set-Location 'C:\Claude\zt\metatrash'
go test -v ./internal/service -run '^TestAgentDiscovery' -count=1 -timeout=180s
if ($LASTEXITCODE) { throw 'Action discovery checks failed; stop' }
Set-Location 'C:\Claude\zt\metatrash.com'
.\scripts\release-laptop.ps1 -Version 0.96.26 -CoreTestsTimeoutMinutes 30 -CompanyTestsTimeoutMinutes 20
```

The laptop command requires no CGO/C compiler. A Windows `-race requires cgo`
error means the race run never started; use the ordinary command above locally.
Core CI still runs the mandatory lifecycle/race checks and real SQL query with runtime grants
in disposable MariaDB. Its verbose focused step retains EXPLAIN JSON. Company
Actions applies the index twice on fresh/adopted schemas, verifies its columns
and exercises the query on all three database paths. Review the plan: an index
name in possible access paths alone does not establish a cheap query or capacity.
No billable model/provider, SMTP or manual real-account load test is needed for
these regressions. Ordinary production acceptance remains David's responsibility.

## Ordered release and recovery

Use private Company's complete `docs/release-0.96.26.md` and runnable helper.
It builds/checks Core, stages all intended manifest files, requires review of
the staged diff, commits, verifies committed HEAD contents, creates new
v0.95.17, pushes branch/tag and waits for main CI plus the signed tagged Release.
It records the actual accepted SHA in Company's build inputs/go.mod, then
builds/checks/stages/reviews/commits/verifies/tags/pushes v0.96.26 and waits for
Company Actions. Viewer 0.34.3 and Slate 0.21.2 remain pinned.

The Core tag/push steps performed by that helper, shown for review only:

```powershell
git -C 'C:\Claude\zt\metatrash' show --stat --oneline HEAD
git -C 'C:\Claude\zt\metatrash' show HEAD:assets.go
git -C 'C:\Claude\zt\metatrash' tag -a v0.95.17 -m 'Metatrash v0.95.17'
git -C 'C:\Claude\zt\metatrash' push origin main
git -C 'C:\Claude\zt\metatrash' push origin refs/tags/v0.95.17
```

David performs staging/production upgrades using Company's exact tag/SHA,
selectors, active-executable checks/bootstrap, database/executable checkpoints,
health and journal commands. Do not install untagged source or move existing
tags. A failed check/migration blocks installation; retain its evidence. A
failed or timed-out DDL client may leave database work active: inspect the DB
before retrying. Runtime rollback restores the prior executable/selector while
retaining the additive index; no database restore is normally required. Restoring
a whole database would discard intervening writes and needs separate recovery
planning. Any further source fix receives a new version/tag.
