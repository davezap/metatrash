# Release steps: Metatrash 0.90.1, Slate Viewer 0.24.1

Sign in before the trial checkout. No schema, grant or `accounts.json` change.

## 1. Commit the Viewer, build the frontends, commit Metatrash

No `-Install` this time: no new npm packages.

```powershell
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" commit -m 'Slate Viewer 0.24.1: sign in before the free trial checkout'
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.24.1
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.24.1
& "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026\scripts\build-frontends.ps1" -Test
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" commit -m 'Metatrash 0.90.1: sign in before the trial checkout'
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.90.1
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.90.1

```

## 2. Deploy

Run `upgrade.sh` on the server. Nothing else changes.

## 3. Check

1. Signed out (a private window), open https://metatrash.com/pricing. The prices show without signing in, and under Start free trial it says you'll sign in first.
2. Click **Start free trial**. You go to the sign-in page. Sign in (or register a new test address with the email code).
3. You come back to /pricing and Paddle's checkout opens by itself, with your email filled in. The address bar shows plain /pricing.
4. Close the checkout and reload: it doesn't open again.
5. Signed in, open a link to https://metatrash.com/pricing from another site (for example, paste it into an email to yourself and click it). The page should show "Signed in as …", after a quick reload.
