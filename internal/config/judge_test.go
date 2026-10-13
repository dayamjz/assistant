package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// The default is PRD section 10's schema row, pattern for pattern and in that
// order, so a default that drifted from the row fails here rather than in a
// run that quietly judged less than the specification says.
func TestJudgePathsDefaultIsTheSchemaRow(t *testing.T) {
	want := []string{
		"*_test.go", "*_test.py", "test_*.py", "*.test.*", "*.spec.*",
		"**/test/**", "**/tests/**", "**/__tests__/**", "**/testdata/**",
		".github/workflows/**", ".golangci.yml", ".golangci.yaml",
		".eslintrc*", "eslint.config.*", "ruff.toml", "Makefile",
	}
	got := Defaults().JudgePaths.Strings()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("default judge paths = %q, want %q", got, want)
	}
	if trust, _ := TrustOf(KeyJudgePaths); trust != TrustTrusted {
		t.Errorf("judge_paths is %v, want trusted-only", trust)
	}
}

// The default list selects what its conventions name and leaves ordinary
// source alone, through the one matcher, so a pattern that compiled to
// something other than what its text says fails here.
func TestJudgePathsDefaultMatchesTheConventions(t *testing.T) {
	judge := Defaults().JudgePaths
	for _, path := range []string{
		"internal/config/key_test.go", "pkg/test_widget.py", "pkg/widget_test.py",
		"web/app.test.ts", "web/app.spec.js", "test/helper.go", "a/b/tests/x.py",
		"ui/__tests__/x.js", "internal/x/testdata/in.json", ".github/workflows/ci.yml",
		".golangci.yml", ".golangci.yaml", ".eslintrc.json", "eslint.config.mjs",
		"ruff.toml", "Makefile", "sub/Makefile",
	} {
		if !judge.Matches(path) {
			t.Errorf("the default judge paths do not select %q", path)
		}
	}
	for _, path := range []string{
		"internal/config/key.go", "README.md", "testing.go", "contest/a.go",
		".github/CODEOWNERS", "makefile.go", "docs/tests.md",
	} {
		if p, ok := judge.Match(path); ok {
			t.Errorf("the default judge paths select %q through %q", path, p)
		}
	}
}

// A repository's list replaces the default rather than extending it, and an
// empty list is a value: it turns the rule off rather than falling back to the
// default the way an absent key does.
func TestJudgePathsAreReplacedAndAnEmptyListTurnsTheRuleOff(t *testing.T) {
	replaced := resolveRun(t, Absent(OriginGlobal),
		mustParse(t, OriginTrusted, `{"judge_paths": ["spec/**"]}`), Absent(OriginPushed)).Config
	if got := strings.Join(replaced.JudgePaths.Strings(), ","); got != "spec/**" {
		t.Errorf("JudgePaths = %q, want the trusted list alone", got)
	}
	if replaced.JudgePaths.Matches("a_test.go") {
		t.Error("a repository list extended the default instead of replacing it")
	}

	off := resolveRun(t, Absent(OriginGlobal),
		mustParse(t, OriginTrusted, `{"judge_paths": []}`), Absent(OriginPushed)).Config
	if len(off.JudgePaths) != 0 {
		t.Errorf("JudgePaths = %v, want an empty list to turn the rule off", off.JudgePaths.Strings())
	}

	absent := resolveRun(t, Absent(OriginGlobal),
		mustParse(t, OriginTrusted, `{}`), Absent(OriginPushed)).Config
	if len(absent.JudgePaths) != len(defaultJudgePatterns) {
		t.Errorf("an absent key resolved %d judge paths, want the %d defaults",
			len(absent.JudgePaths), len(defaultJudgePatterns))
	}
}

// A pushed branch's own judge_paths has no effect: not to shrink the list,
// not to empty it, and not to replace it. It is dropped and reported, so the
// author is told the setting did nothing. This is P7 for the one key that
// decides which edits a person sees: a branch able to set it could exempt its
// own edits to its own checks.
func TestAPushedBranchCannotSetJudgePaths(t *testing.T) {
	principles.Cite(t, principles.P7)
	for _, doc := range []string{
		`{"judge_paths": []}`,
		`{"judge_paths": ["nothing-matches-this/**"]}`,
	} {
		pushed := mustParse(t, OriginPushed, doc)

		withDefault := resolveRun(t, Absent(OriginGlobal), Absent(OriginTrusted), pushed)
		if !withDefault.Config.JudgePaths.Matches("a_test.go") ||
			len(withDefault.Config.JudgePaths) != len(defaultJudgePatterns) {
			t.Errorf("%s: JudgePaths = %v, want the default untouched", doc, withDefault.Config.JudgePaths.Strings())
		}
		reported := false
		for _, r := range withDefault.Rejected {
			if r.Key == KeyJudgePaths && r.Origin == OriginPushed {
				reported = true
			}
		}
		if !reported {
			t.Errorf("%s: the pushed judge_paths was dropped without being reported: %v", doc, withDefault.Rejected)
		}

		trusted := mustParse(t, OriginTrusted, `{"judge_paths": ["spec/**"]}`)
		withTrusted := resolveRun(t, Absent(OriginGlobal), trusted, pushed)
		if got := strings.Join(withTrusted.Config.JudgePaths.Strings(), ","); got != "spec/**" {
			t.Errorf("%s: JudgePaths = %q, want the trusted copy's list", doc, got)
		}
	}
}

// A malformed judge pattern is refused when the document is parsed, from
// either repository origin, naming the key and the element. A pushed branch
// cannot set the key, and its value is still validated, per PRD section 10.
func TestAMalformedJudgePathIsRefusedAtParse(t *testing.T) {
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		for _, doc := range []string{
			`{"judge_paths": ["a**b"]}`,
			`{"judge_paths": [""]}`,
			`{"judge_paths": [3]}`,
			`{"judge_paths": "*_test.go"}`,
		} {
			_, err := Parse(origin, []byte(doc))
			var ke *KeyError
			if !errors.As(err, &ke) || ke.Key != KeyJudgePaths {
				t.Errorf("%v %s: err = %v, want a refusal naming judge_paths", origin, doc, err)
			}
		}
	}
}
