package service

import (
	"testing"

	"github.com/dayamjz/assistant/internal/tasks"
)

// intakeInstructions keeps what the coordinator wrote and stops at the first
// section the program appended to the file, recognized only at the start of
// a line: the isolation check dispatch appends, and the promotion section
// internal/tasks appends, in either order.
func TestIntakeInstructionsStopAtTheFirstSectionTheProgramAppended(t *testing.T) {
	t.Parallel()
	const written = "Cap every page's images.\n\nKeep them under 200 KB."
	check := "\n\n" + secondCheckHeading + "\n\nRun this line first.\n\n    test -d /home/x/tasks/1/copy\n"
	promoted := "\n\n" + tasks.PromotionHeading + "\n\nThe intent for it is: ship it.\n"
	for _, c := range []struct {
		name, text, want string
	}{
		{"nothing appended", written + "\n", written},
		{"the isolation check", written + check, written},
		{"a promotion after the isolation check", written + check + promoted, written},
		{"a promotion alone", written + promoted, written},
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
