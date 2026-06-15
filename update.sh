#!/bin/bash
# ============================================
# UPDATE.SH - One-Click Update Script
# Gateway Login (SSO Gateway)
# ============================================
#
# Steps:
#   1. Backup Postgres database (pg_dump)
#   2. Pull latest code from GitHub
#   3. Detect config.yaml or migration changes
#   4. Rebuild images (no cache) — done BEFORE stopping old containers
#      so a build failure leaves the running stack intact
#   5. Recreate containers (down + up)
#   6. Run database migrations
#   7. Recovery: mark stale running sync_runs as failed
#   8. Health check (api /healthz)
#   9. Cleanup old backups (keep last N)
#
# Usage:
#   ./update.sh                    # update from origin/main
#   ./update.sh --no-backup        # skip DB backup (e.g. dev)
#   ./update.sh --ref <branch>     # update from a different branch
#   ./update.sh --compose <file>   # use a custom compose file (default: prod)
#
# Requires: bash, docker, git, openssl

set -euo pipefail

# ----- Args -----
COMPOSE_FILE="deploy/docker-compose.prod.yml"
REF="main"
DO_BACKUP=1

while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-backup)  DO_BACKUP=0; shift ;;
        --ref)        REF="$2"; shift 2 ;;
        --compose)    COMPOSE_FILE="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,30p' "$0"
            exit 0
            ;;
        *)
            echo "unknown arg: $1" >&2
            exit 2
            ;;
    esac
done

COMPOSE_CMD="docker compose -f $COMPOSE_FILE"

echo ""
echo "============================================"
echo "  Gateway Login - Update"
echo "  compose: $COMPOSE_FILE"
echo "  ref:     $REF"
echo "============================================"
echo ""

# ----- Sanity -----
if [ ! -f "$COMPOSE_FILE" ] && [ ! -f "deploy/docker-compose.yml" ]; then
    echo "ERROR: no compose file found at $COMPOSE_FILE"
    echo "Run this script from the project root."
    exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: docker not found in PATH"
    exit 1
fi
if ! command -v git >/dev/null 2>&1; then
    echo "ERROR: git not found in PATH"
    exit 1
fi
if [ ! -d ".git" ]; then
    echo "ERROR: not a git repository"
    exit 1
fi

# ----- Step 1: backup -----
if [ "$DO_BACKUP" -eq 1 ]; then
    echo "[1/9] Backing up database..."
    BACKUP_DIR="backups"
    mkdir -p "$BACKUP_DIR"
    TIMESTAMP=$(date +%Y%m%d_%H%M%S)
    BACKUP_FILE="$BACKUP_DIR/db_backup_$TIMESTAMP.sql.gz"

    if $COMPOSE_CMD ps --status running postgres 2>/dev/null | grep -q postgres; then
        # pg_dump the sso database (the schema is `sso` in our prod compose).
        $COMPOSE_CMD exec -T postgres pg_dump -U sso -d sso --no-owner --no-acl \
            | gzip -9 > "$BACKUP_FILE"
        if [ -s "$BACKUP_FILE" ]; then
            echo "  -> $BACKUP_FILE ($(stat -c %s "$BACKUP_FILE" 2>/dev/null || stat -f %z "$BACKUP_FILE") bytes)"
        else
            echo "WARNING: backup file is empty"
        fi
    else
        echo "  postgres container not running; skipping backup"
    fi
else
    echo "[1/9] Skipping backup (--no-backup)"
    BACKUP_FILE=""
fi

# ----- Step 2: pull code -----
echo ""
echo "[2/9] Pulling latest code (ref=$REF)..."
if ! git pull origin "$REF"; then
    echo "ERROR: git pull failed"
    echo "Try: git stash && git pull origin $REF && git stash pop"
    exit 1
fi
echo "  code updated"

# ----- Step 3: detect changes -----
echo ""
echo "[3/9] Detecting relevant changes..."
SCHEMA_CHANGED=$(git diff HEAD~1 --name-only 2>/dev/null | grep -E "internal/db/migrations/" || true)
CONFIG_CHANGED=$(git diff HEAD~1 --name-only 2>/dev/null | grep -E "internal/store/config\.go|internal/setup/setup\.go" || true)
DEPLOY_CHANGED=$(git diff HEAD~1 --name-only 2>/dev/null | grep -E "^deploy/" || true)

