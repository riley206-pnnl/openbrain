package brain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/intent"
)

// ErrEmptyContent means a bulk-import item carried no content. The whole batch
// is refused rather than silently skipping the item, so a caller never receives
// a success-shaped summary that hides a dropped item.
var ErrEmptyContent = errors.New("bulk import: item has empty content")

// ErrBatchTooLarge means a bulk-import call submitted more items than the
// configured cap allows. The whole batch is refused before any item is
// embedded, so an unbounded batch cannot drive one embedder round-trip per
// item or hold a single write transaction open over an unbounded row count.
var ErrBatchTooLarge = errors.New("bulk import: batch exceeds max items")

// ErrContentTooLong means one bulk-import item's content exceeds the
// configured per-item length cap. The whole batch is refused before any item
// is embedded, mirroring ErrBatchTooLarge's fail-closed contract.
var ErrContentTooLong = errors.New("bulk import: item content exceeds max length")

// DefaultBulkImportMaxItems is the batch-size cap used when no config is
// supplied (cfg is nil) or the configured value is non-positive.
const DefaultBulkImportMaxItems = 500

// DefaultBulkImportMaxContentRunes is the per-item content length cap (in
// runes) used when no config is supplied or the configured value is
// non-positive. Mirrors config.DefaultIngestMaxBytes's role for the ingest
// path.
const DefaultBulkImportMaxContentRunes = 10000

// BulkItem is one thought submitted to BulkImport before embedding.
type BulkItem struct {
	Content     string
	ThoughtType string
	Tags        []string
}

// BulkImport embeds every item and stores the whole batch in ONE atomic
// transaction: either all items persist or none do. A batch over the
// configured size cap, an over-length item, a malformed item (blank content),
// or an embed failure aborts the whole batch with a typed error before any row
// is written, so a partial write is impossible and the caller always gets a
// loud error instead of a success-shaped summary. The size and length caps are
// enforced in a validation pass BEFORE any embed call runs, so an oversized
// batch never drives a single embedder round-trip. It returns the new thought
// ids in input order.
func (b *Brain) BulkImport(ctx context.Context, items []BulkItem, source string) ([]string, error) {
	if len(items) == 0 {
		return nil, db.ErrEmptyBatch
	}

	maxItems := effectiveBulkMaxItems(b.cfg)
	if len(items) > maxItems {
		return nil, fmt.Errorf("bulk import: batch of %d items exceeds max of %d: %w",
			len(items), maxItems, ErrBatchTooLarge)
	}

	maxContentRunes := effectiveBulkMaxContentRunes(b.cfg)
	for i, it := range items {
		if strings.TrimSpace(it.Content) == "" {
			return nil, fmt.Errorf("bulk import: item %d: %w", i, ErrEmptyContent)
		}
		if n := len([]rune(it.Content)); n > maxContentRunes {
			return nil, fmt.Errorf("bulk import: item %d: content length %d exceeds max of %d: %w",
				i, n, maxContentRunes, ErrContentTooLong)
		}
	}

	inputs := make([]db.ThoughtInput, len(items))
	for i, it := range items {
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

// effectiveBulkMaxItems returns the configured batch-size cap, falling back to
// DefaultBulkImportMaxItems when cfg is nil or the configured value is
// non-positive.
func effectiveBulkMaxItems(cfg *config.Config) int {
	if cfg != nil && cfg.BulkImportMaxItems > 0 {
		return cfg.BulkImportMaxItems
	}
	return DefaultBulkImportMaxItems
}

// effectiveBulkMaxContentRunes returns the configured per-item content length
// cap, falling back to DefaultBulkImportMaxContentRunes when cfg is nil or the
// configured value is non-positive.
func effectiveBulkMaxContentRunes(cfg *config.Config) int {
	if cfg != nil && cfg.BulkImportMaxContentChars > 0 {
		return cfg.BulkImportMaxContentChars
	}
	return DefaultBulkImportMaxContentRunes
}
