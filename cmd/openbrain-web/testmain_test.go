package main

import (
	"os"
	"testing"
)

// testPlaceholderDBPassword is a clearly synthetic value, never a real
// credential, used only to satisfy config.Load's required,notEmpty
// constraint on DBPassword.
const testPlaceholderDBPassword = "test-placeholder-not-a-real-password"

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
	if os.Getenv("OPENBRAIN_DB_PASSWORD") == "" {
		os.Setenv("OPENBRAIN_DB_PASSWORD", testPlaceholderDBPassword)
	}
	os.Exit(m.Run())
}
