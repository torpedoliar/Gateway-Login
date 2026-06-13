package karyawan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("karyawan not found")

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) Upsert(ctx context.Context, k *Karyawan) error {
	const q = `
INSERT INTO karyawan (nik_hris, nik_santos, nama_karyawan, nama_departemen, nama_jabatan,
                      tgl_bergabung, tgl_keluar, lokasi, gender, source_updated_at, raw_payload)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (nik_hris) DO UPDATE SET
  nik_santos        = EXCLUDED.nik_santos,
  nama_karyawan     = EXCLUDED.nama_karyawan,
  nama_departemen   = EXCLUDED.nama_departemen,
  nama_jabatan      = EXCLUDED.nama_jabatan,
  tgl_bergabung     = EXCLUDED.tgl_bergabung,
  tgl_keluar        = EXCLUDED.tgl_keluar,
  lokasi            = EXCLUDED.lokasi,
  gender            = EXCLUDED.gender,
  source_updated_at = EXCLUDED.source_updated_at,
  synced_at         = now(),
  raw_payload       = EXCLUDED.raw_payload
RETURNING synced_at
`
	raw := k.RawPayload
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	return r.pool.QueryRow(ctx, q,
		k.NIKHRIS, k.NIKSantos, k.NamaKaryawan, k.NamaDepartemen, k.NamaJabatan,
		k.TglBergabung, k.TglKeluar, k.Lokasi, k.Gender, k.SourceUpdatedAt, raw,
	).Scan(&k.SyncedAt)
}

func (r *Repo) GetByNIK(ctx context.Context, nik string) (*Karyawan, error) {
	const q = `
SELECT nik_hris, COALESCE(nik_santos,''), nama_karyawan, COALESCE(nama_departemen,''),
       COALESCE(nama_jabatan,''), tgl_bergabung, tgl_keluar, COALESCE(lokasi,''),
       COALESCE(gender,''), source_updated_at, synced_at, raw_payload
FROM karyawan WHERE nik_hris = $1
`
	return r.scanOne(ctx, q, nik)
}

func (r *Repo) scanOne(ctx context.Context, q string, args ...any) (*Karyawan, error) {
	k := &Karyawan{}
	var tglIn, tglOut, srcUpd *time.Time
	err := r.pool.QueryRow(ctx, q, args...).Scan(
		&k.NIKHRIS, &k.NIKSantos, &k.NamaKaryawan, &k.NamaDepartemen, &k.NamaJabatan,
		&tglIn, &tglOut, &k.Lokasi, &k.Gender, &srcUpd, &k.SyncedAt, &k.RawPayload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan karyawan: %w", err)
	}
	k.TglBergabung = tglIn
	k.TglKeluar = tglOut
	k.SourceUpdatedAt = srcUpd
	return k, nil
}

// List returns rows matching f and the total count (ignoring limit/offset).
func (r *Repo) List(ctx context.Context, f Filter) ([]Karyawan, int, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	var (
		conds []string
		args  []any
	)
	add := func(clause string, val any) {
		conds = append(conds, clause)
		args = append(args, val)
	}
	if f.NIKHRIS != "" {
		add("nik_hris = $"+itoa(len(args)+1), f.NIKHRIS)
	}
	if f.NIKSantos != "" {
		add("nik_santos = $"+itoa(len(args)+1), f.NIKSantos)
	}
	if f.NamaKaryawan != "" {
		add("nama_karyawan ILIKE $"+itoa(len(args)+1), "%"+f.NamaKaryawan+"%")
	}
	if f.Departemen != "" {
		add("nama_departemen ILIKE $"+itoa(len(args)+1), "%"+f.Departemen+"%")
	}
	if f.Jabatan != "" {
		add("nama_jabatan ILIKE $"+itoa(len(args)+1), "%"+f.Jabatan+"%")
	}
	if f.Lokasi != "" {
		add("lokasi = $"+itoa(len(args)+1), f.Lokasi)
	}
	if f.StatusAktif != nil {
		if *f.StatusAktif {
			add("tgl_keluar IS NULL", nil)
		} else {
			add("tgl_keluar IS NOT NULL", nil)
		}
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	// total count
	var total int
	countQ := "SELECT COUNT(*) FROM karyawan " + where
	if err := r.pool.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	// page
	listQ := `
SELECT nik_hris, COALESCE(nik_santos,''), nama_karyawan, COALESCE(nama_departemen,''),
       COALESCE(nama_jabatan,''), tgl_bergabung, tgl_keluar, COALESCE(lokasi,''),
       COALESCE(gender,''), source_updated_at, synced_at, raw_payload
FROM karyawan ` + where + `
ORDER BY nik_hris ASC
LIMIT $` + itoa(len(args)+1) + ` OFFSET $` + itoa(len(args)+2)
	rows, err := r.pool.Query(ctx, listQ, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()

	var out []Karyawan
	for rows.Next() {
		k := Karyawan{}
		var tglIn, tglOut, srcUpd *time.Time
		if err := rows.Scan(
			&k.NIKHRIS, &k.NIKSantos, &k.NamaKaryawan, &k.NamaDepartemen, &k.NamaJabatan,
			&tglIn, &tglOut, &k.Lokasi, &k.Gender, &srcUpd, &k.SyncedAt, &k.RawPayload,
		); err != nil {
			return nil, 0, fmt.Errorf("scan list: %w", err)
		}
		k.TglBergabung = tglIn
		k.TglKeluar = tglOut
		k.SourceUpdatedAt = srcUpd
		out = append(out, k)
	}
	return out, total, rows.Err()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	buf := [20]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
