# SSO Gateway (Karyawan Data Provider)

Go gateway that mirrors `sja.m_karyawan` from VPS MySQL to local Postgres
and exposes a REST API for downstream apps. App servers still handle
their own auth/login — gateway only provides employee data.

## Architecture

```
App Servers  ──►  Gateway API (X-API-Key)
                       │
                       ├── svc-sync (cron, pulls m_karyawan from VPS)
                       ├── svc-api  (REST endpoints)
                       ├── svc-setup (one-shot CLI, config + keys)
                       ├── Postgres (mirror)
                       └── Redis (rate limit)
```

## Quick Start

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

## Development

```bash
make tidy
make build
make test
make test-integration    # requires docker compose up postgres redis
```

## VPS Prerequisites

- MySQL user `sso_replicator` with `GRANT SELECT ON sja.m_karyawan TO 'sso_replicator'@'%'`
- Tabel `m_karyawan` punya kolom `updated_at` (DATETIME, indexed)
- VPS firewall allow gateway's public IP on port 3306

## File Layout

- `deploy/config.yaml` — VPS credential (password AES-encrypted) + API keys (mounted as `gateway-config` volume)
- `deploy/.env` — `GATEWAY_MASTER_KEY` (decryption key, chmod 600)
- `internal/db/migrations/` — schema migrations
- `internal/crypto/` — AES-256-GCM helper
- `internal/store/` — YAML config loader
