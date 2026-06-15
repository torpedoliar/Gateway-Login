# Gateway Login

A Go-based gateway that mirrors employee data (`sja.m_karyawan`) from a
remote VPS MySQL to a local Postgres, then exposes a REST API for
downstream application servers. App servers stop depending on VPS
availability for employee data, and the gateway enforces API-key auth
and per-key rate limits centrally.

## Why this exists

- **Single source of truth** stays on the VPS. The gateway is read-only
  against it.
- **App servers** (.NET, Next.js, Node.js + Prisma, legacy PHP, etc.) no
  longer connect to MySQL directly. They call a stable HTTP API.
- **Auth/login** remains in each application. The gateway only provides
  data — it is not an SSO/identity provider.
- **Reliability**: when the VPS goes down (the original motivation for
  this project), the gateway keeps serving from its local mirror.

## Architecture

```
                          ┌────────────────────────────┐
   App Server 1  ───►      │  Gateway API (Go, chi)    │  ◄── X-API-Key
   App Server 2  ───►      │  GET /api/v1/karyawan     │
   App Server 3  ───►      │  GET /api/v1/karyawan/:nik│
                          └─────────┬──────────────────┘
                                    │
                                    │ pull (MySQL wire, every 5 min)
                                    │ SELECT ... FROM sja.m_karyawan
                                    │ WHERE updated_at > :watermark
                                    ▼
                          ┌────────────────────────────┐
                          │  VPS MySQL                 │
                          │  sja.m_karyawan (read-only)│
                          └────────────────────────────┘

  Sidecars:  svc-sync (cron), svc-setup (one-shot CLI)
  Storage:   Postgres (mirror), Redis (rate limit + cache)
```

## Features

- ✅ **MySQL-to-Postgres sync** with watermark-based incremental pull
- ✅ **AES-256-GCM at rest** for the VPS password in `config.yaml`
- ✅ **API-key auth** (SHA-256 hashed, opaque plaintext to caller)
- ✅ **Per-key rate limiting** (atomic Lua, Redis-backed)
- ✅ **Multi-resource advisory locks** for safe parallel-sync safety
- ✅ **Crash recovery** for in-flight syncs (no stuck `running` rows)
- ✅ **Prometheus metrics** + alert-friendly counters
- ✅ **Daily backups** with retention + optional S3
- ✅ **Container-first** deployment (dev + production compose files)
- ✅ **Compatible** with .NET, Next.js, Node.js, Prisma, legacy PHP —
  anything that can speak HTTPS

## Quick start (development)

```bash
# 1. Copy env template
cp .env.example .env

# 2. Start Postgres + Redis
make docker-up

# 3. Run setup CLI (interactive: VPS host, port, db, user, password)
docker compose -f deploy/docker-compose.yml run --rm setup
# Note the API key printed at the end.

# 4. Start api + sync services
docker compose -f deploy/docker-compose.yml up -d api sync

# 5. Test
curl -H "X-API-Key: <key>" http://localhost:8080/api/v1/karyawan?limit=5
curl -H "X-API-Key: <key>" http://localhost:8080/api/v1/karyawan/EMP001
```

## Production deploy

See [deploy/README.md](./deploy/README.md) for the full guide. TL;DR:

```sh
./deploy/bootstrap.sh              # generate secrets + JWT keys
# edit deploy/.env: VPS_MYSQL_DSN, JWT_ISSUER
docker compose -f deploy/docker-compose.prod.yml build
docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
docker compose -f deploy/docker-compose.prod.yml run --rm setup   # save the printed API key
docker compose -f deploy/docker-compose.prod.yml up -d
```

Production features:
- Secrets mounted as files (`/run/secrets/*`), never in env
- Resource limits (CPU + memory) on every service
- Healthchecks on every long-running service
- `no-new-privileges` security options
- Log rotation (json-file, 50MB × 10)
- Daily `pg_dump` with 14-day retention, optional S3
- See [deploy/MONITORING.md](./deploy/MONITORING.md) for Prometheus alerts

## API

| Endpoint                              | Auth         | Description                  |
|---------------------------------------|--------------|------------------------------|
| `GET /api/v1/karyawan`                | `X-API-Key`  | List + filter + paginate     |
| `GET /api/v1/karyawan/{nik_hris}`     | `X-API-Key`  | Single record by NIK         |
| `GET /healthz`                        | none         | Liveness                     |
| `GET /metrics`                        | none         | Prometheus metrics           |

### Query parameters for `GET /api/v1/karyawan`

