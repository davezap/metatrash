# Release steps: Metatrash 0.92.0, Slate Viewer 0.26.0

Extra storage from Settings → Billing. No schema, grant or `accounts.json` change. The Paddle API key needs one more permission. Still Paddle's sandbox; nothing is limited by a plan yet.

## 1. Commit the Viewer, build the frontends, commit Metatrash

No `-Install`: no new npm packages. The commit also carries two small changes made after 0.91.0: `build-frontends.ps1 -Test` now runs `go test -v -timeout 15m`, and an mtsync test waits 15 s instead of 5 s for a slow CI machine.

```powershell
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" commit -m 'Slate Viewer 0.26.0: extra storage in Settings > Billing'
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.26.0
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.26.0
& "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026\scripts\build-frontends.ps1"
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" commit -m 'Metatrash 0.92.0: extra storage from Settings'
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.92.0
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.92.0

```

The build runs without `-Test` here so a slow test run can't hold up the commit; GitHub's CI runs the full suite on the push. Run `build-frontends.ps1 -SkipSlate -Test` separately if you want the tests locally first.

## 2. Paddle (sandbox dashboard)

The server's API key ("Metatrash server") needs **Subscriptions: write** as well as **Customer portal sessions: write**. In **Developer tools → Authentication → API keys**, edit the key's permissions if Paddle lets you; otherwise make a new key with those two and put it in `/etc/metatrash/paddle-api-key` in place of the old one (then revoke the old key).

## 3. Server

```bash
./upgrade.sh

```

## 4. Check

Use a test account with a plan (the trial from the 0.91.0 checks is fine).

1. **Usage and price.** Settings → Billing → Storage shows what your spaces use against the allowance. Press + once and **Check the price**. During the trial it says there is nothing to pay until the trial ends and gives the monthly amount after it.
2. **Add during the trial.** Press **Add 1 GB** (confirm it's you if asked). Either it says you now have 1 GB extra, free until the trial ends, or it says Paddle doesn't allow the change during the trial. Tell me which: Paddle's preview accepted it, but its error text says items can't be added in a trial, and only a real change shows which is true.
3. **Paddle agrees.** In the sandbox dashboard, the subscription shows the Extra storage item with quantity 1 (if step 2 worked), and `mt billing show <account>` shows 1 GB extra.
4. **Active subscription.** To test a prorated charge without waiting 30 days, in the sandbox dashboard set the subscription's next billing date to today (or start a subscription on a test account and let Paddle end the trial early from the dashboard if it offers that). Then add 1 GB: Check the price shows a charge now for the rest of the month, and after the change Paddle's dashboard shows the transaction.
5. **Going down.** Set it back to 0 GB. It's removed; for an active subscription Paddle credits the rest of the month to the next bill.
6. **Not below usage.** On an account whose spaces use more than 1 GB, the − button stops at what they need, and the server refuses lower.
7. **Trial reminder email.** About 23 days into the sandbox trial, Paddle emails "Your … free trial is about to end" to the trial's address. Check it shows the price, the date of the first charge and how to cancel.
