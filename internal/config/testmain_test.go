package config

import (
	"os"
	"testing"
)

// testPlaceholderDBPassword is a clearly synthetic value, never a real
// credential, used only to satisfy the required,notEmpty constraint on
// DBPassword so tests that call Load() without setting the env var
// themselves still exercise the rest of the config parsing path.
const testPlaceholderDBPassword = "test-placeholder-not-a-real-password"

// TestMain ensures OPENBRAIN_DB_PASSWORD is present before any test in this
// package runs. Load() requires a non-empty value; without this, every test
// that calls Load() (directly or via the package-level Get() cache) fails
// with a required-environment-variable error rather than exercising the
// behavior under test. A value already set in the environment is left
// untouched, so a caller who wants to test password handling explicitly can
// still do so with t.Setenv in an individual test.
func TestMain(m *testing.M) {
	if os.Getenv("OPENBRAIN_DB_PASSWORD") == "" {
		os.Setenv("OPENBRAIN_DB_PASSWORD", testPlaceholderDBPassword)
	}
	os.Exit(m.Run())
}
