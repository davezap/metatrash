# Core 0.99.4: serves Slate's code colouring

Prepared 2026-11-19 by Claude (thread "Colour and edit code files with
CodeMirror") for Company 0.96.61, with Slate 0.26.0 and Slate Viewer 0.46.0.

Tier: Core. Every instance shows code files and code blocks with Slate; the
server only has to hand Slate its language files.

## What changes

- Slate 0.26.0 loads `languages/languages.js` (CodeMirror's language list)
  and one chunk per language from next to `slate.js` the first time a code
  file or a fenced block with a language shows. On the site `slate.js` is
  `/assets/slate.js`, so these are `/assets/languages/languages.js` and
  `/assets/languages/chunks/*.js`.
- `SetFrontend` (`frontend.go`) keeps a bundle's `slate/languages/**/*.js`
  with its `slate.js` and `slate.css`; `serveAsset` (`browser.go`) serves
  them, `chunks/` with a one-year immutable cache (hashed names). Nothing is
  embedded in Core for this: the chunks use the CodeMirror inside the
  bundle's `slate.js`, so they must come from the same build. Core's own
  embedded `web/slate.js` is older and never asks.
- No schema, grant, configuration, dependency or tool schema change.

## Checks

`go test ./internal/service -run 'TestFrontend|TestSlateAndMermaidAssets'`
and `go vet ./internal/service` passed in the cloud on a copy of this
checkout. After the install, the Company check script fetches
`/assets/languages/languages.js`.
