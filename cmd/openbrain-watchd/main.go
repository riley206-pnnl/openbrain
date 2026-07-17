// Command openbrain-watchd runs the folder watcher daemon that auto-ingests
// documents when files are created or modified in configured watch directories.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/windingriverholdings/openbrain/internal/brain"
	"github.com/windingriverholdings/openbrain/internal/config"
	"github.com/windingriverholdings/openbrain/internal/db"
	"github.com/windingriverholdings/openbrain/internal/embeddings"
	"github.com/windingriverholdings/openbrain/internal/version"
	"github.com/windingriverholdings/openbrain/internal/watcher"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// --version reports the build version and exits before any config load,
	// DB connection, or watcher start, matching cmd/openbrain-mcp's --version
	// path. A version check must boot with zero dependencies: without this
	// guard, running "openbrain-watchd --version" panicked loading config
	// (OPENBRAIN_DB_PASSWORD required) before the flag was ever read.
	if versionRequested(os.Args[1:]) {
		printVersion(os.Stdout)
		return
	}

	cfg := config.MustLoad()

	if cfg.WatchDirs == "" {
		slog.Error("OPENBRAIN_WATCH_DIRS not set — nothing to watch")
		os.Exit(1)
	}

	dirs := watcher.ParseWatchDirs(cfg.WatchDirs)
	slog.Info("configured watch directories", "count", len(dirs), "dirs", dirs)

	if cfg.IngestDir == "" {
		slog.Error("OPENBRAIN_INGEST_DIR must be set — watch dirs must be within IngestDir")
		os.Exit(1)
	}

	// Determine state file path: explicit config or IngestDir-relative only (no /tmp fallback)
	statePath := cfg.WatchStateFile
	if statePath == "" {
		statePath = filepath.Join(cfg.IngestDir, ".watchd-state.json")
	}
	cfg.WatchStateFile = statePath

	// Load persisted state
	state, err := watcher.LoadState(statePath)
	if err != nil {
		slog.Error("failed to load state", "path", statePath, "error", err)
		os.Exit(1)
	}
	slog.Info("loaded watcher state", "path", statePath, "tracked_files", len(state.Files))

	// Connect to database
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := db.NewPool(ctx, cfg.DBUrl())
	if err != nil {
		slog.Error("db connection failed", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Create embedder and brain
	embedder := embeddings.NewOllamaEmbedder(cfg)

	// Validate embedding config matches DB before watching.
	configDB := db.NewPgxEmbeddingConfigDB(pool)
	if err := db.ValidateEmbeddingConfig(ctx, configDB, cfg.EmbeddingModel, cfg.EmbeddingDim); err != nil {
		slog.Error("embedding config validation failed", "error", err)
		os.Exit(1)
	}

	b := brain.New(pool, embedder, cfg)
	adapter := watcher.NewBrainAdapter(b)

	// Create watcher
	w, err := watcher.New(adapter, cfg, state)
	if err != nil {
		slog.Error("failed to create watcher", "error", err)
		os.Exit(1)
	}

	// Graceful shutdown on SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		slog.Info("received signal, shutting down", "signal", sig)
		cancel()
	}()

	slog.Info("starting watchd daemon")
	if err := w.Watch(ctx); err != nil {
		slog.Error("watcher exited with error", "error", err)
		os.Exit(1)
	}

	// Save state on clean exit
	if err := state.Save(statePath); err != nil {
		slog.Warn("failed to save state on exit", "error", err)
	}
	slog.Info("watchd daemon stopped")
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
