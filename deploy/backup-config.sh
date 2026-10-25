#!/bin/bash
# Daily emailed copy of the Metatrash configuration: /etc/metatrash (totp-key
# included), the backup settings and the service's unit, as one tar archive
# encrypted with gpg (AES-256, passphrase in CONFIG_PASSPHRASE_FILE) and sent
# through the service's own SMTP settings in accounts.json.
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

if [ ! -s "$CONFIG_PASSPHRASE_FILE" ]; then
	log "$CONFIG_PASSPHRASE_FILE missing or empty"
	exit 1
fi

# SMTP settings from accounts.json, as the service sends sign-in codes.
smtp=$(python3 -c '
import json, sys
c = json.load(open(sys.argv[1]))
for k in ("smtpHost", "smtpPort", "smtpUsername", "smtpFrom", "smtpPasswordFile"):
    v = c.get(k)
    if not v:
        sys.exit("accounts.json has no " + k)
    print(v)
' "$ACCOUNTS_CONFIG")
{ read -r smtp_host; read -r smtp_port; read -r smtp_user; read -r smtp_from; read -r smtp_password_file; } <<<"$smtp"

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

# The message: a short note and the archive.
boundary="metatrash-$(od -An -N12 -tx1 /dev/urandom | tr -d ' \n')"
last_backup=$(systemctl show metatrash-backup.service -p Result -p ExecMainExitTimestamp 2>/dev/null | tr '\n' ' ' || true)
{
	echo "From: Metatrash <$smtp_from>"
	echo "To: $CONFIG_MAIL_TO"
	echo "Subject: Metatrash configuration $(hostname -s) $day"
	echo "Date: $(date -R)"
	echo "Message-ID: <$boundary@$(hostname -f 2>/dev/null || hostname)>"
	echo "MIME-Version: 1.0"
	echo "Content-Type: multipart/mixed; boundary=\"$boundary\""
	echo
	echo "--$boundary"
	echo "Content-Type: text/plain; charset=utf-8"
	echo
	echo "The Metatrash configuration from $(hostname -s), encrypted with the"
	echo "configuration passphrase (not in this email). To open it:"
	echo
	echo "  gpg -d $name | tar -xz"
	echo
	echo "(Windows: Git Bash has gpg and tar.) It holds: ${paths[*]}"
	echo
	echo "Last off-site backup run: ${last_backup:-unknown}"
	echo
	echo "--$boundary"
	echo "Content-Type: application/octet-stream; name=\"$name\""
	echo "Content-Disposition: attachment; filename=\"$name\""
	echo "Content-Transfer-Encoding: base64"
	echo
	base64 -w 76 "$work/$name"
	echo "--$boundary--"
} >"$work/message.eml"

# Send with curl; the password goes through a config on stdin, never argv.
password=$(tr -d '\r\n' <"$smtp_password_file")
password=${password//\\/\\\\}
password=${password//\"/\\\"}
user=${smtp_user//\\/\\\\}
user=${user//\"/\\\"}
scheme=smtp
if [ "$smtp_port" = 465 ]; then
	scheme=smtps
fi
printf 'user = "%s:%s"\n' "$user" "$password" |
	curl -sS --config - --ssl-reqd --crlf -m 60 --retry 3 \
		--url "$scheme://$smtp_host:$smtp_port" \
		--mail-from "$smtp_from" --mail-rcpt "$CONFIG_MAIL_TO" \
		--upload-file "$work/message.eml"
log "sent $name ($(du -h "$work/$name" | cut -f1)) to $CONFIG_MAIL_TO"
