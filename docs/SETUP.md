# First-time deploy wizard

`cmd/setup` is an interactive CLI that walks a new operator through
configuring the gateway from scratch. It is the recommended way to
bring up a fresh deployment.

## TL;DR

```sh
docker compose -f deploy/docker-compose.prod.yml run --rm setup
```

Follow the prompts. The wizard writes encrypted config, runs
migrations, generates an API key, and triggers an initial sync. Total
time: ~2 minutes, no further commands needed.

## What the wizard does

```
┌─ VPS credentials  (asked)
│   host, port, database, username, password
│
├─ API key          (asked; auto-generate if blank)
│
├─ Test connection  (auto)
│   aborts flow on failure so bad creds never reach disk
│
├─ Resolve master key  (auto)
│   - reuse from .env if present
│   - else reuse from config.yaml if present
│   - else generate fresh 32 bytes
│
├─ Encrypt + save    (auto)
│   AES-256-GCM(config.yaml.password, master_key) → write
│   base64(master_key) → write to .env
│
└─ Migrations + key insert + initial sync  (auto)
    runs only if POSTGRES_DSN is set
```

## The prompts

```
? VPS host (e.g. vps.your-domain.com):
? VPS MySQL port:                       [3306]
? Database name:                        [sja]
? Username:                             [sso_replicator]
? Password:                             ****
? API key (leave blank to auto-generate):
```

Defaults in `[brackets]` are pre-filled. Hit Enter to accept.

## How to run

### In a fresh production deploy

`deploy/docker-compose.prod.yml` already wires the `setup` service with
`stdin_open: true`, `tty: true`, and `profiles: ["setup"]`. Run it
after Postgres + Redis are up:

```sh
docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
docker compose -f deploy/docker-compose.prod.yml run --rm setup
docker compose -f deploy/docker-compose.prod.yml up -d
```

The `setup` service is one-shot. It exits after the flow completes;
Docker removes the container because of `--rm`.

### In a dev environment

```sh
# Host-level Go run (no Docker)
go run ./cmd/setup
```

The wizard reads its env from the process; set them inline if needed:

```sh
POSTGRES_DSN="postgres://sso:sso@localhost:5432/sso?sslmode=disable" \
GATEWAY_MIGRATIONS_DIR=./internal/db/migrations \
  go run ./cmd/setup
```

## What you get at the end

The wizard prints a summary like:

```
--- Setup summary ---
VPS host:         vps.your-domain.com
VPS database:     sja
Config written:   /etc/gateway/config.yaml
Env written:      /etc/gateway/.env
Master key:       aBcDeF...32-bytes-base64...
Migrations:       applied
API key:          inserted into api_keys
Initial sync:     1234 rows upserted

API key (save this, shown once):
  ssogw_AbcDefXyZ...
```

**Save the `ssogw_...` key** — it is printed exactly once. Losing it
means re-running setup (which generates a new one and revokes the
old via upsert).

## Failure modes

| Symptom | Cause | Recovery |
|---|---|---|
| `vps connection: ...` | Bad host/port/credentials | Re-run setup; the connection test runs before any FS write, so disk is untouched. |
| Wizard aborts mid-flow | Ctrl+C | Same as above — partial writes are not committed. Re-run cleanly. |
| `POSTGRES_DSN not set; skipping migrations...` | Setup ran without the env | Re-run with `POSTGRES_DSN` set, or run migrations manually: `docker compose -f deploy/docker-compose.prod.yml run --rm setup` (the wizard is idempotent). |
| `migrations: ...` | Migrations dir not mounted | Check `deploy/Dockerfile.setup` copies `internal/db/migrations` and `MIGRATIONS_PATH` env is set. |
| `api key insert: ...` | DB up but `api_keys` table missing | Run migrations first (the wizard does this automatically if DSN is set). |
| `initial sync failed (will be retried by svc-sync): ...` | VPS unreachable **or** `config.yaml` is broken | Re-run setup; svc-sync will retry the next cron tick. |
| `decrypt vps password: ...` at svc-sync startup | Master key mismatch between `deploy/.env` and what was used to encrypt `config.yaml` | The wizard reuses the on-disk master key. If `deploy/.env` was deleted manually, regenerate it via setup. |

## Re-running the wizard

The wizard is **idempotent on the data side** but **stateful on disk**:

- ✅ Re-running on a working stack: the VPS password is re-encrypted
  with the existing master key; the `default` API key is upserted
  (replaced).
- ⚠️  Re-running with a **different** master key (e.g. someone deleted
  `deploy/.env` and setup generated a new one) invalidates the
  encrypted password in `config.yaml`. svc-sync will fail to start.
  Fix: delete `config.yaml` and `.env` first, then re-run.
- ⚠️  Custom operator-added API keys (not the `default` one) are
  preserved across re-runs.

## Rotating the API key (without re-running the full wizard)

If you only need a fresh API key — not a fresh VPS password — generate
one inline and insert it:

```sh
KEY=$(openssl rand -base64 32 | tr -d '=' | tr '/+' '_-')
HASH=$(printf "%s" "$KEY" | openssl dgst -sha256 -hex | awk '{print $2}')

docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c \
  "INSERT INTO api_keys (id, key_hash, description) VALUES ('app-rotated', '$HASH', 'rotated 2026-06-15') ON CONFLICT (id) DO UPDATE SET key_hash = EXCLUDED.key_hash;"
```

Then revoke the old key:

```sql
UPDATE api_keys SET revoked = true WHERE id = 'app-default';
```

## Non-interactive mode (for automation)

If you want to script setup (e.g. in Terraform / Ansible), the wizard
can be driven by piping answers to its stdin:

```sh
printf '%s\n%s\n%s\n%s\n%s\n%s\n' \
  'vps.example.com' \
  '3306' \
  'sja' \
  'sso_replicator' \
  'the_password' \
  '' \
  | docker compose -f deploy/docker-compose.prod.yml run --rm setup
```

The 6th field is the API key; leaving it empty triggers auto-generate.
The output's "API key (save this, shown once):" line is what to
capture — the rest of the flow is fully automatic.

## Where the secrets end up

| File | Permission | Contents |
|---|---|---|
| `deploy/.env` | 0600 | `GATEWAY_MASTER_KEY=<base64>` (32 raw bytes) |
| `deploy/config.yaml` | 0600 | `vps.password_encrypted` (AES-256-GCM ciphertext, base64) |
| Postgres `api_keys` table | n/a | SHA-256 hex of the plaintext API key |
| Container stdout | 0600 (only if you redirect) | The plaintext API key, exactly once |

`deploy/.env` is git-ignored. `deploy/config.yaml` is git-ignored.
Neither file is ever written to a Docker image layer — both live in
the `gateway-config` named volume and are mounted into api + sync
containers as read-only.

## Verifying a successful setup

```sh
# 1. Postgres has the schema
docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c "\dt"

# 2. The API key is in the table (hash, not plaintext)
docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c \
  "SELECT id, key_hash, revoked FROM api_keys;"

# 3. Mirror is populated
docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c \
  "SELECT COUNT(*) FROM karyawan;"

# 4. API responds
KEY=ssogw_...   # from the wizard output
curl -H "X-API-Key: $KEY" \
  https://gateway.your-domain.com/api/v1/karyawan?limit=5
```

If all four checks pass, the gateway is production-ready.
