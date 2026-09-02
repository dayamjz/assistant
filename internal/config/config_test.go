package config

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultsAreIndependentBetweenCalls(t *testing.T) {
	a := Defaults()
	a.Agent[0] = "mutated"
	if Defaults().Agent[0] != DefaultAgent {
		t.Fatal("Defaults returns a value sharing the default agent list; a caller can change every later default")
	}
}

// One layer resolves many times, so a caller editing what it got back must not
// reach the next resolution.
func TestResolvedSlicesAreIndependentOfTheLayer(t *testing.T) {
	repo := mustParse(t, OriginTrusted, `{
		"agent": ["claude"],
		"ignore_patterns": ["docs/**"],
		"document": {"ownership": [{"subject": "s", "document": "d.md"}]},
		"review": {"path_rules": [{"paths": ["internal/**"], "guidance": "g"}]}
	}`)
	first := resolve(t, Absent(OriginTrusted), repo).Config
	first.Agent[0] = "mutated"
	first.IgnorePatterns[0] = mustPattern(t, "*.mutated")
	first.DocumentOwnership[0].Document = "mutated.md"
	first.ReviewPathRules[0].Guidance = "mutated"
	first.ReviewPathRules[0].Paths[0] = mustPattern(t, "*.mutated")

	second := resolve(t, Absent(OriginTrusted), repo).Config
	if second.Agent[0] != "claude" {
		t.Errorf("Agent = %v", second.Agent)
	}
	if second.IgnorePatterns[0].String() != "docs/**" {
		t.Errorf("IgnorePatterns = %v", second.IgnorePatterns.Strings())
	}
	if second.DocumentOwnership[0].Document != "d.md" {
		t.Errorf("DocumentOwnership = %+v", second.DocumentOwnership)
	}
	if second.ReviewPathRules[0].Guidance != "g" || second.ReviewPathRules[0].Scope() != "internal/**" {
		t.Errorf("ReviewPathRules = %+v", second.ReviewPathRules)
	}
}

func TestConfigIsIgnoredNamesThePattern(t *testing.T) {
	c := resolve(t, Absent(OriginTrusted), mustParse(t, OriginTrusted,
		`{"ignore_patterns": ["docs/**", "*.golden"]}`)).Config
	p, ok := c.IsIgnored("docs/api/a.md")
	if !ok || p.String() != "docs/**" {
		t.Errorf("IsIgnored returned (%q, %v)", p, ok)
	}
	if _, ok := c.IsIgnored("internal/config/key.go"); ok {
		t.Error("a path no pattern selects must not be ignored")
	}
	if _, ok := Defaults().IsIgnored("anything"); ok {
		t.Error("the default ignore list must ignore nothing")
	}
}

func TestRenderFixMessage(t *testing.T) {
	c := Defaults()
	got, err := c.RenderFixMessage("tighten the pattern parser")
	if err != nil {
		t.Fatalf("RenderFixMessage: %v", err)
	}
	if got != "fix: tighten the pattern parser" {
		t.Errorf("RenderFixMessage = %q", got)
	}

	custom := resolve(t, Absent(OriginTrusted), mustParse(t, OriginTrusted,
		`{"commit": {"fix_message": "fix(gate): {summary}"}}`)).Config
	if got, err := custom.RenderFixMessage("stop guessing"); err != nil || got != "fix(gate): stop guessing" {
		t.Errorf("RenderFixMessage = (%q, %v)", got, err)
	}
}

func TestRenderFixMessageRefusesASummaryThatCouldDisguiseTheCommit(t *testing.T) {
	c := Defaults()
	cases := []struct {
		name    string
		summary string
		names   string
	}{
		{"empty", "", "empty"},
		{"blank", "   ", "empty"},
		{"newline", "ok\nSigned-off-by: someone", "line break"},
		{"carriage return", "ok\rnope", "line break"},
		{"control character", "ok\x07", "control character"},
		{"tab", "ok\tnope", "control character"},
		{"bidi override", "ok\u202egnp", "formatting character"},
		{"zero width space", "ok\u200bnope", "formatting character"},
		{"too long", strings.Repeat("x", MaxCommitSubjectRunes), "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.RenderFixMessage(tc.summary)
			if err == nil {
				t.Fatalf("RenderFixMessage accepted %q and returned %q", tc.summary, got)
			}
			if got != "" {
				t.Errorf("RenderFixMessage returned %q alongside its error", got)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("error %v does not wrap ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("error %q does not say %q", err, tc.names)
			}
			if !strings.Contains(err.Error(), string(KeyCommitFixMessage)) {
				t.Errorf("error %q does not name the key", err)
			}
		})
	}
}

