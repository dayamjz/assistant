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

// CompactionStats is one successful Jev compact's stats line. The numeric
// fields match the plugin's CompactResult.stats JSON; Path is added by the
// hook (or defaulted to CompactionJev on read) and is not part of stats.
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
// path InstallJev prepared (when the harvest succeeded), and every
// CompactionStats line appended during that invocation. The zero value means
// no config dir we control, or the harvest could not be read (store columns
// stay unreported). A prepared path with no new events still sets Path and
// has an empty Events slice — that is not the zero value.
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
	defer func() { _ = f.Close() }()
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
			// Skip a corrupt line rather than stalling the offset and
			// turning later good lines into known zeros in usage.
			continue
		}
		if ev.Path == "" {
			ev.Path = CompactionJev
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, after, fmt.Errorf("scanning compaction stats: %w", err)
	}
	return events, size, nil
}

// compactionReportFor fills CompactionReport from the path record and any
// stats lines written since after. at, when non-nil, advances to the new end
// offset so a later round does not re-count the same lines. An I/O failure
// leaves the zero report (store columns stay unreported) and does not move at.
func compactionReportFor(configDir string, at *int64) CompactionReport {
	after := int64(0)
	if at != nil {
		after = *at
	}
	events, next, err := HarvestCompactionStats(configDir, after)
	if err != nil {
		return CompactionReport{}
	}
	if at != nil {
		*at = next
	}
	rep := CompactionReport{Events: events}
	if d, err := ReadCompactionDecision(configDir); err == nil {
		rep.Path = d.Path
		rep.Reason = d.Reason
	}
	return rep
}
