package version

import (
	"bytes"
	"errors"
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
		{"version flag not in first position does not trigger", []string{"foo", "--version"}, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			got, err := HandleFlag(tt.args, &buf)
			if got != tt.wantHandled {
				t.Errorf("HandleFlag(%v) handled = %v, want %v", tt.args, got, tt.wantHandled)
			}
			if err != nil {
				t.Errorf("HandleFlag(%v) err = %v, want nil (writer never fails in this table)", tt.args, err)
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
	handled, err := HandleFlag([]string{"--version"}, &buf)
	if !handled {
		t.Fatal("HandleFlag([--version]) handled = false, want true")
	}
	if err != nil {
		t.Fatalf("HandleFlag([--version]) err = %v, want nil", err)
	}

	want := "v0.0.0-handleflag-format-test\n"
	if got := buf.String(); got != want {
		t.Errorf("HandleFlag output = %q, want %q", got, want)
	}
}

// failingWriter always fails the write, simulating a broken stdout pipe (a
// closed pipe, a full disk) to prove HandleFlag surfaces the failure instead
// of swallowing it: the exact silent-failure risk Wren flagged in review.
type failingWriter struct{}

var errFailingWriter = errors.New("simulated write failure")

func (failingWriter) Write([]byte) (int, error) {
	return 0, errFailingWriter
}

// TestHandleFlag_WriteFailureIsSignaled asserts that when the flag is
// recognized but the write fails, HandleFlag reports handled=true AND a
// non-nil, wrapped error. A caller that checks handled without checking err
// would exit 0 on a failed version print, indistinguishable from success to
// anything (like the Phase 2 installer) that only checks the exit code.
func TestHandleFlag_WriteFailureIsSignaled(t *testing.T) {
	handled, err := HandleFlag([]string{"--version"}, failingWriter{})
	if !handled {
		t.Fatal("HandleFlag([--version], failingWriter) handled = false, want true")
	}
	if err == nil {
		t.Fatal("HandleFlag([--version], failingWriter) err = nil, want a wrapped write error")
	}
	if !errors.Is(err, errFailingWriter) {
		t.Errorf("HandleFlag error = %v, want it to wrap %v", err, errFailingWriter)
	}
}
