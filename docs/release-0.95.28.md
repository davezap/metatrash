# Core 0.95.28: brands

Prepared 2026-11-13 by Claude. Claude ran `gofmt`, `go vet` and
`go test ./...` in a scratch copy of v0.95.27 (the database tests skip
without MariaDB; nothing here touches the database), and looked at the
sign-in page in Chromium with Company's Collumii brand in light and dark.
Pending Dave: the release helper, commit, tag/push and Actions, which
Company 0.96.43's manifest drives.

## What changes

Release 1 of the Collumii rebrand (dave-zap/metatrash `branding/`):
Collumii is the company brand, Metatrash stays the core. Tier: Core,
because anyone running their own instance may want to give it their own
name and look; Core carries only the hook and keeps "Metatrash" as the
default. The Collumii name, artwork and colours are Company's.

- `extension.Hooks.Brand *extension.Brand`: `Name`, `HomeTitle`,
  `Description`, `Wordmark` (trusted inline SVG), `CSS`, `Favicon`,
  `TouchIcon`. At most one module may set it; Core checks it at startup.
- With a brand, the name replaces "Metatrash" in page titles, the sign-in,
  app connection and provider return pages, emails (subject, text, From
  name) and new authenticator-app entries. Tool and technical names stay
  (Metatrash Sync, the GitHub app, headers, trailers, agent text).
- `/assets/brand.css` (after `signin.css` on those pages), and the brand's
  `/favicon.ico` and `/apple-touch-icon.png`.
- Link previews on every site app page (Open Graph and X card tags: site
  name, page title, description), with the brand's `ShareImage` as
  `/assets/share.png` when it has one.
- Login code emails also carry a link, `/login#code=…`, that signs in the
  browser that asked for the code (the code after `#` never reaches the
  server). Elsewhere the page says to use that browser or type the code.
  A code page left waiting in another tab then says it can be closed.
- `MailHeader`, `MailAccent`, `MailBackground`: a brand with a mail header
  sends every email with a simple HTML version beside the text. Core alone
  stays plain text.

Without a brand nothing changes. No schema, grants, configuration, tool
schema or dependency changes.

## Release

Through Company 0.96.43's manifest (`release-laptop.ps1 -Version 0.96.43`),
which commits and tags Core v0.95.28 first.

## Rollback

Install the previous Core and Company tags together (v0.95.27 with Company
v0.96.42). Nothing is stored by this change.
