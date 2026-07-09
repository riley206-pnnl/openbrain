package db

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testEmbedding is a fixed synthetic vector. The embedding column is untyped
// vector after migration 008, so any consistent dimension works.
var testEmbedding = []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}

// integrationPool connects to the throwaway test database named by
// OPENBRAIN_TEST_DATABASE_URL. When the variable is unset the whole test is
// skipped so the default `go test ./...` stays green without a database.
func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENBRAIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OPENBRAIN_TEST_DATABASE_URL not set; skipping DB integration test")
	}
	pool, err := NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// cleanupSource removes every thought created under a test-specific source so
// subtests do not contaminate one another.
func cleanupSource(t *testing.T, p *pgxpool.Pool, source string) {
	t.Helper()
	_, err := p.Exec(context.Background(),
		`DELETE FROM thoughts WHERE source = $1`, source)
	require.NoError(t, err)
}

func liveCountBySource(t *testing.T, p *pgxpool.Pool, source string) int {
	t.Helper()
	var n int
	err := p.QueryRow(context.Background(),
		`SELECT count(*) FROM thoughts WHERE source = $1 AND is_current = TRUE`, source).Scan(&n)
	require.NoError(t, err)
	return n
}

// TestSupersedeCapture_DirectRepro exercises the real OB-031 path against a
// live database: old_thought_id supplied directly, asserting the old thought
// is excluded from default search after a successful call and the new thought
// is the sole live version.
func TestSupersedeCapture_DirectRepro(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob031-direct-repro"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	oldID, err := InsertThought(ctx, pool, "stale sadie voice canon", testEmbedding,
		"insight", []string{"sadie"}, source, nil, nil)
	require.NoError(t, err)

	newID, err := SupersedeCapture(ctx, pool, SupersedeParams{
		Content:     "canonical sadie voice canon",
		Embedding:   testEmbedding,
		ThoughtType: "insight",
		Tags:        []string{"sadie"},
		Source:      source,
		OldID:       oldID,
	})
	require.NoError(t, err)
	require.NotEqual(t, oldID, newID)

	// Default search (is_current = TRUE) must return the new thought and not
	// the retired one.
	results, err := SearchThoughts(ctx, pool, testEmbedding, 10, "", nil, -1, nil, nil)
	require.NoError(t, err)
	var ids []string
	for _, r := range results {
		ids = append(ids, r.ID)
	}
	assert.Contains(t, ids, newID, "new thought must be live in default search")
	assert.NotContains(t, ids, oldID, "retired thought must be excluded from default search")

	// Verify the temporal-fact columns directly.
	var isCurrent bool
	var supersededBy *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT is_current, superseded_by::text FROM thoughts WHERE id = $1::uuid`, oldID).
		Scan(&isCurrent, &supersededBy))
	assert.False(t, isCurrent, "old thought is_current must be false")
	require.NotNil(t, supersededBy)
	assert.Equal(t, newID, *supersededBy, "old thought superseded_by must point at the new thought")

	assert.Equal(t, 1, liveCountBySource(t, pool, source), "exactly one live thought for the slot")
}

// TestSupersedeCapture_AlreadySupersededRollsBack asserts that superseding an
// already-retired thought yields a typed error and captures no orphan.
func TestSupersedeCapture_AlreadySupersededRollsBack(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob031-already-superseded"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	oldID, err := InsertThought(ctx, pool, "first", testEmbedding,
		"insight", nil, source, nil, nil)
	require.NoError(t, err)

	_, err = SupersedeCapture(ctx, pool, SupersedeParams{
		Content: "second", Embedding: testEmbedding, ThoughtType: "insight", Source: source, OldID: oldID,
	})
	require.NoError(t, err)
	require.Equal(t, 1, liveCountBySource(t, pool, source))

	// Second supersede of the same, now-retired, old thought must fail typed
	// and must not capture a new thought.
	before := liveCountBySource(t, pool, source)
	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))

	_, err = SupersedeCapture(ctx, pool, SupersedeParams{
		Content: "orphan that must not survive", Embedding: testEmbedding,
		ThoughtType: "insight", Source: source, OldID: oldID,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAlreadySuperseded), "expected ErrAlreadySuperseded, got %v", err)

	var totalAfter int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&totalAfter))
	assert.Equal(t, total, totalAfter, "no orphan thought may be captured on rollback")
	assert.Equal(t, before, liveCountBySource(t, pool, source), "live count unchanged after failed supersede")
}

// TestSupersedeCapture_FailureAfterInsertRollsBack forces a real failure after
// the new-thought INSERT succeeds and before commit, using the package-level
// supersedeTestHooks seam. The existing failure tests (already-superseded,
// not-found) all bail at the lock gate before the insert ever runs, so they
// cannot prove rollback discards a row that was actually written inside the
// transaction. This test proves it against real Postgres: the new thought,
// though successfully INSERTed inside the tx, is absent after the forced
// failure because tx.Rollback actually ran.
func TestSupersedeCapture_FailureAfterInsertRollsBack(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob031-fail-after-insert"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	oldID, err := InsertThought(ctx, pool, "stale, must stay live on rollback", testEmbedding,
		"insight", nil, source, nil, nil)
	require.NoError(t, err)

	supersedeTestHooks.failAfterInsert = func() error {
		return errors.New("injected post-insert failure")
	}
	t.Cleanup(func() { supersedeTestHooks.failAfterInsert = nil })

	_, err = SupersedeCapture(ctx, pool, SupersedeParams{
		Content: "orphan that must not survive a real rollback", Embedding: testEmbedding,
		ThoughtType: "insight", Source: source, OldID: oldID,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected post-insert failure")

	// The new thought was really INSERTed inside the tx (proving this test
	// exercises the post-insert path, not the lock gate), but must not survive
	// the forced failure: only the seeded old thought remains for this source.
	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))
	assert.Equal(t, 1, total, "the INSERTed new thought must not survive a real Postgres rollback")

	var isCurrent bool
	var supersededBy *string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT is_current, superseded_by::text FROM thoughts WHERE id = $1::uuid`, oldID).
		Scan(&isCurrent, &supersededBy))
	assert.True(t, isCurrent, "old thought must remain live: the retire UPDATE never committed")
	assert.Nil(t, supersededBy)
}

