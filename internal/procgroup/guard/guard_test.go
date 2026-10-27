package guard_test

import (
	"go/types"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup/guard"
)

// TestNoProgramIsStartedOutsideProcgroup is P15's "an external program the
// service invokes runs under a deadline, and when the deadline passes it is
// ended together with everything it started", held structurally: the
// module's non-test code, type-checked for every platform it builds on,
// names a starter only in internal/procgroup and in the functions
// guard.Exceptions lists, and every row there still covers a use.
//
// The count of owned uses is this test's own positive control: procgroup
// starts every command it runs, so a load that found none there read
// nothing, and passing it would be a guard looking at nothing.
// TestTheGuardFindsEveryShapeOfStartInTheControl is the other, over a
// fixture whose every start is known.
func TestNoProgramIsStartedOutsideProcgroup(t *testing.T) {
	principles.Cite(t, principles.P15)
	uses, err := guard.Load(moduleRoot(t), "./...")
	if err != nil {
		t.Fatal(err)
	}
	owner := path.Dir(reflect.TypeFor[guard.Use]().PkgPath())
	v := guard.Check(uses, owner, guard.Exceptions)
	if v.Owned == 0 {
		t.Fatalf("no use of a starter was found in %s, which starts every command it runs, so the load read nothing", owner)
	}
	for _, u := range v.Unexcepted {
		t.Errorf("a program is started outside internal/procgroup: %s", u)
	}
	if len(v.Unexcepted) > 0 {
		t.Log("Build the command with procgroup.Command and run it with procgroup.Run. If it cannot go through " +
			"procgroup, add a row to guard.Exceptions whose Reason and Why say why and what ends it.")
	}
	for _, e := range v.Stale {
		t.Errorf("guard.Exceptions lists %s in %s (%s), which names no starter: remove the row", e.Function, e.File, e.Reason)
	}
}

// TestTheExceptionListIsWellformed holds every row to a reason and a Why,
// and holds Wellformed to refusing each way a row can fall short of that.
func TestTheExceptionListIsWellformed(t *testing.T) {
	if err := guard.Wellformed(guard.Exceptions); err != nil {
		t.Fatal(err)
	}
	good := guard.Exception{File: "a.go", Function: "F", Reason: guard.DocumentedGit, Why: "because"}
	for name, bad := range map[string][]guard.Exception{
		"no file":      {{Function: "F", Reason: guard.DocumentedGit, Why: "because"}},
		"no function":  {{File: "a.go", Reason: guard.DocumentedGit, Why: "because"}},
		"zero reason":  {{File: "a.go", Function: "F", Why: "because"}},
		"unknown":      {{File: "a.go", Function: "F", Reason: guard.ProductUnderTest + 1, Why: "because"}},
		"no why":       {{File: "a.go", Function: "F", Reason: guard.DocumentedGit}},
		"a repetition": {good, good},
	} {
		if guard.Wellformed(bad) == nil {
			t.Errorf("Wellformed accepted a list with %s", name)
		}
	}
}

