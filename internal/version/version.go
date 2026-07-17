// Package version holds the canonical build version for openbrain.
//
// The var below is the sole version source for all openbrain binaries.
// It defaults to "dev" so an un-injected build (local dev, a CI branch
// build, a pull-request check) reports a clearly non-release value.
//
// At build time the real version is stamped in via linker flags, NOT by
// rewriting this source file. The Makefile derives the version from the
// nearest git tag and injects it:
//
//	go build -ldflags "-X github.com/windingriverholdings/openbrain/internal/version.Version=$(git describe --tags --always)" ./cmd/...
//
// Stamping at build time (instead of a release-time source rewrite) keeps the
// release flow from pushing a bump commit to main, which branch protection
// rejects. The release workflow creates only a git tag and a GitHub release;
// the tag is what the next `make build` reads through git describe.
package version

import (
	"fmt"
	"io"
	"log/slog"
)

// Version is the canonical openbrain release version.
// "dev" means an un-injected build (no ldflags stamp). A tagged build carries
// the semver string from git describe, for example "v0.3.0".
var Version = "dev"

// HandleFlag reports whether args names the --version flag (checked as the
// flag form only, in first position, matching cmd/openbrain's original
// convention) and, when it does, writes Version to w before returning true.
//
// Every openbrain binary (openbrain, openbrain-mcp, openbrain-web,
// openbrain-watchd, openbrain-telegram, openbrain-slack) calls this as the
// FIRST thing in main(), before any config load, DB connection, or other
// startup work: a version check must boot with zero dependencies so the
// Phase 2 installer can query any managed binary on a fresh host with no
// environment configured. Callers exit/return 0 when this reports true.
func HandleFlag(args []string, w io.Writer) bool {
	if len(args) == 0 || args[0] != "--version" {
		return false
	}
	// The write itself is checked (not ignored) so a broken stdout pipe is a
	// visible signal, not silent noise; there is nothing more to do about it
	// here; the flag was still recognized, so the caller still exits 0.
	if _, err := fmt.Fprintln(w, Version); err != nil {
		slog.Error("writing version output failed", "error", err)
	}
	return true
}
