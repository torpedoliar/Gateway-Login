CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS karyawan (
  nik_hris          text PRIMARY KEY,
  nik_santos        text,
  nama_karyawan     text NOT NULL,
  nama_departemen   text,
  nama_jabatan      text,
  tgl_bergabung     date,
  tgl_keluar        date,
  lokasi            text,
  gender            text,
  source_updated_at timestamptz,
  synced_at         timestamptz NOT NULL DEFAULT now(),
  raw_payload       jsonb
);

CREATE INDEX IF NOT EXISTS idx_karyawan_nik_santos ON karyawan(nik_santos);
CREATE INDEX IF NOT EXISTS idx_karyawan_nama_ilike ON karyawan(nama_karyawan text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_karyawan_departemen_ilike ON karyawan(nama_departemen text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_karyawan_jabatan_ilike ON karyawan(nama_jabatan text_pattern_ops);
CREATE INDEX IF NOT EXISTS idx_karyawan_lokasi ON karyawan(lokasi);
CREATE INDEX IF NOT EXISTS idx_karyawan_tgl_keluar ON karyawan(tgl_keluar);
CREATE INDEX IF NOT EXISTS idx_karyawan_source_updated_at ON karyawan(source_updated_at);

CREATE TABLE IF NOT EXISTS sync_state (
  resource    text PRIMARY KEY,
  watermark   timestamptz,
  last_run_at timestamptz,
  last_status text,
  last_error  text
);

CREATE TABLE IF NOT EXISTS sync_runs (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  resource      text NOT NULL,
  started_at    timestamptz NOT NULL DEFAULT now(),
  finished_at   timestamptz,
  rows_upserted int NOT NULL DEFAULT 0,
  status        text NOT NULL,
  error         text
);

CREATE INDEX IF NOT EXISTS idx_sync_runs_resource_started
  ON sync_runs(resource, started_at DESC);

CREATE TABLE IF NOT EXISTS api_keys (
  id           text PRIMARY KEY,
  key_hash     text NOT NULL,
  description  text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  revoked      boolean NOT NULL DEFAULT false
);
