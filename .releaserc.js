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
// time via ldflags from git describe (see the Makefile). The release flow
// therefore makes ZERO push to refs/heads/main: it creates only the git tag
// and the GitHub release.
//
// This config overrides the shared plugin array to drop the two branch-writing
// plugins:
//   - changelog: removed. The changelog lives in the GitHub release notes
//     (produced by release-notes-generator and published by the github
//     plugin), not in a CHANGELOG.md committed to main.
//   - git: removed. It is the plugin that pushed HEAD:main. Removing it is the
//     fix for the GH013 rejection.
//
// The retained plugins:
//   1. commitAnalyzer: compute the semver bump from conventional commits.
//   2. releaseNotes:   generate the release notes for the new version.
//   3. github:         create the git TAG (refs/tags/*, not protected) and the
//      GitHub release, publishing the generated notes. This is the sole
//      version authority's terminal output; it makes no commit to main.
//
// Named-plugin composition (v0.2.0+ API): plugins are referenced by name from
// base.namedPlugins, not by positional index, so a shared-config reorder does
// not silently reshuffle this chain.

'use strict'

const base = require('@wrsoftware/semantic-release-config')
const { commitAnalyzer, releaseNotes, github } = base.namedPlugins

module.exports = {
  extends: '@wrsoftware/semantic-release-config',
  plugins: [
    commitAnalyzer, // step 1: analyze commits, compute bump type
    releaseNotes,   // step 2: generate release notes
    github          // step 3: create the tag and GitHub release (no push to main)
  ]
}
