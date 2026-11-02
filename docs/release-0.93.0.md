# Release steps: Metatrash 0.93.0, Slate Viewer 0.27.0

Yearly billing: US$100 a year (NZ$100, A$150, £100, €100, C$140), with extra storage at US$10 per GB a year. Schema v25, one new grant, two new keys in `accounts.json`.

The prices already exist in Paddle (made through the connectors on 2026-11-02):

| | Sandbox | Live |
|---|---|---|
| Plan, yearly | `pri_01m6f0kf0v20v39czmf3x3h9ms` | `pri_01m6f0kfkqytkehjtckh5m0wdh` |
| Storage, yearly | `pri_01m6f0kf28p4pdvgkjnpbt4e71` | `pri_01m6f0kfpqjg98a2yb2eded06v` |

## 1. CI file (by hand)

Copy `/mnt/project-files/paddle-checkout/mt/.github/workflows/ci.yml` (in the project files) over `.github\workflows\ci.yml` in the Metatrash folder. It now applies schema v1 to v25 and records v25.

## 2. Commit the Viewer, build the frontends, commit Metatrash

```powershell
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" commit -m 'Slate Viewer 0.27.0: monthly or yearly on the pricing page'
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.27.0
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.27.0
& "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026\scripts\build-frontends.ps1"
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" commit -m 'Metatrash 0.93.0: yearly billing (schema v25)'
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.93.0
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.93.0

```

## 3. Server: schema, grants, accounts.json, upgrade

Back up the database first, as for any schema change. Then, before `upgrade.sh`:

```bash
sudo mysql metatrash < deploy/account-schema-v25.sql
sudo mysql metatrash < deploy/account-grants.sql

```

(Run them from the updated checkout, after `git pull`, or copy the two files over first.)

Add the two sandbox yearly prices to the `paddle` part of `/etc/metatrash/accounts.json`:

```json
"planYearlyPriceId": "pri_01m6f0kf0v20v39czmf3x3h9ms",
"storageYearlyPriceId": "pri_01m6f0kf28p4pdvgkjnpbt4e71"
```

Then:

```bash
./upgrade.sh

```

## 4. Check (sandbox)

1. **Pricing page** (`/pricing`): a Monthly / Yearly switch above the cards. Yearly shows NZ$100.00 / year and NZ$10.00 per GB / year.
2. **Yearly trial**: with a test account that isn't comped, choose Yearly, Start free trial, and pay with a test card. Settings → Billing says "free trial until …" and "Paddle charges the first year on …". `mt billing show <user>` says "Billed yearly".
3. **Storage on it**: Settings → Billing → Storage, add 1 GB, Check the price. It should say nothing to pay during the trial, then the yearly amount. Apply it. Paddle's dashboard shows the subscription with the yearly plan and the yearly storage item.
4. **Monthly still works**: an existing monthly test subscription still says "billed monthly", and adding storage to it still uses the monthly storage price.

Going live later uses the live IDs, which are already in docs/paddle-go-live.md (B3).
