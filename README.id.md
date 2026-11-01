# Gateway Login

[English](README.md) · **Bahasa Indonesia**

Gateway Login adalah layanan Go yang menyimpan salinan lokal tabel master karyawan (`sja.m_karyawan`) dari server MySQL jarak jauh, lalu menyajikannya ke aplikasi internal melalui REST API sederhana. Aplikasi membaca data karyawan dari gateway, tidak langsung ke database VPS, sehingga tetap berjalan ketika VPS lambat atau tidak bisa dijangkau.

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-mirror-4169E1?logo=postgresql&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-rate%20limit-DC382D?logo=redis&logoColor=white)
![Docker Compose](https://img.shields.io/badge/Docker%20Compose-ready-2496ED?logo=docker&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-blue)

## Daftar isi

- [Latar belakang](#latar-belakang)
- [Arsitektur](#arsitektur)
- [Fitur](#fitur)
- [Prasyarat](#prasyarat)
- [Mulai cepat (development)](#mulai-cepat-development)
- [Deployment produksi](#deployment-produksi)
- [Konfigurasi](#konfigurasi)
- [Referensi API](#referensi-api)
- [Integrasi aplikasi](#integrasi-aplikasi)
- [Pengembangan](#pengembangan)
- [Struktur proyek](#struktur-proyek)
- [Lisensi](#lisensi)

## Latar belakang

Database VPS tetap menjadi sumber data utama, dan gateway hanya membaca darinya. Setiap server aplikasi (.NET, Next.js, Node.js dengan Prisma, PHP lama) cukup memanggil satu endpoint HTTP yang stabil, tanpa menyimpan kredensial MySQL sendiri. Login dan sesi tetap ditangani masing-masing aplikasi; gateway hanya menyediakan data dan bukan identity provider.

Proyek ini dibuat setelah gangguan VPS membuat pencarian data karyawan gagal di beberapa aplikasi sekaligus. Dengan mirror lokal, gangguan VPS sekarang hanya menunda perubahan baru sampai sinkronisasi berikutnya berhasil.

## Arsitektur

```
                         ┌─────────────────────────────┐
   App server 1  ───►    │  Gateway API (Go, chi)      │  ◄── X-API-Key
   App server 2  ───►    │  GET /api/v1/karyawan       │
   App server 3  ───►    │  GET /api/v1/karyawan/{nik} │
                         └──────────────┬──────────────┘
                                        │ membaca mirror lokal
                                        ▼
                         ┌─────────────────────────────┐
                         │  PostgreSQL (mirror)        │
                         │  Redis (rate limit, cache)  │
                         └──────────────▲──────────────┘
                                        │ tarik inkremental tiap SYNC_INTERVAL
                                        │ WHERE updated_at > :watermark
                         ┌──────────────┴──────────────┐
                         │  VPS MySQL                  │
                         │  sja.m_karyawan (read-only) │
                         └─────────────────────────────┘
```

Repositori ini menghasilkan tiga binary:

| Binary | Fungsi |
|---|---|
| `cmd/api` | HTTP API dengan autentikasi API key dan rate limit per key |
| `cmd/sync` | Sinkronisasi inkremental MySQL ke PostgreSQL berbasis cron |
| `cmd/setup` | Setup sekali jalan: kredensial VPS, config terenkripsi, migrasi, API key pertama, sinkronisasi awal |

## Fitur

- Sinkronisasi inkremental MySQL ke PostgreSQL berdasarkan watermark `updated_at`, dengan jejak audit lengkap di `sync_runs`.
- Password VPS disimpan di `config.yaml` dalam bentuk terenkripsi AES-256-GCM. Master key berada di environment atau Docker secret.
- API key disimpan sebagai hash SHA-256. Key asli hanya ditampilkan sekali saat dibuat.
- Rate limit per key di Redis memakai skrip Lua atomik (default 300 request per menit).
- `pg_advisory_xact_lock` mencegah dua proses sinkronisasi menyentuh resource yang sama bersamaan. Saat start, layanan sync menandai run yang tertinggal berstatus `running` akibat proses crash sebagai gagal.
- Metrik Prometheus di `/metrics` dan liveness probe di `/healthz`.
- Backup `pg_dump` harian dengan retensi 14 hari dan opsi unggah ke S3.
- File Docker Compose untuk development dan produksi, ditambah deployer PowerShell bertahap untuk host Windows.

## Prasyarat

- Docker dengan plugin Compose (development dan produksi).
- Go 1.25 bila ingin build atau menjalankan binary di luar Docker.
- Akses jaringan dari host gateway ke MySQL VPS di port 3306.
- Di sisi VPS:
  1. User MySQL read-only:
     ```sql
     GRANT SELECT ON sja.m_karyawan TO 'sso_replicator'@'%';
     FLUSH PRIVILEGES;
     ```
  2. Kolom `updated_at` yang terindeks di `m_karyawan`. Bila belum ada:
     ```sql
     ALTER TABLE sja.m_karyawan
       ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
       ON UPDATE CURRENT_TIMESTAMP,
       ADD INDEX idx_updated_at (updated_at);
     ```
  3. Aturan firewall yang mengizinkan IP publik gateway ke port 3306.

## Mulai cepat (development)

```bash
# 1. Salin template environment
cp .env.example .env

# 2. Build dan jalankan PostgreSQL serta Redis
make docker-up

# 3. Jalankan wizard setup (host, port, database, user, password VPS).
#    Simpan API key yang tampil di akhir; key hanya ditampilkan sekali.
docker compose -f deploy/docker-compose.yml run --rm setup

# 4. Jalankan layanan api dan sync
docker compose -f deploy/docker-compose.yml up -d api sync

# 5. Uji API
curl -H "X-API-Key: <key>" "http://localhost:8080/api/v1/karyawan?limit=5"
curl -H "X-API-Key: <key>" "http://localhost:8080/api/v1/karyawan/EMP001"
```

Wizard setup berjalan tanpa interaksi bila `SETUP_HOST` dan variabel `SETUP_*` terkait sudah diisi. Cara inilah yang dipakai `deploy/deploy.ps1`.

## Deployment produksi

Panduan lengkap ada di [deploy/README.md](deploy/README.md). Ringkasnya di Linux:

```sh
./deploy/bootstrap.sh       # membuat secret di deploy/secrets dan template deploy/.env
# sunting deploy/.env, isi VPS_MYSQL_DSN
docker compose -f deploy/docker-compose.prod.yml build
docker compose -f deploy/docker-compose.prod.yml up -d postgres redis
docker compose -f deploy/docker-compose.prod.yml run --rm setup   # simpan API key yang tampil
docker compose -f deploy/docker-compose.prod.yml up -d
```

Di Windows dengan Docker Desktop, `deploy\deploy.ps1` menjalankan alur yang sama secara bertahap (Preflight, Secrets, Keys, Env, Build, InfraUp, Setup, StackUp, Verify). Jalankan satu tahap dengan `-Stage <nama>`, atau ulangi semua tahap dengan `-Force`. `deploy/deploy-ssh.ps1` menjalankan deployment di host jarak jauh lewat SSH.

File compose produksi:

- memasang secret sebagai file di `/run/secrets/`, bukan sebagai environment variable;
- memberi batas CPU dan memori, health check, serta `no-new-privileges` di setiap service;
- merotasi log container (json-file, 50 MB × 10);
- menjalankan container backup dengan retensi 14 hari.

`bootstrap.sh` juga membuat pasangan key RS256 dan entri `JWT_ISSUER`. API saat ini hanya memakai autentikasi API key, jadi keduanya belum digunakan.

### Pembaruan

`update.sh` (Linux) dan `update.ps1` (Windows) melakukan backup PostgreSQL, menarik kode terbaru, build image sebelum menghentikan stack yang berjalan, membuat ulang container, menjalankan migrasi, menandai sync run basi sebagai gagal, memeriksa `/healthz`, lalu menghapus backup lama. Bila build gagal, stack yang sedang berjalan tidak tersentuh.

### Monitoring

[deploy/MONITORING.md](deploy/MONITORING.md) berisi daftar metrik dan contoh aturan alert Prometheus.

## Konfigurasi

Variabel utama dari `.env.example`:

| Variabel | Default | Keterangan |
|---|---|---|
| `GATEWAY_HTTP_ADDR` | `:8080` | Alamat listen API |
| `POSTGRES_DSN` | `postgres://sso:sso@postgres:5432/sso?sslmode=disable` | Database mirror lokal |
| `REDIS_ADDR` | `redis:6379` | Redis untuk rate limit dan cache |
| `REDIS_PASSWORD`, `REDIS_DB` | kosong, `0` | Kredensial dan indeks database Redis |
| `GATEWAY_MASTER_KEY` | dibuat oleh setup | 32 byte acak, base64. Mengenkripsi password VPS |
| `SYNC_INTERVAL` | `5m` | Jadwal sinkronisasi (`@every <interval>`) |
| `SYNC_BATCH_SIZE` | `500` | Jumlah baris per batch |
| `API_RATE_LIMIT_PER_MIN` | `300` | Request per menit per API key |
| `LOG_LEVEL` | `info` | Level log zerolog |

Lokasi file setup bisa dipindah dengan `GATEWAY_CONFIG_PATH`, `GATEWAY_DOTENV_PATH`, dan `GATEWAY_MIGRATIONS_DIR`.

## Referensi API

| Endpoint | Auth | Keterangan |
|---|---|---|
| `GET /api/v1/karyawan` | `X-API-Key` | Daftar karyawan dengan filter dan paginasi |
| `GET /api/v1/karyawan/{nik_hris}` | `X-API-Key` | Satu karyawan berdasarkan NIK HRIS |
| `GET /healthz` | tanpa auth | Liveness |
| `GET /metrics` | tanpa auth | Metrik Prometheus |

Parameter query untuk `GET /api/v1/karyawan`:

| Parameter | Pencocokan |
|---|---|
| `nik_hris` | sama persis |
| `nik_santos` | sama persis |
| `nama_karyawan` | mengandung teks, tanpa membedakan huruf besar/kecil |
| `departemen` | mengandung teks pada `NAMA_DEPARTEMEN` |
| `jabatan` | mengandung teks pada `NAMA_JABATAN` |
| `lokasi` | sama persis |
| `status_aktif` | `true` berarti `TGL_KELUAR IS NULL`, `false` sebaliknya |
| `limit` | default 50, maksimal 500 |
| `offset` | default 0 |

Contoh respons:

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

Sinkronisasi membaca sembilan kolom dari `sja.m_karyawan`: `NIK_HRIS` (primary key), `NIK_SANTOS`, `NAMA_KARYAWAN`, `NAMA_DEPARTEMEN`, `NAMA_JABATAN`, `TGL_BERGABUNG`, `TGL_KELUAR` (NULL berarti masih aktif), `LOKASI`, dan `GENDER` (`L`/`P`), ditambah `updated_at` untuk watermark. Detail lebih lanjut ada di [docs/API.md](docs/API.md).

## Integrasi aplikasi

Node.js atau Next.js:

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

PHP, shell, atau klien HTTP lain:

```sh
curl -H "X-API-Key: $KEY" "$GATEWAY/api/v1/karyawan?limit=10"
```

Beri setiap aplikasi API key sendiri agar rate limit dan pencabutan akses berlaku per aplikasi.

## Pengembangan

```bash
make tidy               # go mod tidy
make build              # build bin/api, bin/sync, bin/setup
make test               # unit test (go test ./... -short)
make test-integration   # butuh PostgreSQL dan Redis dari docker compose
```

Menjalankan API tanpa Docker:

```bash
export POSTGRES_DSN="postgres://sso:sso@localhost:5432/sso?sslmode=disable"
export REDIS_ADDR="localhost:6379"
export GATEWAY_MASTER_KEY="$(openssl rand -base64 32)"
go run ./cmd/api
```

Stack: Go 1.25, chi, pgx/v5, go-sql-driver/mysql, go-redis/v9, golang-migrate, robfig/cron, viper, zerolog, dan AES-256-GCM dari standard library.

## Struktur proyek

```
cmd/            entry point api, sync, setup
internal/
  api/          handler dan middleware (auth, rate limit)
  apikey/       penyimpanan API key (hash SHA-256)
  crypto/       helper AES-256-GCM
  db/           koneksi PostgreSQL dan migrasi
  karyawan/     model dan repository karyawan
  redisx/       klien Redis dan rate limiter Lua
  sync/         sinkronisasi inkremental dan advisory lock
  vpsmysql/     klien sumber MySQL
  setup/, store/, config/, server/, logger/, bootstrap/
deploy/         file compose, Dockerfile, nginx.conf, skrip deploy, backup
docs/           API.md, SETUP.md, spesifikasi desain dan rencana
tests/          integration test end-to-end
```

Tabel PostgreSQL: `karyawan` (mirror), `sync_runs` (log audit), `sync_state` (watermark), dan `api_keys`. Dokumen desain dan rencana implementasi ada di [docs/superpowers/](docs/superpowers/).

## Lisensi

Dirilis di bawah [Lisensi MIT](LICENSE).

Dikembangkan oleh Yohanes Octavian Rizky ([@torpedoliar](https://github.com/torpedoliar)).
