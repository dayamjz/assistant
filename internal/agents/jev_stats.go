package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// compactionStatsName is the append-only log the pinned plugin writes one
// CompactionStats line into after a successful Jev compact. Built-in
// fallbacks write nothing. Residual: the hook write is best-effort (a
// missing CLAUDE_CONFIG_DIR or a write error must not refuse compaction).
const compactionStatsName = "compaction-stats.jsonl"

// CompactionStats is one successful Jev compact's result.stats, as the
// pinned plugin records them. Field names match the plugin's CompactResult.stats
// JSON so a line round-trips without a second schema.
type CompactionStats struct {
	Path           CompactionPath `json:"path,omitempty"`
	MessagesBefore int            `json:"messagesBefore"`
	MessagesAfter  int            `json:"messagesAfter"`
	CharsBefore    int            `json:"charsBefore"`
	CharsAfter     int            `json:"charsAfter"`
	Calls          int            `json:"calls"`
	Kept           int            `json:"kept"`
	ResultsDropped int            `json:"resultsDropped"`
	CallsDropped   int            `json:"callsDropped"`
	Pinned         int            `json:"pinned"`
	StateTokens    int            `json:"stateTokens"`
	StateStage     string         `json:"stateStage"`
	Requests       int            `json:"requests"`
	Milliseconds   int            `json:"ms"`
}

// CompactionReport is what one invocation observed about compaction: the
// path InstallJev prepared, and every CompactionStats line the plugin
// appended during that invocation. Empty when the launch had no config dir
// we control, or when nothing compacted.
type CompactionReport struct {
	Path   CompactionPath    `json:"path,omitempty"`
	Reason string            `json:"reason,omitempty"`
	Events []CompactionStats `json:"events,omitempty"`
}

// ReadCompactionStats reads every stats line the plugin appended under
// configDir. A missing file is an empty slice, not an error.
func ReadCompactionStats(configDir string) ([]CompactionStats, error) {
	events, _, err := HarvestCompactionStats(configDir, 0)
	return events, err
}

// HarvestCompactionStats reads stats lines written after the given byte
// offset and returns the new end offset. after past EOF yields no events and
// the file size. A missing file yields (nil, 0, nil).
func HarvestCompactionStats(configDir string, after int64) ([]CompactionStats, int64, error) {
	path := filepath.Join(configDir, compactionStatsName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, after, fmt.Errorf("reading compaction stats: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, after, fmt.Errorf("stating compaction stats: %w", err)
	}
	size := info.Size()
	if after > size {
		after = size
	}
	if after > 0 {
		if _, err := f.Seek(after, io.SeekStart); err != nil {
			return nil, after, fmt.Errorf("seeking compaction stats: %w", err)
		}
	}
	var events []CompactionStats
	sc := bufio.NewScanner(f)
	// One compact's JSON line stays well under a megabyte; raise the default
	// token so a long decisions-adjacent stats line still parses.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev CompactionStats
		if err := json.Unmarshal(line, &ev); err != nil {
			return events, after, fmt.Errorf("decoding compaction stats: %w", err)
		}
		if ev.Path == "" {
			ev.Path = CompactionJev
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return events, after, fmt.Errorf("scanning compaction stats: %w", err)
	}
	return events, size, nil
}

// compactionReportFor fills CompactionReport from the path record and any
// stats lines written since after. at, when non-nil, advances to the new end
// offset so a later round does not re-count the same lines.
func compactionReportFor(configDir string, at *int64) CompactionReport {
	rep := CompactionReport{}
	if d, err := ReadCompactionDecision(configDir); err == nil {
		rep.Path = d.Path
		rep.Reason = d.Reason
	}
	after := int64(0)
	if at != nil {
		after = *at
	}
	events, next, err := HarvestCompactionStats(configDir, after)
	if err != nil {
		return rep
	}
	rep.Events = events
	if at != nil {
		*at = next
	}
	return rep
}
