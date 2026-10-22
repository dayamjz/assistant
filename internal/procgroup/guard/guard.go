// Package guard finds every place this module's non-test code names an API
// that starts an external program, so a test can fail the build on one that
// is not internal/procgroup's and not on the closed exception list in
// exceptions.go. It is how P15's "an external program the service invokes
// runs under a deadline, and when the deadline passes it is ended together
// with everything it started" stops depending on each author remembering it,
// which is how #419 (stage commands) and #422 (lsof) each shipped a program
// with no bound.
//
// The question is asked of the type checker, not of the source text. A use is
// an identifier go/types resolves to one of the functions or methods in
// Starters, so an import under another name, a method promoted through an
// embedded *exec.Cmd, a method value such as cmd.Run passed as a function, and
// a method expression all count, and a local identifier that merely shares a
// name does not. A use counts whether or not it is called: a function value
// can be called anywhere.
//
// # What it does not see
//
//   - A program started through an API not in Starters, such as cgo or an
//     assembly stub, or by a dependency's own code. Only this module's
//     packages are read.
//   - A start through an interface: an *exec.Cmd held as a value of an
//     interface type whose method set includes Start, and started through
//     it, names no method of *exec.Cmd.
//   - _test.go files. Tests start their helper processes directly, and the
//     unbounded test processes of #404, #432 and #440 were of that kind; what
//     ends a test's processes is internal/agents/standin's owner watch and
//     its process check, where a package runs its tests through RunOwned,
//     not this guard.
//   - Files no loaded platform compiles. Load reads the module once per
//     platform in Platforms, and a file built only for another platform is
//     read by none of them.
package guard

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Starters is the closed set of APIs that start a program, by go/types'
// full name for the function or method. os/exec's constructors are in it
// as well as the methods that start a command, so every *exec.Cmd this
// module builds is built where the guard allows: a command built by
// procgroup.Command and handed to procgroup.Run names no starter outside
// internal/procgroup.
//
// Each name is one the type checker resolves. The control fixture names
// every entry, so a string that matches no function fails that test.
// golang.org/x/sys/unix.ForkExec is not an entry: that package has Exec
// and no ForkExec in the version this module requires, and a call to the
// missing name does not type-check. A later version that adds ForkExec
// fails the test that looks the name up until Starters lists it.
var Starters = []string{
	"os/exec.Command",
	"os/exec.CommandContext",
	"(*os/exec.Cmd).Start",
	"(*os/exec.Cmd).Run",
	"(*os/exec.Cmd).Output",
	"(*os/exec.Cmd).CombinedOutput",
	"os.StartProcess",
	"syscall.ForkExec",
	"syscall.StartProcess",
	"syscall.Exec",
	"syscall.CreateProcess",
	"syscall.CreateProcessAsUser",
	"golang.org/x/sys/unix.Exec",
	"golang.org/x/sys/windows.CreateProcess",
	"golang.org/x/sys/windows.CreateProcessAsUser",
}

// Platforms are the GOOS values Load reads the module for: the two supported
// platforms and Windows, whose sources build and are vetted (AGENTS.md), so a
// Windows-only start is found too.
var Platforms = []string{"darwin", "linux", "windows"}

// Use is one identifier in non-test code that resolves to a starter.
type Use struct {
	// File is the file's path relative to the loaded root, with forward
	// slashes.
	File string
	Line int
	// Function is the declaration the use sits in, as Exception.Function
	// spells it: "F" for a function, "(T).M" or "(*T).M" for a method. It is
	// empty for a use outside every function, such as a package-level
	// variable's initializer.
	Function string
	// Package is the import path of the package the file belongs to.
	Package string
	// Starter is the entry of Starters the identifier resolves to.
	Starter string
	// Platforms are the GOOS values whose build of the file holds the use.
	Platforms []string
}

// String renders a use the way a failure reads best.
func (u Use) String() string {
	fn := u.Function
	if fn == "" {
		fn = "package level"
	}
	return fmt.Sprintf("%s:%d in %s: %s (%s)", u.File, u.Line, fn, u.Starter, strings.Join(u.Platforms, ", "))
}