// The rendered subject is measured, not just the template and the summary
// separately: two acceptable halves can still be too long together.
func TestRenderFixMessageMeasuresTheRenderedSubject(t *testing.T) {
	c := Defaults()
	fits := strings.Repeat("x", MaxCommitSubjectRunes-len("fix: "))
	if _, err := c.RenderFixMessage(fits); err != nil {
		t.Fatalf("RenderFixMessage refused a subject that exactly fits: %v", err)
	}
	if _, err := c.RenderFixMessage(fits + "x"); err == nil {
		t.Fatal("RenderFixMessage accepted a subject one character over the limit")
	}
}

func TestPathRuleScopeAndMatching(t *testing.T) {
	c := resolve(t, Absent(OriginTrusted), mustParse(t, OriginTrusted, `{"review": {"path_rules": [
		{"paths": ["internal/**", "*.go"], "guidance": "state the contract"},
		{"paths": ["docs/**"], "guidance": "no new owners"}
	]}}`)).Config
	if len(c.ReviewPathRules) != 2 {
		t.Fatalf("got %d rules", len(c.ReviewPathRules))
	}
	// Rules keep their declared order, so a report can apply them in order.
	if c.ReviewPathRules[0].Guidance != "state the contract" {
		t.Errorf("rules were reordered: %+v", c.ReviewPathRules)
	}
	if got := c.ReviewPathRules[0].Scope(); got != "internal/**, *.go" {
		t.Errorf("Scope() = %q", got)
	}
	if !c.ReviewPathRules[0].Matches("internal/config/key.go") {
		t.Error("the first rule must match a file under internal")
	}
	if c.ReviewPathRules[1].Matches("internal/config/key.go") {
		t.Error("the docs rule must not match a file under internal")
	}
	if !c.ReviewPathRules[1].Matches("docs/prd.html") {
		t.Error("the docs rule must match a file under docs")
	}
}

func TestKeysAreTheWholeSchemaAndUnique(t *testing.T) {
	seen := map[Key]bool{}
	for _, k := range Keys() {
		if seen[k] {
			t.Errorf("%s appears twice in the schema", k)
		}
		seen[k] = true
	}
	for _, k := range []Key{
		KeyAgent, KeyCommandsTest, KeyCommandsLint, KeyCommandsFormat,
		KeyFixRoundsReview, KeyFixRoundsRebase, KeyFixRoundsTest, KeyFixRoundsLint, KeyFixRoundsChecks,
		KeyRunBudget, KeyIgnorePatterns, KeyReviewPathRules, KeyDocumentOwnership,
		KeySuppressProjectInstructions, KeyNoCI, KeyChecksTimeout, KeySessionReuse,
		KeyCommitFixMessage, KeyAllowPushedCommands,
	} {
		if !seen[k] {
			t.Errorf("%s is missing from Keys()", k)
		}
	}
	if len(seen) != len(Keys()) {
		t.Errorf("Keys() has %d entries and %d distinct keys", len(Keys()), len(seen))
	}
}

// Every reserved flag is refused, so the list is enforced rather than
// decorative.
func TestEveryReservedAgentFlagIsRefused(t *testing.T) {
	for _, flag := range ReservedAgentFlags {
		doc := `{"agent": "claude ` + flag + ` value"}`
		if _, err := Parse(OriginTrusted, []byte(doc)); err == nil {
			t.Errorf("Parse accepted the reserved flag %q", flag)
		}
	}
	// A flag that is not reserved is accepted, so the check is not refusing
	// every flag and passing by accident.
	if _, err := Parse(OriginTrusted, []byte(`{"agent": "claude --model opus"}`)); err != nil {
		t.Errorf("Parse refused an ordinary agent flag: %v", err)
	}
	// A longer flag that merely starts with a reserved one is not reserved.
	if _, err := Parse(OriginTrusted, []byte(`{"agent": "claude --resume-later"}`)); err != nil {
		t.Errorf("Parse refused a flag that only shares a prefix with a reserved one: %v", err)
	}
}

func TestKeyErrorRendersKeyAndValue(t *testing.T) {
	err := &KeyError{Key: KeyRunBudget, Value: "0", Detail: "must be at least one"}
	if got, want := err.Error(), "config: run_budget = 0: must be at least one"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	bare := &KeyError{Key: "nope", Detail: "unrecognized key"}
	if got, want := bare.Error(), "config: nope: unrecognized key"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDocumentErrorWrapsTheDecodingError(t *testing.T) {
	_, err := Parse(OriginTrusted, []byte(`{`))
	var de *DocumentError
	if !errors.As(err, &de) {
		t.Fatalf("error %v is not a *DocumentError", err)
	}
	if de.Err == nil {
		t.Error("the underlying decoding error was dropped")
	}
	if !errors.Is(err, ErrMalformed) {
		t.Error("a malformed document must wrap ErrMalformed")
	}
	if errors.Is(err, ErrInvalid) {
		t.Error("a malformed document is not a key-level refusal")
	}
}
