package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/windingriverholdings/openbrain/internal/config"
)

// writeFakeVizScript writes a standalone Python script (no DB, no Ollama, no
// numpy) that stands in for build-brain-viz.py so apiRebuildViz's own
// process-management and output-parsing logic is tested in isolation from
// the real pipeline. It also records each invocation to invocationLog so
// concurrency tests can assert exactly one process ran, not just that the
// second HTTP call returned a particular status (per [[data-invariants]]).
func writeFakeVizScript(t *testing.T, invocationLog, body string) string {
	t.Helper()
	// Every invocation appends a line to invocationLog before running body,
	// so the test can count real process starts regardless of how fast the
	// script itself exits.
	full := "#!/usr/bin/env python3\n" +
		"import os\n" +
		"with open(" + pyStr(invocationLog) + ", 'a') as f:\n" +
		"    f.write('run\\n')\n" +
		body + "\n"
	path := filepath.Join(t.TempDir(), "fake-build-brain-viz.py")
	require.NoError(t, os.WriteFile(path, []byte(full), 0o755))
	return path
}

// pyStr renders a Go string as a Python single-quoted string literal for
// splicing into the tiny fake scripts above. Test-only, and the inputs are
// always t.TempDir() paths, never externally supplied.
func pyStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "\\'") + "'"
}

func rebuildVizCfg(t *testing.T, scriptBody string) *config.Config {
	t.Helper()
	invocationLog := filepath.Join(t.TempDir(), "invocations.log")
	return &config.Config{
		VizScriptPath: writeFakeVizScript(t, invocationLog, scriptBody),
		VizOutputPath: filepath.Join(t.TempDir(), "brain.json"),
	}
}

// waitForJobDone polls job.snapshot until running is false or the deadline
// elapses. The fake scripts in this file are fast (no DB, no Ollama, no
// numpy), so a short bound keeps these tests quick while still tolerating
// scheduler jitter under load.
func waitForJobDone(t *testing.T, job *vizJobState) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running, hasRun, _, _ := job.snapshot()
		if !running && hasRun {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for rebuild job to finish")
}

