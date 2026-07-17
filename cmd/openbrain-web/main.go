// Command openbrain-web runs the HTTP + WebSocket server for the chat UI.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
	"github.com/windingriverholdings/openbrain/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// --version reports the build version and exits before any config load,
	// DB connection, or server start, matching cmd/openbrain-mcp's --version
	// path. A version check must boot with zero dependencies: without this
	// guard, running "openbrain-web --version" panicked loading config
	// (OPENBRAIN_DB_PASSWORD required) before the flag was ever read.
	if versionRequested(os.Args[1:]) {
		printVersion(os.Stdout)
		return
	}

	// Conditional auth posture: an empty OPENBRAIN_WEB_WS_TOKEN leaves the web
	// surface open (no startup abort); a set token is required on every gated
	// route via the ?token= query param. serveHTTP emits the loud open-mode
	// warning when the token is unset. The ≥32-char minimum for a set token is
	// enforced by config.Load (validateWebWSToken).
	cfg := config.MustLoad()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

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

	slog.Info("starting web server", "addr", cfg.WebAddr())
	if err := serveHTTP(ctx, cfg, b, embedder); err != nil {
		slog.Error("web server failed", "error", err)
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
