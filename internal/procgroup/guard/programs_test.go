package guard_test

import (
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/procgroup/guard"
)

// TestNoProgramIsWrittenOutsideProcgroup holds #539's class structurally:
// the module's code, its tests included, type-checked for both supported
// platforms, sets a mode with an execute bit, or a mode the type checker
// cannot read, only in internal/procgroup's non-test code, which is
// procgroup.WriteProgram, and in the functions guard.WriteExceptions lists,
// and every row there still covers a write. A program written any other way
// can be held open for writing by a child a parallel start forks, and its
// own start is then refused with ETXTBSY.
//
// The count of owned writes is this test's positive control: WriteProgram
// sets the mode it is given, which the scan cannot read, so a load that
// found none there read nothing. TestTheWriteScanFindsEveryShapeInTheControl
// is the other, over a fixture whose every write is known.
func TestNoProgramIsWrittenOutsideProcgroup(t *testing.T) {
	writes, err := guard.LoadWrites(moduleRoot(t), "./...")
	if err != nil {
		t.Fatal(err)
	}
	owner := path.Dir(reflect.TypeFor[guard.Write]().PkgPath())
	v := guard.CheckWrites(writes, owner, guard.WriteExceptions)
	if v.Owned == 0 {
		t.Fatalf("no write was found in %s, whose WriteProgram sets the mode it is given, so the load read nothing", owner)
	}
	for _, w := range v.Unexcepted {
		t.Errorf("a program is written outside procgroup.WriteProgram: %s", w)
	}
	if len(v.Unexcepted) > 0 {
		t.Log("Write a file something runs with procgroup.WriteProgram. If what the mode is set on is not a " +
			"program, such as a directory, add a row to guard.WriteExceptions whose Why says what it is.")
	}
	for _, e := range v.Stale {
		t.Errorf("guard.WriteExceptions lists %s in %s, which sets no mode the scan reports: remove the row", e.Function, e.File)
	}
}

// TestTheWriteExceptionListIsWellformed holds every row to a Why, and holds
// WellformedWrites to refusing each way a row can fall short of that.
func TestTheWriteExceptionListIsWellformed(t *testing.T) {
	if err := guard.WellformedWrites(guard.WriteExceptions); err != nil {
		t.Fatal(err)
	}
	good := guard.WriteException{File: "a.go", Function: "F", Why: "a directory"}
	for name, bad := range map[string][]guard.WriteException{
		"no file":      {{Function: "F", Why: "a directory"}},
		"no function":  {{File: "a.go", Why: "a directory"}},
		"no why":       {{File: "a.go", Function: "F"}},
		"a repetition": {good, good},
	} {
		if guard.WellformedWrites(bad) == nil {
			t.Errorf("WellformedWrites accepted a list with %s", name)
		}
	}
}

// TestTheWriteScanFindsEveryShapeInTheControl runs the write scan over
// testdata/control, whose every write is known: a constant mode through
// os.WriteFile, os.OpenFile under a renamed import, os.Chmod and
// (*os.File).Chmod, a mode passed as a parameter, a method value, a test
// file's write, and a write only the darwin build holds. Beside them are a
// look-alike that shares a setter's name, modes with no execute bit, and a
// write only the Windows build holds, none of which may be reported, and an
// owned write in the owner package. Its exception list holds one row that
// covers a write and one that covers none, which must come back stale.
func TestTheWriteScanFindsEveryShapeInTheControl(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("testdata", "control"))
	if err != nil {
		t.Fatal(err)
	}
	writes, err := guard.LoadWrites(root, "./...")
	if err != nil {
		t.Fatal(err)
	}
	gone := guard.WriteException{File: "programs/programs.go", Function: "Gone", Why: "a row with no write"}
	v := guard.CheckWrites(writes, "example.com/control/owner", []guard.WriteException{
		{File: "programs/programs.go", Function: "Excepted", Why: "the control's covered row"},
		gone,
	})
	if v.Owned != 1 {
		t.Errorf("owned writes = %d, want the 1 in owner.Write", v.Owned)
	}
	both := []string{"darwin", "linux"}
	want := []string{
		writeRow("programs/programs.go", "Constant", "os.WriteFile", "0755", both),
		writeRow("programs/programs.go", "Opened", "os.OpenFile", "0700", both),
		writeRow("programs/programs.go", "Changed", "os.Chmod", "0711", both),
		writeRow("programs/programs.go", "Method", "(*os.File).Chmod", "0100", both),
		writeRow("programs/programs.go", "Parameter", "os.WriteFile", "", both),
		writeRow("programs/programs.go", "Value", "(*os.File).Chmod", "", both),
		writeRow("programs/programs_test.go", "TestWrite", "os.WriteFile", "0755", both),
		writeRow("programs/programs_darwin.go", "Darwin", "os.WriteFile", "0755", []string{"darwin"}),
	}
	var got []string
	for _, w := range v.Unexcepted {
		platforms := slices.Clone(w.Platforms)
		slices.Sort(platforms)
		got = append(got, writeRow(w.File, w.Function, w.Setter, w.Mode, platforms))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the write scan reported\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
	if len(v.Stale) != 1 || v.Stale[0] != gone {
		t.Errorf("stale = %+v, want only the row with no write", v.Stale)
	}
}

// TestAPackageThatDoesNotTypeCheckIsAnErrorToTheWriteScan is LoadWrites'
// refusal of what it cannot read, on Load's terms.
func TestAPackageThatDoesNotTypeCheckIsAnErrorToTheWriteScan(t *testing.T) {
	writes, err := guard.LoadWrites(filepath.Join("testdata", "broken"), "./...")
	if err == nil {
		t.Fatalf("LoadWrites answered %v and no error for a package that does not type-check", writes)
	}
	if !strings.Contains(err.Error(), "example.com/broken") {
		t.Errorf("LoadWrites' error %q does not name the package it could not read", err)
	}
}

func writeRow(file, function, setter, mode string, platforms []string) string {
	return file + " " + function + " " + setter + " " + mode + " [" + strings.Join(platforms, " ") + "]"
}
