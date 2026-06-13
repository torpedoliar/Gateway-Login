# SSO Gateway (Karyawan)

Go gateway that mirrors `sja.m_karyawan` from VPS MySQL to local Postgres
and exposes a REST API for downstream apps.

## Quick start

```bash
cp .env.example .env
make docker-up      # start postgres + redis
make setup          # interactive CLI: input VPS credential, generate keys
make docker-up      # restart with config in place
curl -H 'X-API-Key: <key>' http://localhost:8080/api/v1/karyawan
```

## API

| Endpoint                            | Auth         |
|-------------------------------------|--------------|
| `GET /api/v1/karyawan`              | `X-API-Key`  |
| `GET /api/v1/karyawan/{nik_hris}`   | `X-API-Key`  |
| `GET /healthz`                      | none         |
| `GET /metrics`                      | none         |
