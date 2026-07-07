package db

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBulkInsertThoughts_HappyPath inserts a batch and asserts every row is
// live and the ids come back in input order.
func TestBulkInsertThoughts_HappyPath(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob032-bulk-happy"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	inputs := []ThoughtInput{
		{Content: "bulk one", Embedding: testEmbedding, ThoughtType: "note", Source: source},
		{Content: "bulk two", Embedding: testEmbedding, ThoughtType: "insight", Source: source},
		{Content: "bulk three", Embedding: testEmbedding, ThoughtType: "idea", Source: source},
	}

	ids, err := BulkInsertThoughts(ctx, pool, inputs)
	require.NoError(t, err)
	require.Len(t, ids, 3)
	assert.Equal(t, 3, liveCountBySource(t, pool, source), "every batch row must be live")

	// Assert field state: each id resolves to the content at its input index.
	for i, id := range ids {
		var content string
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT content FROM thoughts WHERE id = $1::uuid`, id).Scan(&content))
		assert.Equal(t, inputs[i].Content, content, "id order must match input order")
	}
}

// TestBulkInsertThoughts_InjectedFailureRollsBack forces a real failure after
// every insert succeeds and before commit, proving the WHOLE batch rolls back:
// no orphan row survives. This is the injected-partial-failure regression the
// OB-032 acceptance criteria require for bulk_import.
func TestBulkInsertThoughts_InjectedFailureRollsBack(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob032-bulk-inject-fail"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	bulkInsertTestHooks.failBeforeCommit = func() error {
		return errors.New("injected pre-commit failure")
	}
	t.Cleanup(func() { bulkInsertTestHooks.failBeforeCommit = nil })

	inputs := []ThoughtInput{
		{Content: "must not survive 1", Embedding: testEmbedding, ThoughtType: "note", Source: source},
		{Content: "must not survive 2", Embedding: testEmbedding, ThoughtType: "note", Source: source},
	}

	ids, err := BulkInsertThoughts(ctx, pool, inputs)
	require.Error(t, err)
	assert.Nil(t, ids, "no ids may be returned on rollback")
	assert.Contains(t, err.Error(), "injected pre-commit failure")

	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))
	assert.Equal(t, 0, total, "no batch row may survive a forced pre-commit failure")
}

// TestBulkInsertThoughts_MidBatchInsertFailureRollsBack uses a genuinely
// invalid enum value on the second item so real Postgres rejects the INSERT
// mid-batch. It proves the first item, though already INSERTed inside the tx,
// does not survive: the transaction rolls back as a unit.
func TestBulkInsertThoughts_MidBatchInsertFailureRollsBack(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob032-bulk-midbatch-fail"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	inputs := []ThoughtInput{
		{Content: "valid first", Embedding: testEmbedding, ThoughtType: "note", Source: source},
		{Content: "bad enum second", Embedding: testEmbedding, ThoughtType: "not_a_real_type", Source: source},
	}

	ids, err := BulkInsertThoughts(ctx, pool, inputs)
	require.Error(t, err)
	assert.Nil(t, ids)

	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))
	assert.Equal(t, 0, total, "the first item must not survive when a later item fails to insert")
}

// TestBulkInsertThoughts_EmptyBatch asserts a zero-length batch is a typed
// error and needs no live database.
func TestBulkInsertThoughts_EmptyBatch(t *testing.T) {
	_, err := BulkInsertThoughts(context.Background(), nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEmptyBatch), "expected ErrEmptyBatch, got %v", err)
}

// TestBulkInsertThoughts_EmptyEmbedding asserts an item with no embedding is
// rejected before any row is written, with a typed error.
func TestBulkInsertThoughts_EmptyEmbedding(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob032-bulk-empty-embed"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	inputs := []ThoughtInput{
		{Content: "has embedding", Embedding: testEmbedding, ThoughtType: "note", Source: source},
		{Content: "no embedding", Embedding: nil, ThoughtType: "note", Source: source},
	}

	_, err := BulkInsertThoughts(ctx, pool, inputs)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEmptyEmbedding), "expected ErrEmptyEmbedding, got %v", err)

	var total int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM thoughts WHERE source = $1`, source).Scan(&total))
	assert.Equal(t, 0, total, "no row may be written when one item has an empty embedding")
}

// concurrentBatchThoughtTypes assigns a distinct enum value per batch index so
// TestBulkInsertThoughts_ConcurrentBatches can detect a torn record: a
// concurrent write bug that attaches one batch's rows to a different batch's
// field values would show up as a content/thought_type mismatch, not just a
// wrong aggregate count.
var concurrentBatchThoughtTypes = []string{"note", "insight", "idea", "decision", "memory"}

// TestBulkInsertThoughts_ConcurrentBatches runs several batches against the
// same source concurrently and asserts no write is lost or torn: the final
// live count equals the exact sum of every committed batch, AND every row's
// own content and thought_type still agree with each other (a real torn-record
// check), not merely the aggregate count.
func TestBulkInsertThoughts_ConcurrentBatches(t *testing.T) {
	pool := integrationPool(t)
	ctx := context.Background()
	source := "ob032-bulk-concurrent"
	cleanupSource(t, pool, source)
	t.Cleanup(func() { cleanupSource(t, pool, source) })

	const batches = 5
	const perBatch = 4

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for b := 0; b < batches; b++ {
		wg.Add(1)
		go func(b int) {
			defer wg.Done()
			thoughtType := concurrentBatchThoughtTypes[b%len(concurrentBatchThoughtTypes)]
			inputs := make([]ThoughtInput, perBatch)
			for i := 0; i < perBatch; i++ {
				inputs[i] = ThoughtInput{
					Content:     fmt.Sprintf("batch %d item %d", b, i),
					Embedding:   testEmbedding,
					ThoughtType: thoughtType,
					Source:      source,
				}
			}
			if _, err := BulkInsertThoughts(ctx, pool, inputs); err != nil {
				mu.Lock()
				failures = append(failures, err)
				mu.Unlock()
			}
		}(b)
	}
	wg.Wait()

	require.Empty(t, failures, "no concurrent batch should fail: %v", failures)
	assert.Equal(t, batches*perBatch, liveCountBySource(t, pool, source),
		"every concurrent batch must fully persist with no lost or torn writes")

	// Torn-record check: every row's own thought_type must match the batch
	// encoded in its own content. A torn write (rows from one batch's
	// transaction ending up paired with another batch's field values) would
	// pass the aggregate live-count assertion above but fail here.
	rows, err := pool.Query(ctx,
		`SELECT content, thought_type::text FROM thoughts WHERE source = $1`, source)
	require.NoError(t, err)
	defer rows.Close()

	seen := map[string]bool{}
	for rows.Next() {
		var content, thoughtType string
		require.NoError(t, rows.Scan(&content, &thoughtType))

		var batchIdx, itemIdx int
		_, scanErr := fmt.Sscanf(content, "batch %d item %d", &batchIdx, &itemIdx)
		require.NoError(t, scanErr, "unexpected content shape: %q", content)

		expectedType := concurrentBatchThoughtTypes[batchIdx%len(concurrentBatchThoughtTypes)]
		assert.Equal(t, expectedType, thoughtType,
			"row %q has thought_type %q, expected %q for its own batch: torn record",
			content, thoughtType, expectedType)
		seen[content] = true
	}
	require.NoError(t, rows.Err())
	assert.Len(t, seen, batches*perBatch,
		"every batch/item combination must appear exactly once with no duplicates")
}
