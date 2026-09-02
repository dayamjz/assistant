package config

import (
	"errors"
	"path"
	"strings"
)

// Pattern is one compiled path pattern. It is the single matcher this product
// uses, shared by ignore patterns and by path-scoped review rules, so that a
// path means the same thing to both. PRD section 10 requires one documented
// matcher and requires that a wildcard never cross a path separator; the rules
// below are that documentation.
//
// A pattern is matched against a repository-relative slash-separated path.
//
//   - A pattern containing no separator is a basename pattern: it matches the
//     last segment of a path at any depth. "*.md" matches "docs/api/a.md" and
//     "a.md"; "vendor" matches "third_party/vendor" but not the files under
//     it, because those paths have a different last segment.
//   - A pattern ending in "/**" matches the directory it names and every path
//     beneath it. "docs/**" matches "docs", "docs/a.md", and "docs/a/b.md".
//   - Any other pattern is anchored at the repository root and matches a whole
//     path segment for segment. "docs/*.md" matches "docs/a.md" and does not
//     match "docs/api/a.md".
//   - A segment that is exactly "**" matches zero or more whole segments, so
//     "**/vendor/**" reaches a vendor directory at any depth. "**" is only
//     meaningful as a complete segment of a pattern that has separators:
//     "a**b" is refused at parse time, and so is a bare "**", which would
//     otherwise be a basename pattern. Write "**/*" for every path.
//   - Within a segment, "*", "?", and a "[...]" class have their path.Match
//     meanings and none of them ever matches a separator, because matching is
//     performed one segment at a time and a separator is never inside a
//     segment.
//
// Matching is case-sensitive on every platform, so the same configuration
// selects the same files everywhere. A backslash in a path being matched is
// treated as a separator, which is what makes the no-crossing rule hold for a
// Windows-style path; a backslash in a pattern is refused at parse time rather
// than given an escaping meaning.
//
// The zero Pattern matches nothing. Build one with ParsePattern.
type Pattern struct {
	raw  string
	base string   // basename glob; empty unless this is a basename pattern
	segs []string // anchored segments; nil for a basename pattern
	tree bool     // pattern ended in "/**": the directory and its descendants
}

// ErrBadPattern is returned, wrapped, when a pattern is not well formed. Use
// errors.Is to recognize it; the wrapping error says which pattern and why.
var ErrBadPattern = errors.New("config: malformed path pattern")

// ParsePattern compiles s. It refuses anything it cannot match unambiguously
// rather than silently reinterpreting it, so a typo in configuration surfaces
// at load instead of quietly matching nothing. Every returned error wraps
// ErrBadPattern.
//
// A pattern is bounded at MaxPatternRunes characters and MaxPatternSegments
// segments. Matching is bounded by the product of those two whatever the
// pattern contains, so the ignore list, which a pushed branch may set, cannot
// be turned into an expensive one.
func ParsePattern(s string) (Pattern, error) {
	if s == "" {
		return Pattern{}, patternErr(s, "a pattern may not be empty")
	}
	if n := len([]rune(s)); n > MaxPatternRunes {
		return Pattern{}, patternErr(s, "the pattern is "+itoa(n)+" characters and the limit is "+itoa(MaxPatternRunes))
	}
	if n := strings.Count(s, "/") + 1; n > MaxPatternSegments {
		return Pattern{}, patternErr(s, "the pattern has "+itoa(n)+" segments and the limit is "+itoa(MaxPatternSegments))
	}
	if strings.ContainsRune(s, '\\') {
		return Pattern{}, patternErr(s, `a backslash has no meaning in a pattern; separate segments with "/"`)
	}
	if strings.HasPrefix(s, "/") {
		return Pattern{}, patternErr(s, `patterns are relative to the repository root; remove the leading "/"`)
	}
	body, tree := strings.CutSuffix(s, "/**")
	if tree && body == "" {
		return Pattern{}, patternErr(s, `"/**" needs a directory before it`)
	}
	if strings.HasSuffix(body, "/") {
		return Pattern{}, patternErr(s, `a pattern may not end in "/"`)
	}
	if !tree && !strings.Contains(body, "/") {
		if strings.Contains(body, "**") {
			return Pattern{}, patternErr(s, `"**" is only meaningful as a whole path segment`)
		}
		if err := checkGlob(body); err != nil {
			return Pattern{}, patternErr(s, err.Error())
		}
		return Pattern{raw: s, base: body}, nil
	}
	segs := strings.Split(body, "/")
	for _, seg := range segs {
		switch {
		case seg == "":
			return Pattern{}, patternErr(s, "a pattern may not contain an empty segment")
		case seg == "." || seg == "..":
			return Pattern{}, patternErr(s, `"." and ".." are not allowed in a pattern`)
		case seg == "**":
			continue
		case strings.Contains(seg, "**"):
			return Pattern{}, patternErr(s, `"**" is only meaningful as a whole path segment`)
		}
		if err := checkGlob(seg); err != nil {
			return Pattern{}, patternErr(s, err.Error())
		}
	}
	return Pattern{raw: s, segs: segs, tree: tree}, nil
}

