package brain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/intent"
)

// ErrEmptyContent means a bulk-import item carried no content. The whole batch
// is refused rather than silently skipping the item, so a caller never receives
// a success-shaped summary that hides a dropped item.
var ErrEmptyContent = errors.New("bulk import: item has empty content")

// BulkItem is one thought submitted to BulkImport before embedding.
type BulkItem struct {
	Content     string
	ThoughtType string
	Tags        []string
}

// BulkImport embeds every item and stores the whole batch in ONE atomic
// transaction: either all items persist or none do. A malformed item (blank
// content) or an embed failure aborts the whole batch with a typed error before
// any row is written, so a partial write is impossible and the caller always
// gets a loud error instead of a success-shaped summary. It returns the new
// thought ids in input order.
func (b *Brain) BulkImport(ctx context.Context, items []BulkItem, source string) ([]string, error) {
	if len(items) == 0 {
		return nil, db.ErrEmptyBatch
	}

	inputs := make([]db.ThoughtInput, len(items))
	for i, it := range items {
		if strings.TrimSpace(it.Content) == "" {
			return nil, fmt.Errorf("bulk import: item %d: %w", i, ErrEmptyContent)
		}
		thoughtType := it.ThoughtType
		if thoughtType == "" {
			thoughtType = intent.InferType(it.Content)
		}
		embedding, err := b.embedder.Embed(ctx, it.Content)
		if err != nil {
			return nil, fmt.Errorf("bulk import: embed item %d: %w", i, err)
		}
		inputs[i] = db.ThoughtInput{
			Content:     it.Content,
			Embedding:   embedding,
			ThoughtType: thoughtType,
			Tags:        it.Tags,
			Source:      source,
		}
	}

	ids, err := b.bulkInsertFn(ctx, inputs)
	if err != nil {
		slog.Error("bulk import failed", "items", len(items), "source", source, "error", err)
		return nil, fmt.Errorf("bulk import: %w", err)
	}

	slog.Info("bulk import committed", "count", len(ids), "source", source)
	return ids, nil
}
