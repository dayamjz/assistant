package standin

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"

	"github.com/dayamjz/assistant/internal/principles"
)

// tempTagUse is one call to TempDir, SweepTempDirs, or RegisterTempTag, charged
// to one test binary. tags are the letters the argument can be seen to hold.
// registered means the argument is the result of RegisteredTempTag, so the
// letter is the one this binary passed to RegisterTempTag. unknown means the
// letter cannot be seen; the check fails rather than ignoring the call.
type tempTagUse struct {
	binary     string
	fn         string
	tags       []string
	registered bool
	unknown    bool
	pos        string
}

// P15: two packages on one letter are one namespace, so a start of either
// reclaims the other's directories. Coverage is the calls, type-checked.
// A slice of the tag constants is not coverage: an empty slice, and a slice
// that drops feed after service's letter is changed to the same p, both used
// to pass.
func TestTempTagsAreOnePerPackage(t *testing.T) {
	principles.Cite(t, principles.P15)

	t.Run("empty coverage", func(t *testing.T) {
		if repeatedLetter(nil) != "" {
			t.Fatal("the hollow list walk reported a duplicate in an empty slice")
		}
		if err := checkTempTagUses(nil); err == nil {
			t.Fatal("an empty coverage list passed")
		}
	})

	t.Run("feed omitted and service shares p", func(t *testing.T) {
		// The list the previous guard walked. Feed's constant is absent,
		// and service's constant was edited to p, so p appears once.
		listed := []string{"a", "b", "c", "d", "e", "f", "g", "i", "j", "k", "n", "p"}
		if dup := repeatedLetter(listed); dup != "" {
			t.Fatalf("the hollow list already repeats %q", dup)
		}
		uses := []tempTagUse{}
		for _, pair := range []struct{ binary, tag string }{
			{"internal/agents/standin", "a"},
			{"internal/store/storetest", "b"},
			{"internal/cli", "c"},
			{"internal/store", "d"},
			{"internal/session", "e"},
			{"internal/fixture", "f"},
			{"internal/stages", "g"},
			{"internal/ipc", "i"},
			{"internal/journey", "j"},
			{"internal/checkpoints", "k"},
			{"internal/agents", "n"},
			{"internal/supervisor/feed", "p"},
			{"internal/service", "p"},
		} {
			uses = append(uses, constantTag(pair.binary, pair.tag)...)
		}
		if err := checkTempTagUses(uses); err == nil {
			t.Fatal("service and feed sharing p passed")
		}
	})

	t.Run("missing tag", func(t *testing.T) {
		uses := constantTag("internal/cli", "c")
		uses = append(uses, tempTagUse{binary: "planted/missing", fn: "TempDir", tags: []string{"q"}, pos: "planted"})
		if err := checkTempTagUses(uses); err == nil {
			t.Fatal("a TempDir with no sweep passed")
		}
	})

	t.Run("accepted", func(t *testing.T) {
		uses := constantTag("internal/cli", "c")
		uses = append(uses, constantTag("internal/service", "s")...)
		uses = append(uses,
			tempTagUse{binary: "internal/agents/standin", fn: "RegisterTempTag", tags: []string{"a"}, pos: "register"},
			tempTagUse{binary: "internal/agents/standin", fn: "TempDir", registered: true, pos: "registered"},
		)
		uses = append(uses, constantTag("internal/agents/standin", "a")...)
		if err := checkTempTagUses(uses); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unseen letter", func(t *testing.T) {
		uses := constantTag("internal/cli", "c")
		uses = append(uses, tempTagUse{binary: "internal/cli", fn: "TempDir", unknown: true, pos: "unseen"})
		if err := checkTempTagUses(uses); err == nil {
			t.Fatal("an argument whose letter cannot be seen passed")
		}
	})

	uses, err := loadTempTagUses(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkTempTagUses(uses); err != nil {
		t.Fatal(err)
	}
	found := map[string]struct{}{}
	for _, u := range uses {
		if u.fn == "TempDir" || u.fn == "SweepTempDirs" {
			found[u.binary] = struct{}{}
		}
	}
	for _, binary := range coverageFloor {
		if _, ok := found[binary]; !ok {
			t.Fatalf("coverage missed %s; found %s", binary, strings.Join(slices.Sorted(maps.Keys(found)), ", "))
		}
	}
	feed, ok := constantTagOf(uses, "internal/supervisor/feed")
	if !ok {
		t.Fatal("feed's calls have no constant tag")
	}
	planted := append(append([]tempTagUse{}, uses...), constantTag("planted/duplicate", feed)...)
	if err := checkTempTagUses(planted); err == nil {
		t.Fatalf("a planted second package on feed's tag %q passed", feed)
	}
	missing := append(append([]tempTagUse{}, uses...), tempTagUse{
		binary: "planted/missing", fn: "TempDir", tags: []string{"q"}, pos: "planted",
	})
	if err := checkTempTagUses(missing); err == nil {
		t.Fatal("a planted TempDir with no sweep passed")
	}
}

// coverageFloor is the set of test binaries the type-checked walk has to
// see. It is not the source of the tags. A walk that missed one of these
// would be the empty-list failure again, on a shorter list.
var coverageFloor = []string{
	"internal/agents",
	"internal/agents/standin",
	"internal/checkpoints",
	"internal/cli",
	"internal/fixture",
	"internal/ipc",
	"internal/journey",
	"internal/service",
	"internal/session",
	"internal/stages",
	"internal/store",
	"internal/store/storetest",
	"internal/supervisor/feed",
}

func constantTag(binary, tag string) []tempTagUse {
	return []tempTagUse{
		{binary: binary, fn: "TempDir", tags: []string{tag}, pos: binary},
		{binary: binary, fn: "SweepTempDirs", tags: []string{tag}, pos: binary},
	}
}

func constantTagOf(uses []tempTagUse, binary string) (string, bool) {
	for _, u := range uses {
		if u.binary == binary && len(u.tags) == 1 && (u.fn == "TempDir" || u.fn == "SweepTempDirs") {
			return u.tags[0], true
		}
	}
	return "", false
}

// repeatedLetter is the previous guard: it walks a supplied slice and never
// asks where the slice came from. An empty slice, or a slice that dropped a
// package, has no duplicate to report.
func repeatedLetter(tags []string) string {
	seen := map[string]struct{}{}
	for _, tag := range tags {
		if _, ok := seen[tag]; ok {
			return tag
		}
		seen[tag] = struct{}{}
	}
	return ""
}

// checkTempTagUses requires every test binary that calls TempDir or
// SweepTempDirs to have one letter, a sweep of that letter, and a letter no
// other binary uses. tags are the letters a call's argument can be seen to
// hold, including a variable's assignment. registered means the argument is
// RegisteredTempTag's result, so it contributes the letter this binary passed
// to RegisterTempTag; with no letter recorded, that call refuses and stamps
// nothing. unknown fails the check. A value that is not one lowercase letter
// is refused by the function and is not a namespace.
func checkTempTagUses(uses []tempTagUse) error {
	type call struct {
		fn         string
		pos        string
		tags       []string
		registered bool
		unknown    bool
	}
	bins := map[string][]call{}
	callers := 0
	for _, u := range uses {
		if u.fn != "TempDir" && u.fn != "SweepTempDirs" && u.fn != "RegisterTempTag" {
			continue
		}
		if u.fn == "TempDir" || u.fn == "SweepTempDirs" {
			callers++
		}
		bins[u.binary] = append(bins[u.binary], call{
			fn: u.fn, pos: u.pos, tags: u.tags, registered: u.registered, unknown: u.unknown,
		})
	}
	if callers == 0 {
		return errors.New("temp tag coverage is empty: no call to standin.TempDir or SweepTempDirs")
	}
	seen := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(bins)) {
		reg := map[string]string{}
		for _, c := range bins[name] {
			if c.fn != "RegisterTempTag" {
				continue
			}
			if c.unknown {
				return fmt.Errorf("%s: RegisterTempTag argument is not a letter this walk can see", c.pos)
			}
			for _, tag := range c.tags {
				if err := validTempTag(tag); err != nil {
					return fmt.Errorf("%s: RegisterTempTag: %w", c.pos, err)
				}
				reg[tag] = c.pos
			}
		}
		got := map[string]string{}
		sweep := map[string]struct{}{}
		stamped := false
		for _, c := range bins[name] {
			if c.fn != "TempDir" && c.fn != "SweepTempDirs" {
				continue
			}
			if c.unknown {
				return fmt.Errorf("%s: %s argument is not a letter this walk can see", c.pos, c.fn)
			}
			letters := append([]string{}, c.tags...)
			if c.registered {
				if len(reg) == 0 {
					continue
				}
				for tag := range reg {
					letters = append(letters, tag)
				}
			}
			if len(letters) == 0 {
				continue
			}
			stamped = true
			for _, tag := range letters {
				if err := validTempTag(tag); err != nil {
					return fmt.Errorf("%s: %s: %w", c.pos, c.fn, err)
				}
				got[tag] = c.pos
				if c.fn == "SweepTempDirs" {
					sweep[tag] = struct{}{}
				}
			}
		}
		for tag, pos := range reg {
			got[tag] = pos
			stamped = true
		}
		if !stamped {
			continue
		}
		if len(got) != 1 {
			return fmt.Errorf("%s: want exactly one temp tag, found %d (%s)", name, len(got), formatTags(got))
		}
		var tag string
		for found := range got {
			tag = found
		}
		if _, ok := sweep[tag]; !ok {
			return fmt.Errorf("%s: SweepTempDirs does not sweep %q", name, tag)
		}
		if prev, ok := seen[tag]; ok {
			return fmt.Errorf("tag %q is used by %s and %s", tag, prev, name)
		}
		seen[tag] = name
	}
	if len(seen) == 0 {
		return errors.New("temp tag coverage is empty: no call to standin.TempDir or SweepTempDirs")
	}
	return nil
}

