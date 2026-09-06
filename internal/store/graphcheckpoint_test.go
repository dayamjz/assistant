package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// appendGraphCheckpoint appends against the anchor and fails the test if the
// append is refused.
func appendGraphCheckpoint(t *testing.T, s *Store, run string, anchor int, payload string) GraphCheckpoint {
	t.Helper()
	entry, err := s.AppendGraphCheckpoint(context.Background(), run, run, anchor, []byte(payload))
	if err != nil {
		t.Fatalf("AppendGraphCheckpoint(%s, anchored to %d): %v", run, anchor, err)
	}
	return entry
}

// graphCheckpointPayloads returns a run's payloads in sequence order.
func graphCheckpointPayloads(t *testing.T, s *Store, run string) []string {
	t.Helper()
	history, err := s.GraphCheckpointHistory(context.Background(), run)
	if err != nil {
		t.Fatalf("GraphCheckpointHistory(%s): %v", run, err)
	}
	out := make([]string, 0, len(history))
	for i, entry := range history {
		if entry.Run != run {
			t.Fatalf("entry %d belongs to run %s, want %s", i, entry.Run, run)
		}
		if entry.Seq != i+1 {
			t.Fatalf("entry %d has sequence %d, want %d: the history has a gap", i, entry.Seq, i+1)
		}
		out = append(out, string(entry.Payload))
	}
	return out
}

func TestAnAcceptedAppendIsAssignedTheSequenceOnePastItsAnchor(t *testing.T) {
	s := openStore(t)

	// This is the property a caller whose payload carries its own sequence
	// relies on to know the value before the write, so it is checked directly
	// rather than only through what a history happens to look like afterwards.
	for anchor := 0; anchor < 4; anchor++ {
		entry := appendGraphCheckpoint(t, s, "run", anchor, fmt.Sprintf("cp-%d", anchor+1))
		if entry.Seq != anchor+1 {
			t.Fatalf("an append anchored to %d was assigned sequence %d, want %d",
				anchor, entry.Seq, anchor+1)
		}
		if entry.WrittenAt.IsZero() {
			t.Fatalf("the entry assigned sequence %d records no write time", entry.Seq)
		}
	}
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 4 {
		t.Fatalf("the run has %d checkpoints, want 4", len(got))
	}
}

func TestAClaimOnARunThatAlreadyHasAHistoryIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	appendGraphCheckpoint(t, s, "run", 0, "cp-1")
	appendGraphCheckpoint(t, s, "run", 1, "cp-2")

	_, err := s.AppendGraphCheckpoint(ctx, "run", "", 0, []byte("over the top"))
	if !errors.Is(err, ErrGraphRunExists) {
		t.Fatalf("claiming a run that has a history = %v, want ErrGraphRunExists", err)
	}
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 2 || got[0] != "cp-1" || got[1] != "cp-2" {
		t.Fatalf("the refused claim changed the history to %v", got)
	}
}

func TestAnAppendAnchoredToAPositionTheRunHasLeftIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	appendGraphCheckpoint(t, s, "run", 0, "cp-1")
	appendGraphCheckpoint(t, s, "run", 1, "cp-2")
	appendGraphCheckpoint(t, s, "run", 2, "cp-3")

	cases := map[string]struct {
		anchorRun string
		anchorSeq int
	}{
		"a position the run has moved past":  {anchorRun: "run", anchorSeq: 2},
		"a position the run has not reached": {anchorRun: "run", anchorSeq: 9},
		"the same position in another run":   {anchorRun: "other", anchorSeq: 3},
		"a sequence no entry can have":       {anchorRun: "run", anchorSeq: -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.AppendGraphCheckpoint(ctx, "run", tc.anchorRun, tc.anchorSeq, []byte("lost"))
			var moved *GraphAnchorError
			if !errors.As(err, &moved) {
				t.Fatalf("append = %T %v, want a *GraphAnchorError", err, err)
			}
			if !errors.Is(err, ErrGraphAnchor) {
				t.Errorf("the refusal does not match ErrGraphAnchor: %v", err)
			}
			if moved.Stands != 3 {
				t.Errorf("the refusal says the run stands at %d, want 3", moved.Stands)
			}
			if moved.AnchorRun != tc.anchorRun || moved.AnchorSeq != tc.anchorSeq {
				t.Errorf("the refusal names anchor %s#%d, want %s#%d",
					moved.AnchorRun, moved.AnchorSeq, tc.anchorRun, tc.anchorSeq)
			}
			if got := graphCheckpointPayloads(t, s, "run"); len(got) != 3 {
				t.Errorf("the refused append left %d checkpoints, want 3", len(got))
			}
		})
	}
}

func TestConcurrentAppendsAtOneTipLandExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	appendGraphCheckpoint(t, s, "run", 0, "cp-1")

	// Every caller was handed the same tip and decides against it, which is
	// the interleaving a check made outside the write would let through. The
	// refusals must be the anchor's: a primary key violation here would mean
	// two callers both got past the decision and were separated by the table
	// rather than by it.
	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	release := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, errs[i] = s.AppendGraphCheckpoint(ctx, "run", "run", 1, fmt.Appendf(nil, "cp-2 by %d", i))
		}(i)
	}
	close(release)
	wg.Wait()

	landed := 0
	for i, err := range errs {
		var moved *GraphAnchorError
		switch {
		case err == nil:
			landed++
		case errors.As(err, &moved):
			if moved.Stands != 2 {
				t.Errorf("caller %d was told the run stands at %d, want 2", i, moved.Stands)
			}
		default:
			t.Fatalf("caller %d: %T %v, want either success or a *GraphAnchorError", i, err, err)
		}
	}
	if landed != 1 {
		t.Fatalf("%d of %d appends against one tip landed, want exactly 1", landed, callers)
	}
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 2 {
		t.Fatalf("the run has %d checkpoints, want 2: the appends interleaved", len(got))
	}
}

// holdTheWriter occupies the store's single writer connection with an open
// transaction and returns a function that releases it. Every other write queues
// behind it, which is what lets a test put two callers past a decision made
// anywhere but inside the write before either of them writes.
func holdTheWriter(t *testing.T, s *Store) func() {
	t.Helper()
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.inTx(context.Background(), func(*sql.Tx) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Errorf("the transaction holding the writer connection: %v", err)
		}
	}
}

// queuedForTheWriter blocks until want callers have queued for the writer
// connection since baseline.
//
// It is what makes the test below decide rather than hope. A caller that is
// queued for the writer has finished everything it does before opening its
// transaction, so releasing the writer at that moment guarantees any decision
// made outside the transaction was made against the tip as it stood before.
func queuedForTheWriter(t *testing.T, s *Store, baseline, want int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if queued := s.write.Stats().WaitCount - baseline; queued >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callers queued for the writer connection, want %d",
				s.write.Stats().WaitCount-baseline, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestTheAnchorIsDecidedInsideTheWriteThatActsOnIt(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	appendGraphCheckpoint(t, s, "run", 0, "cp-1")

	// Both callers are held short of writing until each has passed everything
	// it does before its transaction opens, so an anchor decided anywhere but
	// inside that transaction was decided against a tip of 1 by both of them.
	// Exactly one may then land, and the other's refusal has to be the
	// anchor's: an append that got past the decision and was stopped by the
	// table's own uniqueness is the check-then-write this contract forbids,
	// and it reads as a failed write rather than as a run that moved.
	release := holdTheWriter(t, s)
	baseline := s.write.Stats().WaitCount

	const callers = 2
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.AppendGraphCheckpoint(ctx, "run", "run", 1, fmt.Appendf(nil, "cp-2 by %d", i))
		}(i)
		queuedForTheWriter(t, s, baseline, int64(i+1))
	}
	release()
	wg.Wait()

	landed := 0
	for i, err := range errs {
		var moved *GraphAnchorError
		switch {
		case err == nil:
			landed++
		case errors.As(err, &moved):
			if moved.Stands != 2 {
				t.Errorf("caller %d was told the run stands at %d, want 2", i, moved.Stands)
			}
		default:
			t.Fatalf("caller %d: %T %v, want either success or a *GraphAnchorError", i, err, err)
		}
	}
	if landed != 1 {
		t.Fatalf("%d of %d appends landed, want exactly 1", landed, callers)
	}
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 2 {
		t.Fatalf("the run has %d checkpoints, want 2", len(got))
	}
}

