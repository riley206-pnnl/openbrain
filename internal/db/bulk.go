package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors returned by BulkInsertThoughts so callers can branch on the
// cause rather than string-matching.
var (
	// ErrEmptyBatch means a bulk write was asked to persist zero thoughts.
	ErrEmptyBatch = errors.New("bulk insert: empty batch")
	// ErrEmptyEmbedding means one item in a bulk write carried no embedding
	// vector. The whole batch is refused before any row is written.
	ErrEmptyEmbedding = errors.New("bulk insert: empty embedding vector")
)

// bulkRollbackTimeout bounds the deferred tx.Rollback in BulkInsertThoughts. It
// mirrors supersedeRollbackTimeout: rollback runs on a fresh context derived
// from Background, never the caller's ctx, so a request whose ctx is already
// cancelled still releases the connection cleanly instead of poisoning the pool.
const bulkRollbackTimeout = 5 * time.Second

// bulkInsertTestHooks lets integration tests in this package force a real
// Postgres failure between the last insert and the commit, proving the whole
// batch rolls back. The field is nil in production and unexported, so no code
// outside this package can set it.
var bulkInsertTestHooks struct {
	// failBeforeCommit, when non-nil, runs after every insert has succeeded and
	// before the commit. A non-nil return aborts the transaction, so a real
	// tx.Rollback must discard every row inserted inside it.
	failBeforeCommit func() error
}

// ThoughtInput is one thought to persist in a bulk operation. It carries the
// same fields insertThoughtTx consumes, so the whole batch shares the single
// INSERT statement used everywhere else.
type ThoughtInput struct {
	Content     string
	Embedding   []float32
	ThoughtType string
	Tags        []string
	Source      string
	Summary     *string
	Metadata    map[string]any
}

// BulkInsertThoughts inserts every input inside ONE transaction. Either all
// rows commit or none do: a partial batch is impossible. On success it returns
// the new ids in input order; on any failure it returns a typed error with no
// row written, so callers get a loud error rather than a success-shaped summary
// that hides a partial write.
func BulkInsertThoughts(ctx context.Context, p *pgxpool.Pool, inputs []ThoughtInput) ([]string, error) {
	if len(inputs) == 0 {
		return nil, ErrEmptyBatch
	}
	// Reject an empty embedding before opening a transaction so the batch fails
	// closed with a typed error and no partial work.
	for i, in := range inputs {
		if len(in.Embedding) == 0 {
			return nil, fmt.Errorf("bulk insert: item %d: %w", i, ErrEmptyEmbedding)
		}
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("bulk insert: begin tx: %w", err)
	}
	// Roll back on a fresh, short-timeout context derived from Background so a
	// cancelled request ctx cannot leave the connection poisoned. Rollback is a
	// no-op once the tx has committed, so this defer is safe on both paths:
	// after a successful commit, Rollback returns pgx.ErrTxClosed, which is
	// expected and silent. Any OTHER rollback error means the connection may
	// not have been cleanly released on a real failure path, so it is logged
	// rather than silently dropped.
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), bulkRollbackTimeout)
		defer cancel()
		if rbErr := tx.Rollback(rollbackCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			slog.Warn("bulk insert: rollback failed", "error", rbErr)
		}
	}()

	ids := make([]string, len(inputs))
	for i, in := range inputs {
		id, err := insertThoughtTx(ctx, tx, in.Content, in.Embedding, in.ThoughtType,
			in.Tags, in.Source, in.Summary, in.Metadata)
		if err != nil {
			return nil, fmt.Errorf("bulk insert: item %d: %w", i, err)
		}
		ids[i] = id
	}

	// Test-only seam: force a failure after every insert has succeeded but
	// before commit, proving a real Postgres rollback discards the whole batch.
	// Nil in production.
	if bulkInsertTestHooks.failBeforeCommit != nil {
		if hookErr := bulkInsertTestHooks.failBeforeCommit(); hookErr != nil {
			return nil, fmt.Errorf("bulk insert: %w", hookErr)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("bulk insert: commit: %w", err)
	}

	slog.Info("bulk thoughts inserted", "count", len(ids))
	return ids, nil
}
