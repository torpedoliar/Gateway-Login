package syncpkg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/yourorg/sso-gateway/internal/karyawan"
	"github.com/yourorg/sso-gateway/internal/vpsmysql"
)

// KaryawanRow is re-exported from vpsmysql for test convenience.
type KaryawanRow = vpsmysql.KaryawanRow

// Source abstracts the VPS MySQL read methods.
type Source interface {
	FetchKaryawanUpdatedSince(ctx context.Context, since time.Time) ([]vpsmysql.KaryawanRow, error)
}

type Syncer struct {
	pool     *pgxpool.Pool
	src      Source
	resource string
}

func New(pool *pgxpool.Pool, src Source, _ int) *Syncer {
	return &Syncer{pool: pool, src: src, resource: "karyawan"}
}

func (s *Syncer) SyncKaryawan(ctx context.Context) (int, error) {
	runID := uuid.New()
	startedAt := time.Now().UTC()

	_, err := s.pool.Exec(ctx,
		`INSERT INTO sync_runs (id, resource, started_at, status) VALUES ($1, $2, $3, 'running')`,
		runID, s.resource, startedAt)
	if err != nil {
		return 0, fmt.Errorf("insert sync_run: %w", err)
	}

	watermark, err := s.readWatermark(ctx)
	if err != nil {
		return s.failRun(ctx, runID, fmt.Errorf("read watermark: %w", err))
	}

	rows, err := s.src.FetchKaryawanUpdatedSince(ctx, watermark)
	if err != nil {
		return s.failRun(ctx, runID, fmt.Errorf("fetch from source: %w", err))
	}

	repo := karyawan.NewRepo(s.pool)
	upserted := 0
	maxTs := watermark
	for _, r := range rows {
		raw, _ := json.Marshal(map[string]any{
			"NIK_HRIS":        r.NIKHRIS,
			"NIK_SANTOS":      nullStr(r.NIKSantos),
			"NAMA_KARYAWAN":   r.NamaKaryawan,
			"NAMA_DEPARTEMEN": nullStr(r.NamaDepartemen),
			"NAMA_JABATAN":    nullStr(r.NamaJabatan),
			"TGL_BERGABUNG":   nullTime(r.TglBergabung),
			"TGL_KELUAR":      nullTime(r.TglKeluar),
			"LOKASI":          nullStr(r.Lokasi),
			"GENDER":          nullStr(r.Gender),
			"updated_at":      r.UpdatedAt,
		})
		updated := r.UpdatedAt
		k := &karyawan.Karyawan{
			NIKHRIS:         r.NIKHRIS,
			NIKSantos:       nullStr(r.NIKSantos),
			NamaKaryawan:    r.NamaKaryawan,
			NamaDepartemen:  nullStr(r.NamaDepartemen),
			NamaJabatan:     nullStr(r.NamaJabatan),
			TglBergabung:    nullTimePtr(r.TglBergabung),
			TglKeluar:       nullTimePtr(r.TglKeluar),
			Lokasi:          nullStr(r.Lokasi),
			Gender:          nullStr(r.Gender),
			SourceUpdatedAt: &updated,
			RawPayload:      raw,
		}
		if err := repo.Upsert(ctx, k); err != nil {
			return s.failRun(ctx, runID, fmt.Errorf("upsert %s: %w", r.NIKHRIS, err))
		}
		upserted++
		if r.UpdatedAt.After(maxTs) {
			maxTs = r.UpdatedAt
		}
	}

	_, err = s.pool.Exec(ctx,
		`INSERT INTO sync_state (resource, watermark, last_run_at, last_status)
		 VALUES ($1, $2, $3, 'success')
		 ON CONFLICT (resource) DO UPDATE SET
		   watermark   = EXCLUDED.watermark,
		   last_run_at = EXCLUDED.last_run_at,
		   last_status = EXCLUDED.last_status,
		   last_error  = NULL`,
		s.resource, maxTs, time.Now().UTC())
	if err != nil {
		return s.failRun(ctx, runID, fmt.Errorf("update sync_state: %w", err))
	}

	_, err = s.pool.Exec(ctx,
		`UPDATE sync_runs SET finished_at = $1, rows_upserted = $2, status = 'success' WHERE id = $3`,
		time.Now().UTC(), upserted, runID)
	if err != nil {
		return upserted, fmt.Errorf("finalize sync_run: %w", err)
	}
	return upserted, nil
}

func (s *Syncer) readWatermark(ctx context.Context) (time.Time, error) {
	var wm *time.Time
	err := s.pool.QueryRow(ctx, "SELECT watermark FROM sync_state WHERE resource = $1", s.resource).Scan(&wm)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row yet — start from epoch.
		return time.Unix(0, 0).UTC(), nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("read watermark: %w", err)
	}
	if wm == nil {
		return time.Unix(0, 0).UTC(), nil
	}
	return *wm, nil
}

func (s *Syncer) failRun(ctx context.Context, runID uuid.UUID, cause error) (int, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE sync_runs SET finished_at = $1, status = 'failed', error = $2 WHERE id = $3`,
		time.Now().UTC(), cause.Error(), runID); err != nil {
		// Surface the secondary failure so a stuck "running" row is at least
		// visible in logs even when the DB is partially unavailable.
		cause = fmt.Errorf("%w (also: finalize sync_runs: %v)", cause, err)
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO sync_state (resource, last_run_at, last_status, last_error)
		 VALUES ($1, $2, 'failed', $3)
		 ON CONFLICT (resource) DO UPDATE SET
		   last_run_at = EXCLUDED.last_run_at,
		   last_status = EXCLUDED.last_status,
		   last_error  = EXCLUDED.last_error`,
		s.resource, time.Now().UTC(), cause.Error()); err != nil {
		cause = fmt.Errorf("%w (also: update sync_state: %v)", cause, err)
	}
	return 0, cause
}

func nullStr(ns sql.NullString) string {
	if !ns.Valid {
		return ""
	}
	return ns.String
}

func nullTime(nt sql.NullTime) any {
	if !nt.Valid {
		return nil
	}
	return nt.Time
}

func nullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}
