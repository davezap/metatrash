#!/usr/bin/env bash
# Installs metatrash-docling, the converter Metatrash runs for better PDF
# text (docs/docling.md). Run as root on the server, then add agents.docling
# to accounts.json and restart Metatrash.
#
#   sudo bash deploy/docling/install.sh [PREFIX]
#
# PREFIX defaults to /opt/metatrash-docling. It holds the virtual
# environment, the models (PREFIX/models) and the command Metatrash runs,
# PREFIX/bin/metatrash-docling. Nothing is downloaded after this: conversion
# runs offline. Set PYTHON to choose the interpreter (default python3.11, installed with dnf when missing,
# else python3; Docling needs 3.10 or later). It needs about 6 GB free on
# PREFIX's disk and /var/tmp while installing. To run the converter as a
# service on its own machine, use install-server.sh instead (it runs this).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
prefix="${1:-/opt/metatrash-docling}"
python="${PYTHON:-}"
if [ -z "$python" ]; then
  if ! command -v python3.11 >/dev/null 2>&1 && command -v dnf >/dev/null 2>&1; then
    # Amazon Linux 2023's python3 is 3.9; Docling needs 3.10 or later.
    dnf install -y python3.11
  fi
  if command -v python3.11 >/dev/null 2>&1; then python=python3.11; else python=python3; fi
fi
"$python" - <<'PY'
import sys
if sys.version_info < (3, 10):
    sys.exit(f"Docling needs Python 3.10 or later, not {sys.version.split()[0]}; set PYTHON or install python3.11")
PY

echo "Installing metatrash-docling into $prefix with $("$python" --version)"
# pip unpacks PyTorch (over 1 GB) in TMPDIR; /tmp is often a small memory
# disk (Amazon Linux 2023: half the RAM), so build on disk instead.
build="$(mktemp -d /var/tmp/metatrash-docling-build.XXXXXX)"
trap 'rm -rf "$build"' EXIT
export TMPDIR="$build" PIP_CACHE_DIR="$build/cache"
"$python" -m venv "$prefix"
"$prefix/bin/pip" install --quiet --upgrade pip
# On x86_64, take PyTorch's CPU-only build (the default one carries CUDA,
# several GB); on ARM64 (Graviton) PyPI's build is already CPU-only.
extra=()
if [ "$(uname -m)" = x86_64 ]; then
  extra=(--extra-index-url https://download.pytorch.org/whl/cpu)
fi
"$prefix/bin/pip" install --quiet "${extra[@]}" -r "$here/requirements.txt"
"$prefix/bin/pip" freeze > "$prefix/installed-freeze.txt"

# The models (layout, tables, OCR, ...) go beside the environment, so
# conversion never fetches anything.
"$prefix/bin/docling-tools" models download --output-dir "$prefix/models"

install -m 0755 "$here/metatrash_docling.py" "$prefix/metatrash_docling.py"
cat > "$prefix/bin/metatrash-docling" <<SH
#!/bin/sh
exec "$prefix/bin/python" "$prefix/metatrash_docling.py" "\$@"
SH
chmod 0755 "$prefix/bin/metatrash-docling"
chmod -R a+rX "$prefix"

"$prefix/bin/metatrash-docling" version
echo
echo "Installed. Add to the agents section of accounts.json, then restart Metatrash:"
echo "  \"docling\": {\"command\": \"$prefix/bin/metatrash-docling\"}"
echo "Check a PDF by hand first:"
echo "  time $prefix/bin/metatrash-docling convert --input some.pdf | head -40"
