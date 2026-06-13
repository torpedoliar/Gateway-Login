package apikey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("api key not found")

type Entry struct {
	ID          string
	KeyHash     string
	Description string
	CreatedAt   time.Time
	LastUsedAt  *time.Time
	Revoked     bool
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Create(ctx context.Context, e *Entry) error {
	const q = `
INSERT INTO api_keys (id, key_hash, description)
VALUES ($1, $2, $3)`
	_, err := s.pool.Exec(ctx, q, e.ID, e.KeyHash, e.Description)
	return err
}

func (s *Store) GetByHash(ctx context.Context, hash string) (*Entry, error) {
	const q = `
SELECT id, key_hash, COALESCE(description,''), created_at, last_used_at, revoked
FROM api_keys
WHERE key_hash = $1 AND revoked = false
LIMIT 1`
	e := &Entry{}
	err := s.pool.QueryRow(ctx, q, hash).Scan(
		&e.ID, &e.KeyHash, &e.Description, &e.CreatedAt, &e.LastUsedAt, &e.Revoked,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get api key: %w", err)
	}
	return e, nil
}

func (s *Store) MarkUsed(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, "UPDATE api_keys SET last_used_at = now() WHERE id = $1", id)
	return err
}
