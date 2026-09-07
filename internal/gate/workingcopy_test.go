package gate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
)

// TestWorkingCopyForAnswersTheQuestionAHookArrivesWith is the whole point of
// the operation: a hook is handed an identifier and nothing else, and
// everything a push has to be validated against hangs off the working copy.
//
// The identifier comes from the gate the initialization returned rather than
// from a hash this test computed, so what is exercised is the round trip the
// hooks actually make.
func TestWorkingCopyForAnswersTheQuestionAHookArrivesWith(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	got, err := gate.WorkingCopyFor(ctx(t), home, g.ID(), opts()...)
	if err != nil {
		t.Fatalf("WorkingCopyFor(%s): %v", g.ID(), err)
	}
	if want := g.WorkingPath(); got != want {
		t.Fatalf("WorkingCopyFor(%s) = %q, want %q", g.ID(), got, want)
	}
}

// TestWorkingCopyForAsksTheWorkingCopyRatherThanBelievingTheIndex is the rule
// held.claimants applies, applied here: a binding is a candidate worth asking
// about and never the answer.
//
// The working copy is left standing and its assistant remote is removed, which
// is a person detaching a project from its gate by hand. The index still names
// it, so an implementation that read the binding and stopped would answer with
// a working copy that no longer points at this gate, and a push would start a
// run for a repository nobody attached to it.
func TestWorkingCopyForAsksTheWorkingCopyRatherThanBelievingTheIndex(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, index, opts := homeWithIndex(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	rawGit(t, wc.path, "remote", "remove", gate.RemoteName)

	// The index is unchanged, so the candidate is still there to be believed.
	if bound := boundWorkingPaths(t, index, g.ID()); len(bound) != 1 {
		t.Fatalf("the index records %v as bound to gate %s, want the one working copy; "+
			"without it this test would pass for the wrong reason", bound, g.ID())
	}
	got, err := gate.WorkingCopyFor(ctx(t), home, g.ID(), opts()...)
	if !errors.Is(err, gate.ErrGateUnbound) {
		t.Fatalf("WorkingCopyFor(%s) = %q, %v; want ErrGateUnbound", g.ID(), got, err)
	}
	if !strings.Contains(err.Error(), gate.RemoteName) {
		t.Errorf("the refusal does not name the remote that would change it: %v", err)
	}
}

// TestWorkingCopyForRefusesAnIdentifierThatIsNotAGateOfThisHome covers the
// three ways an identifier reaches this operation without naming a gate.
//
// The escaping cases are the reason the identifier is checked rather than
// joined onto the home's repository directory and read: an identifier is a
// value on a command line, and one carrying a separator would name a path
// outside that directory.
func TestWorkingCopyForRefusesAnIdentifierThatIsNotAGateOfThisHome(t *testing.T) {
	home, opts := newHome(t)
	for _, c := range []struct {
		name string
		id   string
		want error
	}{
		{"a well-formed identifier nothing is filed under", "0123456789abcdef", gate.ErrNoGate},
		{"a parent reference", "../../etc", gate.ErrInvalidSpec},
		{"a path separator", "a/b", gate.ErrInvalidSpec},
		{"an empty identifier", "", gate.ErrInvalidSpec},
		{"an identifier of the wrong length", "0123456789abcde", gate.ErrInvalidSpec},
		{"an identifier that is not hexadecimal", "zzzzzzzzzzzzzzzz", gate.ErrInvalidSpec},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := gate.WorkingCopyFor(ctx(t), home, c.id, opts()...)
			if !errors.Is(err, c.want) {
				t.Fatalf("WorkingCopyFor(%q) = %q, %v; want %v", c.id, got, err, c.want)
			}
		})
	}
}

// TestWorkingCopyForRefusesWithoutAnIndex is the same refusal every other
// operation here makes and for the same reason: which working copies are bound
// to a gate is recorded in the home's index, and an operation that guessed
// would attach a push to a repository nobody named.
func TestWorkingCopyForRefusesWithoutAnIndex(t *testing.T) {
	home, _ := newHome(t)
	got, err := gate.WorkingCopyFor(ctx(t), home, "0123456789abcdef")
	if !errors.Is(err, gate.ErrNoIndex) {
		t.Fatalf("WorkingCopyFor with no index = %q, %v; want ErrNoIndex", got, err)
	}
}

// TestWorkingCopyForRefusesAGateWhoseRecordWillNotRead keeps this operation on
// the same footing as the rest of the package. A record that cannot be read is
// a fact that cannot be established, and answering from the index alone would
// be this operation quietly relaxing a stance every other one holds.
func TestWorkingCopyForRefusesAGateWhoseRecordWillNotRead(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.WriteFile(filepath.Join(g.Repository(), recordName), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("damaging the gate record: %v", err)
	}
	got, err := gate.WorkingCopyFor(ctx(t), home, g.ID(), opts()...)
	if !errors.Is(err, gate.ErrMalformedRecord) {
		t.Fatalf("WorkingCopyFor over a damaged record = %q, %v; want ErrMalformedRecord", got, err)
	}
}

// TestWorkingCopyForFindsAGateThroughTheIndexWhenItsRecordIsGone is the other
// half of "neither source is believed on its own". A gate whose record file is
// gone is a gate to repair rather than nothing, and the index is what still
// names the working copy it belongs to, so a push to it is still answerable.
func TestWorkingCopyForFindsAGateThroughTheIndexWhenItsRecordIsGone(t *testing.T) {
	gitEnvironment(t)
	wc := newWorkingCopy(t)
	home, opts := newHome(t)
	command, _ := recorderCommand(t, 0)

	g, err := gate.Initialize(ctx(t), gate.Spec{Home: home, WorkingPath: wc.path, Command: command}, opts()...)
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := os.Remove(filepath.Join(g.Repository(), recordName)); err != nil {
		t.Fatalf("removing the gate record: %v", err)
	}
	got, err := gate.WorkingCopyFor(ctx(t), home, g.ID(), opts()...)
	if err != nil {
		t.Fatalf("WorkingCopyFor over a gate with no record: %v", err)
	}
	if want := g.WorkingPath(); got != want {
		t.Fatalf("WorkingCopyFor = %q, want %q", got, want)
	}
}
