# Docling PDF conversion

Optional, from Core 0.95.5 (on a machine of its own from 0.95.6). Without it, a PDF's text comes from `pdftotext`
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

## Where it runs

A conversion needs 1–2 GB of memory while it runs, plus about 6 GB of disk
for Docling and its models. On a small server (2 GB of RAM or less) that can
run Metatrash itself out of memory, so run the converter **on a machine of
its own** (0.95.6): `metatrash-docling serve` there, and `agents.docling.url`
on the Metatrash server. On a server with room to spare, Metatrash can run it
directly (`agents.docling.command`). Everything else (the queue, the stored
Markdown, reads, search, the sweep) is the same either way.

## On a machine of its own

For example an EC2 t4g.medium (2 vCPU, 4 GB) with a 20 GB disk, in the same
VPC as the Metatrash server. On that machine, as root, from a Core checkout:

```sh
sudo dnf install -y git
git clone --depth 1 --branch v0.95.7 https://github.com/davezap/metatrash.git
sudo bash metatrash/deploy/docling/install-server.sh
```

(`install.sh` installs `python3.11` with dnf when it is missing.) Each
conversion is logged with its size and seconds:
`sudo journalctl -u metatrash-docling -f`.

`install-server.sh` runs `install.sh` (below), creates the
`metatrash-docling` user, a token in `/etc/metatrash-docling/token`, and the
`metatrash-docling` systemd service on port 8090 (`LISTEN` and `THREADS`
change the address and CPU threads). It prints the token and the
`agents.docling` lines to add on the Metatrash server.

The service answers plain HTTP with a bearer token: keep it on the private
network. Allow port 8090 only from the Metatrash servers' security group (or
their private addresses), never from the internet. It converts one PDF at a
time (others get 503 and Metatrash tries them again in 15 minutes), keeps the
models loaded between conversions, and systemd caps it at 90% of the
machine's memory.

On the Metatrash server, put the token in a file only the service reads and
point `agents.docling` at the converter's private address:

```sh
sudo install -m 0640 -o root -g metatrash /dev/null /etc/metatrash/docling-token
sudoedit /etc/metatrash/docling-token         # the token, one line
```

```json
"docling": {
  "url": "http://10.0.1.5:8090",
  "tokenFile": "/etc/metatrash/docling-token"
}
```

Metatrash starts even when the converter is down: it asks the converter for
its version at start and every five minutes, and converts nothing until it
answers (reads keep pdftotext's text). A PDF the converter can't reach or is
busy for is tried again in 15 minutes; one it can't convert, in a day. Several
Metatrash servers (staging and production) can share one converter.

## Install (on the Metatrash server)

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

Only `command` (or `url` and `tokenFile`) is needed; the others show their
defaults. `workers` 1–4,
`threads` 1–16 (CPU threads per conversion), `timeoutSeconds` 30–3600 per PDF,
`maxPages` 1–2000, `maxMB` 1–100, `ocr` `"auto"` (RapidOCR reads scanned
pages) or `"off"`. With `command`, the service runs `command version` at
start and refuses to start if it doesn't answer; `metatrash check` shows the version and limits.

Memory: Docling loads its layout and table models for each conversion (about
1–2 GB while it runs). Size `workers` to the server's spare memory and
measure on your own machine before raising it.

## Remove

Delete `agents.docling` and restart. Reads go back to `pdftotext`'s text;
the stored Markdown is left in the bucket (it is small) and swept with its
PDFs.
