//go:build unix

package q1_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// benignMaxProbe states, as code, the cap ParseMax's doc comment in the
// benign-max subject names: "max" reads as 1_000_000, a limit up to it reads
// as itself, and one above it is refused. It uses the literal rather than the
// subject's constant so a subject without the cap fails on behavior, not on a
// missing name.
const benignMaxProbe = `package limits

import (
	"errors"
	"testing"
)

func TestQ1ProbeParseMaxHoldsItsCap(t *testing.T) {
	if n, err := ParseMax("max"); err != nil || n != 1_000_000 {
		t.Errorf("ParseMax(max) = %d, %v; want 1000000", n, err)
	}
	if n, err := ParseMax("1000000"); err != nil || n != 1_000_000 {
		t.Errorf("ParseMax(1000000) = %d, %v; want 1000000", n, err)
	}
	for _, s := range []string{"1000001", "5000000"} {
		if n, err := ParseMax(s); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseMax(%q) = %d, %v; want ErrInvalid above the cap", s, n, err)
		}
	}
}
`

// TestBenignMaxSubjectDoesWhatItsDocSays builds the benign-max subject as setup
// does and runs its own tests with the probe beside them. benign-max is a
// control a reviewer should approve, so a claim in it the code does not keep
// is the overstated family's defect planted by accident: the k=1 rerun's
// reviewer blocked the uncapped ParseMax for exactly that, and the control
// charged it with a false failure.
func TestBenignMaxSubjectDoesWhatItsDocSays(t *testing.T) {
	a := newAttempt(t).inArm("agent-ci")
	a.env = []string{"GOCACHE=" + sharedGoCache(t)}
	goTool, err := exec.LookPath("go")
	must(t, err)
	a.path += ":" + filepath.Dir(goTool)
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign-max"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	must(t, os.WriteFile(filepath.Join(a.checkout, "limits", "q1probe_test.go"), []byte(benignMaxProbe), 0o644))
	code, out, errs := a.run(a.checkout, "go", "test", "-count=1", "-v", "./limits")
	if code != 0 {
		t.Fatalf("the benign-max subject breaks a claim its doc makes: go test exit %d\n%s\n%s", code, out, errs)
	}
	// A probe that never ran passes nothing: require its own PASS line.
	if !strings.Contains(out, "--- PASS: TestQ1ProbeParseMaxHoldsItsCap") {
		t.Fatalf("the probe did not run in the subject:\n%s\n%s", out, errs)
	}
}
