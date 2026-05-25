package extract

import (
	"context"
	"errors"
	"testing"

	wrsllm "github.com/windingriverholdings/wrs-llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider records the (prompt, system) it was called with and returns a
// canned response or error. It implements wrsllm.Provider.
type fakeProvider struct {
	name      string
	resp      string
	err       error
	callCount int
}

func (f *fakeProvider) Generate(_ context.Context, _, _ string) (string, error) {
	f.callCount++
	if f.err != nil {
		return "", f.err
	}
	return f.resp, nil
}

// TestSelectProviderTwoTier pins the EXACT two-tier behavior post-migration:
// short/simple text -> fast provider; long or correction-heavy text -> primary.
// An empty fast model (or fast==primary) means there is no fast tier.
func TestSelectProviderTwoTier(t *testing.T) {
	fast := &fakeProvider{name: "fast"}
	primary := &fakeProvider{name: "primary"}

	tests := []struct {
		name      string
		hasFast   bool
		text      string
		threshold int
		want      *fakeProvider
	}{
		{"short text uses fast", true, "short note", 500, fast},
		{"correction text uses primary", true, "actually, we switched to Valkey", 500, primary},
		{"long text uses primary", true, makeLong(501), 500, primary},
		{"boundary at threshold uses fast", true, makeLong(500), 500, fast},
		{"empty text uses primary", true, "", 500, primary},
		{"no fast tier always primary", false, "short note", 500, primary},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel := &tierSelector{
				primary:   primary,
				fast:      fast,
				hasFast:   tt.hasFast,
				threshold: tt.threshold,
			}
			got, err := sel.selectProvider(tt.text)
			require.NoError(t, err)
			assert.Same(t, tt.want, got, "tier selection mismatch")
		})
	}
}

// TestSelectProviderNoneReturnsNil pins that the "none" provider mode yields a
// nil provider (extraction disabled), exactly as before the migration.
func TestSelectProviderNoneReturnsNil(t *testing.T) {
	sel := &tierSelector{} // zero value: no primary, no fast
	got, err := sel.selectProvider("anything")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestExtractThoughtsPropagatesError pins that a provider error (including
// wrsllm.ErrEmptyCompletion, which is now an explicit failure) propagates out
// of ExtractThoughts rather than being swallowed.
func TestExtractThoughtsEmptyCompletionPropagates(t *testing.T) {
	empty := &fakeProvider{name: "primary", err: wrsllm.ErrEmptyCompletion}
	restore := setSelectorForTest(&tierSelector{primary: empty, threshold: 500})
	defer restore()

	_, err := ExtractThoughts(context.Background(), "some text")
	require.Error(t, err)
	assert.True(t, errors.Is(err, wrsllm.ErrEmptyCompletion), "expected ErrEmptyCompletion to propagate, got %v", err)
	assert.Equal(t, 1, empty.callCount)
}

// TestExtractThoughtsNoneDisabled pins that when no provider is configured
// (none mode), ExtractThoughts returns (nil, nil) — the deep-extract caller
// then falls back to simple capture.
func TestExtractThoughtsNoneDisabled(t *testing.T) {
	restore := setSelectorForTest(&tierSelector{}) // no providers
	defer restore()

	got, err := ExtractThoughts(context.Background(), "some text")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestExtractThoughtsHappyPath pins that a successful generation flows through
// ParseExtractionResponse.
func TestExtractThoughtsHappyPath(t *testing.T) {
	resp := `[{"content":"Decided to use Go","thought_type":"decision","tags":["lang"],"subjects":["Go"]}]`
	ok := &fakeProvider{name: "primary", resp: resp}
	restore := setSelectorForTest(&tierSelector{primary: ok, threshold: 500})
	defer restore()

	got, err := ExtractThoughts(context.Background(), "we decided to use Go")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Decided to use Go", got[0].Content)
	assert.Equal(t, "decision", got[0].ThoughtType)
}

func makeLong(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return string(b)
}
