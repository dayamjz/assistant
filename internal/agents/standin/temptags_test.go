package standin

import (
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

// tempTagUse is one call to TempDir, SweepTempDirs, or RegisterTempTag.
// binary is the directory of the file, which is the test binary go test
// builds. tag is set when the argument is a string constant.
type tempTagUse struct {
	binary   string
	fn       string
	tag      string
	constant bool
	pos      string
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
		uses = append(uses, tempTagUse{binary: "planted/missing", fn: "TempDir", tag: "q", constant: true, pos: "planted"})
		if err := checkTempTagUses(uses); err == nil {
			t.Fatal("a TempDir with no sweep passed")
		}
	})

	t.Run("accepted", func(t *testing.T) {
		uses := constantTag("internal/cli", "c")
		uses = append(uses, constantTag("internal/service", "s")...)
		uses = append(uses, tempTagUse{binary: "internal/agents/standin", fn: "TempDir", pos: "variable"})
		uses = append(uses, constantTag("internal/agents/standin", "a")...)
		if err := checkTempTagUses(uses); err != nil {
			t.Fatal(err)
		}
	})

	uses, err := loadTempTagUses()
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
		binary: "planted/missing", fn: "TempDir", tag: "q", constant: true, pos: "planted",
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
		{binary: binary, fn: "TempDir", tag: tag, constant: true, pos: binary},
		{binary: binary, fn: "SweepTempDirs", tag: tag, constant: true, pos: binary},
	}
}

func constantTagOf(uses []tempTagUse, binary string) (string, bool) {
	for _, u := range uses {
		if u.binary == binary && u.constant && (u.fn == "TempDir" || u.fn == "SweepTempDirs") {
			return u.tag, true
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
// SweepTempDirs to have one constant tag, a sweep of that tag, and a letter
// no other binary uses. A call whose argument is not a constant, the tag
// RegisterTempTag recorded for a helper, adds no letter of its own.
func checkTempTagUses(uses []tempTagUse) error {
	type bin struct {
		tags  map[string]string
		sweep string
		swept bool
	}
	bins := map[string]*bin{}
	callers := 0
	for _, u := range uses {
		if u.fn != "TempDir" && u.fn != "SweepTempDirs" {
			continue
		}
		callers++
		b := bins[u.binary]
		if b == nil {
			b = &bin{tags: map[string]string{}}
			bins[u.binary] = b
		}
		if !u.constant {
			continue
		}
		if err := validTempTag(u.tag); err != nil {
			return fmt.Errorf("%s: %s: %w", u.pos, u.fn, err)
		}
		b.tags[u.tag] = u.pos
		if u.fn == "SweepTempDirs" {
			b.swept = true
			b.sweep = u.tag
		}
	}
	if callers == 0 {
		return errors.New("temp tag coverage is empty: no call to standin.TempDir or SweepTempDirs")
	}
	for _, u := range uses {
		if u.fn != "RegisterTempTag" {
			continue
		}
		b := bins[u.binary]
		if b == nil || !u.constant {
			continue
		}
		if err := validTempTag(u.tag); err != nil {
			return fmt.Errorf("%s: RegisterTempTag: %w", u.pos, err)
		}
		b.tags[u.tag] = u.pos
	}
	seen := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(bins)) {
		b := bins[name]
		if len(b.tags) != 1 {
			return fmt.Errorf("%s: want exactly one temp tag, found %d (%s)", name, len(b.tags), formatTags(b.tags))
		}
		if !b.swept {
			return fmt.Errorf("%s: calls TempDir but SweepTempDirs is missing", name)
		}
		var tag string
		for found := range b.tags {
			tag = found
		}
		if tag != b.sweep {
			return fmt.Errorf("%s: SweepTempDirs tag %q does not match %q", name, b.sweep, tag)
		}
		if prev, ok := seen[tag]; ok {
			return fmt.Errorf("tag %q is used by %s and %s", tag, prev, name)
		}
		seen[tag] = name
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

func loadTempTagUses() ([]tempTagUse, error) {
	root, err := moduleRoot()
	if err != nil {
		return nil, err
	}
	cfg := &packages.Config{
		Dir:        root,
		Mode:       packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Tests:      true,
		BuildFlags: []string{"-mod=readonly"},
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
				tag, isConst, tagErr := tagValue(pkg.TypesInfo, call.Args[0])
				if tagErr != nil {
					errs = append(errs, fmt.Errorf("%s: %s: %w", pos, fn, tagErr))
					return true
				}
				dir := filepath.Dir(pos.Filename)
				rel, relErr := filepath.Rel(root, dir)
				if relErr != nil || strings.HasPrefix(rel, "..") {
					rel = dir
				}
				uses = append(uses, tempTagUse{
					binary:   filepath.ToSlash(rel),
					fn:       fn,
					tag:      tag,
					constant: isConst,
					pos:      pos.String(),
				})
				return true
			})
		}
	}
	return uses, errors.Join(errs...)
}

func calledTempFunc(info *types.Info, fun ast.Expr) (string, bool) {
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
	switch fn.Name() {
	case "TempDir", "SweepTempDirs", "RegisterTempTag":
		return fn.Name(), true
	default:
		return "", false
	}
}

func tagValue(info *types.Info, arg ast.Expr) (string, bool, error) {
	for {
		paren, ok := arg.(*ast.ParenExpr)
		if !ok {
			break
		}
		arg = paren.X
	}
	tv, ok := info.Types[arg]
	if !ok {
		return "", false, errors.New("no type for the tag argument")
	}
	if tv.Value == nil {
		return "", false, nil
	}
	if tv.Value.Kind() != constant.String {
		return "", false, fmt.Errorf("tag argument is %s, want a string", tv.Value.Kind())
	}
	return constant.StringVal(tv.Value), true, nil
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
