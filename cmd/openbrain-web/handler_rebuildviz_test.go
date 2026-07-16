package main

import (
	"context"
	"encoding/json"
	"errors"
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

// TestApiRebuildViz_UsesConfiguredPythonInterpreter confirms runVizRebuild
// invokes cfg.VizPythonPath rather than a hardcoded "python3", by pointing
// VizPythonPath at a nonexistent interpreter path and asserting the job
// fails with that exact path in the error. If the exec call ignored the
// config field and fell back to "python3" off PATH, this would either
// succeed (wrong interpreter, no error) or fail with a different message.
func TestApiRebuildViz_UsesConfiguredPythonInterpreter(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("all clusters labeled via LLM")`)
	cfg.VizPythonPath = "/nonexistent/does-not-exist/python3-viz-venv"

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
	require.Error(t, lastErr)
	assert.Contains(t, lastErr.Error(), "does-not-exist",
		"error must reference the configured interpreter path, proving it was actually used")
}

// TestApiRebuildViz_DefaultsToPython3WhenInterpreterUnset confirms a Config
// with VizPythonPath left unset (the zero value, as every other test in this
// file constructs it) still resolves the real "python3" and runs the fake
// script successfully, i.e. zero behavior change from before this field
// existed.
func TestApiRebuildViz_DefaultsToPython3WhenInterpreterUnset(t *testing.T) {
	cfg := rebuildVizCfg(t, `print("all clusters labeled via LLM")`)
	require.Empty(t, cfg.VizPythonPath, "test setup: VizPythonPath must be unset to exercise the default")

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
	assert.NoError(t, lastErr)
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

// ── review fix: reset stale state on re-trigger ─────────────────────────

// TestApiRebuildViz_ReTriggerAfterCompletion_ResetsStaleState drives a full
// rebuild to completion (degraded, so both lastErr==nil and degraded==true
// are non-zero-value), then triggers a SECOND rebuild and confirms
// tryStart succeeds again AND /status no longer reports the PRIOR run's
// degraded flag or stale progress while the new run is in flight: the
// second run's job state must start clean, not carry the first run's
// leftovers forward.
func TestApiRebuildViz_ReTriggerAfterCompletion_ResetsStaleState(t *testing.T) {
	invocationLog := filepath.Join(t.TempDir(), "invocations.log")
	lockFile := filepath.Join(t.TempDir(), "release.lock")
	outputPath := filepath.Join(t.TempDir(), "brain.json")

	// First run: degraded, so job.degraded lands on true and the sidecar is
	// left at pct:100/phase:done.
	firstScript := writeFakeVizScript(t, invocationLog, `
with open(`+pyStr(vizProgressPath(outputPath))+`, 'w') as f:
    f.write('{"pct":100,"phase":"done","clusters_done":3,"clusters_total":3}')
print("BRAIN_VIZ_DEGRADED=true")
`)
	cfg := &config.Config{VizScriptPath: firstScript, VizOutputPath: outputPath}
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req1 := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr1 := httptest.NewRecorder()
	h(rr1, req1)
	require.Equal(t, http.StatusAccepted, rr1.Code)
	waitForJobDone(t, job)

	firstStatus := getRebuildVizStatus(t, cfg, job)
	require.Equal(t, "done", firstStatus.State)
	require.True(t, firstStatus.Degraded, "test setup: first run must be degraded")
	require.Equal(t, 100, firstStatus.Pct, "test setup: first run must leave pct:100 in the sidecar")

	// Second run: a clean (non-degraded) build that blocks on lockFile so we
	// can observe /status WHILE it is running, before it has written its own
	// first progress update.
	cfg.VizScriptPath = writeFakeVizScript(t, invocationLog, `
import time
deadline = time.time() + 5
while not os.path.exists(`+pyStr(lockFile)+`):
    if time.time() > deadline:
        raise SystemExit("test lock file never appeared")
    time.sleep(0.01)
print("all clusters labeled via LLM")
`)

	req2 := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr2 := httptest.NewRecorder()
	h(rr2, req2)
	require.Equal(t, http.StatusAccepted, rr2.Code, "tryStart must succeed again after the first run completed")

	midRunStatus := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "running", midRunStatus.State)
	assert.False(t, midRunStatus.Degraded, "the second run's mid-flight status must not carry the first run's degraded=true forward")
	assert.Zero(t, midRunStatus.Pct, "the second run's mid-flight status must not carry the first run's stale pct:100 forward")
	assert.Empty(t, midRunStatus.Phase, "the second run's mid-flight status must not carry the first run's stale phase:done forward")

	require.NoError(t, os.WriteFile(lockFile, []byte("go"), 0o644))
	waitForJobDone(t, job)

	finalStatus := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "done", finalStatus.State)
	assert.False(t, finalStatus.Degraded, "the second (clean) run must report degraded:false, not the first run's true")
}

// ── review fix: TTL boundary is >, not >= ────────────────────────────────

// TestVizIsStale_TTLBoundary asserts the staleness check's boundary
// condition directly (elapsed == ttl exactly), which is not reliably
// reproducible through a real file mtime and wall-clock time.Since call:
// by the time the check runs, elapsed is always at least a few nanoseconds
// past whatever offset the test set up. Exercising the pure comparison
// function pins the boundary regardless of wall-clock jitter.
func TestVizIsStale_TTLBoundary(t *testing.T) {
	ttl := 24 * time.Hour

	tests := []struct {
		name    string
		elapsed time.Duration
		want    bool
	}{
		{"elapsed exactly equals ttl: not stale", ttl, false},
		{"elapsed one nanosecond under ttl: not stale", ttl - 1, false},
		{"elapsed one nanosecond over ttl: stale", ttl + 1, true},
		{"ttl disabled (0): never stale even with huge elapsed", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			effectiveTTL := ttl
			if tt.name == "ttl disabled (0): never stale even with huge elapsed" {
				effectiveTTL = 0
				tt.elapsed = 30 * 24 * time.Hour
			}
			assert.Equal(t, tt.want, vizIsStale(effectiveTTL, tt.elapsed))
		})
	}
}

// ── review fix: bounded failure-output tail surfaced in /status error ───

// TestApiRebuildViz_FailureOutput_TailSurfacedInStatusError confirms a
// failing script's captured output (not just the bare process error like
// "exit status 1") flows through into /status's error field, bounded, so an
// operator can see WHY it failed (missing deps, DB down, Ollama down)
// without reading the server log.
func TestApiRebuildViz_FailureOutput_TailSurfacedInStatusError(t *testing.T) {
	const marker = "OPENBRAIN_TEST_MARKER_missing_dependency_numpy"
	cfg := rebuildVizCfg(t, `import sys
print("`+marker+`")
sys.exit(1)
`)
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	waitForJobDone(t, job)
	body := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "error", body.State)
	assert.Contains(t, body.Error, marker, "the failure's captured stdout must be surfaced in the status error field, not just the bare exit code")
}

// TestBoundedOutputTail_CapsLength confirms the tail helper never returns
// more than vizOutputTailMaxLen bytes of payload, even when the captured
// output is far larger (a runaway script must not turn /status into an
// unbounded log dump).
func TestBoundedOutputTail_CapsLength(t *testing.T) {
	huge := strings.Repeat("x", vizOutputTailMaxLen*10)
	tail := boundedOutputTail([]byte(huge))
	assert.LessOrEqual(t, len(tail), vizOutputTailMaxLen+len("...(truncated)...\n"))
	assert.Contains(t, tail, "truncated")
}

// TestBoundedOutputTail_KeepsEndOfOutput confirms truncation preserves the
// END of the captured output (the traceback / final error line is almost
// always last), not the start.
func TestBoundedOutputTail_KeepsEndOfOutput(t *testing.T) {
	huge := strings.Repeat("x", vizOutputTailMaxLen*10) + "FINAL_ERROR_LINE"
	tail := boundedOutputTail([]byte(huge))
	assert.Contains(t, tail, "FINAL_ERROR_LINE")
}

// TestBoundedOutputTail_RedactsCredentialLikeLines confirms a line that
// looks like a DSN or URL with embedded credentials (scheme://user:pass@host)
// has the credential portion redacted, mirroring internal/db.redactDSN,
// while ordinary Python traceback lines pass through unchanged.
func TestBoundedOutputTail_RedactsCredentialLikeLines(t *testing.T) {
	out := "connecting to postgres://openbrain:s3cr3t@db.internal:5432/openbrain\n" +
		"Traceback (most recent call last):\n" +
		"  File \"build-brain-viz.py\", line 42, in <module>\n" +
		"ConnectionError: could not connect\n"
	tail := boundedOutputTail([]byte(out))
	assert.NotContains(t, tail, "s3cr3t", "a credential embedded in a DSN-like line must not be echoed verbatim")
	assert.Contains(t, tail, "postgres://***@db.internal:5432/openbrain")
	assert.Contains(t, tail, "ConnectionError: could not connect")
}

// TestBoundedOutputTail_RedactsLibpqPasswordKeyValue confirms a libpq
// keyword/value credential token (password=... or pwd=..., the form
// build-brain-viz.py actually builds its conninfo string in, never a URL) has
// its value redacted, while unrelated keyword/value pairs on the same line
// pass through unchanged.
func TestBoundedOutputTail_RedactsLibpqPasswordKeyValue(t *testing.T) {
	out := "connecting with host=db.internal user=openbrain password=hunter2 sslmode=disable\n"
	tail := boundedOutputTail([]byte(out))
	assert.NotContains(t, tail, "hunter2", "a libpq password= value must not be echoed verbatim")
	assert.Contains(t, tail, "password=[REDACTED]")
	assert.Contains(t, tail, "host=db.internal", "unrelated keyword/value pairs must not be touched")
	assert.Contains(t, tail, "sslmode=disable", "unrelated keyword/value pairs must not be touched")
}

// TestBoundedOutputTail_RedactsLibpqPwdKeyValue confirms the "pwd=" alias is
// also redacted, case-insensitively.
func TestBoundedOutputTail_RedactsLibpqPwdKeyValue(t *testing.T) {
	out := "PWD=s3cr3t user=openbrain\n"
	tail := boundedOutputTail([]byte(out))
	assert.NotContains(t, tail, "s3cr3t")
	assert.Contains(t, tail, "[REDACTED]")
}

// TestBoundedOutputTail_RedactsMalformedConninfoPasswordFragment pins the
// concrete leak this fix closes: OPENBRAIN_DB_PASSWORD containing a space
// (e.g. a diceware passphrase) makes build-brain-viz.py's unquoted
// "password=<value>" conninfo construction unparsable by psycopg, which
// raises psycopg.ProgrammingError: missing "=" after "<word>" in connection
// info string, and (per the real failure) the preceding captured output
// contains the literal password=<value> assignment with the spaced value.
//
// A spaced, unquoted password cannot be fully bounded by a keyword/value
// regex (libpq's own grammar can't tell where an unquoted value ends either:
// that's exactly why it raised the parse error), so this test pins the
// documented minimum: the token contiguous to "password=" is scrubbed. Words
// after the first space are not distinguishable from any other connection
// keyword and are accepted as a known limitation (see libpqCredentialRe's
// doc comment).
func TestBoundedOutputTail_RedactsMalformedConninfoPasswordFragment(t *testing.T) {
	out := "building conninfo: host=localhost user=openbrain password=correct horse battery staple sslmode=disable\n" +
		"psycopg.ProgrammingError: missing \"=\" after \"horse\" in connection info string\n"
	tail := boundedOutputTail([]byte(out))
	assert.NotContains(t, tail, "password=correct", "the token contiguous to password= must be scrubbed")
	assert.Contains(t, tail, "password=[REDACTED]")
	assert.Contains(t, tail, "missing \"=\" after \"horse\" in connection info string", "the error message itself is not a credential and must survive so the operator can diagnose it")
}

// ── review fix: distinguish the 5-minute rebuild timeout ────────────────

// TestClassifyVizRebuildFailure_DeadlineExceeded_DistinctMessage unit-tests
// the classification helper directly with a synthetic context.DeadlineExceeded,
// rather than waiting out a real timeout: the helper is the single seam
// runVizRebuild calls into, so this pins the branch without a slow test.
func TestClassifyVizRebuildFailure_DeadlineExceeded_DistinctMessage(t *testing.T) {
	cmdErr := errors.New("signal: killed")
	got := classifyVizRebuildFailure(cmdErr, context.DeadlineExceeded, []byte("some partial output"), 5*time.Minute)
	assert.ErrorContains(t, got, "timed out")
	assert.NotContains(t, got.Error(), "signal: killed", "the opaque exec-killed message must not leak through when the real cause is a timeout")
}

// TestClassifyVizRebuildFailure_OrdinaryFailure_IncludesOutputTail confirms
// the non-timeout branch still wraps the process error and appends the
// bounded output tail.
func TestClassifyVizRebuildFailure_OrdinaryFailure_IncludesOutputTail(t *testing.T) {
	cmdErr := errors.New("exit status 1")
	got := classifyVizRebuildFailure(cmdErr, nil, []byte("ModuleNotFoundError: No module named 'numpy'"), 5*time.Minute)
	assert.ErrorContains(t, got, "exit status 1")
	assert.ErrorContains(t, got, "ModuleNotFoundError")
}

// TestApiRebuildViz_RealTimeout_ReportsDistinctError drives an actual
// context deadline through runVizRebuild end-to-end (not just the
// classify-helper unit test above), using cfg.VizRebuildTimeout overridden
// to a few milliseconds so the test stays fast.
func TestApiRebuildViz_RealTimeout_ReportsDistinctError(t *testing.T) {
	cfg := rebuildVizCfg(t, `
import time
time.sleep(5)
print("should never get here")
`)
	cfg.VizRebuildTimeout = 50 * time.Millisecond
	job := &vizJobState{}
	h := apiRebuildViz(cfg, job)

	req := httptest.NewRequest(http.MethodPost, "/api/rebuild-viz", nil)
	rr := httptest.NewRecorder()
	h(rr, req)
	require.Equal(t, http.StatusAccepted, rr.Code)

	waitForJobDone(t, job)
	body := getRebuildVizStatus(t, cfg, job)
	assert.Equal(t, "error", body.State)
	assert.Contains(t, body.Error, "timed out")
}
