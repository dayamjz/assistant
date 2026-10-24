package guard

import (
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ModeSetters is the closed set of APIs that create a file with a mode or
// set a file's mode, by go/types' full name, each with the index of its mode
// argument. A call to one with an execute bit in that argument is a program
// being written, which procgroup.WriteProgram owns (#539).
var ModeSetters = map[string]int{
	"os.WriteFile":     2,
	"os.OpenFile":      2,
	"os.Chmod":         1,
	"(*os.File).Chmod": 0,
}

// executeBits are the permission bits that make a file a program.
const executeBits = 0o111

// WritePlatforms are the GOOS values LoadWrites reads the module for: the two
// supported platforms, which are the ones that fork. On Windows a start
// does not fork and package syscall does not take syscall.ForkLock, so a
// write there has nothing for WriteProgram's ordering to fix.
var WritePlatforms = []string{"darwin", "linux"}

// Write is one use of a mode setter, in test code or not, whose mode either
// carries an execute bit or is not a constant the type checker can read. The
// second counts because a mode handed through a parameter is how a program
// would be written past a check of constants alone: internal/gate wrote its
// hooks through a function that takes the mode.
type Write struct {
	// File, Line, Function and Package are Use's.
	File     string
	Line     int
	Function string
	Package  string
	// Setter is the entry of ModeSetters the use resolves to.
	Setter string
	// Mode is the constant mode the call passes, or "" when it passes one
	// the type checker cannot evaluate, or the use is not a call at all.
	Mode string
	// Platforms are the GOOS values whose build of the file holds the use.
	Platforms []string
}

// String renders a write the way a failure reads best.
func (w Write) String() string {
	fn := w.Function
	if fn == "" {
		fn = "package level"
	}
	mode := w.Mode
	if mode == "" {
		mode = "a mode that is not a constant"
	}
	return fmt.Sprintf("%s:%d in %s: %s with %s (%s)", w.File, w.Line, fn, w.Setter, mode, strings.Join(w.Platforms, ", "))
}

// LoadWrites type-checks the packages matched by patterns under root, their
// tests included, once for each platform in WritePlatforms, and returns every
// Write in them, merged across platforms and sorted by file and line. As
// with Load, a package that does not load or type-check cleanly is an error,
// never a skip.
func LoadWrites(root string, patterns ...string) ([]Write, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	type key struct {
		file      string
		line, col int
	}
	merged := map[key]*Write{}
	for _, goos := range WritePlatforms {
		cfg := &packages.Config{
			Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
				packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports,
			Dir:   abs,
			Env:   append(os.Environ(), "GOOS="+goos, "GOWORK=off"),
			Tests: true,
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
			for _, w := range writes(p) {
				rel, err := filepath.Rel(abs, w.File)
				if err != nil {
					return nil, err
				}
				w.File = filepath.ToSlash(rel)
				k := key{w.File, w.Line, w.col}
				if m, ok := merged[k]; ok {
					if !slices.Contains(m.Platforms, goos) {
						m.Platforms = append(m.Platforms, goos)
					}
					continue
				}
				w.Platforms = []string{goos}
				merged[k] = &w.Write
			}
		}
	}
	out := make([]Write, 0, len(merged))
	for _, w := range merged {
		out = append(out, *w)
	}
	slices.SortFunc(out, func(a, b Write) int {
		if c := strings.Compare(a.File, b.File); c != 0 {
			return c
		}
		return a.Line - b.Line
	})
	return out, nil
}

// locatedWrite is a Write before its path is made relative, with the column
// that tells two uses on one line apart.
type locatedWrite struct {
	Write
	col int
}

// writes returns every Write in one type-checked package. A test variant of
// a package holds its non-test files too, and LoadWrites merges the two by
// position.
func writes(p *packages.Package) []locatedWrite {
	files := make(map[string]*ast.File, len(p.Syntax))
	for _, f := range p.Syntax {
		files[p.Fset.Position(f.Pos()).Filename] = f
	}
	// A call's identifier, mapped to the call, so a use that is not one of
	// these is a function value whose mode nothing here can read.
	calls := map[*ast.Ident]*ast.CallExpr{}
	for _, f := range p.Syntax {
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id := calledIdent(call.Fun); id != nil {
					calls[id] = call
				}
			}
			return true
		})
	}
	var out []locatedWrite
	for id, obj := range p.TypesInfo.Uses {
		fn, ok := obj.(*types.Func)
		if !ok {
			continue
		}
		at, ok := ModeSetters[fn.FullName()]
		if !ok {
			continue
		}
		mode := ""
		if call, ok := calls[id]; ok && at < len(call.Args) {
			if v := p.TypesInfo.Types[call.Args[at]].Value; v != nil {
				if bits, exact := constant.Int64Val(constant.ToInt(v)); exact {
					if bits&executeBits == 0 {
						continue
					}
					mode = fmt.Sprintf("%#o", bits)
				}
			}
		}
		pos := p.Fset.Position(id.Pos())
		out = append(out, locatedWrite{
			Write: Write{
				File:     pos.Filename,
				Line:     pos.Line,
				Function: enclosing(files[pos.Filename], id.Pos()),
				Package:  p.PkgPath,
				Setter:   fn.FullName(),
				Mode:     mode,
			},
			col: pos.Column,
		})
	}
	return out
}

