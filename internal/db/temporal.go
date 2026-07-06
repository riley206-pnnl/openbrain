package db

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/windingriverholdings/openbrain/internal/model"
)

// Sentinel errors returned by SupersedeCapture so callers can branch on the
// cause rather than string-matching.
var (
	// ErrOldThoughtNotFound means the old_thought_id does not exist.
	ErrOldThoughtNotFound = errors.New("supersede: old thought not found")
	// ErrAlreadySuperseded means the target thought is already retired. A
	// concurrent supersede of the same target observes this and does not mint a
	// duplicate live thought.
	ErrAlreadySuperseded = errors.New("supersede: old thought already superseded")
)

// SupersedeParams carries everything needed to atomically capture the new
// thought and retire the old one in a single transaction.
type SupersedeParams struct {
	Content     string
	Embedding   []float32
	ThoughtType string
	Tags        []string
	Source      string
	Summary     *string
	Metadata    map[string]any
	// OldID is the thought being retired.
	OldID string
}

// SupersedeCapture captures the new thought and marks the old thought
// superseded inside ONE transaction. Either both writes commit or both roll
// back: no orphan capture is ever left behind. The old row is locked with
// SELECT ... FOR UPDATE and the retire is a conditional UPDATE guarded on
// is_current, so two concurrent supersedes of the same target cannot both
// capture; the loser gets ErrAlreadySuperseded.
//
// It returns the new thought id on success.
func SupersedeCapture(ctx context.Context, p *pgxpool.Pool, params SupersedeParams) (newID string, err error) {
	if len(params.Embedding) == 0 {
		return "", fmt.Errorf("supersede capture: empty embedding vector")
	}
	metadata := params.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	tags := params.Tags
	if tags == nil {
		tags = []string{}
	}

	tx, err := p.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("supersede capture: begin tx: %w", err)
	}
	// Rollback is a no-op once the tx has committed, so this defer is safe on
	// both the success and failure paths.
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the old row for the lifetime of the transaction. A concurrent
	// supersede of the same target blocks here until we commit, then reads the
	// retired state below.
	var isCurrent bool
	err = tx.QueryRow(ctx,
		`SELECT is_current FROM thoughts WHERE id = $1::uuid FOR UPDATE`,
		params.OldID,
	).Scan(&isCurrent)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrOldThoughtNotFound
	}
	if err != nil {
		return "", fmt.Errorf("supersede capture: lock old thought: %w", err)
	}
	if !isCurrent {
		return "", ErrAlreadySuperseded
	}

	// Capture the new thought inside the transaction.
	err = tx.QueryRow(ctx, `
		INSERT INTO thoughts (content, summary, embedding, thought_type, tags, source, metadata)
		VALUES ($1, $2, $3::vector, $4::thought_type, $5, $6, $7)
		RETURNING id::text`,
		params.Content, params.Summary, VecLiteral(params.Embedding),
		params.ThoughtType, tags, params.Source, metadata,
	).Scan(&newID)
	if err != nil {
		return "", fmt.Errorf("supersede capture: insert new thought: %w", err)
	}

	// Retire the old thought. The is_current guard makes this a no-op if the
	// row was retired between the lock and here (belt and suspenders alongside
	// FOR UPDATE); RowsAffected must be exactly 1.
	tag, err := tx.Exec(ctx, `
		UPDATE thoughts
		SET is_current = FALSE, valid_until = NOW(), superseded_by = $1::uuid
		WHERE id = $2::uuid AND is_current = TRUE`,
		newID, params.OldID,
	)
	if err != nil {
		return "", fmt.Errorf("supersede capture: mark old superseded: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", fmt.Errorf("supersede capture: expected to retire exactly 1 row, affected %d: %w",
			tag.RowsAffected(), ErrAlreadySuperseded)
	}

	if err = tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("supersede capture: commit: %w", err)
	}

	slog.Info("thought superseded", "old", ShortID(params.OldID), "new", ShortID(newID))
	return newID, nil
}

// ShortID returns the first 8 characters of an id, or the whole string when it
// is shorter, so a malformed external id never triggers a slice panic.
func ShortID(id string) string {
	if len(id) < 8 {
		return id
	}
	return id[:8]
}

// GetThoughtTimeline returns all thoughts (current and superseded) linked to a subject.
func GetThoughtTimeline(ctx context.Context, p *pgxpool.Pool, subjectName string, topK int) ([]model.ThoughtRow, error) {
	rows, err := p.Query(ctx, `
		SELECT t.id::text, t.content, t.summary, t.thought_type::text,
		       t.tags, t.source, t.created_at,
		       NULL::float8 AS score
		FROM thoughts t
		JOIN thought_subjects ts ON ts.thought_id = t.id
		WHERE LOWER(ts.subject_name) = LOWER($1)
		ORDER BY t.created_at DESC
		LIMIT $2`,
		subjectName, topK,
	)
	if err != nil {
		return nil, fmt.Errorf("thought timeline: %w", err)
	}
	defer rows.Close()

	var results []model.ThoughtRow
	for rows.Next() {
		t, err := scanThought(rows)
		if err != nil {
			return nil, fmt.Errorf("scan timeline result: %w", err)
		}
		results = append(results, t)
	}
	return results, rows.Err()
}

// LinkSubjects associates a thought with entity subjects (people, tools, concepts).
func LinkSubjects(ctx context.Context, p *pgxpool.Pool, thoughtID string, subjects []model.SubjectLink) error {
	if len(subjects) == 0 {
		return nil
	}

	for _, s := range subjects {
		_, err := p.Exec(ctx, `
			INSERT INTO thought_subjects (thought_id, subject_name, subject_type)
			VALUES ($1::uuid, $2, $3)
			ON CONFLICT DO NOTHING`,
			thoughtID, s.Name, s.Type,
		)
		if err != nil {
			return fmt.Errorf("link subject %q: %w", s.Name, err)
		}
	}

	slog.Info("subjects linked", "thought", thoughtID[:8], "count", len(subjects))
	return nil
}
