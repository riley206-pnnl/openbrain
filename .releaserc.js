// .releaserc.js: openbrain semantic-release config.
//
// EXTENDS the shared WRS config pinned at github:windingriverholdings/
// semantic-release-config#v0.2.0. The five-plugin chain (commit-analyzer,
// release-notes-generator, changelog, git, github) is inherited from the
// shared config. This file inserts @semantic-release/exec between the
// changelog and git steps to rewrite the canonical version var before the
// bump commit is made.
//
// Plugin order:
//   1. commitAnalyzer: compute bump type from conventional commits
//   2. releaseNotes:   generate changelog entry
//   3. changelog:      write CHANGELOG.md
//   4. exec:           rewrite var Version in internal/version/version.go
//   5. git:            commit the rewritten version file and CHANGELOG.md
//   6. github:         create the tag and GitHub release
//
// The @semantic-release/exec prepareCmd:
//   a. grep-count guard: fails loudly if the var line is not found exactly
//      once (catches refactors that move the var without updating this file).
//   b. sed rewrite: replaces the "dev" (or previous semver) literal with the
//      computed ${nextRelease.version} string.
//
// Named-plugin composition (v0.2.0+ API):
//   Plugins are composed by destructuring base.namedPlugins, not by positional
//   index. Named references are stable across chain reorders; positional
//   indices break silently when the shared config grows or reorders.
//
//   base.namedPlugins.git is the tuple ['@semantic-release/git', { assets, message }].
//   To override the assets list, we destructure the tuple and spread the base
//   options, then add VERSION_FILE to assets. This keeps the base commit
//   message template intact and avoids duplicating it here.
//
// Maintenance: if the shared config adds a new named plugin, add the
// corresponding destructure here. Divergence from the shared plugin chain
// is a review finding.

'use strict'

const base = require('@wrsoftware/semantic-release-config')
const { commitAnalyzer, releaseNotes, changelog, git, github } = base.namedPlugins

// VERSION_FILE is the path (relative to repo root) containing the canonical
// var Version line. Update this constant if the file is ever moved, and
// update the corresponding version_file in projects/openbrain/project.yml.
const VERSION_FILE = 'internal/version/version.go'

// VERSION_PATTERN is the exact string that identifies the var line.
// It must match the line verbatim (modulo the version string itself) so the
// grep-count guard and the sed substitution target the same line.
const VERSION_PATTERN = 'var Version = '

// gitPluginName and gitPluginOpts: destructure the base git tuple so we can
// extend the assets list without re-stating the commit message template.
// git is ['@semantic-release/git', { assets: ['CHANGELOG.md'], message: '...' }]
const [gitPluginName, gitPluginOpts] = git

module.exports = {
  extends: '@wrsoftware/semantic-release-config',
  plugins: [
    commitAnalyzer,   // step 1: analyze commits, compute bump type
    releaseNotes,     // step 2: generate changelog entry
    changelog,        // step 3: write CHANGELOG.md
    // step 4: rewrite var Version in internal/version/version.go.
    // The grep-count guard fires before sed; if the pattern is not found
    // exactly once, the prepare step fails loudly and the release is aborted
    // before any tag is created.
    ['@semantic-release/exec', {
      prepareCmd:
        // Guard: confirm the var line exists exactly once.
        `count=$(grep -c '${VERSION_PATTERN}' ${VERSION_FILE}); ` +
        `if [ "$count" -ne 1 ]; then ` +
        `  echo "ERROR: expected exactly 1 line matching '${VERSION_PATTERN}' in ${VERSION_FILE}, found $count" >&2; ` +
        `  exit 1; ` +
        `fi; ` +
        // Rewrite: replace the quoted string after 'var Version = ' with the new version.
        `sed -i 's|${VERSION_PATTERN}"[^"]*"|${VERSION_PATTERN}"\${nextRelease.version}"|' ${VERSION_FILE}`
    }],
    // step 5: commit CHANGELOG.md and the rewritten version file.
    // Extends the base git plugin options: keeps the shared commit message
    // template and adds VERSION_FILE to the assets list.
    [gitPluginName, {
      ...gitPluginOpts,
      assets: [...gitPluginOpts.assets, VERSION_FILE]
    }],
    github            // step 6: create the tag and GitHub release
  ]
}