func TestTheRunClaimIsDecidedInsideTheWriteThatActsOnIt(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	release := holdTheWriter(t, s)
	baseline := s.write.Stats().WaitCount

	const callers = 2
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.AppendGraphCheckpoint(ctx, "run", "", 0, fmt.Appendf(nil, "claimed by %d", i))
		}(i)
		queuedForTheWriter(t, s, baseline, int64(i+1))
	}
	release()
	wg.Wait()

	claimed := 0
	for i, err := range errs {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, ErrGraphRunExists):
		default:
			t.Fatalf("caller %d: %T %v, want either success or ErrGraphRunExists", i, err, err)
		}
	}
	if claimed != 1 {
		t.Fatalf("%d of %d claims landed, want exactly 1", claimed, callers)
	}
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 1 {
		t.Fatalf("the run has %d checkpoints, want 1", len(got))
	}
}

func TestConcurrentClaimsOfOneRunLandExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	const callers = 8
	errs := make([]error, callers)
	var wg sync.WaitGroup
	release := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, errs[i] = s.AppendGraphCheckpoint(ctx, "run", "", 0, fmt.Appendf(nil, "claimed by %d", i))
		}(i)
	}
	close(release)
	wg.Wait()

	claimed := 0
	for i, err := range errs {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, ErrGraphRunExists):
		default:
			t.Fatalf("caller %d: %v, want either success or ErrGraphRunExists", i, err)
		}
	}
	if claimed != 1 {
		t.Fatalf("%d of %d claims on one run landed, want exactly 1", claimed, callers)
	}
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 1 {
		t.Fatalf("the run has %d checkpoints, want 1", len(got))
	}
}

func TestCopyingIntoARunClaimsItWholeAndOnlyOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	copied, err := s.CopyGraphCheckpoints(ctx, "fork", [][]byte{[]byte("a"), []byte("b")})
	if err != nil {
		t.Fatalf("CopyGraphCheckpoints: %v", err)
	}
	if len(copied) != 2 || copied[0].Seq != 1 || copied[1].Seq != 2 {
		t.Fatalf("the copy wrote %d entries at %v", len(copied), copied)
	}
	if got := graphCheckpointPayloads(t, s, "fork"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("the fork holds %v, want a b", got)
	}

	if _, err := s.CopyGraphCheckpoints(ctx, "fork", [][]byte{[]byte("c")}); !errors.Is(err, ErrGraphRunExists) {
		t.Fatalf("copying into a run that has a history = %v, want ErrGraphRunExists", err)
	}
	if got := graphCheckpointPayloads(t, s, "fork"); len(got) != 2 {
		t.Fatalf("the refused copy left %d checkpoints, want 2", len(got))
	}
	if _, err := s.CopyGraphCheckpoints(ctx, "empty", nil); err == nil {
		t.Error("copying nothing into a run was accepted, which would claim the run and leave it empty")
	}
}

func TestConcurrentCopiesIntoOneRunLandExactlyOnce(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	const callers = 6
	errs := make([]error, callers)
	var wg sync.WaitGroup
	release := make(chan struct{})
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-release
			_, errs[i] = s.CopyGraphCheckpoints(ctx, "fork", [][]byte{
				fmt.Appendf(nil, "first by %d", i),
				fmt.Appendf(nil, "second by %d", i),
			})
		}(i)
	}
	close(release)
	wg.Wait()

	landed := 0
	for i, err := range errs {
		switch {
		case err == nil:
			landed++
		case errors.Is(err, ErrGraphRunExists):
		default:
			t.Fatalf("caller %d: %v, want either success or ErrGraphRunExists", i, err)
		}
	}
	if landed != 1 {
		t.Fatalf("%d of %d copies into one run landed, want exactly 1", landed, callers)
	}
	history := graphCheckpointPayloads(t, s, "fork")
	if len(history) != 2 {
		t.Fatalf("the fork has %d checkpoints, want 2: two copies interleaved", len(history))
	}
	// Both entries came from the caller that won, rather than one from each of
	// two that both got past the claim.
	if strings.TrimPrefix(history[0], "first") != strings.TrimPrefix(history[1], "second") {
		t.Fatalf("the fork holds %v, which is two callers' work", history)
	}
}