// calledIdent is the identifier a call's function expression names, through
// parentheses and a selector, or nil for any other shape.
func calledIdent(fun ast.Expr) *ast.Ident {
	for {
		paren, ok := fun.(*ast.ParenExpr)
		if !ok {
			break
		}
		fun = paren.X
	}
	switch f := fun.(type) {
	case *ast.Ident:
		return f
	case *ast.SelectorExpr:
		return f.Sel
	}
	return nil
}

// WriteException is one function outside internal/procgroup that may use a
// mode setter LoadWrites reports, because what it sets the mode of is not a
// program: a directory, whose execute bit is its search permission, or a
// file whose mode is carried over from another that is not one.
type WriteException struct {
	// File and Function are Exception's.
	File     string
	Function string
	// Why is what the function sets the mode of, and why that is not a
	// program something starts.
	Why string
}

// WriteVerdict is what CheckWrites found, on Verdict's terms.
type WriteVerdict struct {
	// Owned counts the writes in the owner's package. Zero means the load
	// read nothing there, which the caller reports as such.
	Owned int
	// Unexcepted are the writes the build fails on.
	Unexcepted []Write
	// Stale are the exceptions no write matched.
	Stale []WriteException
}

// CheckWrites sorts writes into those in owner's non-test files, those an
// exception covers, and the rest, and reports the exceptions nothing matched. An exception
// covers every write in its file and function.
func CheckWrites(writes []Write, owner string, exceptions []WriteException) WriteVerdict {
	var v WriteVerdict
	matched := make([]bool, len(exceptions))
	for _, w := range writes {
		if w.Package == owner && !strings.HasSuffix(w.File, "_test.go") {
			v.Owned++
			continue
		}
		covered := false
		for i, e := range exceptions {
			if e.File == w.File && e.Function == w.Function {
				matched[i] = true
				covered = true
			}
		}
		if !covered {
			v.Unexcepted = append(v.Unexcepted, w)
		}
	}
	for i, e := range exceptions {
		if !matched[i] {
			v.Stale = append(v.Stale, e)
		}
	}
	return v
}

// WellformedWrites reports the first row of exceptions that names no file or
// function or gives no Why, and a row that repeats another's file and
// function.
func WellformedWrites(exceptions []WriteException) error {
	seen := map[[2]string]bool{}
	for i, e := range exceptions {
		switch {
		case e.File == "" || e.Function == "":
			return fmt.Errorf("guard: write exception %d names no file or no function", i)
		case e.Why == "":
			return fmt.Errorf("guard: write exception %d (%s in %s) says nothing in Why", i, e.Function, e.File)
		case seen[[2]string{e.File, e.Function}]:
			return fmt.Errorf("guard: write exception %d repeats %s in %s", i, e.Function, e.File)
		}
		seen[[2]string{e.File, e.Function}] = true
	}
	return nil
}

// WriteExceptions is the closed list: every function outside
// internal/procgroup whose code, tests included, may use a mode setter
// LoadWrites reports. A row that covers no write fails the build as stale.
// A row excuses every write in its function, so a program written there
// later would be excused too; each Why names what the function writes so a
// reviewer can see a write that is not that.
var WriteExceptions = []WriteException{
	{
		File: "internal/agents/standin/tmux.go", Function: "SocketDir",
		Why: "Makes a test's socket directory owner-only; the mode is a directory's.",
	},
	{
		File: "internal/cli/hostinstall.go", Function: "writeAnchored",
		Why: "Writes an agent host's settings file, a JSON document, with the mode the file already had.",
	},
	{
		File: "internal/fixture/git.go", Function: "writeFile",
		Why: "Writes a planted file whose mode has no execute bit; one that has one goes to procgroup.WriteProgram first.",
	},
	{
		File: "internal/fixture/git.go", Function: "copyTree",
		Why: "Copies a file whose mode has no execute bit; one that has one goes to procgroup.WriteProgram first.",
	},
	{
		File: "internal/gate/initialize_test.go", Function: "refuseWrites",
		Why: "Takes the write permission off a directory and puts it back; the modes are a directory's.",
	},
	{
		File: "internal/home/fleet_test.go", Function: "TestTheLockProbeAnswersUnknownForAHomeItCannotRead",
		Why: "Puts back the mode of a home's root, a directory, that the test made unreadable.",
	},
	{
		File: "internal/stages/rebase_conflict_test.go", Function: "(modeFixer).Apply",
		Why: "Sets the executable bit on the conflicted file the fixer resolves. The test does not run that file.",
	},
	{
		File: "internal/store/storetest/storetest.go", Function: "Seed",
		Why: "Copies a database template, an SQLite file, with the template's own mode.",
	},
	{
		File: "internal/vcs/rebase_test.go", Function: "TestUncommittedDigestSeesAModeOnlyChangeOnAConflictedPath",
		Why: "Sets the executable bit on a conflicted file so the digest sees a mode-only change. The test does not run that file.",
	},
}