// Load type-checks the packages matched by patterns under root once for each
// platform, and returns every use of a starter in their non-test files,
// merged across platforms and sorted by file and line. A package that does
// not load or type-check cleanly for one of the platforms is an error, never
// a skip: a package the guard could not read is one it would pass unread.
func Load(root string, patterns ...string) ([]Use, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	merged := map[useKey]*Use{}
	for _, goos := range Platforms {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
				packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
			Dir: abs,
			Env: append(os.Environ(), "GOOS="+goos, "GOWORK=off"),
		}
		pkgs, err := packages.Load(cfg, patterns...)
		if err != nil {
			return nil, fmt.Errorf("guard: loading for %s: %w", goos, err)
		}
		if len(pkgs) == 0 {
			return nil, fmt.Errorf("guard: loading for %s matched no package", goos)
		}
		var errs []error
		for _, p := range pkgs {
			for _, e := range p.Errors {
				errs = append(errs, fmt.Errorf("guard: %s for %s: %w", p.PkgPath, goos, e))
			}
		}
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		for _, p := range pkgs {
			for _, u := range uses(p) {
				rel, err := filepath.Rel(abs, u.File)
				if err != nil {
					return nil, err
				}
				u.File = filepath.ToSlash(rel)
				k := useKey{u.File, u.Line, u.col, u.Starter}
				if m, ok := merged[k]; ok {
					m.Platforms = append(m.Platforms, goos)
					continue
				}
				u.Platforms = []string{goos}
				merged[k] = &u.Use
			}
		}
	}
	out := make([]Use, 0, len(merged))
	for _, u := range merged {
		out = append(out, *u)
	}
	slices.SortFunc(out, func(a, b Use) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Starter, b.Starter)
	})
	return out, nil
}

type useKey struct {
	file      string
	line, col int
	starter   string
}

// located is a Use before its path is made relative, with the column that
// tells two uses on one line apart.
type located struct {
	Use
	col int
}

// uses returns every use of a starter in one type-checked package.
func uses(p *packages.Package) []located {
	files := make(map[string]*ast.File, len(p.Syntax))
	for _, f := range p.Syntax {
		files[p.Fset.Position(f.Pos()).Filename] = f
	}
	var out []located
	for id, obj := range p.TypesInfo.Uses {
		fn, ok := obj.(*types.Func)
		if !ok || !slices.Contains(Starters, fn.FullName()) {
			continue
		}
		pos := p.Fset.Position(id.Pos())
		// A use in a file the package's syntax does not hold has no
		// function this guard can place it in, and enclosing answers none
		// for it rather than the use being dropped.
		f := files[pos.Filename]
		out = append(out, located{
			Use: Use{
				File:     pos.Filename,
				Line:     pos.Line,
				Function: enclosing(f, id.Pos()),
				Package:  p.PkgPath,
				Starter:  fn.FullName(),
			},
			col: pos.Column,
		})
	}
	return out
}

// enclosing names the function declaration in f that holds pos, on
// Use.Function's terms.
func enclosing(f *ast.File, pos token.Pos) string {
	if f == nil {
		return ""
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || pos < fd.Pos() || pos >= fd.End() {
			continue
		}
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return fd.Name.Name
		}
		return "(" + receiver(fd.Recv.List[0].Type) + ")." + fd.Name.Name
	}
	return ""
}

// receiver spells a receiver's type without its type parameters.
func receiver(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + receiver(t.X)
	case *ast.IndexExpr:
		return receiver(t.X)
	case *ast.IndexListExpr:
		return receiver(t.X)
	case *ast.Ident:
		return t.Name
	}
	return fmt.Sprintf("%T", e)
}

// Verdict is what Check found: the uses neither the owner nor an exception
// covers, and the exceptions that covered no use.
type Verdict struct {
	// Owned counts the uses in the owner's package. Zero means the loaded
	// tree held no use there either, which for this module is a load that
	// read nothing and is reported by the caller as such.
	Owned int
	// Unexcepted are the uses the build fails on.
	Unexcepted []Use
	// Stale are the exceptions no use matched. An exception that excuses
	// nothing is a hole waiting for a use to fill it, so it fails the build
	// as well.
	Stale []Exception
}

// Check sorts uses into those in owner, the import path whose package may
// name a starter, those an exception covers, and the rest, and reports the
// exceptions nothing matched. An exception covers every use in its file and
// function, so a function that both builds and starts a command needs one
// entry.
func Check(uses []Use, owner string, exceptions []Exception) Verdict {
	var v Verdict
	matched := make([]bool, len(exceptions))
	for _, u := range uses {
		if u.Package == owner {
			v.Owned++
			continue
		}
		covered := false
		for i, e := range exceptions {
			if e.File == u.File && e.Function == u.Function {
				matched[i] = true
				covered = true
			}
		}
		if !covered {
			v.Unexcepted = append(v.Unexcepted, u)
		}
	}
	for i, e := range exceptions {
		if !matched[i] {
			v.Stale = append(v.Stale, e)
		}
	}
	return v
}
