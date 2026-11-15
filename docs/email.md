# Receiving email into folders

Core 0.97.0. A folder with an **email service** gets its own address. Each
message sent to it becomes one Markdown file in that folder, and the folder's
actions run on that file the same way they do for any new file. You could,
for example, have agents triage security notices as they arrive.

```json
{
  "services": [{"type": "email", "allow": ["no-reply@sns.amazonaws.com"]}],
  "actions": [{"name": "triage", "on": "*.md", "do": "Triage this finding ..."}]
}
```

## What a message becomes

- `inbox/2026-11-15-0632-cve-2026-0001-in-openssl-d25354f65825.md`: front
  matter (`type: email`, from, to, cc, subject, date, received, message_id,
  in_reply_to, reply_to_file, thread, attachments, checks), a heading, then
  the body. The body is the plain-text part when there is one, otherwise the
  HTML part converted to Markdown. Times are UTC.
- `inbox/assets/<id>/<attachment>`: each attachment, kept as a virtual file.
  PDFs and Word files are converted by Docling when it is set up.
- `inbox/assets/<id>/message.eml`: the original message, kept as proof.

`<id>` is 12 hex digits of the Message-ID. It is how a reply finds the
message it answers anywhere in the space, even after agents have moved files
around, and it stops the same message being filed twice (a duplicate gets a
200 and nothing new is filed). **Agents must keep the `-<id>.md` ending when
they rename a message.**

A reply whose earlier message is already in the space links back to it
(`reply_to_file`), keeps the same `thread`, and loses the history it quotes:
the text from "On … wrote:", "-----Original Message-----", an Outlook
From:/Sent: block, or a closing block of `>` lines. Set `"history": "keep"`
to keep that history. The full original is still in `message.eml`.

Limits: 36 MB per raw message, 25 MB of attachments, 50 attachments, 200
messages a folder and 1,000 a space per hour. Anything left out is listed
under **Attachments** in the file. Without a virtual-file bucket
(`agents.downloads`), only the Markdown is kept.

## Settings (`.metatrash.json`, owned spaces only)

| Key | Meaning |
| --- | --- |
| `allow` | When set, only these senders get through: an address (`a@example.com`), a domain (`example.com`) or a domain and its subdomains (`*.example.com`), all in lower case. A sender on the list whose DMARC check failed is refused as forged. |
| `block` | Senders to refuse, in the same forms. Block wins over allow. |
| `address` | The address's version, 1 by default. Raise it to retire the address and get a new one. |
| `history` | `strip` (the default) or `keep`. |

With no `allow`, anyone may send. The random part of the address is the
protection, so share it only where it's needed. Mail marked as a virus or
spam by the relay is refused. Email can't go into a GitHub folder.

Everything in an email was written by whoever sent it, so treat it as
untrusted input. A folder action that reads email should get only the tools
it needs (`tools`), and it can't send mail.

## The address

`<space id>-<tag>@<domain>`. The tag is an HMAC of the space, the folder and
the address version, made with the server's email key, so it can't be
guessed. Renaming or moving the folder gives it a new address. Anything after
a `+` is ignored (`…+cve@…`). The space's owner reads the address from:

```
GET /api/v1/account/spaces/{space}/email?folder=inbox/      → {"folder", "address"}
GET /api/v1/site/spaces/{owner}/{slug}/email?folder=inbox/
```

## Server setup (`accounts.json`)

```json
"email": {
  "enabled": true,
  "domain": "in.example.com",
  "keyFile": "/etc/metatrash/email-address.key",
  "secretFile": "/etc/metatrash/email-drop.secret"
}
```

- `keyFile`: 64 hex digits (`openssl rand -hex 32`). Changing it changes
  every folder's address.
- `secretFile`: the mail drop's bearer secret, at least 32 characters. Leave
  it out if mail only arrives through a module, such as the hosted service's
  Amazon SES receiver (`extension.EmailReceiver`).

Point the domain's MX record at a mail server that hands each message to the
**mail drop**:

```
POST /email/inbound
Authorization: Bearer <secret>
X-Metatrash-Recipient: <envelope recipient>
X-Metatrash-Checks: spf=pass; dkim=pass; dmarc=pass; spam=pass; virus=pass   (optional)
<raw message>
```

The response codes:

- 201: filed.
- 200: duplicate, nothing new filed.
- 404: no folder has that address.
- 403: the folder refused the sender.
- 422: the message was refused as a virus or spam.
- 413: the message is too large.
- 429 or 503: try again later.

For example, a Postfix pipe transport:

```
# master.cf
metatrash unix - n n - 4 pipe flags=R user=nobody argv=/usr/bin/curl -sf -X POST
  -H @/etc/metatrash/email-drop.header -H X-Metatrash-Recipient:${recipient}
  --data-binary @- https://example.com/email/inbound
```

Here `email-drop.header` holds the single line `Authorization: Bearer <secret>`.
