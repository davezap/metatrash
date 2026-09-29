# Slate document viewer — 0.5.2

The public Spaces browser renders `.md` and `.markdown` files (case-insensitive) with Slate in read-only preview. Other UTF-8 files keep the plain-text view. The escaped server-rendered text remains visible if JavaScript is disabled or Slate cannot initialise.

`internal/service/web/slate.js` and `slate.css` are unchanged copies from the supplied `Slate_Markdown_Components_v0.9.2` folder (its README identifies the distribution as 0.9.2-wire.1). Both are embedded in the Go binary alongside the small host integration, `document.js`. There is no runtime link to that source folder or new build dependency. Host styling lives in `style.css`.

The host disables document/settings persistence, editing, and interactive tasks. The heading outline starts closed and can be opened. Navigation folders also start closed on every page load; each count includes every document beneath that folder, including nested folders.

The explorer CSP allows bundled same-origin scripts and Slate's table-alignment style attributes. HTTPS/data images are supported; external scripts, embedded video frames, and media remain blocked. Raw HTML is escaped by Slate. The homepage and account policies are unchanged.

## Focused owner checks

After your normal build and deployment:

- Open README.md and check headings, lists, tables, links, fenced code, and disabled task checkboxes. Confirm there are no editing controls.
- Open a non-Markdown file; verify it is still shown verbatim.
- Check folder counts against nested files and confirm folders start closed, including after navigating to a document.
- Check a narrow screen and the document outline toggle. Disable JavaScript and confirm the plain-text fallback.
- If hosting beneath a folder, confirm all three new assets load beneath that prefix.

No build, runtime tests, or deployment were run during implementation.

Before snapshot: `docs/snapshots/0017-slate-viewer-before/`.
Incremental patch: `patch/0017-slate-viewer.patch`, applied after 0016.
