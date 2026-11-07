# Docling PDF conversion

Optional, from Core 0.95.5. Without it, a PDF's text comes from `pdftotext`
(poppler-utils): plain lines, no headings, no tables, no OCR. With it, the
server also converts each PDF with [Docling](https://github.com/docling-project/docling)
and reads and search use that Markdown instead: headings with levels (from
the PDF's bookmarks, numbering and font style), tables as Markdown tables,
reading order across columns, and OCR for scanned pages.

## How it works

- Conversion is slow (seconds a page on a CPU), so it never runs inside a
  request or the write queue. A bounded queue (`workers`, default 1)
  converts a PDF after it is uploaded, or the first time its text is read
  without a conversion, so PDFs stored before Docling was installed get one
  too.
- Until the conversion is done, `read` and `search` use `pdftotext`'s text
  as before, and `read`'s note says a better conversion is being made.
- The Markdown is stored in the space-files bucket beside the content:
  `files/<space ID>/<sha256>.docling-<converter>.md`. `<converter>` comes
  from `metatrash-docling version`, so a new Docling makes new Markdown
  rather than mixing outputs. Git, history, mtsync, GitHub folders and folder
  actions see no change: a conversion never commits, so it can't start a sync
  loop or trigger an action again.
- The Markdown is removed with its PDF by the daily sweep and with the space,
  and is not counted as the space's storage.
- A conversion that fails (a damaged PDF, a timeout, no text) leaves the PDF
  and its `pdftotext` text as they were; it is logged
  (`docling: space …, PDF …: …`) and tried again after a day or a restart.
  PDFs over `maxMB` are not converted; only the first `maxPages` pages are,
  and the Markdown ends with a line saying so.
- Pages after the first start with `<!-- page N -->`, as with `pdftotext`.
  Pictures appear as `<!-- image -->`.

## Install

On the server, as root (Python 3.10 or later; on Amazon Linux 2023
`sudo dnf install python3.11`):

```sh
sudo bash deploy/docling/install.sh            # into /opt/metatrash-docling
/opt/metatrash-docling/bin/metatrash-docling version
time /opt/metatrash-docling/bin/metatrash-docling convert --input some.pdf | head -60
```

`install.sh` makes a virtual environment, installs the pinned packages in
`deploy/docling/requirements.txt` (on x86_64 with PyTorch's CPU-only build),
writes the exact installed set to `/opt/metatrash-docling/installed-freeze.txt`,
downloads Docling's models into `/opt/metatrash-docling/models` and installs
the `metatrash-docling` command (`deploy/docling/metatrash_docling.py`). After
that nothing is downloaded: the converter runs with `HF_HUB_OFFLINE=1` and an
environment of its own (none of the service's settings or secrets).

To install the same packages on a second server, copy `installed-freeze.txt`
and run `/opt/metatrash-docling/bin/pip install -r installed-freeze.txt`
after creating the environment.

## Configure

Add `docling` to the `agents` section of `accounts.json` (it needs
`agents.downloads`, where PDFs are kept) and restart:

```json
"agents": {
  "downloads": { "...": "..." },
  "docling": {
    "command": "/opt/metatrash-docling/bin/metatrash-docling",
    "workers": 1,
    "threads": 2,
    "timeoutSeconds": 600,
    "maxPages": 200,
    "maxMB": 50,
    "ocr": "auto"
  }
}
```

Only `command` is needed; the others show their defaults. `workers` 1–4,
`threads` 1–16 (CPU threads per conversion), `timeoutSeconds` 30–3600 per PDF,
`maxPages` 1–2000, `maxMB` 1–100, `ocr` `"auto"` (RapidOCR reads scanned
pages) or `"off"`. The service runs `command version` at start and refuses
to start if it doesn't answer; `metatrash check` shows the version and limits.

Memory: Docling loads its layout and table models for each conversion (about
1–2 GB while it runs). Size `workers` to the server's spare memory and
measure on your own machine before raising it.

## Remove

Delete `agents.docling` and restart. Reads go back to `pdftotext`'s text;
the stored Markdown is left in the bucket (it is small) and swept with its
PDFs.
