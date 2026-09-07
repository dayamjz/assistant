package journey_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// ownership is what the product did when it was asked about a project
// directory that was copied after it was gated, so the copy carries the
// original's remote.
type ownership struct {
	// original and copy are the gate identifiers the two working copies ended
	// up with.
	original string
	copy     string
	// removalRefused is whether removing the gate through the copy was
	// refused, and removalMessage is what it was refused with.
	removalRefused bool
	removalMessage string
	// ejectRefused is whether the same removal through the binary was refused,
	// and ejectMessage is what it said.
	ejectRefused bool
	ejectMessage string
	// stillThere is what a refused removal has to have left standing: the gate
	// repository, the original's remote, and the copy's.
	gateStillThere     bool
	originalRemote     string
	copyRemoteAfterAll string
}

// TestACopiedProjectDirectoryDoesNotOwnTheGateItInherited drives the two
// ownership conditions internal/fixture plants for a working copy that was
// copied after it was gated.
//
// It cites no principle. internal/fixture records neither condition against
// one, and claiming a principle for a test written about something else is the
// failure internal/principles is built to make visible rather than a bonus.
//
// The two conditions are opposite answers to one question, which is why they
// are driven together. Removing the gate through the copy has to refuse and
// leave everything standing, because the gate is the original's and an
// inherited remote is not evidence of owning it. Initializing through the copy
// has to succeed and give the copy a gate of its own, because refusing there
// would leave the copy with nothing when there was a gate available.
func TestACopiedProjectDirectoryDoesNotOwnTheGateItInherited(t *testing.T) {
	scenario := claim(t, fixture.ScenarioCopiedWorkingCopy)
	removal, err := journey.Condition("refusal-copied-working-copy-removal")
	if err != nil {
		t.Fatalf("%v", err)
	}
	adoption, err := journey.Condition("adoption-copied-working-copy-initialize")
	if err != nil {
		t.Fatalf("%v", err)
	}

	j := open(t, scenario)
	var created machine.Init
	if err := succeeds(t, j.Command("init", "--default-branch", fixture.DefaultBranch)).Decode(&created); err != nil {
		t.Fatalf("reading what the initialization created: %v", err)
	}
	observed := ownership{original: created.Gate.ID}

	// The copy is taken here and nowhere else: after a gate exists to inherit
	// a remote from, and before anything is asked of the copy.
	copied, err := fixture.CopyGatedWorkingCopy(scenario)
	if err != nil {
		t.Fatalf("copying the gated working copy: %v", err)
	}

	// The mechanism the condition names, driven directly. The binary's own
	// removal refuses earlier and for another reason, which is checked below;
	// this is the refusal the condition is about.
	records, err := store.Open(t.Context(), filepath.Join(j.Root(), "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the home's records: %v", err)
	}
	defer func() { _ = records.Close() }()
	err = gate.Remove(t.Context(), gate.Spec{Home: j.Root(), WorkingPath: copied}, gate.WithIndex(records))
	observed.removalRefused = errors.Is(err, gate.ErrGateClaimed)
	if err != nil {
		observed.removalMessage = err.Error()
	}

	// The same act through the binary, which must also leave everything
	// standing whatever it refuses with.
	answer := j.CommandIn(copied, "eject", "--confirm")
	observed.ejectRefused = answer.Code != machine.ExitOK
	observed.ejectMessage = answer.Message()

	observed.gateStillThere = isDirectory(filepath.Join(j.Root(), "repos", observed.original+".git"))
	observed.originalRemote = remoteURL(t, scenario, scenario.WorkingCopy)
	observed.copyRemoteAfterAll = remoteURL(t, scenario, copied)

	// Initializing through the copy, which is the opposite answer.
	var adopted machine.Init
	if err := succeeds(t, j.CommandIn(copied, "init", "--default-branch", fixture.DefaultBranch)).
		Decode(&adopted); err != nil {
		t.Fatalf("reading what initializing the copy created: %v", err)
	}
	observed.copy = adopted.Gate.ID

	holds := journey.Check[ownership]{
		What: "removing the gate through a copy that inherited its remote refuses and removes nothing, " +
			"and initializing through the same copy gives it a gate of its own rather than the original's",
		Holds: func(o ownership) error {
			if !o.removalRefused {
				return fmt.Errorf("removing the gate through the copy was not refused with %s; it "+
					"answered %q", "gate.ErrGateClaimed", o.removalMessage)
			}
			if missing := journey.Carries(o.removalMessage, removal.Expect.MessageContains); len(missing) > 0 {
				return fmt.Errorf("the refusal does not say %q; it said:\n%s", missing, o.removalMessage)
			}
			if !o.ejectRefused {
				return fmt.Errorf("the binary removed the gate through the copy: %s", o.ejectMessage)
			}
			if !o.gateStillThere {
				return errors.New("the gate repository is gone, and a refused removal removes nothing")
			}
			if o.originalRemote == "" {
				return errors.New("the original lost its own remote to a removal that was refused")
			}
			if o.copyRemoteAfterAll == "" {
				return errors.New("the copy lost the remote it inherited to a removal that was refused")
			}
			if o.copy == "" {
				return errors.New("initializing through the copy created no gate")
			}
			if o.copy == o.original {
				return fmt.Errorf("the copy adopted the original's gate %s, and a remote it inherited is "+
					"not evidence that it owns one", o.original)
			}
			return nil
		},
		Counterfeits: []journey.Counterfeit[ownership]{
			{Named: "the removal through the copy went ahead", Break: func(o ownership) ownership {
				o.removalRefused = false
				return o
			}},
			{Named: "the binary's own removal went ahead", Break: func(o ownership) ownership {
				o.ejectRefused = false
				return o
			}},
			{Named: "the refused removal took the gate repository with it", Break: func(o ownership) ownership {
				o.gateStillThere = false
				return o
			}},
			{Named: "the refused removal detached the original", Break: func(o ownership) ownership {
				o.originalRemote = ""
				return o
			}},
			{Named: "the refused removal detached the copy", Break: func(o ownership) ownership {
				o.copyRemoteAfterAll = ""
				return o
			}},
			{Named: "the copy was given the original's own gate", Break: func(o ownership) ownership {
				o.copy = o.original
				return o
			}},
			{Named: "the copy was refused a gate and left with none", Break: func(o ownership) ownership {
				o.copy = ""
				return o
			}},
			{Named: "the refusal says none of what the condition requires", Break: func(o ownership) ownership {
				o.removalMessage = "gate: no"
				return o
			}},
		},
	}
	if err := holds.Verify(observed); err != nil {
		t.Fatalf("%v", err)
	}
	if adoption.Expect.Summary == "" {
		t.Fatalf("the adoption condition records no expectation, so what it must produce has changed")
	}
	if !slices.Contains([]string{"", adoption.Expect.Sentinel}, "") {
		t.Fatalf("the adoption condition now names a sentinel, so it is a refusal and this drives it as one")
	}
}

// remoteURL is the address a working copy reaches its gate by, empty when it
// has no such remote.
func remoteURL(t *testing.T, scenario fixture.Scenario, dir string) string {
	t.Helper()
	out, err := journey.Git(scenario, dir, "config", "--get", "remote."+gateRemote+".url")
	if err != nil {
		return ""
	}
	return out
}

// isDirectory reports whether a path is a directory that exists.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
