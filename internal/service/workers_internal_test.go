package service

import (
	"testing"

	"github.com/dayamjz/assistant/internal/tasks"
)

// intakeInstructions keeps what the coordinator wrote and stops at the first
// section the program appended to the file, recognized only at the start of
// a line: the isolation check dispatch appends, the promotion section
// internal/tasks appends, and the note a carry on appends, in any order.
// Without the carry-on note among them, the intake text of a task carried
// on from a carried-on task copies the first note, words to the first
// worker, and its runs' intent carries them.
func TestIntakeInstructionsStopAtTheFirstSectionTheProgramAppended(t *testing.T) {
	t.Parallel()
	const written = "Cap every page's images.\n\nKeep them under 200 KB."
	check := "\n\n" + secondCheckHeading + "\n\nRun this line first.\n\n    test -d /home/x/tasks/1/copy\n"
	promoted := "\n\n" + tasks.PromotionHeading + "\n\nThe intent for it is: ship it.\n"
	carried := "\n\n" + carryOnHeading + "\n\nYou were started in the working copy of an earlier task.\n"
	for _, c := range []struct {
		name, text, want string
	}{
		{"nothing appended", written + "\n", written},
		{"the isolation check", written + check, written},
		{"a promotion after the isolation check", written + check + promoted, written},
		{"a promotion alone", written + promoted, written},
		{"a carry-on note", written + carried, written},
		{"a promotion after a carry-on note", written + carried + promoted, written},
		{"a heading quoted mid-line is the coordinator's text",
			"Mention " + secondCheckHeading + " inline." + check,
			"Mention " + secondCheckHeading + " inline."},
		{"nothing but an appended section", check, ""},
	} {
		if got := intakeInstructions(c.text); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
