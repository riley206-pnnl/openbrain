// Command openbrain-slack runs the Slack bot in socket mode.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/windingriverholdings/openbrain/internal/version"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	// --version reports the build version and exits, matching
	// cmd/openbrain-mcp's --version path. This binary is a stub today (no
	// config load in main), but it must still self-identify: the installer
	// queries every managed binary uniformly, and a silently-ignored
	// --version is a hard blocker regardless of the stub's current safety.
	if versionRequested(os.Args[1:]) {
		printVersion(os.Stdout)
		return
	}

	// TODO: implement Slack bot
	slog.Info("slack bot not yet implemented")
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
