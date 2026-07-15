// Command openbrain-web runs the HTTP + WebSocket server for the chat UI.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
)

// requireWebToken enforces that a web auth token is configured before the
// server binds any address. staticAuth and wsHandler fail open when
// cfg.WebWSToken is empty, so an unset token used to leave every route
// (including the write endpoints /api/capture and /api/ingest) reachable
// with no authentication. This is now a fatal startup condition instead of
// a warning: the openbrain-web binary refuses to start rather than serve
// unauthenticated. There is no opt-out; set OPENBRAIN_WEB_WS_TOKEN.
func requireWebToken(cfg *config.Config) error {
	if cfg.WebWSToken == "" {
		return fmt.Errorf("OPENBRAIN_WEB_WS_TOKEN is empty; a web auth token is required to start openbrain-web, set OPENBRAIN_WEB_WS_TOKEN to a token at least 32 characters long")
	}
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := config.MustLoad()
	if err := requireWebToken(cfg); err != nil {
		slog.Error("startup validation failed", "error", err)
		os.Exit(1)
	}
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
