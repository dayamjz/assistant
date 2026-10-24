//go:build race

package guard_test

import (
	"fmt"
	"os"
	"path/filepath"
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
	return n
}
`, i))
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
