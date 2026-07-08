package mcptools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
)

// This file covers OB-049: every MCP tool handler that declares a required
// string parameter must reject a call missing it (naming the key), and must
// refuse empty/whitespace-only content before any embed call runs, with a
// message that is never confused with a genuine backend embedding failure.

// --- mcpCapture ---

func TestMcpCapture_MissingContentNamesKey(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpCapture(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "content", "rejection must name the missing key")
}

func TestMcpCapture_EmptyContentReturnsDistinctError(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpCapture(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{"content": "   "}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	text := resultText(t, result)
	assert.NotContains(t, text, "ollama", "an empty-input rejection must not be misattributed to the backend")
	assert.Contains(t, text, "empty")
}

func TestMcpCapture_ValidContentSucceeds(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	b.SetStoreFnForTesting(func(_ context.Context, _ string, _ []float32, _ string, _ []string, _ string) (string, error) {
		return "00000001-0000-0000-0000-000000000000", nil
	})
	handler := mcpCapture(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{"content": "a valid thought"}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "Captured")
}

// --- mcpSearch ---

func TestMcpSearch_MissingQueryNamesKey(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpSearch(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "query", "rejection must name the missing key")
}

func TestMcpSearch_EmptyQueryReturnsDistinctError(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpSearch(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{"query": "  "}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	text := resultText(t, result)
	assert.NotContains(t, text, "ollama", "an empty-input rejection must not be misattributed to the backend")
	assert.Contains(t, text, "empty")
}

// --- mcpExtract ---

func TestMcpExtract_MissingTextNamesKey(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpExtract(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "text", "rejection must name the missing key")
}

// --- mcpSupersede ---

func TestMcpSupersede_MissingContentNamesKey(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpSupersede(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "content", "rejection must name the missing key")
}

func TestMcpSupersede_EmptyContentReturnsDistinctError(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpSupersede(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{"content": "   "}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	text := resultText(t, result)
	assert.NotContains(t, text, "ollama", "an empty-input rejection must not be misattributed to the backend")
	assert.Contains(t, text, "empty")
}

// --- mcpIngestDocument ---

func TestMcpIngestDocument_MissingFilePathNamesKey(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpIngestDocument(b, &config.Config{})

	result, err := handler(context.Background(), toolRequest(map[string]any{}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError)
	assert.Contains(t, resultText(t, result), "file_path", "rejection must name the missing key")
}
