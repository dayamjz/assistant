package machine_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// digestTask is a task resolved to state, with progress as the resolution
// states it.
func digestTask(id string, state taskstate.State, progress bool) machine.Task {
	return machine.Task{
		Record: store.Task{ID: id},
		State:  taskstate.Resolution{State: state, Source: taskstate.SourceEvent, Progress: progress},
	}
}

// The digest is the read's own parts in counts and nothing else: every
// count is a part's length, Landed is what the read shows plus what it
// counted past its bound, and Unconfirmed is the tasks under way that
// resolve Running with no live terminal behind them. Every state
// internal/taskstate enumerates is placed under way with progress unset,
// so a state added there is counted by the rule rather than by a list
// here, and only Running is unconfirmed: the two Running tasks without
// progress. The control is a Running task whose resolution does carry
// progress: it is under way and is not unconfirmed, so the count is
// neither every Running task nor the wrong half of them.
func TestTheDigestCountsTheReadsOwnParts(t *testing.T) {
	t.Parallel()
	var under []machine.Task
	for _, state := range taskstate.States() {
		under = append(under, digestTask("t-"+string(state), state, false))
	}
	under = append(under, digestTask("t-live", taskstate.Running, true), digestTask("t-quiet", taskstate.Running, false))
	read := machine.CatchUp{
		Waiting:    []machine.Waiting{{}, {}},
		Landed:     []machine.Landed{{}, {}, {}},
		MoreLanded: 4,
		UnderWay:   machine.UnderWay{Tasks: under, Runs: []machine.Run{{}}},
		Stopped:    []machine.Task{digestTask("f", taskstate.Failed, false), digestTask("u", taskstate.Unknown, false)},
	}
	got := read.Digest()
	want := machine.Digest{Waiting: 2, Landed: 7, UnderWay: len(under), Unconfirmed: 2, Advancing: 1, Stopped: 2}
	if got != want {
		t.Fatalf("the digest is %+v, want %+v", got, want)
	}
	if (machine.CatchUp{}).Digest() != (machine.Digest{}) {
		t.Fatalf("an empty read's digest is %+v, want zero", (machine.CatchUp{}).Digest())
	}
}

// The digest travels with the read under digest, computed from the parts
// as they are encoded, so the structured answer cannot carry counts that
// disagree with the parts beside them, and a decoded read recomputes the
// same counts. The control changes a part and sees the encoded digest
// follow it, so the key is not a constant that happened to match.
func TestTheDigestTravelsWithTheReadItCounts(t *testing.T) {
	t.Parallel()
	read := machine.CatchUp{
		Waiting:  []machine.Waiting{{}},
		UnderWay: machine.UnderWay{Tasks: []machine.Task{digestTask("a", taskstate.Running, false)}, Runs: []machine.Run{}},
	}
	encoded := func(c machine.CatchUp) (machine.Digest, machine.CatchUp) {
		t.Helper()
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Digest *machine.Digest `json:"digest"`
		}
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		if document.Digest == nil {
			t.Fatalf("the encoded read has no digest: %s", raw)
		}
		var back machine.CatchUp
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		return *document.Digest, back
	}
	digest, back := encoded(read)
	if digest != read.Digest() || back.Digest() != digest {
		t.Fatalf("encoded %+v, the read counts %+v, the decoded read %+v", digest, read.Digest(), back.Digest())
	}
	read.Stopped = []machine.Task{digestTask("b", taskstate.Failed, false)}
	if moved, _ := encoded(read); moved.Stopped != 1 || moved == digest {
		t.Fatalf("after a part changed the encoded digest is %+v, want it to follow the part", moved)
	}

	// A document whose digest disagrees with its parts is not one this
	// shape writes, and it is refused rather than read; one with no digest
	// is only its parts and decodes.
	var refused machine.CatchUp
	if err := json.Unmarshal([]byte(`{"waiting":[],"digest":{"waiting":3}}`), &refused); err == nil {
		t.Fatalf("a digest counting decisions the parts do not hold decoded as %+v", refused)
	}
	var bare machine.CatchUp
	if err := json.Unmarshal([]byte(`{"waiting":[{}]}`), &bare); err != nil || bare.Digest().Waiting != 1 {
		t.Fatalf("a read with no digest decoded as %+v (%v)", bare.Digest(), err)
	}
}