// checkGlob reports whether one segment is a well-formed glob.
func checkGlob(seg string) error {
	if _, err := path.Match(seg, "a"); err != nil {
		return errors.New("malformed wildcard: " + err.Error())
	}
	return nil
}

func patternErr(pattern, detail string) error {
	return &patternError{pattern: pattern, detail: detail}
}

type patternError struct {
	pattern string
	detail  string
}

func (e *patternError) Error() string {
	return "config: pattern " + quote(e.pattern) + ": " + e.detail
}

func (e *patternError) Unwrap() error { return ErrBadPattern }

// String returns the pattern exactly as it was written in configuration, so an
// error or a report can name what the author typed.
func (p Pattern) String() string { return p.raw }

// Match reports whether the pattern selects the given repository-relative
// path, under the rules documented on Pattern. A path is normalized first:
// backslashes become separators, a leading "/" or "./" is dropped, and empty
// and "." segments are dropped. An empty path matches nothing, and so does the
// zero Pattern.
func (p Pattern) Match(filePath string) bool {
	return p.matchSegs(splitPath(filePath))
}

// matchSegs matches an already normalized path, so a set can normalize once
// and test every pattern against the same segments.
func (p Pattern) matchSegs(segs []string) bool {
	if len(segs) == 0 {
		return false
	}
	if p.base != "" {
		return matchSegment(p.base, segs[len(segs)-1])
	}
	if p.segs == nil {
		return false
	}
	return matchSegments(p.segs, segs, p.tree)
}

// splitPath normalizes a path into its meaningful segments.
func splitPath(filePath string) []string {
	filePath = strings.ReplaceAll(filePath, `\`, "/")
	parts := strings.Split(filePath, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

// matchSegments matches pattern segments against path segments. A "**" segment
// consumes zero or more path segments. When extra is true the path may have
// segments left over after the pattern is exhausted, which is what makes a
// trailing "/**" cover a directory's descendants as well as the directory.
//
// Only the most recent "**" is ever reconsidered, and each reconsideration
// starts one path segment further along, so the work is bounded by
// len(pat)*len(name) however many "**" segments a pattern contains. A pattern
// arriving from a pushed branch therefore cannot make matching take
// exponential time. Greedy backtracking is exact here rather than an
// approximation, because every pattern segment other than "**" consumes
// exactly one path segment.
func matchSegments(pat, name []string, extra bool) bool {
	starPat, starName := -1, 0
	pi, ni := 0, 0
	for ni < len(name) {
		switch {
		case pi < len(pat) && pat[pi] == "**":
			starPat, starName = pi, ni
			pi++
		case pi < len(pat) && matchSegment(pat[pi], name[ni]):
			pi++
			ni++
		case extra && pi == len(pat):
			return true
		case starPat >= 0:
			starName++
			ni = starName
			pi = starPat + 1
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == "**" {
		pi++
	}
	return pi == len(pat)
}

// matchSegment matches one segment. The pattern was validated by ParsePattern,
// so path.Match cannot report a syntax error here; a match failure and a
// malformed pattern are both reported as no match, and the second cannot occur.
func matchSegment(pat, seg string) bool {
	ok, err := path.Match(pat, seg)
	return ok && err == nil
}

// PatternSet is an ordered list of patterns, matched as a whole.
type PatternSet []Pattern

// Match returns the first pattern in declaration order that selects the path,
// so a caller can report which pattern was responsible, and reports whether
// any did.
func (s PatternSet) Match(filePath string) (Pattern, bool) {
	segs := splitPath(filePath)
	for _, p := range s {
		if p.matchSegs(segs) {
			return p, true
		}
	}
	return Pattern{}, false
}

// Matches reports whether any pattern in the set selects the path.
func (s PatternSet) Matches(filePath string) bool {
	_, ok := s.Match(filePath)
	return ok
}

// Strings returns the patterns as they were written, in declaration order.
func (s PatternSet) Strings() []string {
	out := make([]string, len(s))
	for i, p := range s {
		out[i] = p.raw
	}
	return out
}
