package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
)

// fakeSearchEmbedder returns a fixed-dimension embedding for any input, so
// apiSearchNodes/apiGetThought integration tests exercise the real hybrid
// search SQL without a live Ollama instance. Dimension must match the
// embedding column values inserted by the test (via db.InsertThought) and
// cfg.EmbeddingDim (the value HybridSearchThoughts casts vector(N) to).
type fakeSearchEmbedder struct{ dim int }

func (f fakeSearchEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	v := make([]float32, f.dim)
	for i := range v {
		v[i] = 0.1
	}
	return v, nil
}

func (f fakeSearchEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i], _ = f.Embed(context.Background(), texts[i])
	}
	return out, nil
}

func (f fakeSearchEmbedder) Dimension() int { return f.dim }

// searchIntegrationPool connects to the throwaway test database named by
// OPENBRAIN_TEST_DATABASE_URL, mirroring internal/db's integrationPool
// helper. When the variable is unset, the calling test is skipped so the
// default `go test ./...` stays green without a live Postgres instance.
func searchIntegrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("OPENBRAIN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("OPENBRAIN_TEST_DATABASE_URL not set; skipping DB integration test")
	}
	pool, err := db.NewPool(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

func searchTestConfig() *config.Config {
	return &config.Config{
		EmbeddingDim:            8,
		SearchTopK:              10,
		SearchScoreThreshold:    0.0,
		SearchFilteredThreshold: 0.0,
	}
}

// ── apiSearchNodes ──────────────────────────────────────────────────────────

func TestApiSearchNodes_MissingQuery_Returns400(t *testing.T) {
	b := brain.New(nil, nil, searchTestConfig())
	h := apiSearchNodes(b)

	req := httptest.NewRequest(http.MethodGet, "/api/search/nodes", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestApiSearchNodes_MalformedFromDate_Returns400(t *testing.T) {
	b := brain.New(nil, nil, searchTestConfig())
	h := apiSearchNodes(b)

	req := httptest.NewRequest(http.MethodGet, "/api/search/nodes?q=foo&from=not-a-date", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid from date")
}

func TestApiSearchNodes_MalformedToDate_Returns400(t *testing.T) {
	b := brain.New(nil, nil, searchTestConfig())
	h := apiSearchNodes(b)

	req := httptest.NewRequest(http.MethodGet, "/api/search/nodes?q=foo&to=13/45/2026", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Contains(t, rr.Body.String(), "invalid to date")
}

// TestApiSearchNodes_Success_ReturnsMatchingNode seeds a single, distinctively
// worded thought via a live DB, then confirms the handler's 200 response
// carries that thought's real field values (id, type, tags, summary,
// content) rather than merely returning without error. Requires
// OPENBRAIN_TEST_DATABASE_URL; skips otherwise.
func TestApiSearchNodes_Success_ReturnsMatchingNode(t *testing.T) {
	pool := searchIntegrationPool(t)
	ctx := context.Background()
	cfg := searchTestConfig()
	embedder := fakeSearchEmbedder{dim: cfg.EmbeddingDim}

	source := "handler-search-nodes-test"
	_, _ = pool.Exec(ctx, `DELETE FROM thoughts WHERE source = $1`, source)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM thoughts WHERE source = $1`, source)
	})

	summary := "zephyrquokka summary"
	embedding, err := embedder.Embed(ctx, "zephyrquokka")
	require.NoError(t, err)
	id, err := db.InsertThought(ctx, pool, "the zephyrquokka runs at dawn", embedding,
		"note", []string{"tag-a"}, source, &summary, nil)
	require.NoError(t, err)

	b := brain.New(pool, embedder, cfg)
	h := apiSearchNodes(b)

	req := httptest.NewRequest(http.MethodGet, "/api/search/nodes?q=zephyrquokka", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var results []struct {
		ID      string   `json:"id"`
		Type    string   `json:"type"`
		Tags    []string `json:"tags"`
		Summary string   `json:"summary"`
		Content string   `json:"content"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&results))
	require.NotEmpty(t, results, "expected at least one matching node for the seeded keyword")

	var found *struct {
		ID      string   `json:"id"`
		Type    string   `json:"type"`
		Tags    []string `json:"tags"`
		Summary string   `json:"summary"`
		Content string   `json:"content"`
	}
	for i := range results {
		if results[i].ID == id {
			found = &results[i]
			break
		}
	}
	require.NotNil(t, found, "seeded thought %s must be present in results", id)
	assert.Equal(t, "note", found.Type)
	assert.Equal(t, []string{"tag-a"}, found.Tags)
	assert.Equal(t, summary, found.Summary)
	assert.Equal(t, "the zephyrquokka runs at dawn", found.Content)
}

// ── apiGetThought ────────────────────────────────────────────────────────────

func TestApiGetThought_MissingID_Returns400(t *testing.T) {
	b := brain.New(nil, nil, searchTestConfig())
	h := apiGetThought(b)

	req := httptest.NewRequest(http.MethodGet, "/api/thought/", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestApiGetThought_NotFound_Returns404 asks for a syntactically valid but
// nonexistent UUID against a live (empty-of-that-id) DB, asserting the
// not-found branch specifically, not the query-error branch a malformed
// UUID would otherwise trigger. Requires OPENBRAIN_TEST_DATABASE_URL; skips
// otherwise.
func TestApiGetThought_NotFound_Returns404(t *testing.T) {
	pool := searchIntegrationPool(t)
	cfg := searchTestConfig()
	embedder := fakeSearchEmbedder{dim: cfg.EmbeddingDim}
	b := brain.New(pool, embedder, cfg)
	h := apiGetThought(b)

	// A syntactically valid UUID that db.InsertThought (via gen_random_uuid())
	// will not have generated, so the query succeeds and returns zero rows,
	// exercising the not-found branch rather than the query-error branch a
	// malformed UUID string would trigger.
	missingID := "00000000-0000-0000-0000-000000000000"
	req := httptest.NewRequest(http.MethodGet, "/api/thought/"+missingID, nil)
	req.URL.Path = "/api/thought/" + missingID
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestApiGetThought_Success_ReturnsBodyShape seeds a known thought via a live
// DB, then confirms the handler's 200 response carries the exact seeded
// field values, not just a 200 status. Requires OPENBRAIN_TEST_DATABASE_URL;
// skips otherwise.
func TestApiGetThought_Success_ReturnsBodyShape(t *testing.T) {
	pool := searchIntegrationPool(t)
	ctx := context.Background()
	cfg := searchTestConfig()
	embedder := fakeSearchEmbedder{dim: cfg.EmbeddingDim}

	source := "handler-get-thought-test"
	_, _ = pool.Exec(ctx, `DELETE FROM thoughts WHERE source = $1`, source)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM thoughts WHERE source = $1`, source)
	})

	summary := "detail panel summary"
	embedding, err := embedder.Embed(ctx, "detail panel content")
	require.NoError(t, err)
	id, err := db.InsertThought(ctx, pool, "detail panel content body", embedding,
		"idea", []string{"tag-b", "tag-c"}, source, &summary, nil)
	require.NoError(t, err)

	b := brain.New(pool, embedder, cfg)
	h := apiGetThought(b)

	req := httptest.NewRequest(http.MethodGet, "/api/thought/"+id, nil)
	req.URL.Path = "/api/thought/" + id
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		ID      string   `json:"id"`
		Type    string   `json:"type"`
		Tags    []string `json:"tags"`
		Source  string   `json:"source"`
		Summary string   `json:"summary"`
		Content string   `json:"content"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))

	assert.Equal(t, id, body.ID)
	assert.Equal(t, "idea", body.Type)
	assert.Equal(t, []string{"tag-b", "tag-c"}, body.Tags)
	assert.Equal(t, source, body.Source)
	assert.Equal(t, summary, body.Summary)
	assert.Equal(t, "detail panel content body", body.Content)
}
