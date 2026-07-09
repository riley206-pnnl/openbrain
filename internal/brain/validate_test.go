package brain

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireNonEmptyText_RejectsEmptyString(t *testing.T) {
	err := requireNonEmptyText("capture", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyText)
}

func TestRequireNonEmptyText_RejectsWhitespaceOnly(t *testing.T) {
	err := requireNonEmptyText("capture", "   \t\n  ")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyText)
}

func TestRequireNonEmptyText_AcceptsNonEmptyText(t *testing.T) {
	err := requireNonEmptyText("capture", "a real thought")
	assert.NoError(t, err)
}

func TestRequireNonEmptyText_MessageNamesOperationAndIsDistinctFromBackendError(t *testing.T) {
	err := requireNonEmptyText("search", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "search", "the message must name the calling operation")
	assert.NotContains(t, err.Error(), "ollama",
		"an input-validation rejection must never read like a backend failure")
}