| Param          | Description                                |
|----------------|--------------------------------------------|
| `nik_hris`     | Exact match                                |
| `nik_santos`   | Exact match                                |
| `nama_karyawan`| ILIKE %x%                                  |
| `departemen`   | ILIKE %x% (NAMA_DEPARTEMEN)                |
| `jabatan`      | ILIKE %x% (NAMA_JABATAN)                   |
| `lokasi`       | Exact match                                |
| `status_aktif` | `true` = TGL_KELUAR IS NULL, `false` = not |
| `limit`        | Default 50, max 500                        |
| `offset`       | Default 0                                  |

### Response

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

### Source schema (VPS MySQL `sja.m_karyawan`)

The sync reads 9 fixed columns:

| Column            | Notes                                       |
|-------------------|---------------------------------------------|
| `NIK_HRIS`        | Primary key (text)                          |
| `NIK_SANTOS`      | Secondary identifier (text, nullable)       |
| `NAMA_KARYAWAN`   | Full name                                   |
| `NAMA_DEPARTEMEN` | Department                                  |
| `NAMA_JABATAN`    | Position                                    |
| `TGL_BERGABUNG`   | Date (join date)                            |
| `TGL_KELUAR`      | Date (leave date, NULL = active)            |
| `LOKASI`          | Work location                               |
| `GENDER`          | "L" / "P"                                   |

Plus `updated_at` (DATETIME, indexed) for watermark-based sync.

## App integration

**Node.js / Next.js** — use OIDC discovery or direct REST:
```ts
const r = await fetch('https://gateway/api/v1/karyawan?nik_hris=EMP001', {
  headers: { 'X-API-Key': process.env.GATEWAY_API_KEY }
});
const { data } = await r.json();
```

**.NET**:
```csharp
var client = new HttpClient();
client.DefaultRequestHeaders.Add("X-API-Key", apiKey);
var resp = await client.GetAsync($"{gatewayUrl}/api/v1/karyawan?nik_hris=EMP001");
```

**Legacy / PHP / curl**:
```sh
curl -H "X-API-Key: $KEY" "$GATEWAY/api/v1/karyawan?limit=10"
```

## VPS prerequisites

1. MySQL user `sso_replicator` exists with grant:
   ```sql
   GRANT SELECT ON sja.m_karyawan TO 'sso_replicator'@'%';
   FLUSH PRIVILEGES;
   ```
2. Tabel `m_karyawan` has `updated_at` column (DATETIME, indexed). If
   missing:
   ```sql
   ALTER TABLE sja.m_karyawan
     ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
     ON UPDATE CURRENT_TIMESTAMP,
     ADD INDEX idx_updated_at (updated_at);
   ```
3. VPS firewall allows the gateway host's public IP on port 3306.

## Development

```bash
make tidy               # go mod tidy
make build              # build api, sync, setup
make test               # unit tests
make test-integration   # needs docker compose up postgres redis
```

Run a single service locally without Docker:
```bash
export POSTGRES_DSN="postgres://sso:sso@localhost:5432/sso?sslmode=disable"
export REDIS_ADDR="localhost:6379"
export GATEWAY_MASTER_KEY="$(openssl rand -base64 32)"
go run ./cmd/api
```

## Architecture details

- **Tech stack:** Go 1.22, chi router, pgx/v5, go-sql-driver/mysql,
  go-redis/v9, golang-migrate, robfig/cron, viper, zerolog, AES-256-GCM
  via stdlib.
- **Three binaries:**
  - `cmd/api` — HTTP API (X-API-Key auth, rate limit, chi router)
  - `cmd/sync` — cron-driven incremental sync, advisory-locked
  - `cmd/setup` — one-shot interactive CLI (VPS creds, encrypted config,
    initial sync)
- **Sync safety:** `pg_advisory_xact_lock` serializes concurrent syncs
  of the same resource. Stale `running` rows from a crashed syncer are
  recovered on next startup.
- **Schema mirror:** `karyawan` table in Postgres. Audit log in
  `sync_runs`. Watermark in `sync_state`. API keys in `api_keys`.
- **Code layout:** see `cmd/`, `internal/`, `deploy/`, `tests/`.

See [docs/superpowers/specs/](./docs/superpowers/specs/) for the full
design document and [docs/superpowers/plans/](./docs/superpowers/plans/)
for the implementation plan.

## Project status

- ✅ All planned features shipped
- ✅ 22 bugs identified in self-review fixed (commits visible in
  `git log`)
- ✅ Production deploy story in [deploy/README.md](./deploy/README.md)
- ✅ Monitoring + alerting story in [deploy/MONITORING.md](./deploy/MONITORING.md)

## License

MIT (or your preferred permissive license — add `LICENSE` file).
