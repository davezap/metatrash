# GitHub connection

Metatrash mirrors GitHub folders (a `.metatrash.json` `github` service) to
repositories through the **Metatrash GitHub App**. Step 2a (0.17.0) connects
accounts to the app, 2b (0.18.0) allows dot names, 2c (0.19.0) pulls a
repository into its folder; pushing arrives in 2d. See [roadmap.md](roadmap.md).

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

**Link an existing installation** covers an app installed from its GitHub
page first (GitHub then returns without our `state`, and the callback refuses
it). It sends the browser to `github.com/login/oauth/authorize` (client ID,
`state`, `redirect_uri` = the callback), the same attempt cookie and state
apply, and the callback, seeing a link attempt, verifies the user and links
every installation of this app that the user can access. Installations that
another Metatrash account holds (or past the limit) are listed as not
connected. A cancelled authorization (`error=access_denied`) shows a page
saying so.

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

## Dot names and .gitignore (0.18.0)

Repos need names starting with a dot (`.gitignore`, `.github/workflows/`). They
are allowed only inside GitHub folders: writes and moves elsewhere are refused
with the reason. `.git` (any case) is never a name, nor `.metatrash` at the
space root (the service's index). A folder's github service cannot be removed,
nor its `.metatrash.json` deleted, while it still holds dot names; move or
delete them first. Underscore names (`__init__.py`, `_config.yml`) are allowed
in every folder.

Push (coming next) sends a GitHub folder's files minus what its `.gitignore`
files exclude, as git would: comments, negation, directory-only and anchored
patterns, `*`, `?`, `**`, classes, escapes, `.gitignore` files in subfolders,
and no re-including files inside an excluded folder. A test checks the matcher
against `git check-ignore`. `.metatrash.json` files are Metatrash's and are
never pushed.

## Pull (0.19.0)

An agent calls `pull` with a space and a GitHub folder (MCP on `/mcp/account`,
or `POST /api/v1/account/spaces/{space}/pull`). The rules agents see are in
[api-contract.md](api-contract.md#pull-github-folders); this is how it works.

1. **Checks first, in the space.** The folder's `.metatrash.json` gives `repo`
   and `branch`. With a baseline for the same repo and branch, the folder must
   be unchanged since it; otherwise pull refuses before calling GitHub.
2. **Token.** Among the **space owner's** linked installations, the one whose
   account is the repository owner (case-insensitive). None → `forbidden`
   telling the owner to connect or link it; suspended → `forbidden`. An app
   JWT gets an installation token limited to that one repository and
   `contents: read`; GitHub refusing it (the installation does not include the
   repository) → `forbidden` telling the owner to add the repository under the
   app's Repository access. Tokens are used for this pull only and not stored.
3. **Read GitHub, outside the write queue.** `GET /repos/{repo}/branches/{branch}`
   (404 → the repository's default branch is named), the recursive tree
   (truncated → `storage_limit`), then the commit's **tarball** (followed to
   `codeload.github.com` without the token; capped at 256 MiB compressed),
   keeping only wanted files whose content matches their tree blob hash. Files
   the tarball leaves out or changes (`export-ignore`, `export-subst` in
   `.gitattributes`) come from the blob API, up to 200. Files already in the
   folder with the same blob are not downloaded.
4. **Classify.** Kept: UTF-8 text, modes 100644 and 100755, within the file
   limit and the path rules. Skipped with a reason: binaries, oversize, names
   Metatrash refuses, symbolic links, submodules, `.metatrash.json`.
5. **Apply, in the write queue.** If the space moved on meanwhile, pull
   refuses when the folder's files, its settings or its baseline changed (the
   rest of the space may change freely). One commit writes the files and the
   new baseline together, so they cannot disagree.

**Baselines** live in the space's Git as the service file
`.metatrash/github.json` (agents cannot read or write `.metatrash/`): per
folder, the repo, branch, commit, tree, time, every held file's path and blob
hash (the same hash on GitHub and in the space), non-default modes
(executables) and the skipped files with blob, mode and reason. Push (2d) will
diff the folder against `files`, build on the baseline tree so skipped files
are carried through untouched, and stop if the branch has moved past
`commit`. Every commit carries the file forward; deleting a folder's
`.metatrash.json` leaves its baseline in place, and a baseline for another
repo or branch is ignored.

## Storage (schema v6)

`metatrash_github_installations`: installation ID (primary key), Metatrash
account, the GitHub user who linked it, where the app is installed (login and
`User`/`Organization`), status (`active` or `suspended`) and times. No tokens.
Calls made as the app use a short-lived JWT signed with the private key to get
installation tokens on demand. Pull baselines are in each space's Git, not in
the database (no schema change in 0.19.0).