// TestTheGuardFindsEveryShapeOfStartInTheControl runs the guard over
// testdata/control, a module of its own whose every start is known: a
// renamed import, a method value, a method expression, a method promoted
// through an embedded *exec.Cmd, os.StartProcess, a method's receiver, a
// package-level initializer, and a start only the Windows build holds,
// beside a function
// that shares a constructor's name and a test file, neither of which may be
// reported. Its exception list holds one row that covers a use and one that
// covers none, which must come back stale.
func TestTheGuardFindsEveryShapeOfStartInTheControl(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "control"))
	if err != nil {
		t.Fatal(err)
	}
	uses, err := guard.Load(root, "./...")
	if err != nil {
		t.Fatal(err)
	}
	gone := guard.Exception{File: "raw/raw.go", Function: "Gone", Reason: guard.DocumentedGit, Why: "a row with no use"}
	v := guard.Check(uses, "example.com/control/owner", []guard.Exception{
		{File: "raw/raw.go", Function: "Excepted", Reason: guard.DocumentedGit, Why: "the control's covered row"},
		gone,
	})
	if v.Owned != 2 {
		t.Errorf("owned uses = %d, want the 2 in owner.Run", v.Owned)
	}
	all := []string{"darwin", "linux", "windows"}
	want := []string{
		row("raw/raw.go", "Direct", "(*os/exec.Cmd).Run", all),
		row("raw/raw.go", "Direct", "os/exec.Command", all),
		row("raw/raw.go", "Value", "(*os/exec.Cmd).Output", all),
		row("raw/raw.go", "Expression", "(*os/exec.Cmd).Run", all),
		row("raw/raw.go", "Promoted", "(*os/exec.Cmd).Start", all),
		row("raw/raw.go", "Process", "os.StartProcess", all),
		row("raw/raw.go", "Combined", "(*os/exec.Cmd).CombinedOutput", all),
		row("raw/raw.go", "(*wrapped).Method", "(*os/exec.Cmd).Run", all),
		row("raw/raw.go", "", "os/exec.CommandContext", all),
		row("raw/raw_unix.go", "Unix", "syscall.ForkExec", []string{"darwin", "linux"}),
		row("raw/raw_unix.go", "Unix", "syscall.StartProcess", []string{"darwin", "linux"}),
		row("raw/raw_unix.go", "Unix", "syscall.Exec", []string{"darwin", "linux"}),
		row("raw/raw_unix.go", "Unix", "golang.org/x/sys/unix.Exec", []string{"darwin", "linux"}),
		row("raw/raw_windows.go", "Windows", "os/exec.Command", []string{"windows"}),
		row("raw/raw_windows.go", "Syscalls", "syscall.StartProcess", []string{"windows"}),
		row("raw/raw_windows.go", "Syscalls", "syscall.Exec", []string{"windows"}),
		row("raw/raw_windows.go", "Syscalls", "syscall.CreateProcess", []string{"windows"}),
		row("raw/raw_windows.go", "Syscalls", "syscall.CreateProcessAsUser", []string{"windows"}),
		row("raw/raw_windows.go", "Syscalls", "golang.org/x/sys/windows.CreateProcess", []string{"windows"}),
		row("raw/raw_windows.go", "Syscalls", "golang.org/x/sys/windows.CreateProcessAsUser", []string{"windows"}),
	}
	var got []string
	for _, u := range v.Unexcepted {
		got = append(got, row(u.File, u.Function, u.Starter, u.Platforms))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the guard reported\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	if len(v.Stale) != 1 || v.Stale[0] != gone {
		t.Errorf("stale = %+v, want only the row with no use", v.Stale)
	}
	// Every name in Starters has to be one the type checker resolved in this
	// fixture. A string that matches no function is a starter the module-wide
	// test cannot see, so a use of the real function would pass.
	seen := map[string]bool{}
	for _, u := range uses {
		seen[u.Starter] = true
	}
	for _, name := range guard.Starters {
		if !seen[name] {
			t.Errorf("Starters lists %s, which the control never names", name)
		}
	}
}

// TestAPackageThatDoesNotTypeCheckIsAnError is the guard's refusal of what
// it cannot read: testdata/broken starts a program in a call that does not
// type-check, and a Load that skipped the package would report no use and
// pass.
func TestAPackageThatDoesNotTypeCheckIsAnError(t *testing.T) {
	uses, err := guard.Load(filepath.Join("testdata", "broken"), "./...")
	if err == nil {
		t.Fatalf("Load answered %v and no error for a package that does not type-check", uses)
	}
	if !strings.Contains(err.Error(), "example.com/broken") {
		t.Errorf("Load's error %q does not name the package it could not read", err)
	}
}

// TestForkExecIsListedWhenThePackageHasIt is the x/sys name Starters does
// not carry today. golang.org/x/sys/unix has Exec and no ForkExec in the
// version this module requires, so a call to ForkExec does not type-check
// and cannot start a child. When a later version adds the function, this
// test fails until Starters names it, and the control fixture then has to
// name it too or TestTheGuardFindsEveryShapeOfStartInTheControl fails.
func TestForkExecIsListedWhenThePackageHasIt(t *testing.T) {
	principles.Cite(t, principles.P15)
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes,
		Dir:  moduleRoot(t),
		Env:  append(os.Environ(), "GOOS=linux", "GOWORK=off"),
	}
	pkgs, err := packages.Load(cfg, "golang.org/x/sys/unix")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].Types == nil {
		t.Fatalf("loading golang.org/x/sys/unix: %+v", pkgs)
	}
	for _, e := range pkgs[0].Errors {
		t.Fatal(e)
	}
	// The package's own Exec is the positive control: a load that yielded
	// an empty scope and no errors would otherwise pass as "no ForkExec".
	if pkgs[0].Types.Scope().Lookup("Exec") == nil {
		t.Fatalf("golang.org/x/sys/unix loaded with no Exec in its scope, so the lookup below checks nothing")
	}
	obj := pkgs[0].Types.Scope().Lookup("ForkExec")
	if obj == nil {
		return
	}
	fn, ok := obj.(*types.Func)
	if !ok || !slices.Contains(guard.Starters, fn.FullName()) {
		t.Fatalf("%s starts a program and is not in Starters", obj)
	}
}

func row(file, function, starter string, platforms []string) string {
	return file + " " + function + " " + starter + " [" + strings.Join(platforms, " ") + "]"
}

// moduleRoot is the nearest directory at or above this test's that holds a
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod at or above the test's directory")
		}
		dir = parent
	}
}
