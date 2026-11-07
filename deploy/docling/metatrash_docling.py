#!/usr/bin/env python3
"""metatrash-docling: the PDF converter Metatrash uses for better PDF text.

Metatrash (internal/service/docling.go) either runs this program once per PDF
on its own server (agents.docling.command), or sends PDFs to it running as a
service on another machine (agents.docling.url, 0.95.6), and keeps the
Markdown it gives:

    metatrash-docling version
        prints one line, "metatrash-docling <wrapper> docling <version> ...
        formats pdf,docx,pptx,xlsx", which names the conversion (a new line
        here means new Markdown) and the file types it takes.

    metatrash-docling convert --input FILE [--max-pages N] [--timeout SECONDS]
                              [--ocr auto|off] [--threads N]
        prints the file's Markdown on stdout and exits 0. FILE is a PDF,
        Word (.docx), PowerPoint (.pptx) or Excel (.xlsx) file, by its
        extension; the page rules below are for PDFs. Pages after the
        first start with a "<!-- page N -->" line, as Metatrash's pdftotext
        text does. Only the first --max-pages pages are converted (a line at
        the end says so). On failure it prints one line on stderr and exits 1.

    metatrash-docling serve --token-file FILE [--listen ADDRESS:PORT]
                            [--threads N] [--max-mb N]
        answers HTTP on ADDRESS:PORT (default 0.0.0.0:8090), one conversion
        at a time, with the models loaded once:
          GET  /version   the version line (text/plain)
          POST /convert?maxPages=N&timeout=S&ocr=auto|off&format=pdf|docx|pptx|xlsx
                          body: the file (format defaults to pdf); answer:
                          its Markdown (text/markdown),
                          422 with a one-line reason when it can't be
                          converted, 503 while another conversion runs.
        Both need "Authorization: Bearer <token>" with the token in FILE.
        It speaks plain HTTP: keep it on a private network and let only
        Metatrash servers reach it (a security group or firewall).

Headings get levels (Docling's heading hierarchy: PDF bookmarks, numbering,
then font style), tables come out as Markdown tables and pictures as an
"<!-- image -->" placeholder. Models are read from <venv>/models (install.sh
downloads them) and nothing is fetched from the network while converting.
"""

import argparse
import hmac
import os
import sys
import tempfile
import threading
import time

WRAPPER_VERSION = "4"
# The file types convert takes (by extension), named on the version line so
# Metatrash sends only these (0.95.8).
FORMATS = ("pdf", "docx", "pptx", "xlsx")


def models_dir():
    return os.environ.get("METATRASH_DOCLING_MODELS") or os.path.join(sys.prefix, "models")


def version_line():
    from importlib.metadata import version as package_version

    return f"metatrash-docling {WRAPPER_VERSION} docling {package_version('docling')} docling-core {package_version('docling-core')} formats {','.join(FORMATS)}"


def offline(threads):
    # Never reach for the network: the models are installed beside us.
    os.environ.setdefault("HF_HUB_OFFLINE", "1")
    os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")
    os.environ["OMP_NUM_THREADS"] = str(threads)


class ConversionError(Exception):
    """A PDF that can't be converted; the message is one line for a log."""


