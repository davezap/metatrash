# Core 0.97.2: several email addresses in one folder

Prepared 2026-11-16 by Claude (thread "Tidy folder properties and hidden
files"), for Company 0.96.53's combined release.

Tier: Core. Receiving email into a folder is Core (0.97.0), and anyone
running their own instance wants several addresses per folder as much as
the hosted service.

Dave wanted Receive email to be one more kind of action under Automation,
with several per folder. Until now a folder could hold one email service.
Now it can hold several, each with a `name` that tells them apart and its own
address, allowed and blocked senders and history setting.

Existing addresses are unchanged: a service without a name hashes exactly
what it did before. A named service's address also covers its name, so two
services in one folder never share an address. Removing a named service and
adding one with the same name and version later brings back the same address,
just as removing and re-adding the unnamed service does today.

`GET .../email?folder=x/&name=alerts` gives a named service's address. The
answer gains `name`. Without `name` it answers as before.

Files: `internal/service/email_in.go`, `email_http.go`, `folder_config.go`,
`email_in_test.go`, `assets.go`, `CHANGELOG.md`, `docs/email.md`, this file.
No schema, grant, configuration, dependency or tool schema change.

Checked in the cloud: `go vet` and `go test ./internal/service/` pass.

Check after release (website, a space you own, a folder that already
receives email):

1. Properties > Automation lists the existing address as a Receive email
   action, with the same address as before.
2. Add action > Receive email, name it `alerts`: a second, different address
   appears straight away.
3. Send a message to each address: each becomes a file in the folder.
