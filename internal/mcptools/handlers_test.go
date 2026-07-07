package mcptools

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/extract"
)

// stubEmbedder returns a fixed embedding for any text, letting handler tests
// run without a live embedding model.
type stubEmbedder struct{}

func (stubEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{0.1, 0.2, 0.3}, nil
}

func (stubEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

func (stubEmbedder) Dimension() int { return 3 }

// toolRequest builds a minimal mcp.CallToolRequest carrying the given
// arguments, matching the shape request.GetArguments() expects.
func toolRequest(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

// resultText extracts the text of the first content item in a CallToolResult.
func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, result.Content, "expected at least one content item")
	tc, ok := mcp.AsTextContent(result.Content[0])
	require.True(t, ok, "expected text content")
	return tc.Text
}

// --- mcpBulkImport ---

// TestMcpBulkImport_MalformedItemReturnsToolError asserts a non-object item in
// the thoughts array returns an error result (IsError=true), not a toolText
// success result, and never reaches the store.
func TestMcpBulkImport_MalformedItemReturnsToolError(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, nil)
	handler := mcpBulkImport(b)

	req := toolRequest(map[string]any{
		"thoughts": []any{"not an object"},
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError, "a malformed item must return an error result, not a success text result")
}

// TestMcpBulkImport_StoreFailureReturnsToolError asserts a store-layer failure
// (simulated here with raw driver-shaped detail, mirroring what a real pgx
// enum-constraint violation looks like) returns an error result and the raw
// detail never reaches the caller-facing text.
func TestMcpBulkImport_StoreFailureReturnsToolError(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, nil)
	b.SetSeamsForTesting(nil, func(_ context.Context, _ []db.ThoughtInput) ([]string, error) {
		return nil, errors.New(`ERROR: invalid input value for enum thought_type: "bogus" (SQLSTATE 22P02)`)
	})
	handler := mcpBulkImport(b)

	req := toolRequest(map[string]any{
		"thoughts": []any{
			map[string]any{"content": "a valid thought", "thought_type": "note"},
		},
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError, "a store failure must return an error result")

	text := resultText(t, result)
	assert.NotContains(t, text, "SQLSTATE", "raw driver detail must never reach the caller")
	assert.NotContains(t, text, "thought_type", "raw column/enum detail must never reach the caller")
}

// TestMcpBulkImport_HappyPathReturnsCount asserts a well-formed batch returns
// a non-error result reporting the imported count.
func TestMcpBulkImport_HappyPathReturnsCount(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, nil)
	b.SetSeamsForTesting(nil, func(_ context.Context, inputs []db.ThoughtInput) ([]string, error) {
		ids := make([]string, len(inputs))
		for i := range inputs {
			ids[i] = "00000001-0000-0000-0000-000000000000"
		}
		return ids, nil
	})
	handler := mcpBulkImport(b)

	req := toolRequest(map[string]any{
		"thoughts": []any{
			map[string]any{"content": "first", "thought_type": "note"},
			map[string]any{"content": "second", "thought_type": "note"},
		},
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
	assert.Contains(t, resultText(t, result), "2")
}

// --- mcpExtract ---

// TestMcpExtract_AutoCaptureStoreFailureReturnsToolError asserts the
// newly-reachable auto_capture store-failure path (enabled by captureExtracted
// now returning real errors instead of a partial-success string) returns an
// error result with no raw driver detail leaked.
func TestMcpExtract_AutoCaptureStoreFailureReturnsToolError(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, nil)
	b.SetSeamsForTesting(
		func(_ context.Context, _ string) ([]extract.Candidate, error) {
			return []extract.Candidate{{Content: "candidate", ThoughtType: "note"}}, nil
		},
		func(_ context.Context, _ []db.ThoughtInput) ([]string, error) {
			return nil, errors.New(`ERROR: invalid input value for enum thought_type: "bogus" (SQLSTATE 22P02)`)
		},
	)
	handler := mcpExtract(b)

	req := toolRequest(map[string]any{
		"text":         "long input text",
		"auto_capture": true,
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.IsError, "a store failure during auto-capture must return an error result")

	text := resultText(t, result)
	assert.NotContains(t, text, "SQLSTATE", "raw driver detail must never reach the caller")
	assert.NotContains(t, text, "thought_type", "raw column/enum detail must never reach the caller")
}

// TestMcpExtract_AutoCaptureHappyPathReturnsSuccessText asserts a successful
// auto-capture returns a non-error text result.
func TestMcpExtract_AutoCaptureHappyPathReturnsSuccessText(t *testing.T) {
	b := brain.New(nil, stubEmbedder{}, nil)
	b.SetSeamsForTesting(
		func(_ context.Context, _ string) ([]extract.Candidate, error) {
			return []extract.Candidate{{Content: "candidate", ThoughtType: "note"}}, nil
		},
		func(_ context.Context, inputs []db.ThoughtInput) ([]string, error) {
			return []string{"00000001-0000-0000-0000-000000000000"}, nil
		},
	)
	handler := mcpExtract(b)

	req := toolRequest(map[string]any{
		"text":         "long input text",
		"auto_capture": true,
	})
	result, err := handler(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.IsError)
}
