# GitHub connection

Metatrash mirrors GitHub folders (a `.metatrash.json` `github` service) to
repositories through the **Metatrash GitHub App**. Step 2a (0.17.0) connects
accounts to the app; pushing arrives in 2c. See [roadmap.md](roadmap.md).

## Registering the app

GitHub → Settings → Developer settings → GitHub Apps → New GitHub App, under
the account that will own it (Dave's for now; apps can be transferred later).

| Setting | Value |
| --- | --- |
| GitHub App name | Metatrash (its URL name is the `appSlug`) |
| Homepage URL | `https://metatrash.com` |
| Callback URL | `https://metatrash.com/github/callback` |
| Expire user authorization tokens | on (default) |
| Request user authorization (OAuth) during installation | **on** |
| Redirect on update | on |
| Setup URL | none (disabled by the option above) |
| Webhook | active, URL `https://metatrash.com/github/webhook`, secret: a long random value |
| Repository permissions | Contents: read and write; Metadata: read-only; Workflows: read and write |
| Subscribe to events | Push |
| Where can this app be installed | Only on this account (for now) |

Installation events (installed, deleted, suspended) always reach the webhook;
they need no subscription. Workflows permission lets a mirrored folder contain
`.github/workflows/` files.

After creating it: note the **App ID** and **Client ID**, generate a **client
secret** and a **private key** (`.pem`).

## Server configuration

Secrets go in files readable by the service (root:metatrash, 0640), never in
the JSON. Write them with `sudoedit` or copy the downloaded `.pem`:

```sh
sudo install -o root -g metatrash -m 0640 ~/metatrash.private-key.pem /etc/metatrash/github-app.pem
sudoedit /etc/metatrash/github-client-secret    # the client secret
sudoedit /etc/metatrash/github-webhook-secret   # the webhook secret (16+ characters)
sudo chown root:metatrash /etc/metatrash/github-* && sudo chmod 0640 /etc/metatrash/github-*
```

Then add to `accounts.json` and restart:

```json
"github": {
  "enabled": true,
  "appId": 123456,
  "appSlug": "metatrash",
  "clientId": "Iv23li…",
  "clientSecretFile": "/etc/metatrash/github-client-secret",
  "webhookSecretFile": "/etc/metatrash/github-webhook-secret",
  "privateKeyFile": "/etc/metatrash/github-app.pem"
}
```

With `"enabled": false` (or no section) GitHub is off: `/github/*` answers 404
and Your account has no GitHub section. While off, the other fields are not
checked. When on, startup refuses a missing or unreadable file, a short webhook
secret or a key that is not an RSA private key.

## Connecting (Your account → GitHub)

1. **Connect GitHub** (a POST with the page's CSRF token) records an attempt
   for this browser (ten minutes, one use) and redirects to
   `github.com/apps/<appSlug>/installations/new?state=…`. The attempt is bound
   to a SameSite=Lax cookie, since the Strict session cookie is not sent when
   GitHub sends the browser back.
2. The user installs the app (or changes an existing installation) and
   authorizes it. GitHub returns to `/github/callback` with `code`,
   `installation_id`, `setup_action` and `state`. Account routes refuse query
   strings, hence the separate route. Other parameters are refused.
3. The callback checks the cookie and `state`, spends the attempt, exchanges
   `code` for a user token, reads `/user` and finds `installation_id` in
   `/user/installations` (and checks it is this app's). The token proves the
   user can access the installation and is then discarded; it is never
   stored. `installation_id` alone is never trusted.
4. The link is saved and a short page continues to Your account.

An installation belongs to one Metatrash account; another account trying to
link it is refused until the first disconnects. An account can link up to 20
(for example two GitHub accounts and an organization). An organization install
that needs owner approval (`setup_action=request`) shows a page saying so.

**Disconnect** removes the link only. The app stays installed on GitHub until
uninstalled there; each connection links to its installation's settings page.

## Webhook

`POST /github/webhook` accepts only deliveries whose `X-Hub-Signature-256`
matches the webhook secret (HMAC-SHA256 of the raw body, up to 8 MiB);
anything else gets 401. `installation` events for this app update links:
`deleted` removes the link, `suspend` and `unsuspend` set its status. Other
events, including `push`, are acknowledged with 204 and ignored until step 3.
A database failure answers 503, and the delivery can be redelivered from the
app's Advanced settings page on GitHub.

## Storage (schema v6)

`metatrash_github_installations`: installation ID (primary key), Metatrash
account, the GitHub user who linked it, where the app is installed (login and
`User`/`Organization`), status (`active` or `suspended`) and times. No tokens.
Calls made as the app (from 2c) use a short-lived JWT signed with the private
key to get installation tokens on demand.
