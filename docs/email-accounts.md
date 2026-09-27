# Email accounts — 0.4.0

This bite adds email-only registration/login and an authenticated account page. It does not create private spaces yet. Existing public, REST, and MCP behavior remains available without account configuration.

## Gmail configuration

Use smtp.gmail.com on port 587 with required STARTTLS, username and sender `david@204am.com`. The service verifies the TLS certificate and requires TLS 1.2 or later before authentication; it never falls back to plaintext. Gmail requires an app password where supported by the account policy, not a normal account password. See [Google SMTP guidance](https://support.google.com/a/answer/176600?hl=en) and [app passwords](https://support.google.com/mail/answer/185833?hl=en).

The password is read once at startup from a separate protected file. It is never put into the repository, command line, account store, emails, or logs. SMTP errors shown to visitors are generic; failed sends do not leave a usable code.

## Linux server setup

From your updated server checkout, build/install **0.4.0** using the existing upgrade workflow. The new templates are embedded; there is no separate frontend build. Check the installed version with `/usr/local/bin/metatrash version`.

Create the account configuration (this does not replace spaces.json or keys.json):

```bash
sudo install -o root -g metatrash -m 0640 config/accounts.example.json /etc/metatrash/accounts.json
```

For first-time password setup only, create an empty protected file, then edit it:

```bash
sudo install -o root -g metatrash -m 0640 /dev/null /etc/metatrash/smtp-password
sudoedit /etc/metatrash/smtp-password
```

Enter only the Gmail app password, without the visual grouping spaces. Do not run the empty-file command again when updating an existing password; use sudoedit. No credentials need to be sent to Codex.

Enable accounts with a systemd override:

```bash
sudo systemctl edit metatrash
```

Add:

```ini
[Service]
Environment=METATRASH_ACCOUNTS_CONFIG=/etc/metatrash/accounts.json
```

The equivalent command-line option is `-accounts-config /etc/metatrash/accounts.json`. With neither option set, account links are hidden and account routes report that sign-in is unavailable. With an option set but invalid/missing configuration or credentials, startup fails rather than silently disabling authentication. Gmail credentials are validated on the first send, not at startup.

Merge these new lines from the Apache example into the existing HTTPS VirtualHost, retaining the working public/API/MCP rules:

```apache
ProxyPassMatch "^(/login(?:/send|/verify)?|/logout|/account)$" "http://127.0.0.1:8080$1"
ProxyPassReverse /login http://127.0.0.1:8080/login
ProxyPassReverse /account http://127.0.0.1:8080/account
```

Then:

```bash
sudo systemctl daemon-reload
sudo systemctl restart metatrash
sudo systemctl status metatrash --no-pager
sudo apachectl configtest
```

Only after `Syntax OK`:

```bash
sudo systemctl reload httpd
```

Use `https://metatrash.com/login` in a browser. Secure cookies and exact Origin checks intentionally prevent an ordinary localhost HTTP sign-in flow. Public localhost health/root checks still work. If using another hostname, update `origin` to that exact HTTPS origin (no trailing slash). Keep ProxyPreserveHost On and trusted-proxy configuration so host validation and per-client limits work correctly.

## User flow

1. Enter an email address. The same page and response handle both new and existing users.
2. Enter the six-digit emailed code in the same browser. It expires after ten minutes and is consumed on successful verification. Five incorrect attempts invalidate it. A new send replaces the previous code in that browser; successful verification invalidates outstanding codes for that email.
3. New users are saved only after verification. Existing users keep their account ID and allowance. `/account` displays the verified email, allowance, and a sign-out button, and clearly states that space creation is coming next.
4. Sessions expire after 24 hours. Sign-out revokes the current session immediately. Restarting the service invalidates all sessions and pending codes; persistent accounts are preserved.

Email identities are lowercased. Dots and plus aliases are not collapsed. This first version accepts ASCII email addresses only. Account identities and ownership are independent of existing bearer-key private spaces; signing in does not claim any preconfigured space.

## Storage, limits, and administration

- `<data-dir>/accounts.json` is an atomic, mode-0600 JSON store outside repositories. Keep it private and include it in server backups. The existing single-process data-directory lock covers this store. Creation uses a synced sibling temporary file and rename. Corrupt or unsupported data causes startup failure, not a reset.
- Each new account has `maxPrivateSpaces: 1`. To change an allowance, stop the service, back up accounts.json privately, edit only that user's nonnegative integer `maxPrivateSpaces`, preserve file ownership/mode, then start the service. This persists the allowance for the next private-space management bite; no creation endpoint exists yet. Do not edit this file while the service runs.
- Codes use a cryptographic random generator and are stored only as keyed digests in memory. Session tokens are random 256-bit values; only their hashes are held server-side. Cookies use Secure, HttpOnly, SameSite=Strict, Path=/ and the __Host- prefix. Logout also requires CSRF validation. Forms require the exact configured Origin and reject oversized/duplicate fields.
- Send limits: one request per email per minute, three per email per hour, ten per client IP per hour, and 100 total per UTC day. Fixed windows apply; rejected attempts also consume applicable counters. These intentionally conservative initial limits protect the Gmail mailbox and reset on restart. Verification also has a 30-attempt-per-IP/ten-minute limit. Existing ingress limits apply as well.
- At most two SMTP sends run concurrently. SMTP calls have a 15-second deadline. Memory caps are 2,048 pending challenges and 4,096 sessions, with at most eight sessions per account. Persistent accounts are capped at 10,000. These are first-release service limits, not per-user space allowances.
- The email address is displayed only on the authenticated account page or in that browser's pending login form. Account pages are no-store/noindex, with no third-party assets. No email addresses are added to public Git.

## Focused owner checks

Optional fake-mail checks (no real email or SMTP connection):

```bash
go test ./internal/service -run '^TestAccounts(Codes|HTTP)$' -count=1
```

These cover code expiry, attempt exhaustion, single use, account persistence, repeat-login identity/allowance preservation, failed delivery, origin/CSRF rejection, send throttling, session cookies, and logout.

Small live check: register with an email you control, verify the received code, confirm the allowance is one, sign out, and sign in again. An incognito `/account` visit should redirect to `/login`. Confirm the public explorer and MCP remain available. Send limits mean repeated live tests should be paced.

Assistant validation: Go formatting/syntax parsing, configuration JSON parsing, source review, and patch checks only. No project build, test execution, browser runtime check, SMTP connection, email send, or deployment was performed. The owner confirmed the prior 0.3.0 Apache routing fix before this bite.
