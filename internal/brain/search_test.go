package brain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearch_RejectsEmptyQueryBeforeEmbed asserts an empty query is refused
// before the embedder is called, for every search mode (the embed call in
// Search runs unconditionally ahead of the mode switch).
func TestSearch_RejectsEmptyQueryBeforeEmbed(t *testing.T) {
	called := false
	b := &Brain{embedder: trackingEmbedder{&called}}

	_, err := b.Search(context.Background(), "", SearchOpts{Mode: "hybrid"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyText)
	assert.False(t, called, "the embedder must not be called when the query is empty")
}

// TestSearch_RejectsWhitespaceOnlyQueryBeforeEmbed asserts whitespace-only
// queries are treated the same as empty queries.
func TestSearch_RejectsWhitespaceOnlyQueryBeforeEmbed(t *testing.T) {
	called := false
	b := &Brain{embedder: trackingEmbedder{&called}}

	_, err := b.Search(context.Background(), "   ", SearchOpts{Mode: "vector"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyText)
	assert.False(t, called, "the embedder must not be called when the query is whitespace-only")
}
