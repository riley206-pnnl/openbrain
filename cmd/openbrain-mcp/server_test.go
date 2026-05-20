package main

import (
	"testing"

	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/mcptools"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
)

func TestServeMCPCreatesServer(t *testing.T) {
	cfg := &config.Config{
		MCPServerName:    "openbrain",
		MCPServerVersion: "0.0.1-test",
	}
	s := server.NewMCPServer(cfg.MCPServerName, cfg.MCPServerVersion)
	mcptools.RegisterTools(s, nil, nil)
	assert.NotNil(t, s)
}
