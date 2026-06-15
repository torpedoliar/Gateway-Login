# API Access Documentation

How to call the Gateway Login REST API from your application.

## Table of contents

- [Quick start](#quick-start)
- [Authentication](#authentication)
- [Endpoints](#endpoints)
- [Filtering and pagination](#filtering-and-pagination)
- [Response format](#response-format)
- [Error responses](#error-responses)
- [Rate limiting](#rate-limiting)
- [Examples by language](#examples-by-language)
  - [curl](#curl)
  - [Node.js / Next.js](#nodejs--nextjs)
  - [.NET](#net)
  - [Python](#python)
  - [PHP](#php)
  - [Go](#go)
- [Troubleshooting](#troubleshooting)

## Quick start

```sh
# After setup, you have an API key (printed once at the end of `setup`).
# Test it:
curl -H "X-API-Key: YOUR_KEY" \
  https://gateway.your-domain.com/api/v1/karyawan?limit=5
```

Expected response (truncated):
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
  "limit": 5,
  "offset": 0
}
```

## Authentication

Every API request **must** include an `X-API-Key` header. Keys are issued
by the `setup` CLI and stored as SHA-256 hashes in the `api_keys`
table.

```http
GET /api/v1/karyawan HTTP/1.1
Host: gateway.your-domain.com
X-API-Key: ssogw_Abc123XyZ...
```

**Storage in your app:**
- Keep the API key in an env var or secrets manager. Never commit it.
- Do not log the key.
- Rotate by issuing a new one in setup and revoking the old (see
  [Operations](#operations) below).

**Missing or invalid key:**

```http
HTTP/1.1 401 Unauthorized
content-type: application/json

{"error": "missing_api_key"}
```
or
```http
{"error": "invalid_api_key"}
```

`/healthz` and `/metrics` are intentionally unauthenticated (rate
limits on `/metrics` are enforced at the nginx layer — see
`deploy/nginx.conf`).

## Endpoints

### `GET /api/v1/karyawan`

List karyawan with optional filtering and pagination.

**Query parameters** (all optional):

| Name            | Type    | Description                                                     |
|-----------------|---------|-----------------------------------------------------------------|
| `nik_hris`      | string  | Exact match on NIK HRIS                                         |
| `nik_santos`    | string  | Exact match on NIK SANTOS                                       |
| `nama_karyawan` | string  | Case-insensitive substring (ILIKE %x%)                         |
| `departemen`    | string  | Case-insensitive substring on NAMA_DEPARTEMEN                   |
| `jabatan`       | string  | Case-insensitive substring on NAMA_JABATAN                      |
| `lokasi`        | string  | Exact match                                                     |
| `status_aktif`  | bool    | `true` = `tgl_keluar IS NULL` (active), `false` = not active    |
| `limit`         | int     | Page size. Default `50`, max `500`                              |
| `offset`        | int     | Skip rows. Default `0`                                          |

**Response 200** (see [Response format](#response-format)).

### `GET /api/v1/karyawan/{nik_hris}`

Fetch a single record by NIK HRIS.

```sh
curl -H "X-API-Key: $KEY" \
  https://gateway/api/v1/karyawan/EMP001
```

**Response 200** (single object, same fields as the list response).
**Response 404** when the NIK is not in the local mirror:
```json
{"error": "not_found"}
```

### `GET /healthz`

Liveness probe. Returns `200 OK` with body `ok`. Does not require
authentication.

### `GET /metrics`

Prometheus metrics. Includes:
- `ratelimit_redis_errors_total{op}` — rate-limit Redis failures
- `ratelimit_denied_total` — denied requests
- Standard Go runtime metrics from `promhttp`

Restrict access at the nginx layer in production (the bundled
`deploy/nginx.conf` allows only `10.0.0.0/8` and `127.0.0.1`).

## Filtering and pagination

Filters compose with AND. To find all active IT staff in Jakarta:

```sh
curl -H "X-API-Key: $KEY" \
  "https://gateway/api/v1/karyawan?departemen=IT&lokasi=Jakarta&status_aktif=true&limit=100"
```

Pagination via `limit` + `offset`:

```sh
# Page 1 (rows 0-49)
curl "...&limit=50&offset=0"
# Page 2 (rows 50-99)
curl "...&limit=50&offset=50"
```

The response's `total` field gives the unfiltered-by-limit count, so
clients can compute `pages = ceil(total / limit)`.

## Response format

### List (`GET /api/v1/karyawan`)

```json
{
  "data": [ /* karyawan objects */ ],
  "total": 1234,
  "limit": 50,
  "offset": 0
}
```

`karyawan` object fields:

| Field             | Type           | Notes                                     |
|-------------------|----------------|-------------------------------------------|
| `nik_hris`        | string         | Primary identifier                        |
| `nik_santos`      | string         | Secondary, may be empty                   |
| `nama_karyawan`   | string         | Full name                                 |
| `nama_departemen` | string         | Department                                |
| `nama_jabatan`    | string         | Position                                  |
| `tgl_bergabung`   | string \| null | `YYYY-MM-DD`                              |
| `tgl_keluar`      | string \| null | `YYYY-MM-DD`, `null` = still active      |
| `lokasi`          | string         | Work location                             |
| `gender`          | string         | `L` / `P`                                |

### Single (`GET /api/v1/karyawan/{nik}`)

```json
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
```

## Error responses

All errors come back as JSON with an `error` field. Status codes used:

| Status | `error` code                  | When                                                       |
|--------|-------------------------------|------------------------------------------------------------|
| 400    | `invalid_body`                | Malformed JSON request body                                |
| 400    | `missing_nik`                 | Empty `nik_hris` path param                                |
| 401    | `missing_api_key`             | No `X-API-Key` header                                      |
| 401    | `invalid_api_key`             | Key not in DB, or revoked                                  |
| 503    | `auth_backend_unavailable`    | Postgres unreachable during key lookup                      |
| 404    | `not_found`                   | NIK not in local mirror                                    |
| 429    | `rate_limited`                | Per-key rate limit exceeded (default 300/min)              |
| 500    | `query_error`                 | Postgres error during the query                            |

Network/transport failures are *not* mapped to a gateway status — the
caller's HTTP client surfaces them as connection errors.

## Rate limiting

Each API key has a default budget of **300 requests/minute** (overridable
via `API_RATE_LIMIT_PER_MIN` env). The limiter is Redis-backed, atomic
(Lua INCR+PEXPIRE), and fixed-window per UTC second.

When exceeded:
```http
HTTP/1.1 429 Too Many Requests
{"error": "rate_limited"}
```

Monitor `ratelimit_redis_errors_total` and `ratelimit_denied_total` on
`/metrics` to detect:
- Client misbehavior (high `denied` rate)
- Redis outage (high `redis_errors` rate → rate limiter is silently
  failing open)

## Examples by language

### curl

```sh
# List active IT staff, page 1
curl -sS -H "X-API-Key: $GATEWAY_API_KEY" \
  "$GATEWAY_URL/api/v1/karyawan?departemen=IT&status_aktif=true&limit=50"

# Search by name (substring)
curl -sS -H "X-API-Key: $GATEWAY_API_KEY" \
  "$GATEWAY_URL/api/v1/karyawan?nama_karyawan=andi"

# Single record
curl -sS -H "X-API-Key: $GATEWAY_API_KEY" \
  "$GATEWAY_URL/api/v1/karyawan/EMP001"
```

### Node.js / Next.js

```ts
// lib/gateway.ts
const BASE = process.env.GATEWAY_URL!;
const KEY  = process.env.GATEWAY_API_KEY!;

export async function getKaryawanByNIK(nik: string) {
  const r = await fetch(`${BASE}/api/v1/karyawan/${encodeURIComponent(nik)}`, {
    headers: { "X-API-Key": KEY },
    cache: "no-store",
  });
  if (r.status === 404) return null;
  if (!r.ok) throw new Error(`gateway ${r.status}: ${await r.text()}`);
  return r.json();
}

export async function listKaryawan(params: Record<string, string|number|boolean> = {}) {
  const q = new URLSearchParams(
    Object.entries(params).map(([k, v]) => [k, String(v)])
  );
  const r = await fetch(`${BASE}/api/v1/karyawan?${q}`, {
    headers: { "X-API-Key": KEY },
  });
  if (!r.ok) throw new Error(`gateway ${r.status}: ${await r.text()}`);
  return r.json() as Promise<{
    data: Karyawan[]; total: number; limit: number; offset: number;
  }>;
}
```

Next.js API route using it:

```ts
// app/api/karyawan/[nik]/route.ts
import { getKaryawanByNIK } from "@/lib/gateway";
export async function GET(_req: Request, { params }: { params: { nik: string } }) {
  const k = await getKaryawanByNIK(params.nik);
  if (!k) return Response.json({ error: "not_found" }, { status: 404 });
  return Response.json(k);
}
```

### .NET

```csharp
// GatewayClient.cs
public class GatewayClient
{
    private readonly HttpClient _http;
    public GatewayClient(IHttpClientFactory f, IConfiguration cfg)
    {
        _http = f.CreateClient();
        _http.DefaultRequestHeaders.Add("X-API-Key", cfg["Gateway:ApiKey"]);
        _http.BaseAddress = new Uri(cfg["Gateway:Url"]);
    }

    public async Task<Karyawan?> GetByNIKAsync(string nik, CancellationToken ct = default)
    {
        var r = await _http.GetAsync($"/api/v1/karyawan/{Uri.EscapeDataString(nik)}", ct);
        if (r.StatusCode == HttpStatusCode.NotFound) return null;
        r.EnsureSuccessStatusCode();
        return await r.Content.ReadFromJsonAsync<Karyawan>(cancellationToken: ct);
    }
}
```

ASP.NET Core registration:

```csharp
// Program.cs
builder.Services.AddHttpClient<GatewayClient>();
builder.Services.Configure<GatewayOptions>(builder.Configuration.GetSection("Gateway"));
```

### Python

```python
import os, requests

BASE = os.environ["GATEWAY_URL"]
KEY  = os.environ["GATEWAY_API_KEY"]
S    = requests.Session()
S.headers["X-API-Key"] = KEY

def get_karyawan_by_nik(nik: str) -> dict | None:
    r = S.get(f"{BASE}/api/v1/karyawan/{nik}", timeout=10)
    if r.status_code == 404:
        return None
    r.raise_for_status()
    return r.json()

def list_karyawan(departemen: str | None = None, status_aktif: bool | None = None,
                  limit: int = 50, offset: int = 0) -> dict:
    params = {"limit": limit, "offset": offset}
    if departemen:   params["departemen"]   = departemen
    if status_aktif is not None: params["status_aktif"] = "true" if status_aktif else "false"
    r = S.get(f"{BASE}/api/v1/karyawan", params=params, timeout=10)
    r.raise_for_status()
    return r.json()
```

### PHP

```php
<?php
$base = getenv("GATEWAY_URL");
$key  = getenv("GATEWAY_API_KEY");

function gateway_get(string $path, array $query = []): array {
    global $base, $key;
    $url = $base . $path . ($query ? "?" . http_build_query($query) : "");
    $ctx = stream_context_create([
        "http" => [
            "header" => "X-API-Key: $key\r\n",
            "ignore_errors" => true,
            "timeout" => 10,
        ],
    ]);
    $body = @file_get_contents($url, false, $ctx);
    if ($body === false) {
        throw new RuntimeException("gateway unreachable");
    }
    $status = ...; // parse $http_response_header[0]
    if ($status === 404) return [];
    if ($status >= 400) throw new RuntimeException("gateway $status: $body");
    return json_decode($body, true);
}
```

### Go

```go
// gateway/client.go
package gateway

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "net/url"
    "strconv"
    "strings"
    "time"
)

type Client struct {
    base string
    key  string
    hc   *http.Client
}

func New(base, key string) *Client {
    return &Client{base: strings.TrimRight(base, "/"), key: key,
        hc: &http.Client{Timeout: 10 * time.Second}}
}

type Karyawan struct {
    NIKHRIS, NIKSantos, NamaKaryawan, NamaDepartemen, NamaJabatan string
    TglBergabung, TglKeluar                                          *string
    Lokasi, Gender                                                  string
}

type ListResult struct {
    Data   []Karyawan `json:"data"`
    Total  int        `json:"total"`
    Limit  int        `json:"limit"`
    Offset int        `json:"offset"`
}

func (c *Client) GetByNIK(ctx context.Context, nik string) (*Karyawan, error) {
    r, err := c.do(ctx, "/api/v1/karyawan/"+url.PathEscape(nik), nil)
    if err != nil { return nil, err }
    if r.StatusCode == http.StatusNotFound { return nil, nil }
    if r.StatusCode != http.StatusOK { return nil, fmt.Errorf("status %d", r.StatusCode) }
    var k Karyawan
    if err := json.NewDecoder(r.Body).Decode(&k); err != nil { return nil, err }
    return &k, nil
}

func (c *Client) List(ctx context.Context, params map[string]string) (*ListResult, error) {
    r, err := c.do(ctx, "/api/v1/karyawan", params)
    if err != nil { return nil, err }
    if r.StatusCode != http.StatusOK { return nil, fmt.Errorf("status %d", r.StatusCode) }
    var out ListResult
    if err := json.NewDecoder(r.Body).Decode(&out); err != nil { return nil, err }
    return &out, nil
}

func (c *Client) do(ctx context.Context, path string, params map[string]string) (*http.Response, error) {
    u := c.base + path
    if len(params) > 0 {
        v := url.Values{}
        for k, vv := range params { v.Set(k, vv) }
        u += "?" + v.Encode()
    }
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
    req.Header.Set("X-API-Key", c.key)
    return c.hc.Do(req)
}
```

## Operations

### Rotate an API key

```sh
# 1. Generate a new key (or pass it as the answer to the prompt)
docker compose -f deploy/docker-compose.prod.yml run --rm setup
# The new key is inserted on conflict (idempotent). Save the printed
# plaintext — it is shown only once.

# 2. Update the app config and restart the app
# 3. Revoke the old key (so it can no longer be used):
docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c "UPDATE api_keys SET revoked = true WHERE id = 'app-default';"
```

### Add a second app's key (without re-running setup)

Generate a key locally with a one-liner, hash it, insert directly:

```sh
KEY=$(openssl rand -base64 32)
HASH=$(printf "%s" "$KEY" | openssl dgst -sha256 -hex | awk '{print $2}')
ID="app-server-2"
docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c \
  "INSERT INTO api_keys (id, key_hash, description) VALUES ('$ID', '$HASH', 'second app');"
echo "Key: $KEY"   # show once, save it
```

### Audit which keys are in use

```sql
SELECT id, description, created_at, last_used_at, revoked
FROM api_keys
ORDER BY created_at;
```

## Troubleshooting

### `401 invalid_api_key` but the key is right

The key is not in the `api_keys` table, or its hash doesn't match. Check:

```sh
docker compose -f deploy/docker-compose.prod.yml exec postgres \
  psql -U sso -d sso -c \
  "SELECT id, encode(key_hash, 'hex'), revoked FROM api_keys;"
```

The hash you store is `sha256(plaintext)` in hex. The middleware does
the same with the header value and looks up an exact match where
`revoked = false`.

### `503 auth_backend_unavailable`

Postgres is down or unreachable from the api container. Check
`docker compose logs api` and verify the `POSTGRES_DSN` matches what's
in the `postgres` service.

### `429 rate_limited`

You exceeded 300 req/min for this key. Either:
- Reduce request rate in your app (paginate!),
- Use a per-key higher limit (set `API_RATE_LIMIT_PER_MIN` higher per
  key, or talk to ops about splitting traffic across multiple keys), or
- Add caching in your app for karyawan data (it changes at most every
  5 min).

### Data looks stale

The sync runs every 5 minutes. The last successful sync is visible at
`/metrics` and in the `sync_state` table:

```sql
SELECT * FROM sync_state;
```

If `last_status` is `failed`, see `sync_runs` and `last_error`.

### New karyawan not appearing

- If the row was just created in MySQL: wait up to 5 minutes (sync
  interval), or trigger an immediate sync by restarting svc-sync:
  ```sh
  docker compose -f deploy/docker-compose.prod.yml restart sync
  ```
- If the row was created before the gateway was deployed: the
  `updated_at` watermark is epoch-zero on first run, so the first
  sync pulls everything. Subsequent syncs are incremental.

## Reference

- Source schema: `sja.m_karyawan` (9 columns + `updated_at`)
- Sync interval: 5 minutes (configurable via `SYNC_INTERVAL` in deploy)
- API rate limit: 300 req/min/key (configurable via `API_RATE_LIMIT_PER_MIN`)
- Health: `GET /healthz`
- Metrics: `GET /metrics` (Prometheus)