func formatTags(tags map[string]string) string {
	keys := slices.Sorted(maps.Keys(tags))
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = fmt.Sprintf("%q at %s", key, tags[key])
	}
	return strings.Join(parts, ", ")
}

// P15: RegisterTempTag(tag) with tag := TempTagFeed records feed's letter.
// SocketDir, the tmux wrapper, and storetest's template then stamp that
// letter, and feed's sweep reclaims the directories. A walk that keeps only
// the constant SweepTempDirs and TempDir arguments still sees service as s.
func TestAVariableHoldingAnotherPackagesTagIsThatTag(t *testing.T) {
	principles.Cite(t, principles.P15)
	t.Run("register", func(t *testing.T) {
		uses := loadReplacing(t, "internal/service/helpers_test.go",
			"\tif err := standin.RegisterTempTag(standin.TempTagService); err != nil {",
			"\ttag := standin.TempTagFeed\n\tif err := standin.RegisterTempTag(tag); err != nil {")
		if err := checkTempTagUses(uses); err == nil {
			t.Fatal("service registering feed's letter through a variable passed")
		}
	})
	t.Run("temp dir", func(t *testing.T) {
		uses := loadReplacing(t, "internal/service/helpers_test.go",
			"\tdir, err := standin.TempDir(standin.TempTagService, \"gitconfig\")",
			"\ttag := standin.TempTagFeed\n\tdir, err := standin.TempDir(tag, \"gitconfig\")")
		if err := checkTempTagUses(uses); err == nil {
			t.Fatal("service stamping feed's letter through a variable passed")
		}
	})
}

