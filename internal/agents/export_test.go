package agents

import "context"

// EndListed exposes endListed, so a test can hand the ending step an identity
// the listing produced and then mutated, which is the reuse it must refuse.
var EndListed = endListed

// ListUnder exposes the platform listing's processes under dir without the
// scope check Sweeper.Sweep makes first, so a test can show the listing
// reaches a process the scope then leaves out.
func ListUnder(ctx context.Context, dir string) ([]Identity, error) {
	found, err := listUnder(ctx, dir)
	return found.under, err
}

// ReadClaudeScreen is claudeScreen's reading of a screen as a word, "not
// drawn", "drawn" or "ready", so a test can hold the reader to screens the
// CLI drew.
func ReadClaudeScreen(screen string) string {
	switch claudeScreen(screen) {
	case screenDrawn:
		return "drawn"
	case screenReady:
		return "ready"
	}
	return "not drawn"
}

// ClaudeObservedVersion is the Claude Code version the terminal mode's
// observations were made on.
const ClaudeObservedVersion = claudeObservedVersion
