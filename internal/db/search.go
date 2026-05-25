package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/windingriverholdings/openbrain/internal/model"
)

// SearchThoughts performs cosine similarity search against thought embeddings.
func SearchThoughts(ctx context.Context, p *pgxpool.Pool, embedding []float32, topK int, thoughtType string, tags []string, scoreThreshold float64) ([]model.ThoughtRow, error) {
	if len(embedding) == 0 {
		return nil, fmt.Errorf("search: empty embedding vector")
	}

	query := `
		SELECT id::text, content, summary, thought_type::text,
		       tags, source, created_at,
		       1 - (embedding <=> $1::vector) AS score
		FROM thoughts
		WHERE is_current = TRUE
		  AND embedding IS NOT NULL`

	args := []any{VecLiteral(embedding)}
	argN := 2

	if thoughtType != "" {
		query += fmt.Sprintf(" AND thought_type = $%d::thought_type", argN)
		args = append(args, thoughtType)
		argN++
	}

	if len(tags) > 0 {
		query += fmt.Sprintf(" AND tags && $%d", argN)
		args = append(args, tags)
		argN++
	}

	query += fmt.Sprintf(`
		ORDER BY embedding <=> $1::vector
		LIMIT $%d`, argN)
	args = append(args, topK)

	rows, err := p.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search thoughts: %w", err)
	}
	defer rows.Close()

	var results []model.ThoughtRow
	for rows.Next() {
		t, err := scanThought(rows)
		if err != nil {
			return nil, fmt.Errorf("scan search result: %w", err)
		}
		if t.Score != nil && *t.Score >= scoreThreshold {
			results = append(results, t)
		}
	}
	return results, rows.Err()
}

// HybridSearchThoughts performs combined keyword (BM25) + semantic (cosine) search.
// thoughtType filters results to a specific thought_type; pass "" to skip filtering.
func HybridSearchThoughts(ctx context.Context, p *pgxpool.Pool, queryText string, embedding []float32, topK int, keywordWeight, semanticWeight, scoreThreshold float64, includeHistory bool, thoughtType string) ([]model.ThoughtRow, error) {
	if len(embedding) == 0 {
		return nil, fmt.Errorf("search: empty embedding vector")
	}

	currentOnly := !includeHistory

	// Pass filter_type as NULL when empty, so SQL applies no type filter.
	var filterType *string
	if thoughtType != "" {
		filterType = &thoughtType
	}

	// Fully type every argument so the 8-arg hybrid_search overload resolves
	// unambiguously, even if a legacy 6-/7-arg overload is ever reintroduced
	// (see sql/010_drop_legacy_hybrid_search_overloads.sql). The embedding is
	// pinned to vector(768) — the project's model-agnostic column stores bare
	// vectors, but the query cast must match the active nomic-embed-text dim so
	// pgvector validates dimensionality and search never silently drifts to 384.
	query := `
		SELECT id::text, content, summary, thought_type::text,
		       tags, source, created_at, combined_score
		FROM hybrid_search(
		         $1::text,
		         $2::vector(768),
		         $3::integer,
		         $4::double precision,
		         $5::double precision,
		         $6::double precision,
		         $7::boolean,
		         $8::text)
		ORDER BY combined_score DESC LIMIT $9`

	rows, err := p.Query(ctx, query,
		queryText, VecLiteral(embedding), topK*2,
		keywordWeight, semanticWeight, scoreThreshold, currentOnly, filterType, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("hybrid search: %w", err)
	}
	defer rows.Close()

	// SQL already applies min_score — no double threshold filtering in Go.
	var results []model.ThoughtRow
	for rows.Next() {
		t, err := scanThought(rows)
		if err != nil {
			return nil, fmt.Errorf("scan hybrid result: %w", err)
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

// KeywordSearchThoughts performs full-text keyword search using tsvector/tsquery.
// thoughtType filters results to a specific thought_type; pass "" to skip filtering.
func KeywordSearchThoughts(ctx context.Context, p *pgxpool.Pool, queryText string, topK int, includeHistory bool, thoughtType string) ([]model.ThoughtRow, error) {
	query := `
		SELECT id::text, content, summary, thought_type::text,
		       tags, source, created_at,
		       ts_rank(fts_vector, websearch_to_tsquery('english', $1)) AS score
		FROM thoughts
		WHERE fts_vector @@ websearch_to_tsquery('english', $1)`

	if !includeHistory {
		query += " AND is_current = TRUE"
	}

	args := []any{queryText}
	argN := 2

	if thoughtType != "" {
		query += fmt.Sprintf(" AND thought_type = $%d::thought_type", argN)
		args = append(args, thoughtType)
		argN++
	}

	query += fmt.Sprintf(" ORDER BY score DESC LIMIT $%d", argN)
	args = append(args, topK)

	rows, err := p.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var results []model.ThoughtRow
	for rows.Next() {
		t, err := scanThought(rows)
		if err != nil {
			return nil, fmt.Errorf("scan keyword result: %w", err)
		}
		results = append(results, t)
	}
	return results, rows.Err()
}
