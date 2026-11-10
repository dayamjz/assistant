package agents_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

type compactionRecorder struct {
	mu      sync.Mutex
	records []agents.Record
}

func (r *compactionRecorder) RecordInvocation(rec agents.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, rec)
}

func (r *compactionRecorder) snapshot() []agents.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agents.Record, len(r.records))
	copy(out, r.records)
	return out
}

func TestReadCompactionStatsMissingFileIsEmpty(t *testing.T) {
	got, err := agents.ReadCompactionStats(t.TempDir())
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v err %v, want empty", got, err)
	}
}

func TestAFixerApplyHarvestsCompactionStatsIntoTheRecord(t *testing.T) {
	agents.SetJevVersionForTest(agents.JevMinClaudeVersion)
	t.Cleanup(func() { agents.SetJevVersionForTest("") })
	spy := &compactionRecorder{}
	env := append(claudeTerminalEnv(t), agents.JevKeyVariable+"=test-key-not-printed")
	agent := standin.New(t, standin.Script{Steps: []standin.Step{
		{Reply: standin.Text("round-1")},
		{Reply: standin.Text("round-2")},
	}},
		agents.WithJevCompaction(func() bool { return true }),
		agents.WithBaseEnvironment(env),
		agents.WithRecorder(spy),
	)
	fixer, err := agents.OpenFixer(t.Context(), agent.Runner(), "")
	if err != nil {
		t.Fatalf("OpenFixer: %v", err)
	}
	inv := agents.Invocation{Dir: t.TempDir(), Prompt: "fix me", Shape: agents.ShapeText}
	if _, err := fixer.Apply(t.Context(), inv); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	dir := agents.FixerJevDirForTest(fixer)
	if dir == "" {
		t.Fatal("fixer opened no Jev config dir")
	}
	// Residual product gap: the fixer does not remove jevDir when dropped.
	// The test must, or leak-check fails the package (CI).
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	line := `{"path":"jev","messagesBefore":8,"messagesAfter":5,"charsBefore":900,"charsAfter":300,"calls":2,"kept":1,"resultsDropped":1,"callsDropped":0,"pinned":6,"stateTokens":50,"stateStage":"fit","requests":1,"ms":9}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "compaction-stats.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixer.Apply(t.Context(), inv); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	got := spy.snapshot()
	if len(got) < 2 {
		t.Fatalf("recorder got %d records, want at least 2", len(got))
	}
	last := got[len(got)-1]
	if last.Compaction.Path != agents.CompactionJev {
		t.Fatalf("Compaction.Path = %q, want jev", last.Compaction.Path)
	}
	if len(last.Compaction.Events) != 1 || last.Compaction.Events[0].CharsBefore != 900 {
		t.Fatalf("Compaction.Events = %+v, want one line with charsBefore 900", last.Compaction.Events)
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
	// A corrupt line is skipped so a later good line still advances the offset.
	if err := os.WriteFile(path, append([]byte(first+second), []byte("not-json\n{\"messagesBefore\":1,\"messagesAfter\":1,\"charsBefore\":10,\"charsAfter\":5,\"calls\":0,\"kept\":0,\"resultsDropped\":0,\"callsDropped\":0,\"pinned\":0,\"stateTokens\":0,\"stateStage\":\"\",\"requests\":0,\"ms\":1}\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	more, end2, err := agents.HarvestCompactionStats(dir, 0)
	if err != nil {
		t.Fatalf("corrupt-tolerant harvest: %v", err)
	}
	if len(more) != 3 || more[2].CharsBefore != 10 {
		t.Fatalf("got %+v, want 3 events including the trailing good line", more)
	}
	if end2 <= end {
		t.Fatalf("offset %d should advance past corrupt line past %d", end2, end)
	}
}
