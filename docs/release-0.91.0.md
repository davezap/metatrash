# Release steps: Metatrash 0.91.0, Slate Viewer 0.25.0

Billing state from Paddle: webhooks, Settings → Billing, the customer portal, comped accounts. Schema v24 and new grants. Still Paddle's sandbox; nothing is limited by a plan yet. Includes 0.90.1 (sign in before the trial), so release this instead of 0.90.1.

## 1. Commit the Viewer, build the frontends, commit Metatrash

No `-Install`: no new npm packages.

```powershell
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" commit -m 'Slate Viewer 0.24.1 and 0.25.0: sign in before the trial, Settings > Billing'
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.24.1
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" tag v0.25.0
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.24.1
git -C "C:\Users\David\OneDrive\Documents\Web\Slate_Viewer" push origin v0.25.0
& "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026\scripts\build-frontends.ps1" -Test
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" add -A
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" commit -m 'Metatrash 0.90.1 and 0.91.0: sign in before the trial, billing state from Paddle'
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.90.1
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" tag v0.91.0
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin HEAD
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.90.1
git -C "C:\Users\David\OneDrive\Documents\Web\metatrash.com - 2026" push origin v0.91.0

```

## 2. Paddle (sandbox dashboard)

1. The webhook destination already exists (made 2026-11-02): **Developer tools → Notifications**, "Metatrash billing (metatrash.com)", sending to `https://metatrash.com/paddle/webhook`. Its secret key is in the project file `paddle-sandbox/webhook-secret.md`.
2. Make an API key: **Developer tools → Authentication → API keys → New API key**, named "Metatrash server", with **Subscriptions: read** and **Customer portal sessions: write** (nothing else). It starts `pdl_sdbx_apikey_`. Paddle shows it once.

## 3. Server

```bash
# Secrets, one value per file, readable only by the service user:
sudo install -m 600 -o metatrash -g metatrash /dev/null /etc/metatrash/paddle-webhook-secret
sudo install -m 600 -o metatrash -g metatrash /dev/null /etc/metatrash/paddle-api-key
sudo nano /etc/metatrash/paddle-webhook-secret   # paste the pdl_ntfset_... secret
sudo nano /etc/metatrash/paddle-api-key          # paste the pdl_sdbx_apikey_... key
```

Use the same user and group that own `/etc/metatrash/accounts.json` if yours differ.

Add the two files to the `paddle` part of `/etc/metatrash/accounts.json`:

```json
"webhookSecretFile": "/etc/metatrash/paddle-webhook-secret",
"apiKeyFile": "/etc/metatrash/paddle-api-key"
```

Then the schema and grants (back up the database first, as for every schema change), and upgrade:

```bash
sudo mysql metatrash < deploy/account-schema-v24.sql
sudo mysql metatrash < deploy/account-grants.sql
mt check
./upgrade.sh
```

`mt check` should report the schema as v24 ready and no missing grants.

## 4. Check

1. **Webhook reaches the server.** In Paddle, **Developer tools → Simulations → New simulation**, type `subscription.created`, destination "Metatrash billing", and send it. The server answers 200, and `mt billing events` lists it as `unlinked` (Paddle's sample names no Metatrash account). Paddle's delivery log shows the response.
2. **Your earlier test subscription** (from the 0.90.0 test, made signed out) is unlinked too. Link it by comping or leave it; better, cancel it in the sandbox dashboard.
3. **A new trial, end to end.** Signed in as a test account without a plan, open https://metatrash.com/pricing, Start free trial, pay with `4242 4242 4242 4242`. Within a few seconds Settings → Billing shows **Personal, free trial until …** and `mt billing show` lists the account as `trial`.
4. **Pricing page.** Open /pricing again: it shows **Manage your plan**, not Start free trial.
5. **Customer portal.** Settings → Billing → Manage subscription. Confirm it's you if asked; Paddle's billing page opens for that subscription. Cancel there: Settings → Billing then says the plan ends on the trial's last day.
6. **Comp.** `mt users comp <your account>` then Settings → Billing shows **Personal, free of charge**. `mt users uncomp` ends it.
7. **Daily check.** After 10 minutes the journal shows nothing unless something changed (`journalctl -u metatrash | grep 'billing check'`); a change you made in the Paddle dashboard while the webhook was failing would show here.
