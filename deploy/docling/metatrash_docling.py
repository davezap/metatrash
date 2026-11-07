#!/usr/bin/env python3
"""metatrash-docling: the PDF converter Metatrash runs for better PDF text.

Metatrash (internal/service/docling.go) runs this program once per PDF, off
the request path, and keeps what it prints as the file's Markdown:

    metatrash-docling version
        prints one line, "metatrash-docling <wrapper> docling <version>",
        which names the conversion: a new line here means new Markdown.

    metatrash-docling convert --input FILE [--max-pages N] [--timeout SECONDS]
                              [--ocr auto|off] [--threads N]
        prints the PDF's Markdown on stdout and exits 0. Pages after the
        first start with a "<!-- page N -->" line, as Metatrash's pdftotext
        text does. Only the first --max-pages pages are converted (a line at
        the end says so). On failure it prints one line on stderr and exits 1.

Headings get levels (Docling's heading hierarchy: PDF bookmarks, numbering,
then font style), tables come out as Markdown tables and pictures as an
"<!-- image -->" placeholder. Models are read from <venv>/models (install.sh
downloads them) and nothing is fetched from the network while converting.
"""

import argparse
import os
import sys

WRAPPER_VERSION = "1"


def models_dir():
    return os.environ.get("METATRASH_DOCLING_MODELS") or os.path.join(sys.prefix, "models")


def version():
    from importlib.metadata import version as package_version

    print(f"metatrash-docling {WRAPPER_VERSION} docling {package_version('docling')} docling-core {package_version('docling-core')}")
    return 0


def convert(args):
    # Never reach for the network: the models are installed beside us.
    os.environ.setdefault("HF_HUB_OFFLINE", "1")
    os.environ.setdefault("TRANSFORMERS_OFFLINE", "1")
    os.environ["OMP_NUM_THREADS"] = str(args.threads)

    from docling.datamodel.accelerator_options import AcceleratorDevice, AcceleratorOptions
    from docling.datamodel.base_models import ConversionStatus, InputFormat
    from docling.datamodel.pipeline_options import (
        HeadingHierarchyOptions,
        OcrAutoOptions,
        PdfPipelineOptions,
    )
    from docling.document_converter import DocumentConverter, PdfFormatOption
    from docling_core.types.doc import ImageRefMode

    options = PdfPipelineOptions(
        artifacts_path=models_dir(),
        do_ocr=args.ocr != "off",
        do_table_structure=True,
        document_timeout=float(args.timeout),
        accelerator_options=AcceleratorOptions(num_threads=args.threads, device=AcceleratorDevice.CPU),
        heading_hierarchy_options=HeadingHierarchyOptions(enabled=True),
    )
    if args.ocr != "off":
        options.ocr_options = OcrAutoOptions()
    converter = DocumentConverter(format_options={InputFormat.PDF: PdfFormatOption(pipeline_options=options)})
    pages = _page_count(args.input)
    result = converter.convert(args.input, raises_on_error=False, page_range=(1, args.max_pages))
    if result.status not in (ConversionStatus.SUCCESS, ConversionStatus.PARTIAL_SUCCESS):
        reasons = "; ".join(str(e.error_message) for e in (result.errors or [])[:3]) or str(result.status.value)
        print(f"conversion failed: {reasons}"[:500], file=sys.stderr)
        return 1
    doc = result.document
    parts = []
    for page_no in sorted(doc.pages):
        text = doc.export_to_markdown(page_no=page_no, image_mode=ImageRefMode.PLACEHOLDER, escape_underscores=False).strip()
        if not text:
            continue
        if page_no > 1:
            parts.append(f"<!-- page {page_no} -->")
        parts.append(text)
    if not parts:
        print("the PDF has no text Docling could read", file=sys.stderr)
        return 1
    out = "\n\n".join(parts) + "\n"
    if result.status == ConversionStatus.PARTIAL_SUCCESS:
        out += "\n<!-- Some pages could not be converted. -->\n"
    if pages > args.max_pages:
        out += f"\n…(pages after {args.max_pages} of {pages} are left out)\n"
    sys.stdout.write(out)
    return 0


def _page_count(path):
    import pypdfium2

    pdf = pypdfium2.PdfDocument(path)
    try:
        return len(pdf)
    finally:
        pdf.close()


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
    args = parser.parse_args()
    if args.command == "version":
        return version()
    try:
        return convert(args)
    except Exception as e:  # one line for Metatrash's log, never a traceback dump
        print(f"conversion failed: {type(e).__name__}: {e}".splitlines()[0][:500], file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
