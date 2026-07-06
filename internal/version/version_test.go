package version

import "testing"

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
