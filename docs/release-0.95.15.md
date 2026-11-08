# Core v0.95.15: resilient release-signing download

Prepared 2026-11-08. Completed: workflow source and version/changelog metadata.
Pending David: builds/tests, staged review, commit, new tag/push and Actions.
Codex has not run tests, builds, signing, release commands or deployments.

Core v0.95.14 repeatedly failed before signing because GitHub's Jsign asset
download returned HTTP 504. This release keeps Jsign 7.5 and its existing
SHA-256, prefers Maven Central and falls back to GitHub release assets. Each
source has four retries, a 15-second connection timeout, a 60-second attempt
timeout and a 120-second retry window. The step has a seven-minute outer
limit. A transfer started before the retry window expires can finish after it.
Only a successful download matching the pinned checksum becomes jsign.jar.
A mismatch fails immediately; exhausting both sources stops publication.

Codex downloaded the Maven Central artifact without executing it and observed
SHA-256 `602a51c3545a6dc4fb99bd2ea7152b26d1345916d0c93ddfbd5936cb735af91c`,
identical to the existing workflow pin. Source:
https://repo.maven.apache.org/maven2/net/jsign/jsign/7.5/jsign-7.5.jar

There is no runtime, configuration, schema, grant, lockfile, dependency version
or mtsync behavior change. No Docker is required on David's laptop. The
optional wait observer introduced in v0.95.14 remains included.

## Ordered release

David follows the complete Company docs/release-0.96.23.md and runnable
scripts/release-laptop.ps1 -Version 0.96.23. Its manifest now selects Core
0.95.15 and includes assets.go, CHANGELOG.md, this guide and the workflow.
The helper runs the prescribed builds/checks, stages all intended files,
shows the staged diff for review, commits, verifies the committed HEAD files,
then creates the NEW tag and pushes branch/tag. It waits for both Core main CI
and the tagged Release workflow before recording Core's actual commit in
Company's lockfile and go.mod. No revision is invented or pinned from a dirty
working tree. Leave existing v0.95.14 unchanged.

These are the helper's tag/push actions for review, not a second procedure:

```powershell
git -C 'C:\Claude\zt\metatrash' tag -a v0.95.15 -m 'Metatrash v0.95.15'
git -C 'C:\Claude\zt\metatrash' push origin main
git -C 'C:\Claude\zt\metatrash' push origin refs/tags/v0.95.15
```

## David's acceptance and recovery

- Confirm the v0.95.15 Release log downloads and verifies the pinned JAR,
  signs both Windows mtsync executables and installer, and publishes the release.
- A GitHub-only 504 should no longer block the first download source. If a
  source is unavailable, confirm bounded retries/fallback. A checksum mismatch
  or failure of both sources must stop before signing and publication.
- Retain normal Go and Company checks. Deployment proceeds only after the
  exact Company tag/commit passes Actions and staging acceptance.

Staging and production configuration paths, full normal build/install commands,
active-executable bootstrap, backups, health checks, journals and rollback are
in the private Company release guide; David runs those existing guarded
deployment scripts. No direct Core server installation is needed for hosted
Company. If release acquisition still fails, retain the log and stop; never
skip verification, publish unsigned Windows installers, or replace a tag.
Runtime rollback restores the previous accepted executable using the Company
checkpoint wrapper; no data/configuration migration is needed. Any correction
after this tag is created uses another new version/tag.
