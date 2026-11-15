# Core 0.97.0 (email part): folders receive email

Prepared 2026-11-15 by Claude (thread "Design receiving emails into spaces").
It ships through Company 0.96.50, which adds the Amazon SES receiver for
in.collumii.com.

**Tier: Core.** The mail drop, the email service and turning a message into
Markdown are what one person running their own instance needs to receive
mail, from any mail server. Hosted receiving through Amazon SES, its
addresses on collumii.com, and showing the address in folder properties are
Company. Routing, queues and response targets come later, in Enterprise.

## What's new

- **Email service** (`{"type": "email"}` in `.metatrash.json`, with
  optional `allow`, `block`, `address` and `history`). Each folder with it
  has its own unguessable address. Every message becomes one Markdown file
  with front matter. Its attachments and the original `.eml` are kept as
  virtual files under `assets/<id>/`, and the folder's actions run on the
  new file. Replies link to the earlier message and lose the history they
  quote. A message delivered twice is filed once. See `docs/email.md`.
- **Mail drop**: `POST /email/inbound`, which needs the bearer secret.
- **`extension.EmailReceiver`**: lets modules (Company's SES receiver)
  deliver mail.
- **REST resource `email`**: `GET …/email?folder=` gives the owner a
  folder's address.

Files:

- New: `internal/service/email_in.go`, `email_parse.go`, `email_file.go`,
  `email_http.go`, `email_in_test.go`, `docs/email.md`, this file.
- Changed: `extension/extension.go`, `internal/service/accounts.go`,
  `folder_config.go`, `http.go`, `service.go`, `site_api.go`,
  `api/tool-schema.json` (services text only), `CHANGELOG.md`.

There's no schema or grant change, and no new dependency (the standard
library parses MIME). Without `email` in accounts.json nothing changes. For
rollback, the old binary starts with the same config. Before it can start
again, the `email` block has to come out of accounts.json, because the old
binary's strict JSON refuses it.

**Version:** part of Core 0.97.0 with search phase 3 and the login fixes, released as Company 0.96.50.

## Tests

The new tests are `TestEmailServiceConfig`, `TestSenderRules`,
`TestParseEmail`, `TestStripQuoted`, `TestReceiveEmail` (filing,
attachments in the bucket, the folder action running, duplicates, a reply
found after its parent was moved, history stripped, block, allow, forged
DMARC, a retired address, +tags, unknown addresses, refusing GitHub folders),
`TestEmailInboundHTTP` and `TestEmailConfigSettings`. The full suite passed
in the cloud, without MariaDB.

## Turning it on (self-hosted)

1. `openssl rand -hex 32 > /etc/metatrash/email-address.key` and
   `openssl rand -hex 32 > /etc/metatrash/email-drop.secret`, both
   readable only by the service user.
2. Add the `email` block to accounts.json (see `docs/email.md`) and
   restart.
3. Point the domain's MX at a mail server that posts each message to the
   mail drop.

## Check after release

1. Add `{"services":[{"type":"email"}]}` to a test folder's
   `.metatrash.json`.
2. Read its address from `GET …/email?folder=<folder>/`.
3. Send it a message with a PDF attached. A dated `.md` file should appear
   in the folder, with the PDF and `message.eml` under `assets/<id>/`.
4. Reply to that message, quoting it. The reply's file should link to the
   first one and leave the quoted text out.