// P15: a TempDir call in a non-test file runs in every test binary that
// imports that file's package. storetest stamping its own letter is charged
// to cli and service, which import it and call Seed, and their sweeps then
// reclaim those directories.
func TestALibraryTempDirIsChargedToEveryImporter(t *testing.T) {
	principles.Cite(t, principles.P15)
	old := "\t\ttag, err := standin.RegisteredTempTag()\n" +
		"\t\tif err != nil {\n" +
		"\t\t\ttemplate.err = err\n" +
		"\t\t\treturn\n" +
		"\t\t}\n" +
		"\t\tdir, err := standin.TempDir(tag, \"dbtemplate\")"
	uses := loadReplacing(t, "internal/store/storetest/storetest.go", old,
		"\t\tdir, err := standin.TempDir(standin.TempTagStoretest, \"dbtemplate\")")
	if err := checkTempTagUses(uses); err == nil {
		t.Fatal("storetest's own letter, stamped from cli and service, passed")
	}
}

func loadReplacing(t *testing.T, rel, old, replacement string) []tempTagUse {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(src, []byte(old)) {
		t.Fatalf("overlay anchor missing in %s", rel)
	}
	uses, err := loadTempTagUses(map[string][]byte{path: bytes.Replace(src, []byte(old), []byte(replacement), 1)})
	if err != nil {
		t.Fatal(err)
	}
	return uses
}

