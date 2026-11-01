# Gateway Login

**English** · [Bahasa Indonesia](README.id.md)

Gateway Login is a Go service that keeps a local copy of the employee master table (`sja.m_karyawan`) from a remote MySQL server and serves it to internal applications over a small REST API. Applications read employee data from the gateway instead of connecting to the VPS database directly, so they keep working when the VPS is slow or unreachable.

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-mirror-4169E1?logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-rate%20limit-DC382D?logo=redis&logoColor=white)
![Docker Compose](https://img.shields.io/badge/Docker%20Compose-ready-2496ED?logo=docker&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)

## Contents

- [Why it exists](#why-it-exists)
- [Architecture](#architecture)
- [Features](#features)
- [Requirements](#requirements)
- [Quick start (development)](#quick-start-development)
- [Production deployment](#production-deployment)
- [Configuration](#configuration)
- [API reference](#api-reference)
- [Integrating an application](#integrating-an-application)
- [Development](#development)
- [Project layout](#project-layout)
- [License](#license)

## Why it exists

The VPS database stays the single source of truth, and the gateway only reads from it. Each application server (.NET, Next.js, Node.js with Prisma, legacy PHP) calls one stable HTTP endpoint instead of holding its own MySQL credentials. Login and session handling remain inside each application; the gateway supplies data and is not an identity provider.

The project started after a VPS outage took employee lookups down across several applications. With a local mirror, a VPS outage now only delays new changes until the next successful sync.

## Architecture

```
                         ┌─────────────────────────────┐
   App server 1  ───►    │  Gateway API (Go, chi)      │  ◄── X-API-Key
   App server 2  ───►    │  GET /api/v1/karyawan       │
   App server 3  ───►    │  GET /api/v1/karyawan/{nik} │
                         └──────────────┬──────────────┘
                                        │ reads local mirror
                                        ▼
                         ┌─────────────────────────────┐
                         │  PostgreSQL (mirror)        │
                         │  Redis (rate limit, cache)  │
                         └──────────────▲──────────────┘
                                        │ incremental pull every SYNC_INTERVAL
                                        │ WHERE updated_at > :watermark
                         ┌──────────────┴──────────────┐
                         │  VPS MySQL                  │
                         │  sja.m_karyawan (read-only) │
                         └─────────────────────────────┘
```

The repository builds three binaries:

| Binary | Role |
|---|---|
| `cmd/api` | HTTP API with API-key authentication and per-key rate limiting |
| `cmd/sync` | Cron-driven incremental sync from MySQL to PostgreSQL |
| `cmd/setup` | One-shot setup: VPS credentials, encrypted config, migrations, first API key, initial sync |

## Features

- Incremental MySQL-to-PostgreSQL sync driven by an `updated_at` watermark, with a full audit trail in `sync_runs`.
- The VPS password is stored in `config.yaml` encrypted with AES-256-GCM. The master key lives in the environment or in a Docker secret.
- API keys are stored as SHA-256 hashes. The plaintext key is shown once, at creation time.
- Per-key rate limiting in Redis uses an atomic Lua script (default 300 requests per minute).
- `pg_advisory_xact_lock` keeps two sync runs from touching the same resource at once. On startup, the sync service marks runs left in `running` by a crashed process as failed.
- Prometheus metrics on `/metrics` and a liveness probe on `/healthz`.
- Daily `pg_dump` backups with 14-day retention and optional upload to S3.
- Docker Compose files for development and production, plus a staged PowerShell deployer for Windows hosts.

## Requirements

- Docker with the Compose plugin (development and production).
- Go 1.25 if you want to build or run the binaries outside Docker.
- Network access from the gateway host to the VPS MySQL server on port 3306.
- On the VPS:
  1. A read-only MySQL user:
     ```sql
     GRANT SELECT ON sja.m_karyawan TO 'sso_replicator'@'%';
     FLUSH PRIVILEGES;
     ```
  2. An indexed `updated_at` column on `m_karyawan`. If it is missing:
     ```sql
     ALTER TABLE sja.m_karyawan
       ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
       ON UPDATE CURRENT_TIMESTAMP,
       ADD INDEX idx_updated_at (updated_at);
     ```
  3. A firewall rule that allows the gateway's public IP on port 3306.

## Quick start (development)

```bash
# 1. Copy the environment template
cp .env.example .env

# 2. Build and start PostgreSQL and Redis
make docker-up

# 3. Run the setup wizard (VPS host, port, database, user, password).
#    Save the API key printed at the end; it is shown only once.
docker compose -f deploy/docker-compose.yml run --rm setup

# 4. Start the API and sync services
docker compose -f deploy/docker-compose.yml up -d api sync

# 5. Try the API
curl -H "X-API-Key: <key>" "http://localhost:8080/api/v1/karyawan?limit=5"
curl -H "X-API-Key: <key>" "http://localhost:8080/api/v1/karyawan/EMP001"
```

The setup wizard runs non-interactively when `SETUP_HOST` and the related `SETUP_*` variables are set, which is how `deploy/deploy.ps1` drives it.

## Production deployment

The full guide is in [deploy/README.md](deploy/README.md). The short version on Linux:

```sh
./deploy/bootstrap.sh       # generates secrets under deploy/secrets and a deploy/.env template
# edit deploy/.env and set VPS_MYSQL_DSN
docker compose -f deploy/docker-compose.prod.yml build
docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
docker compose -f deploy/docker-compose.prod.yml run --rm setup   # save the printed API key
docker compose -f deploy/docker-compose.prod.yml up -d
```

On Windows with Docker Desktop, `deploy\deploy.ps1` runs the same flow in stages (Preflight, Secrets, Keys, Env, Build, InfraUp, Setup, StackUp, Verify). Run a single stage with `-Stage <name>`, or every stage again with `-Force`. `deploy/deploy-ssh.ps1` runs the deployment on a remote host over SSH.

The production compose file:

- mounts secrets as files under `/run/secrets/` instead of passing them as environment variables;
- sets CPU and memory limits, health checks, and `no-new-privileges` on every service;
- rotates container logs (json-file, 50 MB × 10);
- runs the backup container with 14-day retention.

`bootstrap.sh` also generates an RS256 key pair and a `JWT_ISSUER` entry. The current API authenticates with API keys only, so these are not used yet.

### Updating

`update.sh` (Linux) and `update.ps1` (Windows) back up PostgreSQL, pull the latest code, rebuild the images before stopping the running stack, recreate the containers, run migrations, mark stale sync runs as failed, check `/healthz`, and prune old backups. A failed build leaves the running stack untouched.

### Monitoring

[deploy/MONITORING.md](deploy/MONITORING.md) lists the exported metrics and suggested Prometheus alert rules.

## Configuration

Main variables from `.env.example`:

| Variable | Default | Description |
|---|---|---|
| `GATEWAY_HTTP_ADDR` | `:8080` | API listen address |
| `POSTGRES_DSN` | `postgres://sso:sso@postgres:5432/sso?sslmode=disable` | Local mirror database |
| `REDIS_ADDR` | `redis:6379` | Redis for rate limiting and cache |
| `REDIS_PASSWORD`, `REDIS_DB` | empty, `0` | Redis credentials and database index |
| `GATEWAY_MASTER_KEY` | generated by setup | 32 random bytes, base64. Encrypts the VPS password at rest |
| `SYNC_INTERVAL` | `5m` | Sync schedule (`@every <interval>`) |
| `SYNC_BATCH_SIZE` | `500` | Rows per sync batch |
| `API_RATE_LIMIT_PER_MIN` | `300` | Requests per minute per API key |
| `LOG_LEVEL` | `info` | zerolog level |

Setup can relocate its files with `GATEWAY_CONFIG_PATH`, `GATEWAY_DOTENV_PATH`, and `GATEWAY_MIGRATIONS_DIR`.

## API reference

| Endpoint | Auth | Description |
|---|---|---|
| `GET /api/v1/karyawan` | `X-API-Key` | List with filters and pagination |
| `GET /api/v1/karyawan/{nik_hris}` | `X-API-Key` | One employee by NIK HRIS |
| `GET /healthz` | none | Liveness |
| `GET /metrics` | none | Prometheus metrics |

Query parameters for `GET /api/v1/karyawan`:

| Parameter | Match |
|---|---|
| `nik_hris` | exact |
| `nik_santos` | exact |
| `nama_karyawan` | case-insensitive contains |
| `departemen` | case-insensitive contains on `NAMA_DEPARTEMEN` |
| `jabatan` | case-insensitive contains on `NAMA_JABATAN` |
| `lokasi` | exact |
| `status_aktif` | `true` means `TGL_KELUAR IS NULL`, `false` means the opposite |
| `limit` | default 50, maximum 500 |
| `offset` | default 0 |

Example response:

```json
{
  "data": [
    {
      "nik_hris": "EMP001",
      "nik_santos": "SNT-001",
      "nama_karyawan": "Andi",
      "nama_departemen": "IT",
      "nama_jabatan": "Developer",
      "tgl_bergabung": "2020-01-15",
      "tgl_keluar": null,
      "lokasi": "Jakarta",
      "gender": "L"
    }
  ],
  "total": 1234,
  "limit": 50,
  "offset": 0
}
```

The sync reads nine columns from `sja.m_karyawan`: `NIK_HRIS` (primary key), `NIK_SANTOS`, `NAMA_KARYAWAN`, `NAMA_DEPARTEMEN`, `NAMA_JABATAN`, `TGL_BERGABUNG`, `TGL_KELUAR` (NULL means active), `LOKASI`, and `GENDER` (`L`/`P`), plus `updated_at` for the watermark. More detail is in [docs/API.md](docs/API.md).

## Integrating an application

Node.js or Next.js:

```ts
const r = await fetch(`${process.env.GATEWAY_URL}/api/v1/karyawan?nik_hris=EMP001`, {
  headers: { 'X-API-Key': process.env.GATEWAY_API_KEY! },
});
const { data } = await r.json();
```

.NET:

```csharp
var client = new HttpClient();
client.DefaultRequestHeaders.Add("X-API-Key", apiKey);
var resp = await client.GetAsync($"{gatewayUrl}/api/v1/karyawan?nik_hris=EMP001");
```

PHP, shell, or anything else that speaks HTTP:

```sh
curl -H "X-API-Key: $KEY" "$GATEWAY/api/v1/karyawan?limit=10"
```

Give each application its own API key so rate limits and revocation apply per application.

## Development

```bash
make tidy               # go mod tidy
make build              # builds bin/api, bin/sync, bin/setup
make test               # unit tests (go test ./... -short)
make test-integration   # needs PostgreSQL and Redis from docker compose
```

Run the API without Docker:

```bash
export POSTGRES_DSN="postgres://sso:sso@localhost:5432/sso?sslmode=disable"
export REDIS_ADDR="localhost:6379"
export GATEWAY_MASTER_KEY="$(openssl rand -base64 32)"
go run ./cmd/api
```

Stack: Go 1.25, chi, pgx/v5, go-sql-driver/mysql, go-redis/v9, golang-migrate, robfig/cron, viper, zerolog, and AES-256-GCM from the standard library.

## Project layout

```
cmd/            api, sync, setup entry points
internal/
  api/          handlers and middleware (auth, rate limit)
  apikey/       API key store (SHA-256 hashes)
  crypto/       AES-256-GCM helpers
  db/           PostgreSQL pool and migrations
  karyawan/     employee model and repository
  redisx/       Redis client and Lua rate limiter
  sync/         incremental sync and advisory locking
  vpsmysql/     MySQL source client
  setup/, store/, config/, server/, logger/, bootstrap/
deploy/         compose files, Dockerfiles, nginx.conf, deploy scripts, backup
docs/           API.md, SETUP.md, design specs and plans
tests/          end-to-end integration test
```

PostgreSQL tables: `karyawan` (mirror), `sync_runs` (audit log), `sync_state` (watermark), and `api_keys`. The design document and implementation plans are under [docs/superpowers/](docs/superpowers/).

## License

Released under the [MIT License](LICENSE).

Developed by Yohanes Octavian Rizky ([@torpedoliar](https://github.com/torpedoliar)).
