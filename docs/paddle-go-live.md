# Paddle: from sandbox to live

Written 2026-11-02 for Metatrash 0.92.1. Part A sets up the live Paddle account and changes nothing on metatrash.com. Part B switches the site to it, so real cards are charged. Part C is the first real test.

Nothing in Metatrash limits anyone by plan yet (that is phase 4 of the rollout plan). Until it does, a live switch means people can pay, but nobody has to.

Keep the sandbox account as it is. It stays useful for testing later releases.

## Part A: set up the live account (no effect on the site)

Do all of this at **vendors.paddle.com** (the live dashboard), not sandbox-vendors.paddle.com.

### A1. Account approval

1. Open the live dashboard. If a banner asks for business details, identity checks or payout details, finish those first: Zaptronics Limited, NZ, and the bank account for payouts. Paddle has to approve the account before it can sell.
2. Under **Checkout → Website approval** (or the domain review Paddle asks for), add `metatrash.com`. Paddle's reviewers look at the pricing page, the Terms, Privacy and Refunds pages and the footer links. All of these are already live.
3. Wait for both approvals. You can still do A2 to A5 while you wait.

### Done through the connector (2026-11-02)

These are already set up in the live account, copied from the sandbox:

| What | Live ID |
|---|---|
| Product "Spaces - Personal Plan" | `pro_01m6dpteh1s92xd8d9n4nn4qcr` |
| Price "Monthly" (planPriceId) | `pri_01m6dpterheepj8ydzse9bczhx` |
| Product "Extra storage (1 GB)" | `pro_01m6dpteny4q4fsbjve9mbmgsq` |
| Price "Monthly, per GB" (storagePriceId) | `pri_01m6dptetw2dce4n4zpebdbrqt` |
| Price "Yearly" (planYearlyPriceId, 0.93.0) | `pri_01m6f0kfkqytkehjtckh5m0wdh` |
| Price "Yearly, per GB" (storageYearlyPriceId, 0.93.0) | `pri_01m6f0kfpqjg98a2yb2eded06v` |
| Client-side token "metatrash.com" (clientToken) | `live_7d4a4223bed1397a93f97464f65` |
| Webhook destination, **inactive** | `ntfset_01m6dpv81wtzwr1t33wabbc0ab` |

The webhook destination is switched off on purpose: while the site still checks sandbox signatures, live events would only fail. Its secret is in the project file `paddle-live/webhook-secret.md`. So in the steps below, A2, A3.4 and A4.1 are done.

### A2. Products and prices (done)

1. Product **"Spaces - Personal Plan"**, tax category **Standard digital goods / SaaS** (the same as in the sandbox).
   - Price **"Monthly"**: US$10.00 per month, **30-day trial, payment method required**, quantity 1 to 1.
   - Country prices (price overrides), tax included where Paddle collects tax at the location:
     - New Zealand NZ$10
     - Australia A$15
     - United Kingdom £10
     - Eurozone €10 (AT BE BG HR CY EE FI FR DE GR IE IT LV LT LU MT NL PT SK SI ES)
     - Canada C$14, with tax added
2. Product **"Extra storage (1 GB)"**, same tax category.
   - Price **"Monthly, per GB"**: US$1.00 per month, **no trial**, quantity 1 to 100.
   - Country prices: NZ$1, A$1.50, £1, €1 (same eurozone list), C$1.40 with tax added.
3. Write down the two live price IDs (`pri_...`).

### A3. Checkout settings

1. **Checkout → Checkout settings**: default payment link `https://metatrash.com/checkout`.
2. **Currency conversion**: leave it **off** (Dave, 2026-11-02). NZ, AU, GB, CA and the eurozone pay their own prices in their own currency, the US pays US$10, and every other country pays US$10 in US dollars. Turn it off in the sandbox too, so tests match.
3. **Payment methods**: card, PayPal, Apple Pay, Bancontact (as in the sandbox). Verify the Apple Pay domain for `metatrash.com` again: live verification is separate from the sandbox one.
4. **Developer tools → Authentication → Client-side tokens**: make one named "metatrash.com". It starts `live_`. It's public by design and goes in `accounts.json`.

### A4. Webhook and API key