func loadTempTagUses(overlay map[string][]byte) ([]tempTagUse, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		Dir:        root,
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedImports | packages.NeedForTest,
		Tests:      true,
		BuildFlags: []string{"-mod=readonly"},
		Overlay:    overlay,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, err
	}
	var msgs []string
	for _, pkg := range pkgs {
		for _, pkgErr := range pkg.Errors {
			msgs = append(msgs, pkgErr.Error())
		}
	}
	if len(msgs) > 0 {
		return nil, errors.New(strings.Join(msgs, "\n"))
	}
	dirOf := map[string]string{}
	for _, pkg := range pkgs {
		if pkg.ForTest == "" && pkg.PkgPath != "" {
			dirOf[pkg.PkgPath] = relDir(root, pkg.Dir)
		}
	}
	importers := map[string]map[string]struct{}{}
	for _, pkg := range pkgs {
		if pkg.ForTest == "" || (pkg.PkgPath != pkg.ForTest && pkg.PkgPath != pkg.ForTest+"_test") {
			continue
		}
		binary := dirOf[pkg.ForTest]
		if binary == "" {
			binary = relDir(root, pkg.Dir)
		}
		for path := range pkg.Imports {
			set := importers[path]
			if set == nil {
				set = map[string]struct{}{}
				importers[path] = set
			}
			set[binary] = struct{}{}
		}
	}
	seen := map[string]struct{}{}
	var uses []tempTagUse
	var errs []error
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			if len(pkg.Syntax) == 0 {
				continue
			}
			errs = append(errs, fmt.Errorf("%s: no type info", pkg.ID))
			continue
		}
		importPath := pkg.PkgPath
		if pkg.ForTest != "" && (pkg.PkgPath == pkg.ForTest || pkg.PkgPath == pkg.ForTest+"_test") {
			importPath = pkg.ForTest
		}
		for _, file := range pkg.Syntax {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 1 {
					return true
				}
				fn, ok := calledTempFunc(pkg.TypesInfo, call.Fun)
				if !ok {
					return true
				}
				pos := pkg.Fset.Position(call.Lparen)
				key := fmt.Sprintf("%s:%d:%d:%s", pos.Filename, pos.Line, pos.Column, fn)
				if _, dup := seen[key]; dup {
					return true
				}
				seen[key] = struct{}{}
				res := resolveTag(pkg.TypesInfo, pkg.Syntax, call.Args[0])
				home := relDir(root, filepath.Dir(pos.Filename))
				binaries := map[string]struct{}{home: {}}
				if !strings.HasSuffix(pos.Filename, "_test.go") {
					for binary := range importers[importPath] {
						binaries[binary] = struct{}{}
					}
				}
				for _, binary := range slices.Sorted(maps.Keys(binaries)) {
					uses = append(uses, tempTagUse{
						binary:     binary,
						fn:         fn,
						tags:       res.letters,
						registered: res.registered,
						unknown:    res.unknown,
						pos:        pos.String(),
					})
				}
				return true
			})
		}
	}
	return uses, errors.Join(errs...)
}

func relDir(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(dir)
	}
	return filepath.ToSlash(rel)
}

func calledTempFunc(info *types.Info, fun ast.Expr) (string, bool) {
	name, ok := standinFunc(info, fun)
	if !ok {
		return "", false
	}
	switch name {
	case "TempDir", "SweepTempDirs", "RegisterTempTag":
		return name, true
	default:
		return "", false
	}
}

