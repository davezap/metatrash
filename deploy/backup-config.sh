#!/bin/bash
# Daily emailed copy of the Metatrash configuration: /etc/metatrash (totp-key
# included), the backup settings and the service's unit, as one tar archive
# encrypted with gpg (AES-256, passphrase in CONFIG_PASSPHRASE_FILE) and sent
# through the service's own SMTP settings in accounts.json (python3 smtplib).
# upgrade.sh installs it as /usr/local/sbin/metatrash-backup-config; it runs as
# root from metatrash-backup-config.timer. See docs/deployment.md, Backups.
set -euo pipefail
umask 077

ENV_FILE=${METATRASH_BACKUP_ENV:-/etc/metatrash-backup/backup.env}
if [ -r "$ENV_FILE" ]; then
	set -a
	. "$ENV_FILE"
	set +a
fi

: "${CONFIG_MAIL_TO:?set CONFIG_MAIL_TO in $ENV_FILE}"
CONFIG_DIR=${CONFIG_DIR:-/etc/metatrash}
ACCOUNTS_CONFIG=${ACCOUNTS_CONFIG:-$CONFIG_DIR/accounts.json}
CONFIG_PASSPHRASE_FILE=${CONFIG_PASSPHRASE_FILE:-/etc/metatrash-backup/config-passphrase}
CONFIG_MAIL_PATHS=${CONFIG_MAIL_PATHS-/etc/metatrash-backup /etc/systemd/system/metatrash.service /etc/systemd/system/metatrash.service.d}

log() { echo "metatrash-backup-config: $*"; }

# gpg needs its agent even for a passphrase; Amazon Linux's default
# gnupg2-minimal lacks it.
if ! command -v gpg-agent >/dev/null; then
	log "gpg-agent not installed (Amazon Linux: sudo dnf swap -y gnupg2-minimal gnupg2-full)"
	exit 1
fi
if [ ! -s "$CONFIG_PASSPHRASE_FILE" ]; then
	log "$CONFIG_PASSPHRASE_FILE missing or empty"
	exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
day=$(date -u +%Y-%m-%d)
name="metatrash-config-$(hostname -s)-$day.tar.gz.gpg"

# Archive (paths kept whole inside, without the leading /), then encrypt.
paths=("$CONFIG_DIR")
for path in $CONFIG_MAIL_PATHS; do
	if [ -e "$path" ]; then
		paths+=("$path")
	fi
done
tar -czf "$work/config.tar.gz" --exclude="$CONFIG_PASSPHRASE_FILE" -P --transform 's,^/,,' "${paths[@]}"
install -d -m 0700 "$work/gnupg"
gpg --homedir "$work/gnupg" --batch --quiet --yes --no-symkey-cache --pinentry-mode loopback \
	--passphrase-file "$CONFIG_PASSPHRASE_FILE" --symmetric --cipher-algo AES256 \
	-o "$work/$name" "$work/config.tar.gz"
gpgconf --homedir "$work/gnupg" --kill all >/dev/null 2>&1 || true

# Send through the SMTP settings in accounts.json, as the service sends
# sign-in codes (Python's smtplib: Amazon Linux's curl-minimal has no SMTP).
last_backup=$(systemctl show metatrash-backup.service -p Result -p ExecMainExitTimestamp 2>/dev/null | tr '\n' ' ' || true)
ATTACHMENT="$work/$name" DAY="$day" PATHS="${paths[*]}" LAST_BACKUP="${last_backup:-unknown}" \
	python3 -I - "$ACCOUNTS_CONFIG" <<'PY'
import json, os, smtplib, socket, ssl, sys
from email.message import EmailMessage
from email.utils import formatdate, make_msgid

c = json.load(open(sys.argv[1]))
for k in ("smtpHost", "smtpPort", "smtpUsername", "smtpFrom", "smtpPasswordFile"):
    if not c.get(k):
        sys.exit("metatrash-backup-config: accounts.json has no " + k)
password = open(c["smtpPasswordFile"]).read().strip()
path = os.environ["ATTACHMENT"]
name = os.path.basename(path)
host = socket.gethostname().split(".")[0]

m = EmailMessage()
m["From"] = "Metatrash <%s>" % c["smtpFrom"]
m["To"] = os.environ["CONFIG_MAIL_TO"]
m["Subject"] = "Metatrash configuration %s %s" % (host, os.environ["DAY"])
m["Date"] = formatdate(localtime=True)
m["Message-ID"] = make_msgid(domain=socket.getfqdn())
m.set_content(
    "The Metatrash configuration from %s, encrypted with the\n"
    "configuration passphrase (not in this email). To open it:\n\n"
    "  gpg -d %s | tar -xz\n\n"
    "(Windows: Git Bash has gpg and tar.) It holds: %s\n\n"
    "Last off-site backup run: %s\n" % (host, name, os.environ["PATHS"], os.environ["LAST_BACKUP"]))
m.add_attachment(open(path, "rb").read(), maintype="application", subtype="octet-stream", filename=name)

port = int(c["smtpPort"])
context = ssl.create_default_context()
if port == 465:
    s = smtplib.SMTP_SSL(c["smtpHost"], port, timeout=60, context=context)
else:
    s = smtplib.SMTP(c["smtpHost"], port, timeout=60)
    s.starttls(context=context)
with s:
    s.login(c["smtpUsername"], password)
    s.send_message(m)
PY
log "sent $name ($(du -h "$work/$name" | cut -f1)) to $CONFIG_MAIL_TO"
