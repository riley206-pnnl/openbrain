// Command openbrain-mcp runs the OpenBrain MCP server over stdio,
// exposing tools for Claude Code integration.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
	"github.com/windingriverholdings/openbrain/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// --version reports the build version and exits before any config load,
	// DB connection, or server start, matching cmd/openbrain's --version
	// path. A version check must boot with zero dependencies: without this
	// guard, running "openbrain-mcp --version" silently ignored the flag,
	// loaded config, connected to the live database, and started a
	// long-running MCP server, leaving an orphaned process behind.
	if versionRequested(os.Args[1:]) {
		printVersion(os.Stdout)
		return
	}

	cfg := config.MustLoad()
	ctx := context.Background()

	pool, err := db.NewPool(ctx, cfg.DBUrl())
	if err != nil {
		slog.Error("db connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	embedder := embeddings.NewOllamaEmbedder(cfg)

	// Validate embedding config matches DB before serving.
	configDB := db.NewPgxEmbeddingConfigDB(pool)
	if err := db.ValidateEmbeddingConfig(ctx, configDB, cfg.EmbeddingModel, cfg.EmbeddingDim); err != nil {
		slog.Error("embedding config validation failed", "error", err)
		os.Exit(1)
	}

	b := brain.New(pool, embedder, cfg)

	if err := serveMCP(ctx, cfg, b, embedder); err != nil {
		slog.Error("mcp server failed", "error", err)
		os.Exit(1)
	}
}

// versionRequested reports whether the version flag was passed as the first
// argument. Matches cmd/openbrain's convention: the flag form only, checked
// before any other argument handling, so it must run first in main.
func versionRequested(args []string) bool {
	return len(args) > 0 && args[0] == "--version"
}

// printVersion writes the canonical build version to w, the same format
// cmd/openbrain uses for its --version output.
func printVersion(w io.Writer) {
	fmt.Fprintln(w, version.Version)
}
