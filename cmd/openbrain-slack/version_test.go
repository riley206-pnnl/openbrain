package main

import (
	"bytes"
	"testing"

	"github.com/windingriverholdings/openbrain/internal/version"
)

// These tests assert the --version guard exists and behaves identically to
// cmd/openbrain-mcp's. openbrain-slack is a stub today (no config load in
// main), but it must still self-identify: the installer queries every
// managed binary uniformly, and a silently-ignored --version is a hard
// blocker regardless of whether the stub happens to be safe today.

func TestVersionRequested(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
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
			if got := versionRequested(tt.args); got != tt.want {
				t.Errorf("versionRequested(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

// TestPrintVersion asserts the written output is exactly the version string
// plus a trailing newline, not just that the write didn't fail.
func TestPrintVersion(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	version.Version = "v0.0.0-slack-version-flag-test"

	var buf bytes.Buffer
	printVersion(&buf)

	want := "v0.0.0-slack-version-flag-test\n"
	if got := buf.String(); got != want {
		t.Errorf("printVersion output = %q, want %q", got, want)
	}
}
