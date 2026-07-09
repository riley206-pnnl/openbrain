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

// TestMcpSupersede_ExplicitEmptySupersedesQueryIsRefusedLoud is the Wren
// MEDIUM follow-up: an explicitly present but empty supersedes_query must be
// refused by the same guard a whitespace-only value already hits, not
// silently fall back to the absent-key default (searching by the new
// content's own embedding). content is valid so its own embed succeeds via
// stubEmbedder; the rejection must happen in resolveSupersedeTarget, before
// any search or supersede DB call runs (b.pool is nil here, so reaching
// either would panic instead of returning a clean error).
func TestMcpSupersede_ExplicitEmptySupersedesQueryIsRefusedLoud(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, &config.Config{})
	handler := mcpSupersede(b)

	result, err := handler(context.Background(), toolRequest(map[string]any{
		"content":          "valid new content",
		"supersedes_query": "",
	}))
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError, "an explicit empty supersedes_query must be refused, not silently ignored")
	text := resultText(t, result)
	assert.NotContains(t, text, "ollama")
	assert.Contains(t, text, "empty")
}

// --- supersedeQueryArg ---

func TestSupersedeQueryArg_AbsentKeyReturnsNil(t *testing.T) {
	q := supersedeQueryArg(map[string]any{}, "supersedes_query")
	assert.Nil(t, q, "an absent key must fall back silently, so it must stay nil")
}

func TestSupersedeQueryArg_PresentEmptyStringReturnsNonNilPointer(t *testing.T) {
	q := supersedeQueryArg(map[string]any{"supersedes_query": ""}, "supersedes_query")
	require.NotNil(t, q, "an explicitly present empty string must not collapse to the absent-key case")
	assert.Equal(t, "", *q)
}

func TestSupersedeQueryArg_PresentWhitespaceReturnsPointer(t *testing.T) {
	q := supersedeQueryArg(map[string]any{"supersedes_query": "   "}, "supersedes_query")
	require.NotNil(t, q)
	assert.Equal(t, "   ", *q)
}

func TestSupersedeQueryArg_PresentNonEmptyReturnsPointer(t *testing.T) {
	q := supersedeQueryArg(map[string]any{"supersedes_query": "find this"}, "supersedes_query")
	require.NotNil(t, q)
	assert.Equal(t, "find this", *q)
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
