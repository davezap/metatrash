# Core v0.95.14: optional change-wait observation

Prepared 2026-11-08. Source and version updated. David's builds/tests, review,
commit, tag, push, Actions and deployment are pending. Codex ran none of them.

`server.Config.ListWaitObserver` is a trusted construction-time composition
contract with concurrent Started, Saturated and Finished callbacks. No identity,
space, file or credential is exposed. Finished carries a fixed outcome and
elapsed admission time including the HEAD probe; the callback runs just before
the slot is released. Observers must return promptly and perform no I/O or Core
calls. Each admission has one finish; saturated requests have neither. Nil
preserves existing behavior. Core retains the 256 default, authentication and
authorization order, retry advice and serial write scheduler.

Company owns the actual metrics, logs, workload and limits. No schema, grants,
configuration, tool schema, dependencies or mtsync changes. No Docker on laptop.

David uses Company's complete `docs/release-0.96.23.md` sequence and runnable
`scripts/release-laptop.ps1 -Version 0.96.23`. Its manifest names all Core files.
It runs focused regressions (including TestListWaitObserver), stages intended
files, displays their full staged diff for review, commits, verifies HEAD
contents and a clean Core tree, then creates/pushes the new tag:

```powershell
git -C C:\Claude\zt\metatrash tag -a v0.95.14 -m 'Metatrash v0.95.14'
git -C C:\Claude\zt\metatrash push origin main
git -C C:\Claude\zt\metatrash push origin refs/tags/v0.95.14
```

These show the helper's actions; do not repeat them afterward. It waits for
Core CI before recording the exact accepted Core revision in Company and
releasing Company v0.96.23. Never replace an existing tag. Hosted build,
configuration paths, staging/production installs, executable bootstrap, health
verification and rollback commands are all explicit in that Company guide.

Acceptance: lifecycle regressions distinguish timeout/change/cancellation/stop/
probe failure, saturation retains its five-second advice without taking a slot,
and construction freezes the observer before serving. Company's isolated
connections report must balance starts/finishes, release all slots and reject
unauthorized admission while preserving foreground writes. Retain the limits.

Rollback: restore the previous accepted executable using Company's checkpoint
wrapper, or rebuild a consumer against its previous exact Core pin. No data or
configuration migration is required. Future corrections use new versions/tags.
