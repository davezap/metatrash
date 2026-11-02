# Pricing and checkout: release steps (Metatrash 0.90.0, Slate Viewer 0.24.0)

## 1. Commit the Viewer, build the frontends, commit Metatrash

`-Install` is needed this time: the Viewer has a new npm package (`@paddle/paddle-js`).

```powershell
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" commit -m 'Slate Viewer 0.24.0: pricing and checkout pages for metatrash.com (Paddle)'
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.24.0
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.24.0
& "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026\scripts\build-frontends.ps1" -Install -Test
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" commit -m 'Metatrash 0.90.0: pricing and checkout in test mode (Paddle sandbox)'
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.90.0
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.90.0

```

After the build, `internal/service/web/app/.vite/manifest.json` should list both `src/main.ts` and `src/pricing.ts`.

## 2. Server configuration

No schema or grant change. Add this to `/etc/metatrash/accounts.json` (sandbox values from setup; the client token is a public, browser-side token), then run `upgrade.sh`:

```json
"paddle": {
  "enabled": true,
  "environment": "sandbox",
  "clientToken": "test_ebbff7d957dece400c2e9677157",
  "planPriceId": "pri_01m6d01aaxs5w1g9myee7m5cdp",
  "storagePriceId": "pri_01m6d01afz75y4er9q4mb5r4sn"
}
```

If the service doesn't start, the journal names the setting that's wrong. Nothing has a default.

## 3. End-to-end sandbox test

1. Open https://metatrash.com/pricing signed out. It should show a Test mode badge, your local prices (NZ$17.79 for the plan and NZ$1.78 per GB from NZ) and "Prices in NZD".
2. Sign in, then open /pricing again. It should say "Signed in as …".
3. Click **Start free trial**. Paddle's one-page checkout opens as an overlay with your email filled in and the plan with a 30-day trial.
4. Pay with Paddle's test card `4242 4242 4242 4242`, any future expiry date, CVC `100`. You should land on https://metatrash.com/welcome.
5. In the Paddle sandbox dashboard, the new subscription should be trialing, with custom data `metatrash_user_id`.
6. Optional: make a payment link for a transaction in the dashboard. It should open at `/checkout?_ptxn=…` with the checkout already open.

If the overlay doesn't open, check the browser console for a Content-Security-Policy message naming a Paddle host, and tell me which one.