func TestTheLatestCheckpointIsTheHighestSequence(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	if _, err := s.LatestGraphCheckpoint(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the latest checkpoint of an unknown run = %v, want ErrNotFound", err)
	}
	history, err := s.GraphCheckpointHistory(ctx, "absent")
	if err != nil {
		t.Errorf("the history of an unknown run = %v, want no error", err)
	}
	if len(history) != 0 {
		t.Errorf("the history of an unknown run has %d entries", len(history))
	}

	appendGraphCheckpoint(t, s, "run", 0, "cp-1")
	appendGraphCheckpoint(t, s, "run", 1, "cp-2")
	latest, err := s.LatestGraphCheckpoint(ctx, "run")
	if err != nil {
		t.Fatalf("LatestGraphCheckpoint: %v", err)
	}
	if latest.Seq != 2 || string(latest.Payload) != "cp-2" {
		t.Errorf("the latest checkpoint is %d holding %q, want 2 holding cp-2", latest.Seq, latest.Payload)
	}

	// A second run's history is its own.
	appendGraphCheckpoint(t, s, "other", 0, "other-1")
	if got := graphCheckpointPayloads(t, s, "run"); len(got) != 2 {
		t.Errorf("run holds %v after another run was written", got)
	}
}

func TestACheckpointDoesNotRequireARunRecord(t *testing.T) {
	s := openStore(t)

	// A fork names a destination run that has no row of its own and cannot be
	// given one here, so this record deliberately carries no reference to the
	// run table. A foreign key would refuse the operation the durability
	// contract requires to work.
	appendGraphCheckpoint(t, s, "a-run-nobody-created", 0, "cp-1")
	if got := graphCheckpointPayloads(t, s, "a-run-nobody-created"); len(got) != 1 {
		t.Fatalf("the run holds %v, want one checkpoint", got)
	}
}

func TestTheCheckpointHistoryHoldsNoAnchor(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	// The anchor is a property of the request. It reaches this package as two
	// arguments to one accessor, and there is nowhere for it to be kept: this
	// is that statement in a form that fails when a column is added for it.
	rows, err := s.read.QueryContext(ctx, `SELECT name FROM pragma_table_info('graph_checkpoint')`)
	if err != nil {
		t.Fatalf("reading the table's columns: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("reading the table's columns: %v", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the table's columns: %v", err)
	}
	sort.Strings(columns)
	want := []string{"payload", "run", "seq", "written_at"}
	if len(columns) != len(want) {
		t.Fatalf("the record has columns %v, want exactly %v", columns, want)
	}
	for i := range want {
		if columns[i] != want[i] {
			t.Fatalf("the record has columns %v, want exactly %v", columns, want)
		}
	}
}

func TestACheckpointHistorySurvivesReopening(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	s := openStoreAt(t, path)
	appendGraphCheckpoint(t, s, "run", 0, "cp-1")
	appendGraphCheckpoint(t, s, "run", 1, "cp-2")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again := openStoreAt(t, path)
	if got := graphCheckpointPayloads(t, again, "run"); len(got) != 2 || got[1] != "cp-2" {
		t.Fatalf("the reopened store holds %v, want cp-1 cp-2", got)
	}
	// The reopened store decides an anchor against what it read back, so an
	// append from before the restart is refused and the run's own tip is not.
	if _, err := again.AppendGraphCheckpoint(ctx, "run", "run", 1, []byte("stale")); !errors.Is(err, ErrGraphAnchor) {
		t.Errorf("an append anchored to a position left before the restart = %v, want ErrGraphAnchor", err)
	}
	appendGraphCheckpoint(t, again, "run", 2, "cp-3")
	if got := graphCheckpointPayloads(t, again, "run"); len(got) != 3 {
		t.Fatalf("the reopened store holds %d checkpoints, want 3", len(got))
	}
}