class Converter:
    """Docling set up for Metatrash, kept between conversions (the models
    load once per OCR/timeout setting)."""

    def __init__(self, threads):
        self.threads = threads
        self.cache = {}

    def _docling(self, ocr, timeout):
        key = (ocr, timeout)
        if key in self.cache:
            return self.cache[key]
        from docling.datamodel.accelerator_options import AcceleratorDevice, AcceleratorOptions
        from docling.datamodel.base_models import InputFormat
        from docling.datamodel.pipeline_options import (
            HeadingHierarchyOptions,
            OcrAutoOptions,
            PdfPipelineOptions,
        )
        from docling.document_converter import DocumentConverter, PdfFormatOption

        options = PdfPipelineOptions(
            artifacts_path=models_dir(),
            do_ocr=ocr != "off",
            do_table_structure=True,
            document_timeout=float(timeout),
            accelerator_options=AcceleratorOptions(num_threads=self.threads, device=AcceleratorDevice.CPU),
            heading_hierarchy_options=HeadingHierarchyOptions(enabled=True),
        )
        if ocr != "off":
            options.ocr_options = OcrAutoOptions()
        converter = DocumentConverter(
            allowed_formats=[InputFormat.PDF, InputFormat.DOCX, InputFormat.PPTX, InputFormat.XLSX],
            format_options={InputFormat.PDF: PdfFormatOption(pipeline_options=options)},
        )
        self.cache[key] = converter
        return converter

    def markdown(self, path, max_pages, timeout, ocr):
        from docling.datamodel.base_models import ConversionStatus
        from docling_core.types.doc import ImageRefMode

        ext = os.path.splitext(path)[1].lower().lstrip(".")
        if ext not in FORMATS:
            raise ConversionError(f"cannot convert .{ext} files (only {', '.join(FORMATS)})")
        if ext != "pdf":
            return self._whole(path, timeout, ocr)
        pages = _page_count(path)
        result = self._docling(ocr, timeout).convert(path, raises_on_error=False, page_range=(1, max_pages))
        if result.status not in (ConversionStatus.SUCCESS, ConversionStatus.PARTIAL_SUCCESS):
            reasons = "; ".join(str(e.error_message) for e in (result.errors or [])[:3]) or str(result.status.value)
            raise ConversionError(f"conversion failed: {reasons}"[:500])
        doc = result.document
        parts = []
        for page_no in sorted(doc.pages):
            text = doc.export_to_markdown(page_no=page_no, image_mode=ImageRefMode.PLACEHOLDER, escape_underscores=False, escape_html=False).strip()
            if not text:
                continue
            if page_no > 1:
                parts.append(f"<!-- page {page_no} -->")
            parts.append(text)
        if not parts:
            raise ConversionError("the PDF has no text Docling could read")
        out = "\n\n".join(parts) + "\n"
        if result.status == ConversionStatus.PARTIAL_SUCCESS:
            out += "\n<!-- Some pages could not be converted. -->\n"
        if pages > max_pages:
            out += f"\n…(pages after {max_pages} of {pages} are left out)\n"
        return out


    def _whole(self, path, timeout, ocr):
        """A Word, PowerPoint or Excel file's Markdown, in one piece (their
        items do not all carry page numbers)."""
        from docling.datamodel.base_models import ConversionStatus
        from docling_core.types.doc import ImageRefMode

        result = self._docling(ocr, timeout).convert(path, raises_on_error=False)
        if result.status not in (ConversionStatus.SUCCESS, ConversionStatus.PARTIAL_SUCCESS):
            reasons = "; ".join(str(e.error_message) for e in (result.errors or [])[:3]) or str(result.status.value)
            raise ConversionError(f"conversion failed: {reasons}"[:500])
        text = result.document.export_to_markdown(image_mode=ImageRefMode.PLACEHOLDER, escape_underscores=False, escape_html=False).strip()
        if not text:
            raise ConversionError("the file has no text Docling could read")
        return text + "\n"


def _page_count(path):
    import pypdfium2

    try:
        pdf = pypdfium2.PdfDocument(path)
    except Exception as e:
        raise ConversionError(f"the PDF cannot be read (it may be damaged or password-protected): {e}"[:500])
    try:
        return len(pdf)
    finally:
        pdf.close()


def one_line(e):
    return (str(e).splitlines() or [type(e).__name__])[0][:500]


def convert(args):
    offline(args.threads)
    try:
        sys.stdout.write(Converter(args.threads).markdown(args.input, args.max_pages, args.timeout, args.ocr))
    except ConversionError as e:
        print(str(e), file=sys.stderr)
        return 1
    return 0


