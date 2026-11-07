#!/usr/bin/env bash
# Sets up a machine of its own as Metatrash's Docling converter (0.95.6,
# docs/docling.md): installs metatrash-docling (install.sh), a system user,
# a token and the metatrash-docling systemd service listening on port 8090.
#
#   sudo bash deploy/docling/install-server.sh [PREFIX]
#
# Run it on the converter machine, not on the Metatrash server. Afterwards
# copy the printed token to the Metatrash server (agents.docling.tokenFile)
# and allow only the Metatrash servers to reach port 8090 (security group or
# firewall): the service speaks plain HTTP for a private network.
#
# THREADS (default: the number of CPUs) and LISTEN (default 0.0.0.0:8090)
# can be set in the environment.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
prefix="${1:-/opt/metatrash-docling}"
threads="${THREADS:-$(nproc)}"
listen="${LISTEN:-0.0.0.0:8090}"

bash "$here/install.sh" "$prefix"

if ! id metatrash-docling >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/metatrash-docling --create-home --shell /sbin/nologin metatrash-docling
fi
install -d -m 0750 -o root -g metatrash-docling /etc/metatrash-docling
token=/etc/metatrash-docling/token
if [ ! -s "$token" ]; then
  (umask 027; openssl rand -hex 32 > "$token")
fi
chown root:metatrash-docling "$token"
chmod 0640 "$token"

cat > /etc/systemd/system/metatrash-docling.service <<UNIT
[Unit]
Description=Metatrash Docling PDF converter
After=network-online.target
Wants=network-online.target

[Service]
User=metatrash-docling
Group=metatrash-docling
ExecStart=$prefix/bin/metatrash-docling serve --token-file $token --listen $listen --threads $threads
Environment=HOME=/var/lib/metatrash-docling
Environment=TMPDIR=/var/lib/metatrash-docling
Restart=on-failure
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/metatrash-docling
# One conversion at a time; a runaway one is killed, not the machine.
MemoryMax=90%

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now metatrash-docling
sleep 2
systemctl is-active metatrash-docling
curl -fsS --retry 15 --retry-connrefused --retry-delay 2 -H "Authorization: Bearer $(cat "$token")" "http://127.0.0.1:${listen##*:}/version"
echo
echo "Converter running. On the Metatrash server:"
echo "  1. Put this token in /etc/metatrash/docling-token (root:metatrash, 0640):"
echo "     $(cat "$token")"
echo "  2. Add to agents in accounts.json, with this machine's private address:"
echo "     \"docling\": {\"url\": \"http://<private-ip>:${listen##*:}\", \"tokenFile\": \"/etc/metatrash/docling-token\"}"
echo "  3. Restart Metatrash."