func standinFunc(info *types.Info, fun ast.Expr) (string, bool) {
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
		return "", false
	}
	fn, ok := info.Uses[id].(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "github.com/dayamjz/assistant/internal/agents/standin" {
		return "", false
	}
	return fn.Name(), true
}

// unseenLetter is an argument the walk cannot follow. It is not in any file.
var unseenLetter = ast.NewIdent("unseen")

type tagRes struct {
	letters    []string
	registered bool
	unknown    bool
}

func resolveTag(info *types.Info, files []*ast.File, arg ast.Expr) tagRes {
	return resolveTagRec(info, files, arg, map[types.Object]bool{})
}

func resolveTagRec(info *types.Info, files []*ast.File, arg ast.Expr, seen map[types.Object]bool) tagRes {
	for {
		paren, ok := arg.(*ast.ParenExpr)
		if !ok {
			break
		}
		arg = paren.X
	}
	if call, ok := arg.(*ast.CallExpr); ok {
		if name, ok := standinFunc(info, call.Fun); ok && name == "RegisteredTempTag" {
			return tagRes{registered: true}
		}
	}
	if tv, ok := info.Types[arg]; ok && tv.Value != nil {
		return letterFromConstant(tv.Value)
	}
	id, ok := arg.(*ast.Ident)
	if !ok {
		return tagRes{unknown: true}
	}
	obj := info.ObjectOf(id)
	if obj == nil {
		return tagRes{unknown: true}
	}
	if c, ok := obj.(*types.Const); ok {
		return letterFromConstant(c.Val())
	}
	v, ok := obj.(*types.Var)
	if !ok || seen[v] {
		return tagRes{unknown: true}
	}
	seen[v] = true
	sources := valueExprs(info, files, v)
	if len(sources) == 0 {
		return tagRes{unknown: true}
	}
	var out tagRes
	for _, src := range sources {
		out = mergeTag(out, resolveTagRec(info, files, src, seen))
	}
	return out
}

func valueExprs(info *types.Info, files []*ast.File, obj types.Object) []ast.Expr {
	var out []ast.Expr
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.ValueSpec:
				for i, name := range n.Names {
					if info.ObjectOf(name) == obj && i < len(n.Values) {
						out = append(out, n.Values[i])
					}
				}
			case *ast.AssignStmt:
				if len(n.Rhs) == len(n.Lhs) {
					for i, lhs := range n.Lhs {
						id, ok := lhs.(*ast.Ident)
						if ok && info.ObjectOf(id) == obj {
							out = append(out, n.Rhs[i])
						}
					}
					break
				}
				if len(n.Rhs) != 1 {
					break
				}
				for i, lhs := range n.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || info.ObjectOf(id) != obj {
						continue
					}
					if i == 0 {
						out = append(out, n.Rhs[0])
					} else {
						out = append(out, unseenLetter)
					}
				}
			case *ast.RangeStmt:
				id, ok := n.Value.(*ast.Ident)
				if !ok || info.ObjectOf(id) != obj {
					break
				}
				lit, ok := n.X.(*ast.CompositeLit)
				if !ok {
					out = append(out, unseenLetter)
					break
				}
				if len(lit.Elts) == 0 {
					out = append(out, unseenLetter)
					break
				}
				for _, elt := range lit.Elts {
					if kv, ok := elt.(*ast.KeyValueExpr); ok {
						elt = kv.Value
					}
					out = append(out, elt)
				}
			}
			return true
		})
	}
	return out
}

func letterFromConstant(v constant.Value) tagRes {
	if v == nil || v.Kind() != constant.String {
		return tagRes{unknown: true}
	}
	if validTempTag(constant.StringVal(v)) != nil {
		return tagRes{}
	}
	return tagRes{letters: []string{constant.StringVal(v)}}
}

func mergeTag(a, b tagRes) tagRes {
	if b.unknown {
		a.unknown = true
	}
	if b.registered {
		a.registered = true
	}
	a.letters = append(a.letters, b.letters...)
	return a
}

func moduleRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("locating the module root")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found from the temp tag test")
		}
		dir = parent
	}
}
