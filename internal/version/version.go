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
)

// Version is the canonical openbrain release version.
// "dev" means an un-injected build (no ldflags stamp). A tagged build carries
// the semver string from git describe, for example "v0.3.0".
var Version = "dev"

// HandleFlag reports whether args names the --version flag (checked as the
// flag form only, in first position, matching cmd/openbrain's original
// convention) and, when it does, writes Version to w.
//
// Every openbrain binary (openbrain, openbrain-mcp, openbrain-web,
// openbrain-watchd, openbrain-telegram, openbrain-slack) calls this as the
// FIRST thing in main(), before any config load, DB connection, or other
// startup work: a version check must boot with zero dependencies so the
// Phase 2 installer can query any managed binary on a fresh host with no
// environment configured.
//
// The two return values are independent signals: handled reports whether
// args named the flag at all (false means "not our concern, keep going
// through normal startup"); err is non-nil only when handled is true AND the
// write to w failed. Callers MUST check err whenever handled is true and
// exit non-zero on a non-nil err, rather than returning 0 unconditionally.
// Without that check, a broken stdout pipe (a full disk, a closed pipe, a
// installer that closed its read end early) would make a version query
// FAIL silently: the flag was recognized, nothing was ever written, yet the
// process would still exit 0, indistinguishable from a successful print to
// any caller that only checks the exit code (the Phase 2 installer chief
// among them).
func HandleFlag(args []string, w io.Writer) (handled bool, err error) {
	if len(args) == 0 || args[0] != "--version" {
		return false, nil
	}
	if _, writeErr := fmt.Fprintln(w, Version); writeErr != nil {
		return true, fmt.Errorf("writing version: %w", writeErr)
	}
	return true, nil
}
