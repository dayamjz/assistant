//go:build race

package guard_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/procgroup/guard"
)

// TestTheLoadersTypeCheckWithoutARace is #624 held under the race detector,
// which is what fails it: Load and LoadWrites read a module whose packages
// all range over the iterators strings and bytes return, so their checkers
// would each first read the generic instances, iter.Seq[string] among them,
// that the export data of strings and bytes holds, the read go/types races
// on when two checkers make it at once (golang/go#81122). Checked one at a
// time per platform, they do not. A build without the race detector cannot
// see the race, so the test is in race builds only, which is how CI runs
// it.
func TestTheLoadersTypeCheckWithoutARace(t *testing.T) {
	root := instancesModule(t, "")
	// The window is narrow, and a second round is another chance at it: on
	// main before the fix, one round failed 6 of 10 runs and two rounds 19
	// of 20 (Apple M-series, GOMAXPROCS=4).
	for range 2 {
		uses, err := guard.Load(root, "./...")
		if err != nil {
			t.Fatal(err)
		}
		if len(uses) != 0 {
			t.Errorf("Load found %v in a module that starts no program", uses)
		}
		writes, err := guard.LoadWrites(root, "./...")
		if err != nil {
			t.Fatal(err)
		}
		if len(writes) != 0 {
			t.Errorf("LoadWrites found %v in a module that writes no program", writes)
		}
	}
}

// TestALoadOfPackagesThatDoNotCompileIsRefusedWithoutARace is the same race
// through the path that needs no package checked on purpose: a package that
// does not compile has no export data, and a loader that left type
// information to go/packages would see it checked from source there,
// concurrently with the others, before refusing it (the review of #628).
// Each loader must refuse the module, naming a package it could not read,
// with no checker racing another.
func TestALoadOfPackagesThatDoNotCompileIsRefusedWithoutARace(t *testing.T) {
	root := instancesModule(t, "\treturn n + undefinedHere\n")
	for range 2 {
		if uses, err := guard.Load(root, "./..."); err == nil {
			t.Fatalf("Load answered %v and no error for packages that do not compile", uses)
		} else if !strings.Contains(err.Error(), "example.com/instances/p") {
			t.Errorf("Load's error %q names no package it could not read", err)
		}
		if writes, err := guard.LoadWrites(root, "./..."); err == nil {
			t.Fatalf("LoadWrites answered %v and no error for packages that do not compile", writes)
		} else if !strings.Contains(err.Error(), "example.com/instances/p") {
			t.Errorf("LoadWrites' error %q names no package it could not read", err)
		}
	}
}

// instancesModule writes a module of 32 packages whose function ranges over
// each iterator strings and bytes return, with a test file that does too,
// and returns its root. ending, when not empty, is the function's last
// statement in place of its return.
func instancesModule(t *testing.T, ending string) string {
	t.Helper()
	if ending == "" {
		ending = "\treturn n\n"
	}
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/instances\n\ngo 1.25.0\n")
	const packages = 32
	for i := range packages {
		write(fmt.Sprintf("p%d/p.go", i), fmt.Sprintf(`package p%d

import (
	"bytes"
	"strings"
)

// Count counts the pieces of s each iterator yields.
func Count(s string) (n int) {
	for range strings.SplitSeq(s, ",") {
		n++
	}
	for range strings.FieldsSeq(s) {
		n++
	}
	for range strings.Lines(s) {
		n++
	}
	for range bytes.SplitSeq([]byte(s), []byte(",")) {
		n++
	}
	for range bytes.Lines([]byte(s)) {
		n++
	}
%s}
`, i, ending))
		write(fmt.Sprintf("p%d/p_test.go", i), fmt.Sprintf(`package p%d

import (
	"strings"
	"testing"
)

func TestCount(t *testing.T) {
	for field := range strings.SplitSeq("a,b", ",") {
		if Count(field) != 5 {
			t.Fatal(field)
		}
	}
}
`, i))
	}
	return root
}
