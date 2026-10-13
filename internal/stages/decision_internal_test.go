package stages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A path whose diff cannot be read is recorded as a failure for each check,
// with no probability, and reported in the not-run note, and nothing is sent.
// Before judge.Unasked these paths were reported to the person but left out of
// the evaluation record, so the record undercounted what the checks did not
// cover. The diff fails for real: the repository has no commits, so git
// cannot resolve either revision.
func TestAPathWhoseDiffCannotBeReadIsRecordedAndReported(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, err := vcs.InitBare(ctx, filepath.Join(t.TempDir(), "empty.git"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	var sent atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sent.Add(1)
		http.Error(w, "no request was expected", http.StatusTeapot)
	}))
	t.Cleanup(provider.Close)
	rec := &recorded{}
	d := decisionRun{
		mode:   config.DecisionEvaluate,
		client: judge.New("test-key", judge.WithEndpoint(provider.URL), judge.WithRecorder(rec)),
		repo:   repo,
		run:    "run-1",
		from:   strings.Repeat("a", 40),
		head:   strings.Repeat("b", 40),
	}

	screened := d.screenFindings(d.screen(ctx, []string{"a.go"}))
	_, traceNotes := d.traceFindings(d.traces(ctx, []string{"a.go"},
		[]findings.Trace{{Path: "a.go", Reason: "adds a"}}))

	if n := sent.Load(); n != 0 {
		t.Fatalf("%d request(s) left the machine for a diff that could not be read", n)
	}
	rows := rec.all()
	if len(rows) != 2 {
		t.Fatalf("recorded %d rows, want one per check: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Answered || r.Failure != judge.FailureDiffUnreadable || r.Path != "a.go" || r.Run != "run-1" {
			t.Fatalf("an unreadable diff was recorded as %+v", r)
		}
	}
	for name, notes := range map[string]int{"screen": len(screened), "trace": len(traceNotes)} {
		if notes != 1 {
			t.Fatalf("the %s check reported %d note(s) for the unreadable diff, want 1", name, notes)
		}
	}
	if !strings.Contains(screened[0].Description, string(judge.FailureDiffUnreadable)) {
		t.Fatalf("the not-run note does not say why: %q", screened[0].Description)
	}
}

// recorded is a judge.Recorder that keeps what it is handed.
type recorded struct {
	mu   sync.Mutex
	rows []judge.Record
}

func (r *recorded) RecordAnswer(rec judge.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rec)
	return nil
}

func (r *recorded) all() []judge.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]judge.Record(nil), r.rows...)
}
