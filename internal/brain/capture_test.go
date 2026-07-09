package brain

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/intent"
)

// trackingEmbedder records whether Embed/EmbedBatch was invoked, so tests can
// prove a guard rejected input BEFORE any embed call was attempted, per the
// OB-049 requirement that empty input never reaches the embedding backend.
type trackingEmbedder struct {
	embedCalled *bool
}

func (e trackingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	*e.embedCalled = true
	return []float32{0.1, 0.2, 0.3}, nil
}

func (e trackingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	*e.embedCalled = true
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func (trackingEmbedder) Dimension() int { return 3 }

// TestCapture_RejectsEmptyContentBeforeEmbed asserts an empty content string
// is refused before the embedder is ever called, and the error is
// ErrEmptyText, not a backend-attributed embedding error.
func TestCapture_RejectsEmptyContentBeforeEmbed(t *testing.T) {
	called := false
	b := &Brain{embedder: trackingEmbedder{&called}}

	msg, err := b.Capture(context.Background(), intent.ParsedIntent{Text: "", ThoughtType: "note"}, "test")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyText)
	assert.Empty(t, msg)
	assert.False(t, called, "the embedder must not be called when content is empty")
}

// TestCapture_RejectsWhitespaceOnlyContentBeforeEmbed asserts whitespace-only
// content is treated the same as empty content.
func TestCapture_RejectsWhitespaceOnlyContentBeforeEmbed(t *testing.T) {
	called := false
	b := &Brain{embedder: trackingEmbedder{&called}}

	_, err := b.Capture(context.Background(), intent.ParsedIntent{Text: "   \n\t  ", ThoughtType: "note"}, "test")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyText)
	assert.False(t, called, "the embedder must not be called when content is whitespace-only")
}

// TestCapture_ValidContentSucceeds asserts a non-empty capture proceeds past
// the guard, reaches the embedder, and stores successfully end-to-end against
// a fake embedder and a stubbed store seam (no live database required).
func TestCapture_ValidContentSucceeds(t *testing.T) {
	b := &Brain{embedder: staticEmbedder{}}
	var gotContent, gotThoughtType, gotSource string
	var gotEmbedding []float32
	b.storeFn = func(_ context.Context, content string, embedding []float32, thoughtType string, tags []string, source string) (string, error) {
		gotContent, gotEmbedding, gotThoughtType, gotSource = content, embedding, thoughtType, source
		return "00000001-0000-0000-0000-000000000000", nil
	}

	parsed := intent.ParsedIntent{Text: "a valid thought", ThoughtType: "note"}
	msg, err := b.Capture(context.Background(), parsed, "test")

	require.NoError(t, err)
	assert.Contains(t, msg, "Captured")
	assert.Contains(t, msg, "00000001")
	assert.Equal(t, "a valid thought", gotContent, "the exact content must reach the store call")
	assert.NotEmpty(t, gotEmbedding, "the embedding produced by the embedder must reach the store call")
	assert.Equal(t, "note", gotThoughtType)
	assert.Equal(t, "test", gotSource)
}

// TestCapture_StoreFailureSurfacesError asserts a store-layer failure after a
// successful embed still returns a real error, not a success-shaped string.
func TestCapture_StoreFailureSurfacesError(t *testing.T) {
	b := &Brain{embedder: staticEmbedder{}}
	b.storeFn = func(_ context.Context, _ string, _ []float32, _ string, _ []string, _ string) (string, error) {
		return "", assert.AnError
	}

	msg, err := b.Capture(context.Background(), intent.ParsedIntent{Text: "content", ThoughtType: "note"}, "test")
	require.Error(t, err)
	assert.Empty(t, msg)
}
