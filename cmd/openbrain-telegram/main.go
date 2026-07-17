// Command openbrain-telegram runs the Telegram bot in polling mode.
package main

import (
	"log/slog"
	"os"

	"github.com/windingriverholdings/openbrain/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// --version reports the build version and exits. This binary is a stub
	// today (no config load in main), but it must still self-identify: the
	// installer queries every managed binary uniformly, and a
	// silently-ignored --version is a hard blocker regardless of the stub's
	// current safety. version.HandleFlag is the single shared implementation
	// every openbrain binary delegates to.
	if version.HandleFlag(os.Args[1:], os.Stdout) {
		return
	}

	// TODO: implement Telegram bot
	slog.Info("telegram bot not yet implemented")
}