// A task that resolves Running without progress is a worker whose own word
// is the only evidence it is at the work: no live terminal was observed.
// Its line is not the row that says a worker is on the work, which only a
// Running resolution with progress reads as, and no other state's line
// turns on progress at all. The domain is taskstate.States, so a new state
// is held to the same rule from the day it lands.
func TestAWorkerNotSeenLiveIsNeverReadAsAtItsWork(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	live := machine.TaskPhrase(machine.Task{State: taskstate.Resolution{
		State: taskstate.Running, Source: taskstate.SourceProcess, Progress: true}})
	unseen := machine.TaskPhrase(digestTask("a", taskstate.Running, false))
	if live == unseen {
		t.Fatalf("a worker seen live and one not seen read the same: %q", live)
	}
	if !strings.Contains(unseen.String(), "not seen") || strings.Contains(live.String(), "not seen") {
		t.Fatalf("the unseen worker reads %q and the live one %q; only the first says it was not seen", unseen, live)
	}
	if strings.Contains(unseen.String(), live.Outcome) {
		t.Fatalf("the unseen worker's line %q carries the live row's outcome %q", unseen, live.Outcome)
	}
	vocabtest.Assert(t, unseen.String())
	for _, state := range taskstate.States() {
		if state == taskstate.Running {
			continue
		}
		if with, without := machine.TaskPhrase(digestTask("a", state, true)), machine.TaskPhrase(digestTask("a", state, false)); with != without {
			t.Errorf("%s reads %q with progress and %q without; only a worker's presence turns on it", state, with, without)
		}
	}
}

// The digest's row names the most urgent thing its counts show: a decision
// waiting on the person, then work that stopped, then work no worker was
// seen at, then landed work to mark. A read with nothing in any part is the
// quiet row, and a read with something in it never is. The unconfirmed
// count is said in the consequence whenever it is not zero, so a worker
// not seen live is never folded into work counted as moving.
func TestTheDigestRowNamesTheMostUrgentCount(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	quiet := machine.DigestPhrase(machine.Digest{})
	for _, d := range []machine.Digest{{Waiting: 1}, {Landed: 1}, {UnderWay: 1}, {Advancing: 1}, {Stopped: 1}} {
		if machine.DigestPhrase(d) == quiet {
			t.Errorf("%+v reads as the quiet row", d)
		}
	}
	all := machine.Digest{Waiting: 1, Landed: 1, UnderWay: 2, Unconfirmed: 1, Advancing: 1, Stopped: 1}
	for _, c := range []struct {
		d    machine.Digest
		next machine.Digest
	}{
		{all, machine.Digest{Landed: 1, UnderWay: 2, Unconfirmed: 1, Advancing: 1, Stopped: 1}},
		{machine.Digest{Landed: 1, UnderWay: 2, Unconfirmed: 1, Stopped: 1}, machine.Digest{Landed: 1, UnderWay: 2, Unconfirmed: 1}},
		{machine.Digest{Landed: 1, UnderWay: 2, Unconfirmed: 1}, machine.Digest{Landed: 1, UnderWay: 2}},
		{machine.Digest{Landed: 1, UnderWay: 2}, machine.Digest{UnderWay: 2}},
	} {
		if machine.DigestPhrase(c.d).Decision == machine.DigestPhrase(c.next).Decision {
			t.Errorf("%+v and %+v decide the same %q; the more urgent count did not decide", c.d, c.next, machine.DigestPhrase(c.d).Decision)
		}
	}
	if p := machine.DigestPhrase(all); !strings.Contains(p.Consequence, "1 of the work under way") {
		t.Errorf("the unconfirmed count is not said: %q", p)
	}
	if p := machine.DigestPhrase(machine.Digest{UnderWay: 2}); strings.Contains(p.Consequence, "not counted as moving") {
		t.Errorf("a digest with nothing unconfirmed says something is: %q", p)
	}
	for _, d := range []machine.Digest{{}, all} {
		vocabtest.Assert(t, machine.DigestPhrase(d).String())
	}
}