// TestSupersedeCapture_RetireRaceLost forces the retire UPDATE's WHERE clause
// to target a different id than the one just locked, so it genuinely affects
// zero rows against real Postgres. This proves the RowsAffected != 1 branch
// fires as a distinct, honest error (ErrRetireRaceLost) rather than reusing
// ErrAlreadySuperseded, and that the new thought does not survive.
func TestSupersedeCapture_RetireRaceLost(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob031-retire-race-lost"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	oldID, err := InsertThought(ctx, pool, "locked but never actually retired", testEmbedding,
		"insight", nil, source, nil, nil)
	require.NoError(t, err)

	supersedeTestHooks.retireIDOverride = func(string) string {
		// A well-formed but nonexistent UUID: the retire UPDATE's WHERE clause
		// matches zero rows, a genuine RowsAffected() == 0 from real Postgres.
		return "00000000-0000-0000-0000-000000000000"
	}
	t.Cleanup(func() { supersedeTestHooks.retireIDOverride = nil })

	_, err = SupersedeCapture(ctx, pool, SupersedeParams{
		Content: "orphan that must not survive a lost retire race", Embedding: testEmbedding,
		ThoughtType: "insight", Source: source, OldID: oldID,
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRetireRaceLost), "expected ErrRetireRaceLost, got %v", err)
	assert.False(t, errors.Is(err, ErrAlreadySuperseded),
		"a lost retire race is a distinct invariant violation from already-superseded")

	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))
	assert.Equal(t, 1, total, "the new thought must not survive when the retire step affects zero rows")

	var isCurrent bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT is_current FROM thoughts WHERE id = $1::uuid`, oldID).Scan(&isCurrent))
	assert.True(t, isCurrent, "the old thought (never actually retired) must remain live")
}

// TestSupersedeCapture_RejectsEmptyEmbedding asserts the empty-embedding guard
// fires before any database work, so it needs no live database.
func TestSupersedeCapture_RejectsEmptyEmbedding(t *testing.T) {
	_, err := SupersedeCapture(context.Background(), nil, SupersedeParams{
		Content: "x", Embedding: nil, ThoughtType: "insight", Source: "unit", OldID: "irrelevant",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty embedding")
}

// TestSupersedeCapture_OldThoughtNotFound asserts an unknown old_thought_id
// yields the typed ErrOldThoughtNotFound and captures nothing.
func TestSupersedeCapture_OldThoughtNotFound(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob031-not-found"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	_, err := SupersedeCapture(ctx, pool, SupersedeParams{
		Content: "orphan that must not survive", Embedding: testEmbedding,
		ThoughtType: "insight", Source: source,
		OldID: "00000000-0000-0000-0000-000000000000",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrOldThoughtNotFound), "expected ErrOldThoughtNotFound, got %v", err)

	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))
	assert.Equal(t, 0, total, "no thought may be captured when the old thought is absent")
}

// TestShortID covers the short-string branch that never panics.
func TestShortID(t *testing.T) {
	assert.Equal(t, "abc", ShortID("abc"))
	assert.Equal(t, "0123456", ShortID("0123456"))
	assert.Equal(t, "01234567", ShortID("0123456789"))
}

// TestSupersedeCapture_ConcurrentSameTarget asserts row-level locking prevents
// two concurrent supersedes of the same old thought from both capturing. The
// live count stays 1.
func TestSupersedeCapture_ConcurrentSameTarget(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob031-concurrent"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	oldID, err := InsertThought(ctx, pool, "stale", testEmbedding,
		"insight", nil, source, nil, nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var successes, retired int
	for _, content := range []string{"winner-a", "winner-b"} {
		wg.Add(1)
		go func(content string) {
			defer wg.Done()
			_, err := SupersedeCapture(ctx, pool, SupersedeParams{
				Content: content, Embedding: testEmbedding,
				ThoughtType: "insight", Source: source, OldID: oldID,
			})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, ErrAlreadySuperseded):
				retired++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(content)
	}
	wg.Wait()

	assert.Equal(t, 1, successes, "exactly one concurrent supersede should win")
	assert.Equal(t, 1, retired, "the loser observes the already-superseded state")
	assert.Equal(t, 1, liveCountBySource(t, pool, source), "live count must stay 1 under concurrency")
}