1. **Developer tools → Notifications → New destination**: name "Metatrash billing (metatrash.com)", URL `https://metatrash.com/paddle/webhook`, every `subscription.*` event and `transaction.completed`, traffic **platform and simulation**. Copy its secret key (`pdl_ntfset_...`).
2. **Developer tools → Authentication → API keys → New API key**: "Metatrash server", with **Subscriptions: write** and **Customer portal sessions: write** only. It starts `pdl_live_apikey_` and is shown once.

Don't put either of these in chat, in `accounts.json` or in Git. They go straight into files on the server in B2.

### Doing A2 to A4 with Claude

The connector this project has (`paddel-sandbox`) only reaches the sandbox. To let me create the live products, prices, country prices, settings and webhook destination, add a second custom connector the same way you added the sandbox one, with a **live** API key that has catalog and notification-settings write access. Then start a new thread or ask the coordinator to: a session only sees connectors that existed when it started. I can't make API keys or client-side tokens through the connector, and I'll ask before changing any setting.

## Part B: switch metatrash.com to live (real charges)

### B1. Clear the sandbox billing rows

The sandbox's subscriptions and events mean nothing to the live account: the daily check would ask live Paddle about sandbox IDs and fail, and test accounts would keep showing a trial. Back up the database first, as for any change, then:

```bash
sudo mysql metatrash -e "DELETE FROM metatrash_subscriptions; DELETE FROM metatrash_paddle_events;"

```

Comped accounts (`comped_at`) are kept: they don't depend on Paddle.

### B2. Secrets

```bash
sudo install -m 600 -o metatrash -g metatrash /dev/null /etc/metatrash/paddle-live-webhook-secret
sudo install -m 600 -o metatrash -g metatrash /dev/null /etc/metatrash/paddle-live-api-key
sudo nano /etc/metatrash/paddle-live-webhook-secret
sudo nano /etc/metatrash/paddle-live-api-key

```

Paste the live `pdl_ntfset_...` secret into the first file and the `pdl_live_apikey_...` key into the second. Keep the sandbox files as they are, so you can switch back.

### B3. `accounts.json`

In the `paddle` part of `/etc/metatrash/accounts.json`:

```json
"paddle": {
  "enabled": true,
  "environment": "production",
  "clientToken": "live_7d4a4223bed1397a93f97464f65",
  "planPriceId": "pri_01m6dpterheepj8ydzse9bczhx",
  "storagePriceId": "pri_01m6dptetw2dce4n4zpebdbrqt",
  "planYearlyPriceId": "pri_01m6f0kfkqytkehjtckh5m0wdh",
  "storageYearlyPriceId": "pri_01m6f0kfpqjg98a2yb2eded06v",
  "webhookSecretFile": "/etc/metatrash/paddle-live-webhook-secret",
  "apiKeyFile": "/etc/metatrash/paddle-live-api-key"
}
```

The server refuses to start if the token, key or environment don't match each other (a `test_` token with `production`, for example), so a mix-up shows at once.

### B4. Restart

```bash
mt check
sudo systemctl restart metatrash

```

### B5. Switch on the live webhook

Paddle live dashboard → **Developer tools → Notifications** → "Metatrash billing (metatrash.com)" → turn it on (or ask me to). Then turn off the sandbox destination, or leave it: the server only accepts the live secret now, so sandbox events get refused.

The pricing page and Settings → Billing lose their **Test mode** badges.

## Part C: first real charge

1. Open https://metatrash.com/pricing signed out, and check that the prices are right for NZ.
2. With a real account that isn't comped, start the trial with your own card. Settings → Billing shows the trial within a few seconds, and `mt billing show` lists it.
3. Paddle → Notifications → the destination's log shows 200 answers.
4. Settings → Billing → Storage → add 1 GB. During the trial it costs nothing until the trial ends; Paddle's dashboard shows the item.
5. Manage subscription opens Paddle's live customer portal. Cancel the trial there, and Settings → Billing shows it ends without a charge.
6. No money moves during a trial. To test a real charge, buy and then refund it in the dashboard.

## Going back to the sandbox

Put back the sandbox `environment`, token, price IDs and secret files in `accounts.json`, run the B1 delete again, and restart.
