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

// Version is the canonical openbrain release version.
// "dev" means an un-injected build (no ldflags stamp). A tagged build carries
// the semver string from git describe, for example "v0.3.0".
var Version = "dev"
