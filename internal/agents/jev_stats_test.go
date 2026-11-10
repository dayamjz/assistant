package agents_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

func TestReadCompactionStatsMissingFileIsEmpty(t *testing.T) {
	got, err := agents.ReadCompactionStats(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v, want empty", got, err)
	}
}

func TestHarvestCompactionStatsReadsNewLinesOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compaction-stats.jsonl")
	first := `{"path":"jev","messagesBefore":10,"messagesAfter":6,"charsBefore":1000,"charsAfter":400,"calls":3,"kept":1,"resultsDropped":1,"callsDropped":1,"pinned":6,"stateTokens":100,"stateStage":"fit","requests":1,"ms":12}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
		t.Fatal(err)
	}
	events, mid, err := agents.HarvestCompactionStats(dir, 0)
	if err != nil {
		t.Fatalf("HarvestCompactionStats: %v", err)
	}
	if len(events) != 1 || events[0].MessagesBefore != 10 || events[0].Path != agents.CompactionJev {
		t.Fatalf("first harvest %+v", events)
	}
	if mid != int64(len(first)) {
		t.Fatalf("mid offset %d, want %d", mid, len(first))
	}
	second := `{"messagesBefore":6,"messagesAfter":4,"charsBefore":400,"charsAfter":200,"calls":2,"kept":1,"resultsDropped":0,"callsDropped":1,"pinned":6,"stateTokens":80,"stateStage":"","requests":1,"ms":8}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(second); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	more, end, err := agents.HarvestCompactionStats(dir, mid)
	if err != nil {
		t.Fatalf("second harvest: %v", err)
	}
	if len(more) != 1 || more[0].MessagesBefore != 6 || more[0].Path != agents.CompactionJev {
		t.Fatalf("second harvest %+v", more)
	}
	if end <= mid {
		t.Fatalf("end %d should advance past mid %d", end, mid)
	}
	none, again, err := agents.HarvestCompactionStats(dir, end)
	if err != nil || len(none) != 0 || again != end {
		t.Fatalf("idle harvest got %v offset %d err %v", none, again, err)
	}
}
