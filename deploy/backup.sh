#!/bin/bash
# Off-site backup of Metatrash: account database, every space and the
# configuration, sent to a restic repository (Backblaze B2 on the live server).
# upgrade.sh installs it as /usr/local/sbin/metatrash-backup; it runs as root
# from metatrash-backup.timer. Setup and restore: docs/deployment.md, Backups.
#
# Order matters: the database is dumped before the spaces are bundled, so every
# space the dump lists as ready has its repository, with at least the commits
# it had then, in the same backup.
set -euo pipefail
umask 077

ENV_FILE=${METATRASH_BACKUP_ENV:-/etc/metatrash-backup/backup.env}
if [ ! -r "$ENV_FILE" ]; then
	echo "metatrash-backup: $ENV_FILE not found (see docs/deployment.md, Backups)" >&2
	exit 1
fi
set -a
. "$ENV_FILE"
set +a

: "${RESTIC_REPOSITORY:?set RESTIC_REPOSITORY in $ENV_FILE}"
DATA_DIR=${DATA_DIR:-/var/lib/metatrash}
CONFIG_DIR=${CONFIG_DIR:-/etc/metatrash}
STAGING_DIR=${STAGING_DIR:-/var/backups/metatrash}
DATABASE=${DATABASE-metatrash}            # empty: no account database
CONFIG_EXCLUDE=${CONFIG_EXCLUDE-totp-key} # names in CONFIG_DIR kept out of backups
CONFIG_EXTRA=${CONFIG_EXTRA-/etc/systemd/system/metatrash.service /etc/systemd/system/metatrash.service.d}
KEEP_DAILY=${KEEP_DAILY:-14}
KEEP_WEEKLY=${KEEP_WEEKLY:-8}
KEEP_MONTHLY=${KEEP_MONTHLY:-12}
PRUNE_WEEKDAY=${PRUNE_WEEKDAY:-7}         # 1 = Monday ... 7 = Sunday
export RESTIC_CACHE_DIR=${RESTIC_CACHE_DIR:-/var/cache/metatrash-backup}

log() { echo "metatrash-backup: $*"; }

# On failure, tell the optional health check (healthchecks.io style: URL/fail).
ping() {
	[ -n "${HEALTHCHECK_URL:-}" ] || return 0
	curl -fsS -m 10 --retry 3 -o /dev/null "$HEALTHCHECK_URL$1" || log "health check ping failed"
}
trap 'ping /fail' ERR

# Read-only Git against repositories owned by the service user. Hooks, gc and
# inherited Git settings stay off, as in the service.
gitro() {
	env -u GIT_DIR -u GIT_CONFIG GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
		git -c safe.directory='*' -c gc.auto=0 -c core.hooksPath=/dev/null "$@"
}

install -d -m 0700 "$STAGING_DIR" "$STAGING_DIR/db" "$STAGING_DIR/spaces" "$RESTIC_CACHE_DIR"
exec 9>"$STAGING_DIR/.lock"
if ! flock -n 9; then
	log "another backup is running"
	exit 1
fi
failed=0

# 1. Account database: one consistent InnoDB snapshot, no locks on the service.
if [ -n "$DATABASE" ]; then
	dump="$STAGING_DIR/db/$DATABASE.sql"
	mariadb-dump --single-transaction --quick --routines --triggers --hex-blob \
		${MARIADB_DUMP_OPTIONS:-} --databases "$DATABASE" >"$dump.tmp"
	if ! tail -n 1 "$dump.tmp" | grep -q '^-- Dump completed'; then
		log "database dump incomplete"
		exit 1
	fi
	mv -f "$dump.tmp" "$dump"
	log "database $DATABASE dumped ($(du -h "$dump" | cut -f1))"
fi

