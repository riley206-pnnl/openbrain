package main

import (
	"os"
	"os/exec"
	"testing"

	"github.com/windingriverholdings/openbrain/internal/version"
)

// TestVersionFlagWiring re-execs this compiled test binary as the real
// binary (via TestMain's helper-process intercept) with a single
// "--version" argument and a FULLY bare environment (no PATH, no HOME, no
// OPENBRAIN_DB_PASSWORD, nothing but the helper-process sentinel), proving
// main() delegates to version.HandleFlag before any config load, DB
// connection, or other startup work. version.TestHandleFlag in
// internal/version owns the flag-detection and output-format behavior; this
// test owns only the wiring: that THIS binary's main() actually calls it
// first.
//
// This test runs main() in a CHILD process, so Go's coverage instrumentation
// does not credit this package's coverage percentage for main()'s body (a
// known limitation: coverage only counts code executed in-process). CI does
// not gate cmd/ package coverage: .github/workflows/test.yml runs plain
// `go test -race -count=1 ./...` with no -coverprofile and no threshold, and
// the Makefile's own test-cover/ci targets scope coverage to ./internal/...
// only, deliberately excluding ./cmd/... The behavior itself IS exercised
// end to end by this subprocess; the metric drop is a tooling artifact, not
// a coverage gap.
func TestVersionFlagWiring(t *testing.T) {
	t.Parallel()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	cmd := exec.Command(self, "--", "--version")
	cmd.Env = []string{openbrainTestHelperMainEnv + "=1"}

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running %s --version with a bare env: %v", self, err)
	}

	want := version.Version + "\n"
	if got := string(out); got != want {
		t.Errorf("%s --version output = %q, want %q", self, got, want)
	}
}
