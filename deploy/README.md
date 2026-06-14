# Production deploy guide

This guide walks through a fresh production deploy of the SSO Gateway.
It assumes a Linux host with Docker 24+ and docker compose v2.

## TL;DR

```sh
git clone <your-fork> /opt/sso-gateway && cd /opt/sso-gateway
./deploy/bootstrap.sh
# Edit deploy/.env: set VPS_MYSQL_DSN, JWT_ISSUER
docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
docker compose -f deploy/docker-compose.prod.yml run --rm setup
# Save the printed API key
docker compose -f deploy/docker-compose.prod.yml up -d
```

## Architecture

```
Internet
   │ 443/HTTPS
   ▼
┌──────────────────────────────────┐
│  nginx (host or sidecar)         │  TLS termination, HSTS,
│  deploy/nginx.conf               │  X-Real-IP forwarding
└──────────────┬───────────────────┘
               │  plain HTTP
               ▼
┌──────────────────────────────────┐
│  api  ×2 replicas               │  X-API-Key auth, rate limit
│  (docker compose, internal 8080)│
└──────┬─────────────────┬─────────┘
       │                 │
       ▼                 ▼
┌─────────────┐    ┌────────────┐
│  postgres   │    │  redis     │  (internal only)
│  16-alpine  │    │  7-alpine  │
└─────────────┘    └────────────┘
       ▲                 ▲
       │                 │
       └─────┬───────────┘
             │
       ┌─────┴─────┐
       │  sync     │  cron, single instance
       └───────────┘

       ┌───────────┐
       │  backup   │  daily pg_dump, optional S3
       └───────────┘
```

## What this stack provides

- **TLS termination** at nginx (Let's Encrypt recommended).
- **HSTS, security headers**, `server_tokens off`.
- **Healthcheck** on every long-running service.
- **Resource limits** (CPU + RAM) on every service.
- **Secrets** mounted as files in `/run/secrets/*` — never as env vars.
- **Restart policy** `unless-stopped` on every long-running service.
- **Read-only root FS** on the api container where possible.
- **Drop capabilities + no-new-privileges** security options.
- **Log rotation** (json-file driver, 50MB × 10 files).
- **Daily backup** with retention and optional S3 upload.
- **Sync state** recoverable from crashes (`CleanupStaleRuns`).

## First-time setup

### 1. Bootstrap the host

```sh
chmod +x deploy/bootstrap.sh
./deploy/bootstrap.sh
```

This script:
- Generates `deploy/secrets/{postgres,redis,gateway_master}_password.txt`
  (24+ random bytes each, base64, mode 0600).
- Generates `deploy/keys/jwt-{private,public}.pem` (RS256, 2048-bit).
- Creates `deploy/.env` template.
- Creates log + backup data directories.

### 2. Configure environment

Edit `deploy/.env`:
```
VPS_MYSQL_DSN=sso_replicator:REAL_PASSWORD@tcp(vps.your-domain.com:3306)/sja?parseTime=true&readTimeout=30s
JWT_ISSUER=https://gateway.your-domain.com
S3_BUCKET=my-backups
S3_ENDPOINT=https://s3.amazonaws.com
```

### 3. Build images

```sh
docker compose -f deploy/docker-compose.prod.yml build
```

### 4. Bring up infrastructure

```sh
docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
```

Wait for both to be `healthy`:
```sh
docker compose -f deploy/docker-compose.prod.yml ps
```

### 5. Run setup (interactive)

```sh
docker compose -f deploy/docker-compose.prod.yml run --rm setup
```

You'll be prompted for VPS host, port, database, username, password.
The setup CLI:
- Tests the VPS connection.
- Saves VPS config (password AES-encrypted) to `/etc/gateway/config.yaml`.
- Writes the master key to `/etc/gateway/.env`.
- Runs migrations.
- Generates an API key and inserts it into the `api_keys` table.
- Triggers an initial sync.

**Save the printed API key** — it is shown only once.

### 6. Start the application

```sh
docker compose -f deploy/docker-compose.prod.yml up -d
```

This brings up `api` (×2 replicas via `docker compose scale` or
manual `docker compose up -d --scale api=2`).

### 7. Configure your reverse proxy

For the included `deploy/nginx.conf`:
- Place it at `/etc/nginx/conf.d/sso-gateway.conf`.
- Edit `server_name` and TLS cert paths.
- `sudo nginx -t && sudo systemctl reload nginx`.

### 8. Verify

```sh
curl -sS -H "X-API-Key: <key>" \
  https://gateway.your-domain.com/api/v1/karyawan?limit=5 | jq .
```

Expected: a JSON object with `data: [...]` containing 5 karyawan rows.

## Day-2 operations

### Update application

```sh
git pull
docker compose -f deploy/docker-compose.prod.yml build
docker compose -f deploy/docker-compose.prod.yml up -d
```

The api replicas restart one at a time; nginx keeps the cluster up.

### Rotate the VPS password

1. Update the password in your VPS MySQL.
2. Re-run setup — the existing master key is reused, only the VPS
   password is re-encrypted:
   ```sh
   docker compose -f deploy/docker-compose.prod.yml run --rm setup
   ```
3. Restart sync so it picks up the new DSN:
   ```sh
   docker compose -f deploy/docker-compose.prod.yml restart sync
   ```

### Rotate the master key

DANGER: this invalidates the encrypted VPS password in config.yaml.

1. Run setup; if the on-disk master key matches the deployed one,
   the encrypted password can be re-read. If you actually want to
   change the master key, manually delete `config.yaml` and `.env`
   in the `gateway-config` volume first, then re-run setup. Existing
   API key hashes in Postgres are NOT affected (they don't depend
   on the master key).

### View logs

```sh
docker compose -f deploy/docker-compose.prod.yml logs -f --tail=100 api
docker compose -f deploy/docker-compose.prod.yml logs -f --tail=100 sync
```

JSON logs are written to `/var/lib/docker/containers/<id>/<id>-json.log`
with rotation managed by Docker (or the log driver options in the
compose file).

### Monitor

See [MONITORING.md](./MONITORING.md) for metrics, alert rules, and
backup verification.

### Restore from backup

```sh
# Find the latest archive
LATEST=$(docker run --rm -v sso_backupdata:/backupdata alpine ls -t /backupdata | head -1)

# Copy it out
docker run --rm -v sso_backupdata:/backupdata -v $PWD:/out alpine \
  cp /backupdata/$LATEST /out/

# Decompress and restore
gunzip -k /out/$LATEST
docker exec -i sso-postgres-1 psql -U sso -d sso < /out/${LATEST%.gz}
```

## Security checklist

- [ ] All secrets files (`deploy/secrets/*`) are mode 0600, owned by root.
- [ ] `deploy/.env` is mode 0600, owned by root.
- [ ] `deploy/keys/jwt-private.pem` is mode 0600, owned by root.
- [ ] No secrets committed to git (`.gitignore` covers `deploy/.env`,
      `deploy/secrets/`, `deploy/keys/*.pem`).
- [ ] nginx `allow` ACLs on `/metrics` match your scraper's IP range.
- [ ] TLS certs auto-renew (certbot renew hook reloads nginx).
- [ ] VPS MySQL user `sso_replicator` has `GRANT SELECT` only.
- [ ] VPS firewall allows gateway host's public IP on port 3306.
- [ ] Postgres port 5432 is NOT published to the host (`expose` only).
- [ ] Redis port 6379 is NOT published to the host.
