package main

import (
	"os"
	"testing"
)

// openbrainTestHelperMainEnv, when set to "1", tells TestMain to invoke this
// package's real main() directly (with the real program args recovered from
// after "--") instead of running the test suite. TestVersionFlagWiring
// re-execs this same test binary with the env var set and a fully bare
// environment, proving main() reports its version before any config load:
// end to end, not just that the --version branch compiles.
const openbrainTestHelperMainEnv = "OPENBRAIN_TEST_HELPER_MAIN"

// TestMain intercepts the helper-process re-exec used by
// TestVersionFlagWiring. Every other test in this package runs normally.
func TestMain(m *testing.M) {
	if os.Getenv(openbrainTestHelperMainEnv) == "1" {
		for i, a := range os.Args {
			if a == "--" {
				os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
				break
			}
		}
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}