func TestApiRebuildViz_WrongMethod_Returns405(t *testing.T) {
	cfg := &config.Config{VizScriptPath: "unused", VizOutputPath: "unused"}
	h := apiRebuildViz(cfg, &vizJobState{})

	req := httptest.NewRequest(http.MethodGet, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestApiRebuildViz_MissingVizConfig_Returns503(t *testing.T) {
	cfg := &config.Config{} // VizScriptPath and VizOutputPath both empty
	h := apiRebuildViz(cfg, &vizJobState{})

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// TestApiRebuildViz_Returns202AndStarted confirms the trigger no longer
// blocks: it returns 202 with {"status":"started"} immediately, before the
// (fast, fake) script has necessarily finished.
func TestApiRebuildViz_Returns202AndStarted(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("all clusters labeled via LLM")`)
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	require.Equal(t, http.StatusAccepted, rr.Code)
	var body struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal(t, "started", body.Status)

	waitForJobDone(t, job)
}

// TestApiRebuildViz_JobTransitionsRunningToDone drives the job state
// directly (not just the HTTP response) through running -> done, and
// confirms the degraded flag lands correctly for a clean build.
func TestApiRebuildViz_JobTransitionsRunningToDone(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("all clusters labeled via LLM")`)
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	running, _, _, _ := job.snapshot()
	// The goroutine may already have finished by the time we check (fast
	// fake script); what matters is it eventually reaches done, not that we
	// catch it mid-flight.
	_ = running

	waitForJobDone(t, job)
	running, hasRun, degraded, lastErr := job.snapshot()
	assert.False(t, running)
	assert.True(t, hasRun)
	assert.False(t, degraded)
	assert.NoError(t, lastErr)
}

// TestApiRebuildViz_DegradedBuild_ReflectedInJobState is the item-1
// regression test carried forward into the async contract: a build that
// fell back to heuristic labels for every cluster still exits 0 (a valid
// map was written), and the job state must surface that as degraded=true,
// visible via the status endpoint, rather than either swallowing it or
// failing the rebuild.
func TestApiRebuildViz_DegradedBuild_ReflectedInJobState(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("some setup output")
print("BRAIN_VIZ_DEGRADED=true")
`)
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	waitForJobDone(t, job)
	_, hasRun, degraded, lastErr := job.snapshot()
	assert.True(t, hasRun)
	assert.True(t, degraded, "job state must surface degraded=true when the script printed the heuristic-fallback marker")
	assert.NoError(t, lastErr)
}

// TestApiRebuildViz_ScriptFailure_JobStateHoldsError confirms a genuinely
// failing script (nonzero exit) is recorded as a job-state error: the
// degraded-signal handling must not change the existing failure branch, and
// the failure is surfaced via /status (state: error), not an HTTP 500 on the
// trigger call (the trigger has already returned 202 by the time the script
// fails).
func TestApiRebuildViz_ScriptFailure_JobStateHoldsError(t *testing.T) {
	cfg := rebuildVizCfg(t, `import sys
print("boom")
sys.exit(1)
`)
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	waitForJobDone(t, job)
	_, hasRun, degraded, lastErr := job.snapshot()
	assert.True(t, hasRun)
	assert.False(t, degraded)
	assert.Error(t, lastErr)
}

// TestApiRebuildViz_ConcurrentTrigger_Returns409AndRunsOnce is the
// concurrency-guard regression test the plan calls out explicitly: a second
// POST while a rebuild is in flight must return 409 AND must not spawn a
// second build-brain-viz.py process. Asserting only the HTTP status would
// pass even if both processes ran; the invocation-log line count is the
// real assertion.
func TestApiRebuildViz_ConcurrentTrigger_Returns409AndRunsOnce(t *testing.T) {
	invocationLog := filepath.Join(t.TempDir(), "invocations.log")
	// The fake script blocks on a lock file until the test releases it, so
	// the first goroutine is guaranteed still "running" when the second POST
	// arrives.
	lockFile := filepath.Join(t.TempDir(), "release.lock")
	scriptPath := writeFakeVizScript(t, invocationLog, `
import time
deadline = time.time() + 5
while not os.path.exists(`+pyStr(lockFile)+`):
    if time.time() > deadline:
        raise SystemExit("test lock file never appeared")
    time.sleep(0.01)
print("all clusters labeled via LLM")
`)
	cfg := &config.Config{
		VizScriptPath: scriptPath,
		VizOutputPath: filepath.Join(t.TempDir(), "brain.json"),
	}
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req1 := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr1 := httptest.NewRecorder()
	h(rr1, req1)
	require.Equal(t, http.StatusAccepted, rr1.Code)

	// Give the goroutine a moment to actually start the process and flip
	// running=true before the second POST races it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		running, _, _, _ := job.snapshot()
		if running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr2 := httptest.NewRecorder()
	h(rr2, req2)
	assert.Equal(t, http.StatusConflict, rr2.Code, "second concurrent POST must return 409")

	// Release the lock so the first (and only) process can exit.
	require.NoError(t, os.WriteFile(lockFile, []byte("go"), 0o644))
	waitForJobDone(t, job)

	data, err := os.ReadFile(invocationLog)
	require.NoError(t, err)
	lines := strings.Count(string(data), "run\n")
	assert.Equal(t, 1, lines, "exactly one build-brain-viz.py process must have run despite two concurrent POSTs")
}

// ── /api/rebuild-viz/status ──────────────────────────────────────────────

func TestApiRebuildVizStatus_WrongMethod_Returns405(t *testing.T) {
	cfg := &config.Config{}
	h := apiRebuildVizStatus(cfg, &vizJobState{})

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz/status", nil)
	rr := httptest.NewRecorder()
	h(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

type rebuildVizStatusBody struct {
	State         string `json:"state"`
	Pct           int    `json:"pct"`
	Phase         string `json:"phase"`
	ClustersDone  int    `json:"clusters_done"`
	ClustersTotal int    `json:"clusters_total"`
	Exists        bool   `json:"exists"`
	Stale         bool   `json:"stale"`
	Degraded      bool   `json:"degraded"`
	Error         string `json:"error"`
}

func getRebuildVizStatus(t *testing.T, cfg *config.Config, job *vizJobState) rebuildVizStatusBody {
	t.Helper()
	h := apiRebuildVizStatus(cfg, job)
	req := httptest.NewRequest(http.MethodGet, "/api/rebuild-viz/status", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	require.Equal(t, http.StatusOK, rr.Code)

	var body rebuildVizStatusBody
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body
}

// TestApiRebuildVizStatus_Idle_MissingFile is the "never built" case: no
// job has run and brain.json does not exist on disk.
func TestApiRebuildVizStatus_Idle_MissingFile(t *testing.T) {
	cfg := &config.Config{
		VizOutputPath: filepath.Join(t.TempDir(), "brain.json"),
		VizTTL:        24 * time.Hour,
	}
	body := getRebuildVizStatus(t, cfg, &vizJobState{})

	assert.Equal(t, "idle", body.State)
	assert.False(t, body.Exists)
	assert.False(t, body.Stale)
	assert.Zero(t, body.Pct)
}

// TestApiRebuildVizStatus_ExistsFresh_NotStale covers a brain.json whose
// mtime is well within the TTL window.
func TestApiRebuildVizStatus_ExistsFresh_NotStale(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "brain.json")
	require.NoError(t, os.WriteFile(outputPath, []byte(`{}`), 0o644))
	cfg := &config.Config{VizOutputPath: outputPath, VizTTL: 24 * time.Hour}

	body := getRebuildVizStatus(t, cfg, &vizJobState{})
	assert.True(t, body.Exists)
	assert.False(t, body.Stale)
}

// TestApiRebuildVizStatus_ExistsOlderThanTTL_IsStale forces the mtime
// backward past the TTL window and confirms stale flips to true.
func TestApiRebuildVizStatus_ExistsOlderThanTTL_IsStale(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "brain.json")
	require.NoError(t, os.WriteFile(outputPath, []byte(`{}`), 0o644))
	oldTime := time.Now().Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(outputPath, oldTime, oldTime))
	cfg := &config.Config{VizOutputPath: outputPath, VizTTL: 24 * time.Hour}

	body := getRebuildVizStatus(t, cfg, &vizJobState{})
	assert.True(t, body.Exists)
	assert.True(t, body.Stale)
}

// TestApiRebuildVizStatus_TTLDisabled_NeverStale confirms VizTTL == 0
// disables the staleness check regardless of file age, per the plan's
// "empty/0 disables staleness" contract.
func TestApiRebuildVizStatus_TTLDisabled_NeverStale(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "brain.json")
	require.NoError(t, os.WriteFile(outputPath, []byte(`{}`), 0o644))
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(outputPath, oldTime, oldTime))
	cfg := &config.Config{VizOutputPath: outputPath, VizTTL: 0}

	body := getRebuildVizStatus(t, cfg, &vizJobState{})
	assert.True(t, body.Exists)
	assert.False(t, body.Stale, "TTL=0 must disable staleness even for a very old file")
}

// TestApiRebuildVizStatus_MissingSidecar_ToleratedAsZeroProgress confirms
// the status endpoint never errors when the progress sidecar has not been
// written yet (no rebuild has started, or phase 2's script hasn't reached
// its first progress write).
func TestApiRebuildVizStatus_MissingSidecar_ToleratedAsZeroProgress(t *testing.T) {
	cfg := &config.Config{VizOutputPath: filepath.Join(t.TempDir(), "brain.json")}
	body := getRebuildVizStatus(t, cfg, &vizJobState{})

	assert.Zero(t, body.Pct)
	assert.Empty(t, body.Phase)
	assert.Zero(t, body.ClustersDone)
	assert.Zero(t, body.ClustersTotal)
}

// TestApiRebuildVizStatus_CorruptSidecar_ToleratedAsZeroProgress confirms a
// partially-written or malformed sidecar (the writer's os.replace raced the
// reader, or was interrupted) degrades to zero-progress rather than a 500.
func TestApiRebuildVizStatus_CorruptSidecar_ToleratedAsZeroProgress(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "brain.json")
	sidecarPath := vizProgressPath(outputPath)
	require.NoError(t, os.WriteFile(sidecarPath, []byte(`{"pct": 42, "phase": "labeling"`), 0o644)) // truncated JSON
	cfg := &config.Config{VizOutputPath: outputPath}

	body := getRebuildVizStatus(t, cfg, &vizJobState{})
	assert.Zero(t, body.Pct)
	assert.Empty(t, body.Phase)
}

// TestApiRebuildVizStatus_ReadsValidSidecar confirms a well-formed sidecar's
// fields flow straight through into the response.
func TestApiRebuildVizStatus_ReadsValidSidecar(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "brain.json")
	sidecarPath := vizProgressPath(outputPath)
	require.NoError(t, os.WriteFile(sidecarPath, []byte(`{"pct":63,"phase":"labeling","clusters_done":5,"clusters_total":8,"ts":"2026-07-15T00:00:00Z"}`), 0o644))
	cfg := &config.Config{VizOutputPath: outputPath}

	body := getRebuildVizStatus(t, cfg, &vizJobState{})
	assert.Equal(t, 63, body.Pct)
	assert.Equal(t, "labeling", body.Phase)
	assert.Equal(t, 5, body.ClustersDone)
	assert.Equal(t, 8, body.ClustersTotal)
}

// TestApiRebuildVizStatus_RunningState confirms the state field reports
// "running" while job.running is true, independent of file/sidecar state.
func TestApiRebuildVizStatus_RunningState(t *testing.T) {
	cfg := &config.Config{VizOutputPath: filepath.Join(t.TempDir(), "brain.json")}
	job := &vizJobState{}
	require.True(t, job.tryStart())

	body := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "running", body.State)
}

// TestApiRebuildVizStatus_ErrorState confirms a failed job reports state
// "error" with the error message surfaced, and that degraded is false (a
// failed build never wrote a valid map).
func TestApiRebuildVizStatus_ErrorState(t *testing.T) {
	cfg := &config.Config{VizOutputPath: filepath.Join(t.TempDir(), "brain.json")}
	job := &vizJobState{}
	require.True(t, job.tryStart())
	job.finish(assert.AnError, false)

	body := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "error", body.State)
	assert.NotEmpty(t, body.Error)
	assert.False(t, body.Degraded)
}

// TestApiRebuildVizStatus_DoneState_Degraded confirms a completed job with
// degraded=true surfaces both state:"done" and degraded:true simultaneously
// (a degraded build is still a successful one; see plan.md risks).
func TestApiRebuildVizStatus_DoneState_Degraded(t *testing.T) {
	cfg := &config.Config{VizOutputPath: filepath.Join(t.TempDir(), "brain.json")}
	job := &vizJobState{}
	require.True(t, job.tryStart())
	job.finish(nil, true)

	body := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "done", body.State)
	assert.True(t, body.Degraded)
	assert.Empty(t, body.Error)
}
