package main

import (
	"os"
	"testing"
)

// testPlaceholderDBPassword is a clearly synthetic value, never a real
// credential, used only to satisfy config.Load's required,notEmpty
// constraint on DBPassword.
const testPlaceholderDBPassword = "test-placeholder-not-a-real-password"

// openbrainTestHelperMainEnv, when set to "1", tells TestMain to invoke this
// package's real main() directly (with the real program args recovered from
// after "--") instead of running the test suite. TestVersionFlagWiring
// re-execs this same test binary with the env var set and a fully bare
// environment, proving main() delegates to version.HandleFlag before any
// config load: end to end, not just that the delegation line compiles.
const openbrainTestHelperMainEnv = "OPENBRAIN_TEST_HELPER_MAIN"

// TestMain ensures OPENBRAIN_DB_PASSWORD is present before any test in this
// package runs. buildMux, when MCPHTTPEnabled is true, mounts the MCP
// transport via mcptools.RegisterToolsWithOpts, which loads config through
// the package-level config.Get() cache; that cache requires a non-empty
// OPENBRAIN_DB_PASSWORD and panics otherwise. Mirrors the identical TestMain
// in internal/mcphttp, internal/mcptools, internal/brain, and
// cmd/openbrain-mcp: each Go test binary is its own process, so this env var
// must be set independently per package. A value already set in the
// environment is left untouched.
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
	if os.Getenv("OPENBRAIN_DB_PASSWORD") == "" {
		os.Setenv("OPENBRAIN_DB_PASSWORD", testPlaceholderDBPassword)
	}
	os.Exit(m.Run())
}
