#!/bin/sh
# Installs or updates mtsync for the current user on macOS or Linux:
#
#   curl -fsSL https://github.com/davezap/metatrash/releases/latest/download/install.sh | sh
#
# It downloads the build for this computer from the GitHub release, checks it
# against the release's SHA256SUMS, puts it in ~/.local/bin and starts the
# service at every login (mtsync install), or restarts it after an update.
# No administrator rights are needed.
#
# Environment:
#   MTSYNC_VERSION     a release such as 0.41.1 (default: the latest)
#   MTSYNC_DIR         where to put mtsync (default: ~/.local/bin)
#   MTSYNC_NO_SERVICE  set to 1 to install the program only
set -eu

repo="davezap/metatrash"
dir="${MTSYNC_DIR:-$HOME/.local/bin}"

fail() { echo "mtsync install: $*" >&2; exit 1; }

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=macos ;;
  *) fail "this script is for macOS and Linux; on Windows use mtsync-windows-setup.exe" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) fail "no mtsync build for $(uname -m); build it from source (see cmd/mtsync/README.md)" ;;
esac
file="mtsync-$os-$arch"

if [ -n "${MTSYNC_VERSION:-}" ]; then
  base="https://github.com/$repo/releases/download/v${MTSYNC_VERSION#v}"
else
  base="https://github.com/$repo/releases/latest/download"
fi

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
else
  fail "needs curl or wget"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Downloading $file"
fetch "$base/$file" "$tmp/$file" || fail "could not download $base/$file"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || fail "could not download $base/SHA256SUMS"

want=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/$file" | awk '{ print $1 }')
else
  got=$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  fail "checksum mismatch for $file; nothing was installed"
fi
chmod +x "$tmp/$file"

# An update: stop the running service before replacing the program.
target="$dir/mtsync"
updating=false
if [ -x "$target" ]; then
  updating=true
  "$target" stop >/dev/null 2>&1 || true
fi
mkdir -p "$dir"
mv -f "$tmp/$file" "$target"
echo "Installed $("$target" version) in $dir"

if [ "${MTSYNC_NO_SERVICE:-}" != 1 ]; then
  if [ "$os" = linux ]; then
    unit="$HOME/.config/systemd/user/mtsync.service"
  else
    unit="$HOME/Library/LaunchAgents/com.metatrash.mtsync.plist"
  fi
  # install sets up the login service once; after that, start is enough.
  if $updating && [ -f "$unit" ]; then
    "$target" start || echo "The service did not start; mtsync log shows why." >&2
  elif ! "$target" install; then
    echo "The service is not set up to start at login (above says why). Run mtsync run in a terminal to sync, or mtsync install to try again." >&2
  fi
fi

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo
     echo "$dir is not on your PATH yet. Add this line to ~/.profile (or ~/.zshrc on a Mac), then open a new terminal:"
     echo "  export PATH=\"$dir:\$PATH\"" ;;
esac

if ! $updating; then
  echo
  echo "Next: connect this computer to your Metatrash account and choose its spaces,"
  echo "then open a space in Slate Viewer or register a folder:"
  echo "  mtsync login"
  echo "  mtsync add owner/space ~/Metatrash/owner/space"
fi
