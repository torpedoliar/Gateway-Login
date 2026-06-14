package vpsmysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/yourorg/sso-gateway/internal/crypto"
	"github.com/yourorg/sso-gateway/internal/store"
)

// KaryawanRow is the raw row from sja.m_karyawan.
type KaryawanRow struct {
	NIKHRIS         string
	NIKSantos       sql.NullString
	NamaKaryawan    string
	NamaDepartemen  sql.NullString
	NamaJabatan     sql.NullString
	TglBergabung    sql.NullTime
	TglKeluar       sql.NullTime
	Lokasi          sql.NullString
	Gender          sql.NullString
	UpdatedAt       time.Time
}

type Client struct {
	db        *sql.DB
	batchSize int
}

func NewClient(ctx context.Context, dsn string, batchSize int) (*Client, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	return &Client{db: db, batchSize: batchSize}, nil
}

func (c *Client) Close() error { return c.db.Close() }

// SetBatchSize updates the per-fetch LIMIT. Implements syncpkg.BatchSizer.
func (c *Client) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// BuildDSN constructs a MySQL DSN from structured fields. The user/password
// are passed through go-sql-driver's mysql.Config so special characters
// (e.g. '@', '/', '?', '#', '%') are URL-escaped automatically and never
// corrupt the DSN. loc is pinned to UTC so the driver parses naive
// DATETIME values the same way regardless of the Go binary's local TZ.
func BuildDSN(host string, port int, database, user, password string) string {
	cfg := mysql.NewConfig()
	cfg.User = user
	cfg.Passwd = password
	cfg.Net = "tcp"
	cfg.Addr = fmt.Sprintf("%s:%d", host, port)
	cfg.DBName = database
	cfg.ParseTime = true
	cfg.ReadTimeout = 30 * time.Second
	cfg.Loc = time.UTC
	return cfg.FormatDSN()
}

// NewClientFromStoreConfig decrypts the VPS password with masterKey, builds
// the DSN, and returns a connected Client. Consolidates the
// "load-storeCfg → decrypt → BuildDSN → NewClient" sequence shared by
// cmd/sync and cmd/setup so a future change to DSN construction (TLS,
// socket path, options) lives in one place.
func NewClientFromStoreConfig(ctx context.Context, cfg *store.VPSConfig, masterKey, masterB64 string, batchSize int) (*Client, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil vps config")
	}
	if masterB64 == "" {
		return nil, fmt.Errorf("empty master key")
	}
	mk, err := crypto.Base64ToKey(masterB64)
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	pw, err := cfg.GetDecryptedPassword(mk)
	if err != nil {
		return nil, fmt.Errorf("decrypt vps password: %w", err)
	}
	_ = masterKey // reserved for callers that need the raw key directly
	dsn := BuildDSN(cfg.Host, cfg.Port, cfg.Database, cfg.Username, pw)
	return NewClient(ctx, dsn, batchSize)
}

// FetchKaryawanUpdatedSince returns rows with updated_at > since, ordered ascending.
const karyawanQuery = `
SELECT NIK_HRIS, NIK_SANTOS, NAMA_KARYAWAN, NAMA_DEPARTEMEN, NAMA_JABATAN,
       TGL_BERGABUNG, TGL_KELUAR, LOKASI, GENDER, updated_at
FROM sja.m_karyawan
WHERE updated_at > ?
ORDER BY updated_at ASC, NIK_HRIS ASC
LIMIT ?`

func (c *Client) FetchKaryawanUpdatedSince(ctx context.Context, since time.Time) ([]KaryawanRow, error) {
	rows, err := c.db.QueryContext(ctx, karyawanQuery, since, c.batchSize)
	if err != nil {
		return nil, fmt.Errorf("query karyawan: %w", err)
	}
	defer rows.Close()

	var out []KaryawanRow
	for rows.Next() {
		var r KaryawanRow
		if err := rows.Scan(
			&r.NIKHRIS, &r.NIKSantos, &r.NamaKaryawan, &r.NamaDepartemen, &r.NamaJabatan,
			&r.TglBergabung, &r.TglKeluar, &r.Lokasi, &r.Gender, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FetchKaryawanByNIK returns a single row or nil.
const karyawanByNIKQuery = `
SELECT NIK_HRIS, NIK_SANTOS, NAMA_KARYAWAN, NAMA_DEPARTEMEN, NAMA_JABATAN,
       TGL_BERGABUNG, TGL_KELUAR, LOKASI, GENDER, updated_at
FROM sja.m_karyawan
WHERE NIK_HRIS = ?
LIMIT 1`

func (c *Client) FetchKaryawanByNIK(ctx context.Context, nik string) (*KaryawanRow, error) {
	r := &KaryawanRow{}
	err := c.db.QueryRowContext(ctx, karyawanByNIKQuery, nik).Scan(
		&r.NIKHRIS, &r.NIKSantos, &r.NamaKaryawan, &r.NamaDepartemen, &r.NamaJabatan,
		&r.TglBergabung, &r.TglKeluar, &r.Lokasi, &r.Gender, &r.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query by nik: %w", err)
	}
	return r, nil
}

// MaxUpdatedAt returns MAX(updated_at) from m_karyawan, or zero time if empty.
func (c *Client) MaxUpdatedAt(ctx context.Context) (time.Time, error) {
	var t sql.NullTime
	err := c.db.QueryRowContext(ctx, "SELECT MAX(updated_at) FROM sja.m_karyawan").Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("max updated_at: %w", err)
	}
	if !t.Valid {
		return time.Time{}, nil
	}
	return t.Time, nil
}
