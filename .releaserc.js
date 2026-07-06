// .releaserc.js: openbrain semantic-release config.
//
// EXTENDS the shared WRS config pinned at github:windingriverholdings/
// semantic-release-config#v0.2.0. The shared config's default plugin chain
// (commit-analyzer, release-notes-generator, changelog, git, github) writes a
// CHANGELOG.md and pushes a bump commit to main via @semantic-release/git.
//
// openbrain's main branch is protected (ruleset 16819112: "Changes must be
// made through a pull request"), so the github-actions[bot] GITHUB_TOKEN
// identity cannot push a bump commit to refs/heads/main. It is rejected with
// GH013 and the release fails after the version is already computed.
//
// Per .claude/rules/releasing.md section 3, the version is NOT a source
// constant rewritten at release time. It is stamped into the binary at build
// time via ldflags (see the Makefile). The release flow therefore makes ZERO
// push to refs/heads/main: it creates only the git tag and the GitHub release,
// and (OB-043) attaches the compiled binaries as release assets.
//
// This config overrides the shared plugin array to drop the two branch-writing
// plugins:
//   - changelog: removed. The changelog lives in the GitHub release notes
//     (produced by release-notes-generator and published by the github
//     plugin), not in a CHANGELOG.md committed to main.
//   - git: removed. It is the plugin that pushed HEAD:main. Removing it is the
//     fix for the GH013 rejection. It is NOT re-added here.
//
// The plugin chain:
//   1. commitAnalyzer: compute the semver bump from conventional commits.
//   2. releaseNotes:   generate the release notes for the new version.
//   3. buildAssets:    @semantic-release/exec prepareCmd. BUILD-ONLY. Compiles
//      the six binaries into dist/ stamped with the computed release version,
//      then writes dist/SHA256SUMS. It makes NO git commit and NO push and
//      rewrites NO source file, so it does not touch refs/heads/main (OB-043).
//   4. github:         create the git TAG (refs/tags/*, not protected) and the
//      GitHub release, publish the generated notes, AND upload the dist/
//      assets. It makes no commit to main.
//
// Named-plugin composition (v0.2.0+ API): the forge-agnostic plugins are
// referenced by name from base.namedPlugins, so a shared-config reorder does
// not silently reshuffle this chain.

'use strict'

const base = require('@wrsoftware/semantic-release-config')
const { commitAnalyzer, releaseNotes } = base.namedPlugins

// Step 3: @semantic-release/exec, re-added in OB-043 as a BUILD-ONLY prepare
// step. It was removed in OB-040 because the shared config used it to run a
// deploy tail; here it does exactly one thing: build the release-asset binaries.
//
// ${nextRelease.version} is expanded by @semantic-release/exec via lodash
// template to the ACTUAL computed release version (for example 0.3.1), NOT the
// previous tag from git describe. It is prefixed with "v" to match the git tag
// format the deploy build reads through git describe. The version reaches each
// binary through the Makefile dist target's ldflags -X path
// (internal/version.Version), the same symbol the canary smoke-test verifies.
//
// INVARIANT (OB-043): this command ONLY builds. It runs no git add/commit/push
// and rewrites no source file, so it does not touch refs/heads/main and does
// not reintroduce the OB-040 branch-protection fight. See the Makefile `dist`
// target for the build recipe.
const buildAssets = [
  '@semantic-release/exec',
  {
    prepareCmd: 'make dist DIST_VERSION=v${nextRelease.version}'
  }
]

// Step 4: create the tag and GitHub release, and attach the dist/ assets.
// The bare '@semantic-release/github' from the shared config carries no
// options, so configuring assets here loses nothing. The two globs are
// unambiguous: dist/ is a build-output-only, gitignored directory that starts
// empty on the runner and is populated solely by the buildAssets prepareCmd.
//   - dist/openbrain*-linux-amd64 : the six versioned binaries.
//   - dist/SHA256SUMS             : checksums over those six binaries.
const releaseWithAssets = [
  '@semantic-release/github',
  {
    assets: [
      'dist/openbrain*-linux-amd64',
      'dist/SHA256SUMS'
    ]
  }
]

module.exports = {
  extends: '@wrsoftware/semantic-release-config',
  plugins: [
    commitAnalyzer,    // step 1: analyze commits, compute bump type
    releaseNotes,      // step 2: generate release notes
    buildAssets,       // step 3: BUILD binaries + checksums into dist/ (no push)
    releaseWithAssets  // step 4: create tag + release, attach dist assets
  ]
}
