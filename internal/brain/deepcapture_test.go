package brain

import (
	"context"
	"strings"
	"testing"

	wrsllm "github.com/windingriverholdings/wrs-llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/extract"
	"github.com/windingriverholdings/openbrain/internal/intent"
)

// TestDeepCapture_ExtractionFailureIsLoud pins the loud-fallback contract:
// when ExtractThoughts returns a real error (e.g. ErrEmptyCompletion from an
// Ollama outage or empty completion), DeepCapture must STILL persist the raw
// note via simple capture (data preservation) but the returned string MUST
// signal that extraction failed — never a clean success that hides the
// degradation from the user.
func TestDeepCapture_ExtractionFailureIsLoud(t *testing.T) {
	b := New(nil, nil, nil)

	// Force ExtractThoughts to fail the way the wrs-llm migration now surfaces
	// provider failures (HTTP 200 + empty body, 5xx, decode error, etc.).
	b.extractFn = func(_ context.Context, _ string) ([]extract.Candidate, error) {
		return nil, wrsllm.ErrEmptyCompletion
	}

	// Stub the fallback simple-capture so the test needs no DB. Record that it
	// fired (proves the raw note WAS persisted) and return the exact string
	// format a real Capture produces.
	var fallbackFired bool
	b.captureFn = func(_ context.Context, p intent.ParsedIntent, source string) (string, error) {
		fallbackFired = true
		return "Captured [note] deadbeef (cli)", nil
	}

	parsed := intent.ParsedIntent{Text: "a long note that should have been split into many thoughts", ThoughtType: "note"}
	result, err := b.DeepCapture(context.Background(), parsed, "cli")

	require.NoError(t, err, "data is preserved, so no error is returned")

	// (a) The raw note WAS persisted (fallback happened).
	assert.True(t, fallbackFired, "expected fallback simple-capture to persist the raw note")

	// (b) The returned string must SIGNAL extraction failure — not a clean
	// 'Extracted N thoughts' / bare 'Captured' success.
	assert.NotEqual(t, "Captured [note] deadbeef (cli)", result,
		"return string must be annotated, not the bare clean-capture string")
	assert.Contains(t, strings.ToLower(result), "extraction failed",
		"return string must tell the user extraction failed")
	assert.Contains(t, strings.ToLower(result), "single note",
		"return string must tell the user the input was stored as a single note")
	assert.Contains(t, result, "Captured [note] deadbeef (cli)",
		"the underlying capture confirmation should still be surfaced")
}
