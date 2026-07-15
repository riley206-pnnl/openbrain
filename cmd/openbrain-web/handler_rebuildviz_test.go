package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/config"
)

// writeFakeVizScript writes a standalone Python script (no DB, no Ollama, no
// numpy) that stands in for build-brain-viz.py so apiRebuildViz's own
// output-parsing logic is tested in isolation from the real pipeline.
func writeFakeVizScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-build-brain-viz.py")
	require.NoError(t, os.WriteFile(path, []byte("#!/usr/bin/env python3\n"+body+"\n"), 0o755))
	return path
}

func rebuildVizCfg(t *testing.T, scriptBody string) *config.Config {
	t.Helper()
	return &config.Config{
		VizScriptPath: writeFakeVizScript(t, scriptBody),
		VizOutputPath: filepath.Join(t.TempDir(), "brain.json"),
	}
}

func TestApiRebuildViz_WrongMethod_Returns405(t *testing.T) {
	cfg := &config.Config{VizScriptPath: "unused", VizOutputPath: "unused"}
	h := apiRebuildViz(cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApiRebuildViz_MissingVizConfig_Returns503(t *testing.T) {
	cfg := &config.Config{} // VizScriptPath and VizOutputPath both empty
	h := apiRebuildViz(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// TestApiRebuildViz_DegradedBuild_Returns200WithDegradedTrue is the item-1
// regression test: a build that fell back to heuristic labels for every
// cluster still exits 0 (a valid map was written), and the handler must
// surface that as degraded=true in both the response body and the log,
// rather than either swallowing it (old behavior) or failing the rebuild.
func TestApiRebuildViz_DegradedBuild_Returns200WithDegradedTrue(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("some setup output")
print("BRAIN_VIZ_DEGRADED=true")
`)
	h := apiRebuildViz(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		Status   string `json:"status"`
		Degraded bool   `json:"degraded"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "ok", body.Status)
	assert.True(t, body.Degraded, "response must surface degraded=true when the script printed the heuristic-fallback marker")
}

// TestApiRebuildViz_CleanBuild_Returns200WithDegradedFalse documents the
// non-degraded contract: a normal build with no fallback marker reports
// degraded=false, not merely a 200.
func TestApiRebuildViz_CleanBuild_Returns200WithDegradedFalse(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("all clusters labeled via LLM")
`)
	h := apiRebuildViz(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var body struct {
		Status   string `json:"status"`
		Degraded bool   `json:"degraded"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "ok", body.Status)
	assert.False(t, body.Degraded)
}

// TestApiRebuildViz_ScriptFailure_Returns500 confirms a genuinely failing
// script (nonzero exit) still returns 500: the degraded-signal handling
// must not change the existing failure branch.
func TestApiRebuildViz_ScriptFailure_Returns500(t *testing.T) {
	cfg := rebuildVizCfg(t, `import sys
print("boom")
sys.exit(1)
`)
	h := apiRebuildViz(cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}
