#!/bin/sh
# First-time host bootstrap for SSO Gateway production deploy.
# - Generates secrets files
# - Generates master key
# - Creates directory tree
# - Sets restrictive permissions
#
# Idempotent: existing secret files are left untouched (rotate manually
# if you need to change them — that will require re-running setup too).

set -eu

SECRETS_DIR="deploy/secrets"
KEYS_DIR="deploy/keys"
LOGS_DIR="deploy/logs/postgres"
BACKUP_DIR="deploy/backupdata-data"

mkdir -p "$SECRETS_DIR" "$KEYS_DIR" "$LOGS_DIR" "$BACKUP_DIR"
chmod 700 "$SECRETS_DIR" "$KEYS_DIR"

# Generate each secret if absent. Files are 0600 root-only.
write_if_absent() {
    file="$1"
    len="$2"
    if [ -e "$file" ]; then
        echo "exists: $file (skipping)"
    else
        head -c "$len" /dev/urandom | base64 > "$file"
        chmod 600 "$file"
        echo "created: $file"
    fi
}

write_if_absent "$SECRETS_DIR/postgres_password.txt"  24
write_if_absent "$SECRETS_DIR/redis_password.txt"     24
write_if_absent "$SECRETS_DIR/gateway_master_key.txt" 32

# JWT RS256 key pair (mounted read-only into api container).
if [ ! -f "$KEYS_DIR/jwt-private.pem" ]; then
    openssl genpkey -algorithm RSA -out "$KEYS_DIR/jwt-private.pem" -pkeyopt rsa_keygen_bits:2048
    openssl rsa -in "$KEYS_DIR/jwt-private.pem" -pubout -out "$KEYS_DIR/jwt-public.pem"
    chmod 600 "$KEYS_DIR/jwt-private.pem"
    chmod 644 "$KEYS_DIR/jwt-public.pem"
    echo "created: $KEYS_DIR/jwt-{private,public}.pem"
else
    echo "exists: $KEYS_DIR/jwt-private.pem (skipping)"
fi

# .env file (compose reads it for unkeyed env vars; secrets are
# mounted as files, not env). Fill in VPS_MYSQL_DSN and JWT_ISSUER.
if [ ! -f "deploy/.env" ]; then
    cat > deploy/.env <<EOF
# Public, non-secret env consumed by compose variable substitution.
# DO NOT commit this file.

VPS_MYSQL_DSN=changeme:sso_replicator:REAL_PASSWORD@tcp(vps.your-domain.com:3306)/sja?parseTime=true&readTimeout=30s
JWT_ISSUER=https://gateway.your-domain.com
S3_BUCKET=
S3_ENDPOINT=
EOF
    chmod 600 deploy/.env
    echo "created: deploy/.env (edit VPS_MYSQL_DSN, JWT_ISSUER before going to production)"
else
    echo "exists: deploy/.env (skipping)"
fi

cat <<'NEXT'

Bootstrap complete. Next steps:

  1. Edit deploy/.env and set VPS_MYSQL_DSN + JWT_ISSUER.

  2. Pull/build images:
       docker compose -f deploy/docker-compose.prod.yml pull
       docker compose -f deploy/docker-compose.prod.yml build

  3. Bring up the stack:
       docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
       docker compose -f deploy/docker-compose.prod.yml run --rm setup
       docker compose -f deploy/docker-compose.prod.yml up -d

  4. Configure your reverse proxy (nginx example in deploy/nginx.conf)
     to forward 443 -> api:8080.

  5. Verify:
       curl -H "X-API-Key: <key from setup>" https://gateway.your-domain.com/api/v1/karyawan?limit=5

  6. Tail logs:
       docker compose -f deploy/docker-compose.prod.yml logs -f --tail=100
NEXT
