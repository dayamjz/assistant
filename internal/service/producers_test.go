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
// on it claims. A second call site would be a second place the program
// decides a hold without a person, which PRD section 8 gives it for one hold
// and no other, so this reads every non-test source file in the module and
// counts them.
//
// It reads source rather than a type graph because the question is who
// calls a function, which no executable interface answers; the positive
// control is the same scan finding store.ResolvedByMachineInterface where
// internal/ipc is documented to produce it, so a scan that stopped seeing
// calls fails rather than reporting a guarantee it no longer checks.
func TestOnlyTheChecksHoldCloserProducesAProgramResolution(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	program := callSites(t, root, "ResolvedByProgram")
	if want := []string{filepath.Join("internal", "service", "checkshold.go")}; !equalSets(program, want) {
		t.Fatalf("store.ResolvedByProgram is produced in %v, want only %v", program, want)
	}
	machineInterface := callSites(t, root, "ResolvedByMachineInterface")
	if len(machineInterface) == 0 {
		t.Fatal("the scan found no producer of store.ResolvedByMachineInterface, which internal/ipc produces; " +
			"the scan sees nothing, so the count above establishes nothing")
	}
	for _, file := range machineInterface {
		if !strings.HasPrefix(file, filepath.Join("internal", "ipc")+string(filepath.Separator)) {
			t.Fatalf("store.ResolvedByMachineInterface is produced in %s, outside internal/ipc", file)
		}
	}
}

// callSites returns every non-test Go file under root, relative to it, that
// calls a function of the given name through a selector such as store.Name.
func callSites(t *testing.T, root, name string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if base := d.Name(); base != root && (strings.HasPrefix(base, ".") || base == "testdata") {
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
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == name {
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