if [ -n "$SCHEMA_CHANGED" ]; then
    echo "  schema migrations changed:"
    echo "$SCHEMA_CHANGED" | sed 's/^/    /'
    MIGRATION_NEEDED=1
else
    MIGRATION_NEEDED=0
    echo "  no schema changes"
fi
if [ -n "$CONFIG_CHANGED" ]; then
    echo "  config schema changed: re-running setup may be required"
fi
if [ -n "$DEPLOY_CHANGED" ]; then
    echo "  deploy files changed: nginx / compose restart recommended"
fi

# ----- Step 4: rebuild (BEFORE stopping old) -----
echo ""
echo "[4/9] Rebuilding images (this may take 2-5 minutes)..."
$COMPOSE_CMD build --no-cache
echo "  build ok"

# ----- Step 5: stop + remove orphans -----
echo ""
echo "[5/9] Recreating containers..."
$COMPOSE_CMD down --remove-orphans || true

# ----- Step 6: start -----
echo ""
echo "[6/9] Starting containers..."
$COMPOSE_CMD up -d
sleep 5

# ----- Step 7: migrations (only if needed) -----
if [ "${MIGRATION_NEEDED:-0}" -eq 1 ]; then
    echo ""
    echo "[7/9] Running database migrations..."
    if $COMPOSE_CMD exec -T api ls /app/migrations >/dev/null 2>&1; then
        MIGRATIONS_DIR="/app/migrations"
    else
        MIGRATIONS_DIR="./internal/db/migrations"
    fi
    MIGRATIONS_PATH="$MIGRATIONS_DIR" $COMPOSE_CMD run --rm --entrypoint "sh -c 'MIGRATIONS_PATH=/app/migrations /app/gateway migrate'" setup 2>/dev/null \
        || MIGRATIONS_PATH="$MIGRATIONS_DIR" $COMPOSE_CMD run --rm setup
    echo "  migrations applied"
else
    echo ""
    echo "[7/9] Skipping migrations (none changed)"
fi

# ----- Step 8: recover stale sync_runs -----
echo ""
echo "[8/9] Marking stale running sync_runs as failed (crash recovery)..."
# Bounce the sync container so the startup sweeper runs. Cron stops on
# down() so a single restart covers the recovery pass.
$COMPOSE_CMD restart sync
sleep 2

# ----- Step 9: health check + cleanup -----
echo ""
echo "[9/9] Health check + cleanup..."
HEALTH_OK=0
for i in 1 2 3 4 5 6 7 8 9 10; do
    if curl -sf http://localhost:8080/healthz >/dev/null 2>&1 \
       || $COMPOSE_CMD exec -T api wget -q -O - http://localhost:8080/healthz >/dev/null 2>&1; then
        HEALTH_OK=1
        break
    fi
    echo "  waiting for api /healthz (attempt $i/10)..."
    sleep 2
done
if [ "$HEALTH_OK" -eq 1 ]; then
    echo "  api /healthz: ok"
else
    echo "  WARNING: api /healthz did not respond within 20s"
fi

# Keep last 5 backups.
if [ "$DO_BACKUP" -eq 1 ] && [ -d "backups" ]; then
    ls -t backups/db_backup_*.sql.gz 2>/dev/null | tail -n +6 | xargs -r rm
fi

# ----- Done -----
echo ""
echo "============================================"
echo "  UPDATE COMPLETE"
echo "============================================"
echo ""
echo "  api:      http://localhost:8080/healthz"
echo "  metrics:  http://localhost:8080/metrics"
if [ -n "${BACKUP_FILE:-}" ] && [ -f "${BACKUP_FILE:-/dev/null}" ]; then
    echo "  backup:   $BACKUP_FILE"
fi
echo ""
echo "  Tail logs:"
echo "    $COMPOSE_CMD logs -f --tail=100 api"
echo "    $COMPOSE_CMD logs -f --tail=100 sync"
echo ""
if [ -n "${BACKUP_FILE:-}" ] && [ -f "${BACKUP_FILE:-/dev/null}" ]; then
    echo "  Restore if needed:"
    echo "    gunzip -c $BACKUP_FILE | $COMPOSE_CMD exec -T postgres psql -U sso -d sso"
    echo ""
fi
