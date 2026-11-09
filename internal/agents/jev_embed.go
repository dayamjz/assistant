package agents

import "embed"

// jevPluginFS is the pinned fast-jev-compaction tree InstallJev materializes
// into a worker's configuration directory. The files under jevmod/ are the
// plugin at JevPluginVersion; types/ and node_modules are not required at
// runtime for Claude Code's function-hook loader.
//
//go:embed all:jevmod
var jevPluginFS embed.FS
