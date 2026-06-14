#!/bin/sh
# Daily Postgres backup for SSO Gateway.
# - pg_dump to /backup/daily/<UTC-date>.sql.gz
# - Retain BACKUP_RETENTION_DAYS (default 14)
# - Optional upload to S3 (S3_ENDPOINT, S3_BUCKET, AWS_* env)
#
# This script is meant to run as the only process in the backup
# container; it sleeps until the next scheduled time.

set -eu

PGHOST="${PGHOST:-postgres}"
PGPORT="${PGPORT:-5432}"
PGUSER="${PGUSER:-sso}"
PGDATABASE="${PGDATABASE:-sso}"
PGPASSWORD="$(cat "${PGPASSWORD_FILE:-/run/secrets/postgres_password}")"
export PGPASSWORD

BACKUP_DIR="${BACKUP_DIR:-/backup/daily}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
SCHEDULE="${BACKUP_SCHEDULE:-0 2 * * *}"
S3_BUCKET="${S3_BUCKET:-}"
S3_ENDPOINT="${S3_ENDPOINT:-}"

mkdir -p "$BACKUP_DIR"

next_run() {
    # Cron expression → seconds until next run. Simple implementation:
    # sleep until the next 02:00 UTC. For arbitrary cron expressions
    # the operator should swap in a real cron parser.
    now=$(date -u +%s)
    target=$(date -u -d "tomorrow 02:00" +%s 2>/dev/null || date -u -v tomorrow -v 2H -v 0M -v 0S +%s)
    echo $(( target - now ))
}

do_backup() {
    ts=$(date -u +%Y%m%dT%H%M%SZ)
    out="$BACKUP_DIR/${ts}.sql.gz"
    echo "[$(date -Iseconds)] starting backup -> $out"
    if pg_dump -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" --no-owner --no-acl | gzip -9 > "$out"; then
        echo "[$(date -Iseconds)] backup ok: $(stat -c %s "$out" 2>/dev/null || stat -f %z "$out") bytes"
    else
        echo "[$(date -Iseconds)] BACKUP FAILED" >&2
        rm -f "$out"
        return 1
    fi

    if [ -n "$S3_BUCKET" ] && [ -n "$S3_ENDPOINT" ]; then
        # awscli expected in the image. Add in Dockerfile.backup if used.
        AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-}" \
        AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-}" \
        aws --endpoint-url "$S3_ENDPOINT" s3 cp "$out" "s3://$S3_BUCKET/sso-gateway/$ts.sql.gz" || \
            echo "[$(date -Iseconds)] s3 upload failed (non-fatal)"
    fi
}

prune() {
    find "$BACKUP_DIR" -name "*.sql.gz" -type f -mtime "+${RETENTION_DAYS}" -delete -print | \
        while read f; do echo "[$(date -Iseconds)] pruned $f"; done
}

# Main loop
while true; do
    sleep_s=$(next_run)
    echo "[$(date -Iseconds)] next backup in ${sleep_s}s (schedule: $SCHEDULE)"
    sleep "$sleep_s"
    do_backup
    prune
done
