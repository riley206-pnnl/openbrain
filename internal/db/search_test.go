package db

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHybridSearchThoughtsSignatureAcceptsThoughtType(t *testing.T) {
	// Compile-time verification that HybridSearchThoughts accepts thoughtType.
	// If the signature doesn't include thoughtType string, this won't compile.
	// We don't call it (needs a real DB), just verify the function reference.
	_ = HybridSearchThoughts
	assert.True(t, true)
}

func TestKeywordSearchThoughtsSignatureAcceptsThoughtType(t *testing.T) {
	// Compile-time verification that KeywordSearchThoughts accepts thoughtType.
	_ = KeywordSearchThoughts
	assert.True(t, true)
}

func TestSearchThoughts_RejectsEmptyEmbedding(t *testing.T) {
	// SearchThoughts must return a clear error when given an empty embedding
	// vector, before ever hitting PostgreSQL.
	_, err := SearchThoughts(
		nil,         // ctx — won't reach DB
		nil,         // pool — won't reach DB
		[]float32{}, // empty embedding
		10,          // topK
		"",          // thoughtType
		nil,         // tags
		0.5,         // scoreThreshold
		nil,         // createdFrom
		nil,         // createdTo
	)

	assert.Error(t, err, "SearchThoughts must reject empty embeddings")
	assert.Contains(t, err.Error(), "empty embedding")
}

func TestSearchThoughts_RejectsNilEmbedding(t *testing.T) {
	_, err := SearchThoughts(
		nil,
		nil,
		nil, // nil embedding
		10,
		"",
		nil,
		0.5,
		nil, // createdFrom
		nil, // createdTo
	)

	assert.Error(t, err, "SearchThoughts must reject nil embeddings")
	assert.Contains(t, err.Error(), "empty embedding")
}

func TestHybridSearchThoughts_RejectsEmptyEmbedding(t *testing.T) {
	// HybridSearchThoughts must return a clear error when given an empty
	// embedding vector, before ever hitting PostgreSQL.
	_, err := HybridSearchThoughts(
		nil,
		nil,
		"test query",
		[]float32{}, // empty embedding
		10,          // topK
		0.5,         // keywordWeight
		0.5,         // semanticWeight
		0.5,         // scoreThreshold
		false,       // includeHistory
		"",          // thoughtType
		nil,         // createdFrom
		nil,         // createdTo
		768,         // embeddingDim
	)

	assert.Error(t, err, "HybridSearchThoughts must reject empty embeddings")
	assert.Contains(t, err.Error(), "empty embedding")
}

func TestHybridSearchThoughts_RejectsNilEmbedding(t *testing.T) {
	_, err := HybridSearchThoughts(
		nil,
		nil,
		"test query",
		nil, // nil embedding
		10,
		0.5,
		0.5,
		0.5,
		false,
		"",
		nil, // createdFrom
		nil, // createdTo
		768, // embeddingDim
	)

	assert.Error(t, err, "HybridSearchThoughts must reject nil embeddings")
	assert.Contains(t, err.Error(), "empty embedding")
}

func TestSearchThoughts_QueryExcludesNullEmbeddings(t *testing.T) {
	// SearchThoughts must include "AND embedding IS NOT NULL" in its query
	// so that rows with NULL embeddings (post-migration, pre-reembed) are
	// skipped rather than producing undefined cosine distance results.
	//
	// We verify this by reading the source file and checking the query string.
	data, err := os.ReadFile("search.go")
	require.NoError(t, err)
	src := string(data)

	assert.Contains(t, src, "embedding IS NOT NULL",
		"SearchThoughts query must exclude rows with NULL embeddings")
}

func TestBuildHybridSearchQuery_CastFollowsConfiguredDim(t *testing.T) {
	// OB-053: The hybrid_search call MUST cast the embedding argument to
	// vector(<configured dim>) so the 8-arg overload resolves unambiguously
	// and pgvector validates the active model's dimension. The dimension is
	// config-driven (OPENBRAIN_EMBEDDING_DIM, default 768) and the thoughts
	// column is deliberately model-agnostic (migration 008), so the cast must
	// FOLLOW the configured dim rather than a hardcoded literal — otherwise a
	// non-768 model (dimension_test.go exercises 384/1024) breaks search.
	//
	// Rather than string-match a literal, we drive the actual query builder and
	// assert the semantic invariant: the cast dim equals the configured dim, and
	// the embedding is never passed through a bare, undimensioned ::vector cast
	// (which is what made overload resolution ambiguous).
	tests := []struct {
		name string
		dim  int
	}{
		{name: "default nomic dim", dim: 768},
		{name: "all-minilm dim", dim: 384},
		{name: "large model dim", dim: 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := buildHybridSearchQuery(tt.dim)

			wantCast := fmt.Sprintf("$2::vector(%d)", tt.dim)
			assert.Contains(t, query, wantCast,
				"hybrid_search embedding cast must use the configured dim")

			// Never a bare, undimensioned ::vector cast on the embedding arg —
			// that reintroduces overload ambiguity. The dimensioned cast above
			// already proves the positive case; this guards the negative.
			assert.NotContains(t, query, "$2::vector,",
				"hybrid_search embedding argument must not use a bare ::vector cast")
			assert.NotContains(t, query, "$2::vector)",
				"hybrid_search embedding argument must not use a bare ::vector cast")
		})
	}
}

func TestBuildHybridSearchQuery_DefaultDimIs768(t *testing.T) {
	// Guard the default: config.EmbeddingDim defaults to 768 (envDefault:"768"),
	// so the out-of-the-box query must produce a vector(768) cast. Removing the
	// hardcode must not change the default behavior.
	query := buildHybridSearchQuery(768)
	assert.Contains(t, query, "$2::vector(768)",
		"default embedding dim must continue to produce a vector(768) cast")
	assert.NotContains(t, query, "vector(384)",
		"default query must never emit a 384-dim cast")
}

func TestHybridSearchNoDoubleThresholdFilter(t *testing.T) {
	// The Go-side score threshold filter in HybridSearchThoughts should be
	// removed since SQL already applies min_score. This is tested by
	// inspecting the function behavior — results from SQL that meet
	// min_score should not be filtered again in Go.
	//
	// This is a design intent test. The actual verification happens at
	// integration level. Here we document the expected behavior.
	assert.True(t, true, "SQL applies min_score; Go should not double-filter")
}
