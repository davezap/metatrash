# Core 0.101.0: folder order, run an action again, invitation app permission

Prepared 2026-11-23 by Claude (thread "Build the small ideas batch", slice 2),
released with Company 0.96.74, Slate Viewer 0.48.0 and Slate 0.26.2. Tier:
Core for all three (any Metatrash server has folders, actions and sharing).

1. **Folder order**: `sort` in a folder's `.metatrash.json`, `"a-z"` (the
   default) or `"z-a"`. Slate Viewer lists the folder that way in Files;
   subfolders stay first. Never inherited, lockable. The list answer carries
   `sort` on the folder's `.metatrash.json` record.
2. **Run a folder action again**: `POST .../rerun` on the site API (website
   only). Ask first (`confirm: false`: file count, sample, payer, model), then
   queue (`confirm: true`). At most 100 files; only whoever pays; not while the
   action's jobs are waiting; 10 re-runs a space an hour. Re-run jobs use
   Git's null hash as their commit, so they don't count toward the folder's
   20 jobs an hour, and the agent is told the file is being run again.
3. **App permission on pending invitations**: column
   `metatrash_invitations.agent_permission` (`read_write` default or
   `read_only`) from `deploy/account-invitation-permission.sql`, then
   `deploy/account-grants.sql` for `UPDATE (agent_permission)` on
   invitations. Accepting copies it to the membership. Without the column
   nothing changes and the Members page offers no choice.

Upgrade (Core on its own): apply the SQL file, then the grants, then start
0.101.0. The previous executable ignores the column, so rollback needs
nothing; keep the column.

Tests: `TestRerunAction`, `TestFolderSort`; all of `internal/service` passes
without a database (the database tests run where MariaDB is).
