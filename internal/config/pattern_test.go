package config

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func mustPattern(t *testing.T, s string) Pattern {
	t.Helper()
	p, err := ParsePattern(s)
	if err != nil {
		t.Fatalf("ParsePattern(%q): %v", s, err)
	}
	return p
}

// A wildcard must never cross a separator, whichever character wrote it.
func TestPatternWildcardDoesNotCrossSeparator(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"docs/*.md", "docs/a.md", true},
		{"docs/*.md", "docs/api/a.md", false},
		{"docs/*", "docs/a", true},
		{"docs/*", "docs/a/b", false},
		{"docs/*", "docs/a/b/c", false},
		{"a/*/c", "a/b/c", true},
		{"a/*/c", "a/b/x/c", false},
		{"a/?/c", "a/b/c", true},
		{"a/?/c", "a/bb/c", false},
		// A Windows-style path must not smuggle a segment past a wildcard.
		{"docs/*", `docs\a\b`, false},
		{"docs/*", `docs\a`, true},
	}
	for _, c := range cases {
		if got := mustPattern(t, c.pattern).Match(c.path); got != c.want {
			t.Errorf("%q.Match(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A pattern with no separator matches a basename at any depth, and nothing
// else about the path.
func TestPatternBasenameAtAnyDepth(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"*.md", "a.md", true},
		{"*.md", "docs/a.md", true},
		{"*.md", "docs/api/v2/a.md", true},
		{"*.md", "docs/a.md/b.go", false},
		{"*.md", "docs/a.markdown", false},
		// A bare "*" is a basename pattern, so it matches any single segment
		// at any depth. That is the basename rule, not a wildcard crossing a
		// separator: the segments before the last one are not consulted.
		{"*", "a", true},
		{"*", "a/b", true},
		{"vendor", "vendor", true},
		{"vendor", "third_party/nested/vendor", true},
		// The basename rule is about the last segment only: a file inside a
		// matched directory has a different last segment.
		{"vendor", "third_party/vendor/a.go", false},
		{"go.mod", "internal/config/go.mod", true},
	}
	for _, c := range cases {
		if got := mustPattern(t, c.pattern).Match(c.path); got != c.want {
			t.Errorf("%q.Match(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

// A trailing /** matches the directory itself and every descendant, at every
// depth, and stops at a sibling whose name merely starts the same way.
func TestPatternDirectoryTree(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"docs/**", "docs", true},
		{"docs/**", "docs/a.md", true},
		{"docs/**", "docs/api/v2/a.md", true},
		{"docs/**", "docsx/a.md", false},
		{"docs/**", "a/docs/b.md", false},
		{"internal/config/**", "internal/config/pattern.go", true},
		{"internal/config/**", "internal/graph/pattern.go", false},
		// "**" as a whole segment reaches any depth, which is how a nested
		// directory tree is selected.
		{"**/vendor/**", "third_party/nested/vendor/a.go", true},
		{"**/vendor/**", "vendor/a.go", true},
		{"**/vendor/**", "vendorish/a.go", false},
		{"a/**/c", "a/c", true},
		{"a/**/c", "a/b/c", true},
		{"a/**/c", "a/b/x/c", true},
		{"a/**/c", "a/b/c/d", false},
	}
	for _, c := range cases {
		if got := mustPattern(t, c.pattern).Match(c.path); got != c.want {
			t.Errorf("%q.Match(%q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestPatternMatchIsCaseSensitive(t *testing.T) {
	if mustPattern(t, "docs/**").Match("Docs/a.md") {
		t.Error("matching must be case-sensitive on every platform")
	}
}

func TestPatternNormalizesPathBeforeMatching(t *testing.T) {
	p := mustPattern(t, "docs/a.md")
	for _, path := range []string{"docs/a.md", "./docs/a.md", "/docs/a.md", "docs//a.md", `docs\a.md`} {
		if !p.Match(path) {
			t.Errorf("Match(%q) = false, want true", path)
		}
	}
}

func TestPatternEmptyPathAndZeroPatternMatchNothing(t *testing.T) {
	if mustPattern(t, "*").Match("") {
		t.Error("an empty path must not match")
	}
	if (Pattern{}).Match("a.md") {
		t.Error("the zero Pattern must match nothing")
	}
}

func TestParsePatternRefusals(t *testing.T) {
	cases := []struct {
		pattern string
		reason  string
	}{
		{"", "empty"},
		{`docs\a.md`, "backslash"},
		{"/docs/a.md", "leading separator"},
		{"/**", "no directory before /**"},
		{"docs/", "trailing separator"},
		{"docs//a.md", "empty segment"},
		{"./a.md", "dot segment"},
		{"docs/../a.md", "dot dot segment"},
		{"a**b", "partial double star in a path pattern"},
		{"**b/c", "partial double star in a segment"},
		{"a**", "partial double star in a basename pattern"},
		{"**", "a bare double star, which is not a basename pattern"},
		{"[a-", "malformed class"},
		{"docs/[a-", "malformed class in a segment"},
	}
	for _, c := range cases {
		_, err := ParsePattern(c.pattern)
		if err == nil {
			t.Errorf("ParsePattern(%q) accepted a pattern with %s", c.pattern, c.reason)
			continue
		}
		if !errors.Is(err, ErrBadPattern) {
			t.Errorf("ParsePattern(%q) error %v does not wrap ErrBadPattern", c.pattern, err)
		}
		if !strings.Contains(err.Error(), strconv.Quote(c.pattern)) && c.pattern != "" {
			t.Errorf("ParsePattern(%q) error %q does not name the pattern", c.pattern, err)
		}
	}
}

func TestParsePatternAccepts(t *testing.T) {
	for _, s := range []string{"*.md", "docs/**", "a/*/c", "**/vendor/**", "[a-z]*.go", "a?c", "**/*"} {
		if _, err := ParsePattern(s); err != nil {
			t.Errorf("ParsePattern(%q) refused a valid pattern: %v", s, err)
		}
	}
}

// "**" is a path-pattern segment, so a bare "**" is refused as a basename
// pattern and "**/*", which selects every path at every depth, is the way to
// say it.
func TestParsePatternDoubleStarNeedsAPathPattern(t *testing.T) {
	if _, err := ParsePattern("**"); err == nil {
		t.Fatal(`ParsePattern("**") was accepted as a basename pattern`)
	}
	p := mustPattern(t, "**/*")
	for _, path := range []string{"a", "a/b", "a/b/c.go"} {
		if !p.Match(path) {
			t.Errorf(`"**/*".Match(%q) = false, want true`, path)
		}
	}
}

func TestPatternStringRoundTrips(t *testing.T) {
	for _, s := range []string{"*.md", "docs/**", "**/vendor/**"} {
		if got := mustPattern(t, s).String(); got != s {
			t.Errorf("String() = %q, want %q", got, s)
		}
	}
}

func TestPatternSetReportsWhichPatternMatched(t *testing.T) {
	set := PatternSet{mustPattern(t, "docs/**"), mustPattern(t, "*.md")}
	p, ok := set.Match("docs/a.md")
	if !ok || p.String() != "docs/**" {
		t.Fatalf("Match returned (%q, %v), want the first matching pattern", p, ok)
	}
	if p, ok := set.Match("README.md"); !ok || p.String() != "*.md" {
		t.Fatalf("Match returned (%q, %v), want *.md", p, ok)
	}
	if _, ok := set.Match("internal/config/pattern.go"); ok {
		t.Error("Match reported a match for an unmatched path")
	}
	if set.Matches("internal/config/pattern.go") {
		t.Error("Matches reported a match for an unmatched path")
	}
	if got := strings.Join(set.Strings(), ","); got != "docs/**,*.md" {
		t.Errorf("Strings() = %q", got)
	}
	if PatternSet(nil).Matches("a.md") {
		t.Error("an empty set must match nothing")
	}
}
