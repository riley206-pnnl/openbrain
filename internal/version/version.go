// Package version holds the canonical build version for openbrain.
//
// The var below is the sole version source for all openbrain binaries.
// It defaults to "dev" so local builds report a clearly non-release value.
//
// At release time, @semantic-release/exec rewrites this line via a
// grep-count-guarded sed command (see .releaserc.js prepareCmd). After the
// rewrite, @semantic-release/git commits the changed file and creates the tag.
//
// At build time (make build / make install), no ldflags injection is needed:
// the version is already baked into the source by the release commit. Local
// dev builds stay at "dev" without any special flags.
package version

// Version is the canonical openbrain release version.
// "dev" means an unreleased local build; semantic-release overwrites this
// string in the release commit.
var Version = "dev"
