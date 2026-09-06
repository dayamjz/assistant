package principles

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// importPathSuffix is how a file's import of this package is recognized. The
// module path is not spelled out, so this package's own tests can scan a tree
// that is not this module.
const importPathSuffix = "internal/principles"

// Citation is one test's claim on one principle: the call, and the test the
// call sits in. File is relative to the root that was scanned.
type Citation struct {
	Principle Principle
	File      string
	Line      int
	Test      string
}

// String renders a citation the way a failure reads best: the principle, the
// test claiming it, and where to find the call.
func (c Citation) String() string {
	return fmt.Sprintf("%s in %s (%s:%d)", c.Principle, c.Test, c.File, c.Line)
}

// ErrCitation reports a call to Cite this package could not read as a
// citation. It is a refusal and not a skip: a citation nobody can read is the
// case where a principle looks claimed to its author and is not claimed here.
var ErrCitation = errors.New("a call to principles.Cite could not be read as a citation")

// Citations returns every citation under root, in the order the walk found
// them.
//
// A citation is a call to Cite, through an import of this package, lexically
// inside the body of a test function in a _test.go file. Each part of that
// rule earns its place:
//
//   - Through an import, so the call names this package's own constants. A
//     principle that does not exist does not compile, and no other spelling of
//     a citation has to be maintained.
//   - Inside the body, so a helper cannot cite on behalf of its callers. The
//     citing unit is the test a reader can run by name. A call in a closure
//     the test body declares is inside it and does count.
//   - In a test function, so a citation cannot be parked in ordinary code that
//     no test reaches.
//
// A comment is not a citation, which is the point of the rule rather than an
// accident of it. Comments are where these claims lived before this package,
// and a comment can name a principle the PRD does not have, can outlive every
// assertion it was written about, and costs nothing to leave behind. None of
// those is true of a call.
//
// Directories named testdata or vendor, and directories whose names begin with
// a dot, are not walked.
func Citations(root string) ([]Citation, error) {
	_, out, err := scan(root)
	return out, err
}

// scan walks root once and reports how many test files it read along with the
// citations in them. The count is what lets Check tell a repository whose
// tests claim nothing from a root it was never going to find a test under.
func scan(root string) (int, []Citation, error) {
	var files int
	var out []Citation
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p == root {
				return nil
			}
			name := d.Name()
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		files++
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", rel, err)
		}
		found, err := citationsIn(fset, file, rel)
		if err != nil {
			return err
		}
		out = append(out, found...)
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return files, out, nil
}

// citationsIn collects the citations one parsed test file makes.
func citationsIn(fset *token.FileSet, file *ast.File, rel string) ([]Citation, error) {
	pkg, ok := localName(file, importPathSuffix)
	if !ok {
		return nil, nil
	}
	testing, hasTesting := localName(file, "testing")
	if !hasTesting {
		return nil, nil
	}

	var out []Citation
	for _, decl := range file.Decls {
		fn, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || !isTestFunc(fn, testing) {
			continue
		}
		var bad error
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall || !isSelector(call.Fun, pkg, "Cite") {
				return true
			}
			line := fset.Position(call.Pos()).Line
			if len(call.Args) < 2 {
				bad = fmt.Errorf("%w: %s:%d in %s names no principle", ErrCitation, rel, line, fn.Name.Name)
				return false
			}
			for _, arg := range call.Args[1:] {
				p, ok := principleArg(arg, pkg)
				if !ok {
					bad = fmt.Errorf("%w: %s:%d in %s cites something other than a %s constant, so what it claims cannot be read here", ErrCitation, rel, line, fn.Name.Name, pkg)
					return false
				}
				out = append(out, Citation{Principle: p, File: rel, Line: line, Test: fn.Name.Name})
			}
			return true
		})
		if bad != nil {
			return nil, bad
		}
	}
	return out, nil
}

// localName returns the name a file refers to an imported package by, and
// whether it imports it at all. A dot import has no name to qualify a call
// with and is reported as not imported: the rule Citations documents is that
// a citation is qualified, and silently accepting another spelling would make
// the rule something other than what it says.
func localName(file *ast.File, suffix string) (string, bool) {
	for _, spec := range file.Imports {
		p, err := strconv.Unquote(spec.Path.Value)
		if err != nil || (p != suffix && !strings.HasSuffix(p, "/"+suffix)) {
			continue
		}
		if spec.Name == nil {
			return path.Base(p), true
		}
		if spec.Name.Name == "_" || spec.Name.Name == "." {
			return "", false
		}
		return spec.Name.Name, true
	}
	return "", false
}

// isTestFunc reports whether a declaration is a test function as `go test`
// counts one: a top-level func named Test, or Test followed by something that
// does not start with a lower case letter, taking one *testing.T.
func isTestFunc(fn *ast.FuncDecl, testing string) bool {
	if fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
		return false
	}
	if rest := fn.Name.Name[len("Test"):]; rest != "" && unicode.IsLower([]rune(rest)[0]) {
		return false
	}
	params := fn.Type.Params.List
	if len(params) != 1 {
		return false
	}
	star, isStar := params[0].Type.(*ast.StarExpr)
	return isStar && isSelector(star.X, testing, "T")
}

// principleArg reads one argument of a Cite call as the principle it names.
func principleArg(arg ast.Expr, pkg string) (Principle, bool) {
	sel, isSel := arg.(*ast.SelectorExpr)
	if !isSel {
		return 0, false
	}
	if ident, isIdent := sel.X.(*ast.Ident); !isIdent || ident.Name != pkg {
		return 0, false
	}
	name := sel.Sel.Name
	if len(name) < 2 || name[0] != 'P' {
		return 0, false
	}
	n, err := strconv.Atoi(name[1:])
	if err != nil || n < 1 || n > int(^Principle(0)) {
		return 0, false
	}
	return Principle(n), true
}

// isSelector reports whether e is the expression pkg.name.
func isSelector(e ast.Expr, pkg, name string) bool {
	sel, isSel := e.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != name {
		return false
	}
	ident, isIdent := sel.X.(*ast.Ident)
	return isIdent && ident.Name == pkg
}
