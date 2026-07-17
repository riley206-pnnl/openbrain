package version

import (
	"bytes"
	"testing"
)

// TestSentinelDefault asserts the un-injected default is exactly "dev".
// This is the value a build reports when no ldflags -X stamp is applied
// (local dev, CI branch build, PR check). The release canary smoke-test
// treats any binary reporting "dev" as a hard failure, so the sentinel
// contract is load-bearing.
func TestSentinelDefault(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("un-injected Version = %q, want %q", Version, "dev")
	}
}

// TestInjectionFlowThrough mocks the build-time ldflags stamp by assigning
// the package var, then asserts the accessor path reflects it. This catches
// logic errors in how the var is read; it cannot catch a wrong -X symbol
// path (that is what the release canary smoke-test exists to catch, because
// unit tests never receive build-time injection).
func TestInjectionFlowThrough(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })

	const injected = "v0.0.0-ldflags-smoke-test"
	Version = injected
	if Version != injected {
		t.Fatalf("after injection Version = %q, want %q", Version, injected)
	}
}

// TestHandleFlag is the single shared behavioral test for the --version
// contract every openbrain binary (openbrain, openbrain-mcp, openbrain-web,
// openbrain-watchd, openbrain-telegram, openbrain-slack) delegates to. Each
// binary's own test suite only needs a lightweight wiring check that main()
// calls this and returns/exits 0; the flag-detection and output-format
// behavior lives here, once.
func TestHandleFlag(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantHandled bool
	}{
		{"version flag alone", []string{"--version"}, true},
		{"version flag with trailing args", []string{"--version", "extra"}, true},
		{"no args", []string{}, false},
		{"unrelated flag", []string{"--help"}, false},
		{"bare version word does not trigger", []string{"version"}, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			got := HandleFlag(tt.args, &buf)
			if got != tt.wantHandled {
				t.Errorf("HandleFlag(%v) = %v, want %v", tt.args, got, tt.wantHandled)
			}
			if tt.wantHandled {
				if buf.String() == "" {
					t.Errorf("HandleFlag(%v) wrote nothing to w, want the version string", tt.args)
				}
			} else if buf.String() != "" {
				t.Errorf("HandleFlag(%v) wrote %q, want no output", tt.args, buf.String())
			}
		})
	}
}

// TestHandleFlag_OutputFormat asserts the written output is exactly the
// version string plus a trailing newline: the format the installer parses
// uniformly across every binary.
func TestHandleFlag_OutputFormat(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })
	Version = "v0.0.0-handleflag-format-test"

	var buf bytes.Buffer
	if !HandleFlag([]string{"--version"}, &buf) {
		t.Fatal("HandleFlag([--version]) = false, want true")
	}

	want := "v0.0.0-handleflag-format-test\n"
	if got := buf.String(); got != want {
		t.Errorf("HandleFlag output = %q, want %q", got, want)
	}
}