def serve(args):
    offline(args.threads)
    with open(args.token_file, encoding="utf-8") as f:
        token = f.read().strip()
    if len(token) < 32:
        sys.exit(f"{args.token_file} must hold a token of at least 32 characters (openssl rand -hex 32)")
    expected = ("Bearer " + token).encode()
    host, _, port = args.listen.rpartition(":")
    converter = Converter(args.threads)
    busy = threading.Lock()
    line = version_line()
    max_bytes = args.max_mb << 20

    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
    from urllib.parse import parse_qs, urlsplit

    class Handler(BaseHTTPRequestHandler):
        server_version = "metatrash-docling"
        sys_version = ""

        def reply(self, code, body, kind="text/plain; charset=utf-8"):
            data = body.encode("utf-8")
            self.send_response(code)
            self.send_header("Content-Type", kind)
            self.send_header("Content-Length", str(len(data)))
            if code == 503:
                self.send_header("Retry-After", "30")
            self.end_headers()
            self.wfile.write(data)

        def allowed(self):
            given = (self.headers.get("Authorization") or "").encode()
            if hmac.compare_digest(given, expected):
                return True
            self.reply(401, "unauthorized\n")
            return False

        def do_GET(self):
            if urlsplit(self.path).path != "/version":
                return self.reply(404, "not found\n")
            if self.allowed():
                self.reply(200, line + "\n")

        def do_POST(self):
            url = urlsplit(self.path)
            if url.path != "/convert":
                return self.reply(404, "not found\n")
            if not self.allowed():
                return
            try:
                q = {k: v[-1] for k, v in parse_qs(url.query).items()}
                max_pages = int(q.get("maxPages", "200"))
                timeout = int(q.get("timeout", "600"))
                ocr = q.get("ocr", "auto")
                fmt = q.get("format", "pdf")
                length = int(self.headers.get("Content-Length", "-1"))
            except ValueError:
                return self.reply(400, "bad request\n")
            if not (1 <= max_pages <= 2000 and 30 <= timeout <= 3600 and ocr in ("auto", "off") and fmt in FORMATS):
                return self.reply(400, "bad request\n")
            if length < 1:
                return self.reply(411, "Content-Length required\n")
            if length > max_bytes:
                return self.reply(413, f"the PDF is over {args.max_mb} MB\n")
            if not busy.acquire(blocking=False):
                return self.reply(503, "busy: another conversion is running\n")
            try:
                with tempfile.NamedTemporaryFile(suffix="." + fmt) as f:
                    remaining = length
                    while remaining:
                        chunk = self.rfile.read(min(remaining, 1 << 20))
                        if not chunk:
                            return self.reply(400, "short body\n")
                        f.write(chunk)
                        remaining -= len(chunk)
                    f.flush()
                    started = time.monotonic()
                    try:
                        out = converter.markdown(f.name, max_pages, timeout, ocr)
                    except ConversionError as e:
                        self.log_message("conversion failed after %.1f s (%d KB): %s", time.monotonic() - started, length >> 10, e)
                        return self.reply(422, str(e) + "\n")
                    except Exception as e:
                        self.log_message("conversion failed after %.1f s (%d KB): %s", time.monotonic() - started, length >> 10, one_line(e))
                        return self.reply(422, f"conversion failed: {type(e).__name__}: {one_line(e)}\n")
                    self.log_message("converted %d KB in %.1f s (ocr %s)", length >> 10, time.monotonic() - started, ocr)
                self.reply(200, out, "text/markdown; charset=utf-8")
            finally:
                busy.release()

        def log_message(self, fmt, *a):
            sys.stderr.write("%s %s\n" % (self.address_string(), fmt % a))

    server = ThreadingHTTPServer((host or "0.0.0.0", int(port)), Handler)
    print(f"{line}: listening on {args.listen}", file=sys.stderr, flush=True)
    server.serve_forever()


def main():
    parser = argparse.ArgumentParser(prog="metatrash-docling")
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("version")
    c = sub.add_parser("convert")
    c.add_argument("--input", required=True)
    c.add_argument("--max-pages", type=int, default=200)
    c.add_argument("--timeout", type=int, default=600)
    c.add_argument("--ocr", choices=["auto", "off"], default="auto")
    c.add_argument("--threads", type=int, default=2)
    s = sub.add_parser("serve")
    s.add_argument("--token-file", required=True)
    s.add_argument("--listen", default="0.0.0.0:8090")
    s.add_argument("--threads", type=int, default=2)
    s.add_argument("--max-mb", type=int, default=100)
    args = parser.parse_args()
    if args.command == "version":
        print(version_line())
        return 0
    if args.command == "serve":
        return serve(args)
    try:
        return convert(args)
    except Exception as e:  # one line for Metatrash's log, never a traceback dump
        print(f"conversion failed: {type(e).__name__}: {one_line(e)}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
