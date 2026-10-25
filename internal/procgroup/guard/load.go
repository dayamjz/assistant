package guard

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"strings"
	"sync"

	"golang.org/x/tools/go/gcexportdata"
	"golang.org/x/tools/go/packages"
)

// typeCheckEach is typeCheck for each of platforms, all at once, with each
// platform's packages at its index. The platforms share no package, because
// each is loaded on its own and reads its own export data, so their checkers
// are independent ones, which go/types allows to run concurrently: no
// generic instance is read by two of them.
func typeCheckEach(root string, platforms []string, tests bool, patterns []string) ([][]*packages.Package, error) {
	loaded := make([][]*packages.Package, len(platforms))
	errs := make([]error, len(platforms))
	var wg sync.WaitGroup
	for i, goos := range platforms {
		wg.Go(func() { loaded[i], errs[i] = typeCheck(root, goos, tests, patterns) })
	}
	wg.Wait()
	return loaded, errors.Join(errs...)
}

// typeCheck loads the packages patterns match under root for one GOOS, their
// test variants too when tests is set, and type-checks them from source, one
// at a time, filling the Fset, Syntax, Types and TypesInfo of each. A package
// that does not load or type-check cleanly is an error, never a skip.
//
// go/packages is asked for the package graph and each package's export data
// file, and for no type information, so it starts no type checker: it would
// check packages concurrently, from source wherever it has no export data,
// and go/types from Go 1.26 races when two checkers first read the same
// generic instance of an imported package, such as the iter.Seq[string] of
// a range over strings.SplitSeq. A checker reads the instance's right-hand
// side without the lock the other's expansion of it holds (golang/go#81122,
// whose fix, go.dev/cl/841585, is in review and in no release), which failed
// a -race run on macOS (#624). Here one package at a time is read or checked,
// so the instance has one reader, and its lock covers every expansion after
// that. A package with no export data does not build, and is refused before
// anything is read.
//
// What is checked from source is go/packages' own rule: every matched
// package, and every package that imports one of them however indirectly,
// such as a test-only package that imports a test variant. Checked before
// its importers, each one is what they import, so no package sees two
// versions of one type. The rest are read from their export data, also
// before their importers.
func typeCheck(root, goos string, tests bool, patterns []string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedExportFile |
			packages.NeedTypesSizes | packages.NeedModule,
		Dir:   root,
		Env:   append(os.Environ(), "GOOS="+goos, "GOWORK=off"),
		Tests: tests,
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("guard: loading for %s: %w", goos, err)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("guard: loading for %s matched no package", goos)
	}
	var errs []error
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			errs = append(errs, fmt.Errorf("guard: %s for %s: %w", p.PkgPath, goos, e))
		}
		if len(p.Errors) == 0 && p.ExportFile == "" && p.PkgPath != "unsafe" {
			errs = append(errs, fmt.Errorf("guard: %s for %s has no export data, so it does not build", p.ID, goos))
		}
		// Types go/packages filled in are types it read or checked itself,
		// concurrently.
		if p.Types != nil || p.TypesInfo != nil {
			errs = append(errs, fmt.Errorf("guard: go/packages loaded the types of %s for %s itself", p.ID, goos))
		}
	})
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	matched := make(map[*packages.Package]bool, len(pkgs))
	for _, p := range pkgs {
		matched[p] = true
	}
	fromSource := map[*packages.Package]bool{}
	fset := token.NewFileSet()
	// Visit calls post on a package after every package it imports.
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		fromSource[p] = matched[p]
		for _, imp := range p.Imports {
			fromSource[p] = fromSource[p] || fromSource[imp]
		}
		var err error
		switch {
		case p.PkgPath == "unsafe":
			p.Types = types.Unsafe
		case fromSource[p]:
			err = checkFromSource(fset, p)
		default:
			err = readExportData(fset, p)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("guard: %s for %s: %w", p.ID, goos, err))
		}
	})
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return pkgs, nil
}

// readExportData sets p's Types from its export data file, the way
// go/packages reads one: the reader is shown every package p imports however
// indirectly, under its path, so it reuses the ones already read or checked
// rather than making its own.
func readExportData(fset *token.FileSet, p *packages.Package) error {
	f, err := os.Open(p.ExportFile)
	if err != nil {
		return err
	}
	// A file only read from has nothing a close error could lose.
	defer func() { _ = f.Close() }()
	r, err := gcexportdata.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", p.ExportFile, err)
	}
	view := map[string]*types.Package{}
	seen := map[*packages.Package]bool{}
	var show func(imports map[string]*packages.Package)
	show = func(imports map[string]*packages.Package) {
		for _, imp := range imports {
			if !seen[imp] {
				seen[imp] = true
				view[imp.PkgPath] = imp.Types
				show(imp.Imports)
			}
		}
	}
	show(p.Imports)
	tpkg, err := gcexportdata.Read(r, fset, view, p.PkgPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", p.ExportFile, err)
	}
	p.Types = tpkg
	return nil
}

// checkFromSource parses p's compiled files and type-checks them against the
// types of p's imports, which this file read or checked before. p's Types
// are the ones checked here even when checking failed, so p's importers are
// checked against them.
func checkFromSource(fset *token.FileSet, p *packages.Package) error {
	syntax := make([]*ast.File, 0, len(p.CompiledGoFiles))
	for _, name := range p.CompiledGoFiles {
		f, err := parser.ParseFile(fset, name, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		syntax = append(syntax, f)
	}
	info := &types.Info{
		Types:        map[ast.Expr]types.TypeAndValue{},
		Defs:         map[*ast.Ident]types.Object{},
		Uses:         map[*ast.Ident]types.Object{},
		Implicits:    map[ast.Node]types.Object{},
		Instances:    map[*ast.Ident]types.Instance{},
		Scopes:       map[ast.Node]*types.Scope{},
		Selections:   map[*ast.SelectorExpr]*types.Selection{},
		FileVersions: map[*ast.File]string{},
	}
	var errs []error
	conf := types.Config{
		Importer: importerFunc(func(path string) (*types.Package, error) {
			if path == "unsafe" {
				return types.Unsafe, nil
			}
			imp, ok := p.Imports[path]
			if !ok || imp.Types == nil || !imp.Types.Complete() {
				return nil, fmt.Errorf("no type information for import %q", path)
			}
			return imp.Types, nil
		}),
		Sizes: p.TypesSizes,
		Error: func(err error) { errs = append(errs, err) },
	}
	if p.Module != nil && p.Module.GoVersion != "" {
		conf.GoVersion = "go" + strings.TrimPrefix(p.Module.GoVersion, "go")
	}
	pkg := types.NewPackage(p.PkgPath, p.Name)
	// The checker's own error is the first one Error was handed, so it adds
	// nothing to errs.
	_ = types.NewChecker(&conf, fset, pkg, info).Files(syntax)
	p.Fset, p.Syntax, p.Types, p.TypesInfo = fset, syntax, pkg, info
	return errors.Join(errs...)
}

// importerFunc is a types.Importer that is a function.
type importerFunc func(path string) (*types.Package, error)

// Import implements types.Importer.
func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