# 2. Spaces: a full bundle of main for each repository whose main moved since
# its last bundle. Unchanged bundles stay as they are, so each restic snapshot
# holds every space and restores on its own.
declare -A keep=()
bundled=0
unchanged=0
for kind in repos owned-repos; do
	[ -d "$DATA_DIR/$kind" ] || continue
	install -d -m 0700 "$STAGING_DIR/spaces/$kind"
	for repo in "$DATA_DIR/$kind"/*.git; do
		[ -d "$repo" ] || continue
		name=$(basename "$repo" .git)
		out="$STAGING_DIR/spaces/$kind/$name.bundle"
		keep["$out"]=1
		if ! head=$(gitro --git-dir="$repo" rev-parse --verify -q refs/heads/main); then
			log "$kind/$name: no main branch, skipped"
			failed=1
			continue
		fi
		if [ -f "$out" ] && [ "$(gitro bundle list-heads "$out" refs/heads/main 2>/dev/null | cut -d' ' -f1)" = "$head" ]; then
			unchanged=$((unchanged + 1))
			continue
		fi
		rm -f "$out.tmp"
		if gitro --git-dir="$repo" bundle create -q "$out.tmp" refs/heads/main &&
			gitro --git-dir="$repo" bundle verify -q "$out.tmp" >/dev/null 2>&1; then
			mv -f "$out.tmp" "$out"
			bundled=$((bundled + 1))
		else
			rm -f "$out.tmp"
			log "$kind/$name: bundle failed"
			failed=1
		fi
	done
done
# Bundles of repositories that are gone (a public reset, a removed space) leave
# the staging folder; older snapshots keep them.
for out in "$STAGING_DIR"/spaces/*/*.bundle; do
	[ -f "$out" ] || continue
	[ -n "${keep[$out]:-}" ] || rm -f "$out"
done
log "spaces: $bundled bundled, $unchanged unchanged"

# 3. Configuration, without the files CONFIG_EXCLUDE names (back those up
# separately: totp-key must never sit beside the database it unlocks).
rm -rf "$STAGING_DIR/config.tmp"
install -d -m 0700 "$STAGING_DIR/config.tmp"
cp -a "$CONFIG_DIR" "$STAGING_DIR/config.tmp/"
for name in $CONFIG_EXCLUDE; do
	rm -rf "$STAGING_DIR/config.tmp/$(basename "$CONFIG_DIR")/$name"
done
for path in $CONFIG_EXTRA; do
	if [ -e "$path" ]; then
		cp -a "$path" "$STAGING_DIR/config.tmp/"
	fi
done
rm -rf "$STAGING_DIR/config"
mv "$STAGING_DIR/config.tmp" "$STAGING_DIR/config"

# 4. What this backup holds, for whoever restores it.
{
	echo "created: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	echo "host: $(hostname)"
	echo "metatrash: $(/usr/local/bin/metatrash version 2>/dev/null || echo unknown)"
	if [ -n "$DATABASE" ]; then
		echo "schema_version: $(mariadb -N -B -e "SELECT schema_version FROM $DATABASE.metatrash_account_meta" 2>/dev/null || echo unknown)"
	fi
	echo "excluded from config: ${CONFIG_EXCLUDE:-nothing}"
	echo "spaces (main):"
	for out in "$STAGING_DIR"/spaces/*/*.bundle; do
		[ -f "$out" ] || continue
		echo "  ${out#"$STAGING_DIR"/spaces/} $(gitro bundle list-heads "$out" refs/heads/main | cut -d' ' -f1)"
	done
} >"$STAGING_DIR/MANIFEST.txt"

# 5. Off-site copy. restic encrypts on this machine and uploads only new data.
set +e
restic backup --quiet --host metatrash --tag nightly --exclude "$STAGING_DIR/.lock" "$STAGING_DIR"
status=$?
set -e
if [ "$status" -ne 0 ]; then
	log "restic backup failed (exit $status)"
	ping /fail
	exit 1
fi
log "uploaded to $RESTIC_REPOSITORY"

# Weekly: drop snapshots past the retention rules, then check the repository.
if [ "$(date +%u)" = "$PRUNE_WEEKDAY" ]; then
	restic forget --quiet --host metatrash --keep-daily "$KEEP_DAILY" \
		--keep-weekly "$KEEP_WEEKLY" --keep-monthly "$KEEP_MONTHLY" --prune
	restic check --quiet --read-data-subset=5%
	log "pruned and checked"
fi

if [ "$failed" -ne 0 ]; then
	log "finished with errors (see above)"
	ping /fail
	exit 1
fi
ping ""
log "done"
