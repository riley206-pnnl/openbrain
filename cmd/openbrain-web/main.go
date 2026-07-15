// Command openbrain-web runs the HTTP + WebSocket server for the chat UI.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

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
