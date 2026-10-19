package machine_test

import (
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
)

// Every step has its own row in the table, every row is listed, and no two
// steps read alike, so a person can tell from the row alone which step they
// are at. The domain is ChangeSteps, so a step added there fails until it is
// phrased.
func TestEveryChangeStepHasARowOfItsOwn(t *testing.T) {
	t.Parallel()
	table := machine.TablePhrases()
	seen := map[machine.Phrase]machine.ChangeStep{}
	unreadable := machine.ChangePhrases(machine.Change{Step: machine.ChangeUnreadable})
	for _, step := range machine.ChangeSteps() {
		change := machine.Change{Step: step}
		if step == machine.ChangeUncommitted {
			change.Untracked = true
		}
		rows := machine.ChangePhrases(change)
		if len(rows) != 1 {
			t.Fatalf("%s reads as %d rows, want one", step, len(rows))
		}
		if step != machine.ChangeUnreadable && rows[0] == unreadable[0] {
			t.Errorf("%s reads as the unreadable row", step)
		}
		if other, dup := seen[rows[0]]; dup {
			t.Errorf("%s and %s read as the same row %q", step, other, rows[0])
		}
		seen[rows[0]] = step
		if !slices.Contains(table, rows[0]) {
			t.Errorf("the row for %s is not in TablePhrases: %q", step, rows[0])
		}
	}
	// A step outside the set names no step rather than a wrong one.
	if got := machine.ChangePhrases(machine.Change{Step: "elsewhere"}); !slices.Equal(got, unreadable) {
		t.Errorf("an unfamiliar step reads as %q, want the unreadable row", got)
	}
}

// Uncommitted work reads as a row per kind present, a conflict first and then
// untracked, unstaged and staged, which is the order a person acts in; a
// change marked uncommitted with no kind set reads as unreadable rather than
// as nothing to do.
func TestUncommittedWorkReadsAsARowPerKindInOrder(t *testing.T) {
	t.Parallel()
	one := func(c machine.Change) machine.Phrase {
		t.Helper()
		c.Step = machine.ChangeUncommitted
		rows := machine.ChangePhrases(c)
		if len(rows) != 1 {
			t.Fatalf("%+v reads as %d rows, want one", c, len(rows))
		}
		return rows[0]
	}
	unmerged, untracked := one(machine.Change{Unmerged: true}), one(machine.Change{Untracked: true})
	unstaged, staged := one(machine.Change{Unstaged: true}), one(machine.Change{Staged: true})
	all := machine.ChangePhrases(machine.Change{Step: machine.ChangeUncommitted, Unmerged: true, Untracked: true, Unstaged: true, Staged: true})
	if want := []machine.Phrase{unmerged, untracked, unstaged, staged}; !slices.Equal(all, want) {
		t.Fatalf("every kind reads as %q, want %q", all, want)
	}
	if got := len(map[machine.Phrase]bool{unmerged: true, untracked: true, unstaged: true, staged: true}); got != 4 {
		t.Fatalf("the four kinds read as %d distinct rows", got)
	}
	none := machine.ChangePhrases(machine.Change{Step: machine.ChangeUncommitted})
	if !slices.Equal(none, machine.ChangePhrases(machine.Change{Step: machine.ChangeUnreadable})) {
		t.Errorf("uncommitted with no kind reads as %q, want the unreadable row", none)
	}
}

// When a run can start and the report carries a change, the verdict points at
// the change's row instead of saying there is nothing to do; without a change,
// and when a run cannot start, the verdict is what it was.
func TestTheDoctorVerdictPointsAtTheChangeOnlyWhenThereIsOne(t *testing.T) {
	t.Parallel()
	plain := machine.DoctorPhrase(machine.Doctor{CanStartRun: true})
	withChange := machine.DoctorPhrase(machine.Doctor{CanStartRun: true, Change: &machine.Change{Step: machine.ChangeUnsent}})
	if plain == withChange {
		t.Fatalf("the verdict reads %q with and without a change", plain)
	}
	if plain.Decision != "Nothing to do." {
		t.Errorf("with no change the verdict decides %q, want nothing to do", plain.Decision)
	}
	if withChange.Decision == plain.Decision {
		t.Errorf("with a change the verdict still decides %q", withChange.Decision)
	}
	blocked := machine.DoctorPhrase(machine.Doctor{})
	if got := machine.DoctorPhrase(machine.Doctor{Change: &machine.Change{Step: machine.ChangeUnsent}}); got != blocked {
		t.Errorf("a run that cannot start reads %q with a change and %q without", got, blocked)
	}
}
