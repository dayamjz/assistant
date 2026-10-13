package service_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// store.ResolvedByProgram has exactly one producer in this repository, the
// checks-hold closer in this package, which is what store's own doc comment
// on it claims. A second reference would be a second place the program could
// decide a hold without a person, which PRD section 8 gives it for one hold
// and no other, so this reads every non-test source file in the module and
// counts every reference to the name: a call, a function value taken and
// called elsewhere, a dot-imported or unqualified use, all alike, and not
// only a call through a selector. internal/store's own file holds the
// declaration and the one unqualified use that lists the closed set, and is
// the one other file allowed.
//
// It reads source rather than a type graph because the question is who
// refers to a function, which no executable interface answers; the positive
// control is the same scan finding store.ResolvedByMachineInterface where
// internal/ipc is documented to produce it, so a scan that stopped seeing
// references fails rather than reporting a guarantee it no longer checks.
//
// The residual gap is what a source scan cannot see: a reference assembled
// at run time, such as reflection on the exported name, and a file excluded
// from the build by a constraint this scan does not evaluate, which it
// counts anyway and so errs toward refusing. Nothing here reads a built
// binary.
func TestOnlyTheChecksHoldCloserProducesAProgramResolution(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	program := references(t, root, "ResolvedByProgram")
	want := []string{
		filepath.Join("internal", "service", "checkshold.go"),
		filepath.Join("internal", "store", "resolver.go"),
	}
	if !equalSets(program, want) {
		t.Fatalf("store.ResolvedByProgram is referred to in %v, want only %v", program, want)
	}
	machineInterface := references(t, root, "ResolvedByMachineInterface")
	if len(machineInterface) == 0 {
		t.Fatal("the scan found no reference to store.ResolvedByMachineInterface, which internal/ipc produces; " +
			"the scan sees nothing, so the count above establishes nothing")
	}
	for _, file := range machineInterface {
		inIPC := strings.HasPrefix(file, filepath.Join("internal", "ipc")+string(filepath.Separator))
		inStore := file == filepath.Join("internal", "store", "resolver.go")
		if !inIPC && !inStore {
			t.Fatalf("store.ResolvedByMachineInterface is referred to in %s, outside internal/ipc", file)
		}
	}
}

// references returns every non-test Go file under root, relative to it, that
// refers to the given name: as a selector such as store.Name, or as a bare
// identifier, which is a dot import, an unqualified use inside the declaring
// package, or the declaration itself. A declaration counts, because a package
// that declares a second function of the name would be a second producer.
func references(t *testing.T, root, name string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if base := d.Name(); path != root && (strings.HasPrefix(base, ".") || base == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		found := false
		ast.Inspect(parsed, func(n ast.Node) bool {
			// A selector's Sel is itself an *ast.Ident, so one case covers
			// store.Name, a dot-imported Name, and the declaring package's
			// own Name; a field or method of the same name on another type
			// would count too, which errs toward refusing.
			if ident, ok := n.(*ast.Ident); ok && ident.Name == name {
				found = true
			}
			return true
		})
		if found {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning %s: %v", root, err)
	}
	return files
}

// moduleRoot is the directory holding go.mod, found from this file.
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("the test cannot locate itself")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above this test")
		}
		dir = parent
	}
}

func equalSets(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := make(map[string]bool, len(want))
	for _, w := range want {
		set[w] = true
	}
	for _, g := range got {
		if !set[g] {
			return false
		}
	}
	return true
}
