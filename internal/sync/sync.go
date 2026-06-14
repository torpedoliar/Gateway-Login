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
	batch    int
}

// New constructs a Syncer. batchSize is the per-pull LIMIT; if src implements
// BatchSizer, New passes batchSize to the source so each Fetch* call honors
// the operator's SYNC_BATCH_SIZE configuration.
func New(pool *pgxpool.Pool, src Source, batchSize int) *Syncer {
	if batchSize <= 0 {
		batchSize = 500
	}
	if bs, ok := src.(BatchSizer); ok {
		bs.SetBatchSize(batchSize)
	}
	return &Syncer{pool: pool, src: src, resource: "karyawan", batch: batchSize}
}

// BatchSizer is an optional interface a Source may implement to receive the
// configured batch size. vpsmysql.Client implements it; test fakes do not.
type BatchSizer interface {
	SetBatchSize(int)
}

func (s *Syncer) SyncKaryawan(ctx context.Context) (int, error) {
	runID := uuid.New()
	startedAt := time.Now().UTC()

	// Serialize concurrent sync runs of the same resource via a Postgres
	// transaction-scoped advisory lock. The lock is keyed on
	// hashtext('sso_gateway_sync:' || resource) so unrelated resources
	// don't block each other. Held until the surrounding transaction
	// commits or rolls back, so a crashed syncer releases the lock
	// automatically — and the sync_runs row insert/update below is part
	// of the same tx, so a crash mid-sync leaves no 'running' leak.
	lockKey := int64(hashtextSyncResource(s.resource))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin lock tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return 0, fmt.Errorf("acquire advisory lock: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO sync_runs (id, resource, started_at, status) VALUES ($1, $2, $3, 'running')`,
		runID, s.resource, startedAt); err != nil {
		return 0, fmt.Errorf("insert sync_run: %w", err)
	}

	watermark, err := s.readWatermark(ctx)
	if err != nil {
		// Mark this run failed in the same tx so the row is never left
		// in 'running' on a transient error.
		_ = s.markRunFailed(ctx, tx, runID, fmt.Errorf("read watermark: %w", err))
		_ = tx.Commit(ctx)
		return 0, fmt.Errorf("read watermark: %w", err)
	}

	rows, err := s.src.FetchKaryawanUpdatedSince(ctx, watermark)
	if err != nil {
		_ = s.markRunFailed(ctx, tx, runID, fmt.Errorf("fetch from source: %w", err))
		_ = tx.Commit(ctx)
		return 0, fmt.Errorf("fetch from source: %w", err)
	}

	repo := karyawan.NewRepo(s.pool)
	upserted := 0
	maxTs := watermark
	for _, r := range rows {
		raw, err := json.Marshal(map[string]any{
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
		if err != nil {
			cause := fmt.Errorf("marshal raw_payload for %s: %w", r.NIKHRIS, err)
			_ = s.markRunFailed(ctx, tx, runID, cause)
			_ = tx.Commit(ctx)
			return 0, cause
		}
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
			cause := fmt.Errorf("upsert %s: %w", r.NIKHRIS, err)
			_ = s.markRunFailed(ctx, tx, runID, cause)
			_ = tx.Commit(ctx)
			return 0, cause
		}
		upserted++
		if r.UpdatedAt.After(maxTs) {
			maxTs = r.UpdatedAt
		}
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO sync_state (resource, watermark, last_run_at, last_status)
		 VALUES ($1, $2, $3, 'success')
		 ON CONFLICT (resource) DO UPDATE SET
		   watermark   = EXCLUDED.watermark,
		   last_run_at = EXCLUDED.last_run_at,
		   last_status = EXCLUDED.last_status,
		   last_error  = NULL`,
		s.resource, maxTs, time.Now().UTC()); err != nil {
		_ = s.markRunFailed(ctx, tx, runID, fmt.Errorf("update sync_state: %w", err))
		_ = tx.Commit(ctx)
		return 0, fmt.Errorf("update sync_state: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sync_runs SET finished_at = $1, rows_upserted = $2, status = 'success' WHERE id = $3`,
		time.Now().UTC(), upserted, runID); err != nil {
		_ = tx.Commit(ctx) // best effort: keep the watermark + run row success
		return upserted, fmt.Errorf("finalize sync_run: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return upserted, fmt.Errorf("commit sync tx: %w", err)
	}
	return upserted, nil
}

// markRunFailed updates the sync_runs row + sync_state in the given tx.
// Errors are intentionally swallowed; the caller is already in an error
// path and the original error is what gets returned to the user.
func (s *Syncer) markRunFailed(ctx context.Context, tx pgx.Tx, runID uuid.UUID, cause error) error {
	_, _ = tx.Exec(ctx,
		`UPDATE sync_runs SET finished_at = $1, status = 'failed', error = $2 WHERE id = $3`,
		time.Now().UTC(), cause.Error(), runID)
	_, _ = tx.Exec(ctx,
		`INSERT INTO sync_state (resource, last_run_at, last_status, last_error)
		 VALUES ($1, $2, 'failed', $3)
		 ON CONFLICT (resource) DO UPDATE SET
		   last_run_at = EXCLUDED.last_run_at,
		   last_status = EXCLUDED.last_status,
		   last_error  = EXCLUDED.last_error`,
		s.resource, time.Now().UTC(), cause.Error())
	return cause
}

// hashtextSyncResource produces a stable 32-bit hash of a resource name
// for use as a pg_advisory_xact_lock key. Mirrors PostgreSQL's hashtext
// for ASCII names: 32-bit FNV-1a-like fold. No external dep — values
// just need to be unique per resource within a deployment.
func hashtextSyncResource(resource string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(resource); i++ {
		h ^= uint32(resource[i])
		h *= 16777619
	}
	return h
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

// CleanupStaleRuns marks any 'running' sync_runs rows as 'failed' if they
// are older than `stale` (typically 2x the expected sync interval). Called
// once at startup to recover from previous process crashes. Returns the
// number of rows updated.
func CleanupStaleRuns(ctx context.Context, pool *pgxpool.Pool, resource string, stale time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-stale)
	res, err := pool.Exec(ctx,
		`UPDATE sync_runs
		 SET status = 'failed', finished_at = $1,
		     error = COALESCE(error,'') || ' [recovered: stale running row]'
		 WHERE resource = $2 AND status = 'running' AND started_at < $3`,
		time.Now().UTC(), resource, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected(), nil
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
