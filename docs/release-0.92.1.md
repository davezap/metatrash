# Release steps: Metatrash 0.92.1, Slate Viewer 0.26.1

Small fixes on top of 0.92.0. No schema, grant, `accounts.json` or Paddle change.

## 1. CI file (by hand)

Copy `/mnt/project-files/paddle-checkout/mt/.github/workflows/ci.yml` (in the project files) over `.github\workflows\ci.yml` in the Metatrash folder. The only change is `schema_version = 24` in place of 23.

## 2. Commit the Viewer, build the frontends, commit Metatrash

```powershell
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" commit -m 'Slate Viewer 0.26.1: change notes from the docs again, storage hints'
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.26.1
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.26.1
& "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026\scripts\build-frontends.ps1"
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" commit -m 'Metatrash 0.92.1: storage hints, CI schema v24, mtsync test logs'
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.92.1
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.92.1

```

## 3. Server

```bash
./upgrade.sh

```

## 4. Check

1. **CI** goes green on the push. If the mtsync job fails again, its log now has lines starting `mtsync:`; paste them to me.
2. **Settings → Billing → Storage**: with your comped account it now says the storage is set for you. With a trial or paid test account it shows the stepper.
3. **Change notes**: once the v0.26.1 desktop release is built, Help → Check for Updates lists the notes and opens them quickly. The notes for 0.22.0 to 0.26.0 are already in the docs (`tools/slate-viewer/change-logs/`); add `changenotes_0.26.1.md` from the Viewer's `docs/change-notes` once it's released, or ask me to.
