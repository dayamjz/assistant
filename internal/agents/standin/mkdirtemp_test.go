package standin

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/dayamjz/assistant/internal/principles"
)

// P15: a directory a test makes is standin.TempDir's, so a killed test is
// reclaimed at the next start. os.MkdirTemp in a test file stamps nothing
// this sweep reads. The sweep's own unparseable and replaced-identity
// plants are not in a test file, because TempDir cannot make those names.
func TestATestDoesNotCallOsMkdirTemp(t *testing.T) {
	principles.Cite(t, principles.P15)
	got := testMkdirTemps(t, nil)
	if len(got) != 0 {
		t.Fatalf("os.MkdirTemp in test code:\n%s", strings.Join(got, "\n"))
	}
}

// P15: a test file that calls os.MkdirTemp fails this check. The real
// module has none, so a loader that sees nothing would also pass the test
// above; this plant is the call that must be seen.
func TestAPlantedTestMkdirTempIsRefused(t *testing.T) {
	principles.Cite(t, principles.P15)
	root := moduleRootOfTest(t)
	rel := "internal/service/deadline_unix_internal_test.go"
	path := filepath.Join(root, filepath.FromSlash(rel))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	old := []byte("\troot, err := standin.TempDir(\"h\")")
	if !bytes.Contains(src, old) {
		t.Fatalf("overlay anchor missing in %s", rel)
	}
	planted := bytes.Replace(src, old, []byte("\troot, err := os.MkdirTemp(\"\", \"h\")"), 1)
	got := testMkdirTemps(t, map[string][]byte{path: planted})
	if len(got) == 0 {
		t.Fatal("a test file calling os.MkdirTemp passed")
	}
}

func moduleRootOfTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, err := moduleRootFrom(wd)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func testMkdirTemps(t *testing.T, overlay map[string][]byte) []string {
	t.Helper()
	root := moduleRootOfTest(t)
	cfg := &packages.Config{
		Dir:        root,
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Tests:      true,
		BuildFlags: []string{"-mod=readonly"},
		Overlay:    overlay,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	for _, pkg := range pkgs {
		for _, pkgErr := range pkg.Errors {
			msgs = append(msgs, pkgErr.Error())
		}
	}
	if len(msgs) > 0 {
		t.Fatal(strings.Join(msgs, "\n"))
	}
	seen := map[string]struct{}{}
	var found []string
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isOsMkdirTemp(pkg.TypesInfo, call.Fun) {
					return true
				}
				pos := pkg.Fset.Position(call.Lparen)
				if !strings.HasSuffix(pos.Filename, "_test.go") {
					return true
				}
				key := fmt.Sprintf("%s:%d:%d", pos.Filename, pos.Line, pos.Column)
				if _, dup := seen[key]; dup {
					return true
				}
				seen[key] = struct{}{}
				found = append(found, pos.String())
				return true
			})
		}
	}
	return found
}

func isOsMkdirTemp(info *types.Info, fun ast.Expr) bool {
	for {
		paren, ok := fun.(*ast.ParenExpr)
		if !ok {
			break
		}
		fun = paren.X
	}
	var id *ast.Ident
	switch f := fun.(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return false
	}
	fn, ok := info.Uses[id].(*types.Func)
	return ok && fn.Pkg() != nil && fn.Pkg().Path() == "os" && fn.Name() == "MkdirTemp"
}
