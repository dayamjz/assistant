package agents

import (
	"context"
	"strings"
)

// DecodeCodexOutput exposes decodeCodexOutput so tests can feed real
// `codex exec --json` JSONL without going through a process that only speaks
// the helper's Claude-shaped envelope.
func DecodeCodexOutput(stdout []byte) (text, threadID, model string, usage Usage, agentError string, err error) {
	decoded, err := decodeCodexOutput(stdout)
	if err != nil {
		return "", "", "", Usage{}, "", err
	}
	return decoded.text, decoded.threadID, decoded.model, decoded.usage, decoded.agentError, nil
}

// LookPathFirst exposes lookPathFirst for tests of the Cursor binary search.
var LookPathFirst = lookPathFirst

// WithoutEnvNames exposes withoutEnvNames for tests of API-key stripping.
var WithoutEnvNames = withoutEnvNames

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
// drawn", "drawn", "ready" or "busy", so a test can hold the reader to
// screens the CLI drew. The screen is the pane's rows, one to a line, and
// cursor is the row tmux reported its cursor on, as the provider reads
// them; -1 is a screen read with no cursor.
func ReadClaudeScreen(screen string, cursor int) string {
	switch claudeScreen(terminalScreen{rows: strings.Split(screen, "\n"), cursor: cursor}) {
	case screenDrawn:
		return "drawn"
	case screenReady:
		return "ready"
	case screenBusy:
		return "busy"
	}
	return "not drawn"
}

// TerminalWidth is how many columns wide the pane a worker's server opens
// is, so a test can build a line the pane draws over more than one row.
const TerminalWidth = terminalWidth

// ClaudeObservedVersion names the Claude Code versions the terminal mode's
// observations were made on.
const ClaudeObservedVersion = claudeObservedVersion

// ClaudeConfigFor, ClaudeCacheFor and ClaudeHomeFor expose the per-worker
// directories the Claude Code terminal adapter derives from a terminal, so a
// test reads the adapter's own answer rather than restating how it is
// composed.
var (
	ClaudeConfigFor = claudeConfigFor
	ClaudeCacheFor  = claudeCacheFor
	ClaudeHomeFor   = claudeHomeFor
)

// AvailabilityMarker is one row of availabilityMarkers, exported so a test
// can hold every row to the line it recognizes and to a near miss.
type AvailabilityMarker struct {
	Marker  string
	Failure Failure
	Cause   error
}

// AvailabilityMarkers returns availabilityMarkers in order.
func AvailabilityMarkers() []AvailabilityMarker {
	rows := make([]AvailabilityMarker, 0, len(availabilityMarkers))
	for _, row := range availabilityMarkers {
		rows = append(rows, AvailabilityMarker{Marker: row.marker, Failure: row.failure, Cause: row.cause})
	}
	return rows
}

// AvailabilityMessage exposes availabilityMessage.
var AvailabilityMessage = availabilityMessage

// ClaudeGrantFlags exposes claudeGrantFlags, the Claude adapter's
// translation of a grant, so a test can derive the command line a role's
// grant writes rather than restating it.
var ClaudeGrantFlags = claudeGrantFlags

// The bound of each kind of tmux client and the grace each is run with, so a
// test of a client that never answers can say when its call must have
// returned.
const (
	TmuxStartBound  = tmuxStartBound
	TmuxTypeBound   = tmuxTypeBound
	TmuxReadBound   = tmuxReadBound
	TmuxEndBound    = tmuxEndBound
	TmuxClientGrace = tmuxClientGrace
	// TerminalPoll is how often a readiness wait reads the screen again.
	TerminalPoll = terminalPoll
)

// RequireHerdrVersion exposes requireHerdrVersion for tests of the version pin.
var RequireHerdrVersion = requireHerdrVersion

// WriteHerdrFiles exposes writeHerdrFiles for tests of the configuration and
// run-once launch file.
var WriteHerdrFiles = writeHerdrFiles

// HerdrServerEnv exposes herdrServerEnv for tests of ambient herdr stripping.
var HerdrServerEnv = herdrServerEnv

// HerdrRequiredVersion is the herdr release the mechanism pins.
const HerdrRequiredVersion = herdrRequiredVersion

// InterruptAfterWriteForTest makes w, a worker this package started as a
// process, call f with each later send's text once its bytes are written and
// before the send is told, so a test can put the process's end inside that
// window. It is set between sends, never while one is in flight.
func InterruptAfterWriteForTest(w Worker, f func(text string)) { w.(*procWorker).afterWrite = f }
