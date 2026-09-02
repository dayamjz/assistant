package config

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, origin Origin, doc string) Layer {
	t.Helper()
	l, err := Parse(origin, []byte(doc))
	if err != nil {
		t.Fatalf("Parse(%v, %s): %v", origin, doc, err)
	}
	return l
}

func TestParseRequiresAnOrigin(t *testing.T) {
	if _, err := Parse(OriginUnknown, []byte(`{}`)); !errors.Is(err, ErrUnknownOrigin) {
		t.Fatalf("Parse with no origin returned %v, want ErrUnknownOrigin", err)
	}
	if _, err := Parse(Origin(99), []byte(`{}`)); !errors.Is(err, ErrUnknownOrigin) {
		t.Fatalf("Parse with an unrecognized origin returned %v, want ErrUnknownOrigin", err)
	}
	for _, o := range []Origin{OriginTrusted, OriginPushed} {
		if _, err := Parse(o, []byte(`{}`)); err != nil {
			t.Fatalf("Parse(%v) refused a valid document: %v", o, err)
		}
	}
}

// Absent and present-but-empty resolve the same way and are still
// distinguishable, which is what lets a caller say which happened.
func TestAbsentAndEmptyAreDistinctButBothValid(t *testing.T) {
	absent := Absent(OriginTrusted)
	if absent.Present() {
		t.Error("Absent reported a present document")
	}
	for _, doc := range []string{"", "   \n\t ", "{}"} {
		l := mustParse(t, OriginTrusted, doc)
		if !l.Present() {
			t.Errorf("Parse(%q) reported no document", doc)
		}
		if len(l.SetKeys()) != 0 {
			t.Errorf("Parse(%q) set keys %v", doc, l.SetKeys())
		}
	}
	from := func(l Layer) Config {
		res, err := Resolve(l, Absent(OriginTrusted))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		return res.Config
	}
	if from(absent).RunBudget != from(mustParse(t, OriginTrusted, "{}")).RunBudget {
		t.Error("an absent and an empty document must resolve to the same configuration")
	}
}

// A key set to an empty value is set. That is what makes a repository able to
// clear a global setting rather than only add to it.
func TestParseDistinguishesEmptyValueFromAbsentKey(t *testing.T) {
	l := mustParse(t, OriginTrusted, `{"commands": {"test": ""}, "ignore_patterns": []}`)
	if !l.IsSet(KeyCommandsTest) {
		t.Error("an empty command must count as set")
	}
	if !l.IsSet(KeyIgnorePatterns) {
		t.Error("an empty list must count as set")
	}
	if l.IsSet(KeyCommandsLint) {
		t.Error("a key the document never wrote must not count as set")
	}
	if got := l.SetKeys(); len(got) != 2 {
		t.Errorf("SetKeys() = %v, want two keys", got)
	}
}

func TestParseAcceptsTheWholeSchema(t *testing.T) {
	doc := `{
		"agent": ["claude", "fallback"],
		"commands": {"test": "go test ./...", "lint": "make lint", "format": "gofmt -w ."},
		"fix_rounds": {"review": 0, "rebase": 1, "test": 2, "lint": 3, "checks": 4},
		"run_budget": 12,
		"ignore_patterns": ["docs/**", "*.md"],
		"review": {"path_rules": [{"paths": ["internal/**"], "guidance": "check the contracts"}]},
		"document": {"ownership": [{"subject": "the schema", "document": "docs/prd.html"}]},
		"suppress_project_instructions": true,
		"no_ci": true,
		"checks_timeout": "30m",
		"session_reuse": false,
		"commit": {"fix_message": "fix(gate): {summary}"},
		"allow_pushed_commands": true
	}`
	l := mustParse(t, OriginTrusted, doc)
	if got, want := len(l.SetKeys()), len(Keys()); got != want {
		t.Fatalf("SetKeys() has %d keys, want all %d schema keys: %v", got, want, l.SetKeys())
	}
	res, err := Resolve(l, Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	c := res.Config
	if strings.Join(c.Agent, ",") != "claude,fallback" {
		t.Errorf("Agent = %v", c.Agent)
	}
	if c.Commands.Test != "go test ./..." || c.Commands.Lint != "make lint" || c.Commands.Format != "gofmt -w ." {
		t.Errorf("Commands = %+v", c.Commands)
	}
	if c.FixRounds != (FixRounds{Review: 0, Rebase: 1, Test: 2, Lint: 3, Checks: 4}) {
		t.Errorf("FixRounds = %+v", c.FixRounds)
	}
	if c.RunBudget != 12 {
		t.Errorf("RunBudget = %d", c.RunBudget)
	}
	if !c.IgnorePatterns.Matches("docs/a/b.md") || !c.IgnorePatterns.Matches("README.md") {
		t.Errorf("IgnorePatterns = %v", c.IgnorePatterns.Strings())
	}
	if len(c.ReviewPathRules) != 1 || !c.ReviewPathRules[0].Matches("internal/config/key.go") {
		t.Errorf("ReviewPathRules = %+v", c.ReviewPathRules)
	}
	if got := c.ReviewPathRules[0].Scope(); got != "internal/**" {
		t.Errorf("Scope() = %q", got)
	}
	if len(c.DocumentOwnership) != 1 || c.DocumentOwnership[0].Document != "docs/prd.html" {
		t.Errorf("DocumentOwnership = %+v", c.DocumentOwnership)
	}
	if !c.SuppressProjectInstructions || !c.NoCI || c.SessionReuse || !c.AllowPushedCommands {
		t.Errorf("flags = %+v", c)
	}
	if c.ChecksTimeout.String() != "30m0s" {
		t.Errorf("ChecksTimeout = %v", c.ChecksTimeout)
	}
	if c.CommitFixMessage != "fix(gate): {summary}" {
		t.Errorf("CommitFixMessage = %q", c.CommitFixMessage)
	}
}

func TestParseRefusesMalformedDocuments(t *testing.T) {
	cases := []struct{ name, doc string }{
		{"not json", `{`},
		{"not an object", `["a"]`},
		{"a bare string", `"nope"`},
		{"a null document", `null`},
		{"trailing value", `{} {}`},
	}
	for _, c := range cases {
		_, err := Parse(OriginTrusted, []byte(c.doc))
		if err == nil {
			t.Errorf("%s: Parse accepted %s", c.name, c.doc)
			continue
		}
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: error %v does not wrap ErrMalformed", c.name, err)
		}
	}
}

// Every invalid value fails at parse time and names the offending key and the
// value, rather than falling back to a default.
func TestParseRefusalsNameTheKeyAndValue(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		key   Key
		value string
	}{
		{"unknown key", `{"nope": 1}`, "nope", ""},
		{"unknown key in a section", `{"commands": {"nope": "x"}}`, "commands.nope", ""},
		{"section given a scalar", `{"commands": "go test"}`, "commands", `"go test"`},
		{"null value", `{"no_ci": null}`, KeyNoCI, "null"},
		{"wrong type", `{"no_ci": "yes"}`, KeyNoCI, `"yes"`},
		{"negative fix rounds", `{"fix_rounds": {"review": -1}}`, KeyFixRoundsReview, "-1"},
		{"fractional fix rounds", `{"fix_rounds": {"review": 1.5}}`, KeyFixRoundsReview, "1.5"},
		{"fix rounds over the limit", `{"fix_rounds": {"test": 101}}`, KeyFixRoundsTest, "101"},
		{"zero run budget", `{"run_budget": 0}`, KeyRunBudget, "0"},
		{"run budget over the limit", `{"run_budget": 100001}`, KeyRunBudget, "100001"},
		{"bad pattern", `{"ignore_patterns": ["/docs"]}`, KeyIgnorePatterns, `"/docs"`},
		{"pattern not a string", `{"ignore_patterns": [1]}`, KeyIgnorePatterns, "[1]"},
		{"patterns not a list", `{"ignore_patterns": "docs/**"}`, KeyIgnorePatterns, `"docs/**"`},
		{"duration as a number", `{"checks_timeout": 168}`, KeyChecksTimeout, "168"},
		{"duration unparseable", `{"checks_timeout": "a while"}`, KeyChecksTimeout, `"a while"`},
		{"duration not positive", `{"checks_timeout": "0s"}`, KeyChecksTimeout, `"0s"`},
		{"duration over the limit", `{"checks_timeout": "9000h"}`, KeyChecksTimeout, `"9000h"`},
		{"template without a placeholder", `{"commit": {"fix_message": "fix: something"}}`, KeyCommitFixMessage, `"fix: something"`},
		{"template with a newline", `{"commit": {"fix_message": "fix: {summary}\nbody"}}`, KeyCommitFixMessage, "body"},
		{"empty agent list", `{"agent": []}`, KeyAgent, "[]"},
		{"blank agent", `{"agent": "  "}`, KeyAgent, `"  "`},
		{"agent not a string", `{"agent": [1]}`, KeyAgent, "[1]"},
		{"reserved agent flag", `{"agent": "claude --resume abc"}`, KeyAgent, "--resume"},
		{"reserved agent flag with a value", `{"agent": ["claude --output-format=json"]}`, KeyAgent, "--output-format"},
		{"too many agents", `{"agent": ["a","b","c","d","e","f","g","h","i"]}`, KeyAgent, `"i"`},
		{"command with a newline", `{"commands": {"test": "go test\nrm -rf /"}}`, KeyCommandsTest, "rm -rf /"},
		{"rule without paths", `{"review": {"path_rules": [{"guidance": "x"}]}}`, KeyReviewPathRules, "guidance"},
		{"rule without guidance", `{"review": {"path_rules": [{"paths": ["a/**"]}]}}`, KeyReviewPathRules, "a/**"},
		{"rule with empty paths", `{"review": {"path_rules": [{"paths": [], "guidance": "x"}]}}`, KeyReviewPathRules, "guidance"},
		{"rule with empty guidance", `{"review": {"path_rules": [{"paths": ["a/**"], "guidance": " "}]}}`, KeyReviewPathRules, "a/**"},
		{"rule with an unknown field", `{"review": {"path_rules": [{"paths": ["a/**"], "guidance": "x", "why": 1}]}}`, KeyReviewPathRules, "why"},
		{"rule not an object", `{"review": {"path_rules": ["x"]}}`, KeyReviewPathRules, `"x"`},
		{"ownership without a subject", `{"document": {"ownership": [{"document": "d.md"}]}}`, KeyDocumentOwnership, "d.md"},
		{"ownership without a document", `{"document": {"ownership": [{"subject": "s"}]}}`, KeyDocumentOwnership, `"s"`},
		{"ownership with an empty subject", `{"document": {"ownership": [{"subject": "", "document": "d.md"}]}}`, KeyDocumentOwnership, `""`},
		{"two owners for one subject", `{"document": {"ownership": [{"subject": "s", "document": "a.md"}, {"subject": "s", "document": "b.md"}]}}`, KeyDocumentOwnership, `"s"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(OriginTrusted, []byte(c.doc))
			if err == nil {
				t.Fatalf("Parse accepted %s", c.doc)
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error %v does not wrap ErrInvalid", err)
			}
			var ke *KeyError
			if !errors.As(err, &ke) {
				t.Fatalf("error %v is not a *KeyError", err)
			}
			if ke.Key != c.key {
				t.Errorf("named key %q, want %q (%v)", ke.Key, c.key, err)
			}
			if c.value != "" && !strings.Contains(err.Error(), c.value) {
				t.Errorf("message %q does not name %q", err, c.value)
			}
			if !strings.Contains(err.Error(), string(c.key)) {
				t.Errorf("message %q does not name the key", err)
			}
		})
	}
}

// Validation does not depend on the origin: a broken rule on a pushed branch
// surfaces before it merges, even though its value would be discarded.
func TestParseValidatesPushedDocumentsToo(t *testing.T) {
	_, err := Parse(OriginPushed, []byte(`{"no_ci": "yes"}`))
	if err == nil {
		t.Fatal("Parse accepted an invalid trusted-only key from a pushed origin")
	}
	var ke *KeyError
	if !errors.As(err, &ke) || ke.Key != KeyNoCI {
		t.Fatalf("error %v does not name no_ci", err)
	}
}

// A document with several problems reports the same one every time, so a fix
// makes visible progress instead of uncovering an arbitrary next complaint.
func TestParseRefusalIsDeterministic(t *testing.T) {
	doc := []byte(`{"run_budget": 0, "no_ci": "yes", "agent": [], "checks_timeout": "x"}`)
	first := ""
	for range 50 {
		_, err := Parse(OriginTrusted, doc)
		if err == nil {
			t.Fatal("Parse accepted a document with four broken keys")
		}
		if first == "" {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("Parse reported %q then %q for the same document", first, err)
		}
	}
	if !strings.Contains(first, string(KeyAgent)) {
		t.Errorf("expected the first key in sorted order, got %q", first)
	}
}

func TestLayerStringDescribesOriginAndSize(t *testing.T) {
	if got := Absent(OriginTrusted).String(); got != "trusted (no document)" {
		t.Errorf("String() = %q", got)
	}
	if got := mustParse(t, OriginPushed, `{"no_ci": true}`).String(); got != "pushed (1 keys set)" {
		t.Errorf("String() = %q", got)
	}
}

func TestLayerReportsItsOrigin(t *testing.T) {
	if got := Absent(OriginPushed).Origin(); got != OriginPushed {
		t.Errorf("Origin() = %v", got)
	}
	if got := mustParse(t, OriginTrusted, `{}`).Origin(); got != OriginTrusted {
		t.Errorf("Origin() = %v", got)
	}
}

func TestDocumentErrorMessageNamesTheProblem(t *testing.T) {
	_, err := Parse(OriginTrusted, []byte(`["a"]`))
	if err == nil {
		t.Fatal("Parse accepted a document that is not an object")
	}
	if !strings.Contains(err.Error(), "must be a JSON object") {
		t.Errorf("message %q does not say what was wrong", err)
	}
	_, err = Parse(OriginTrusted, []byte(`{`))
	if err == nil {
		t.Fatal("Parse accepted truncated JSON")
	}
	if !strings.Contains(err.Error(), "not valid JSON") || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("message %q does not carry the decoding error", err)
	}
}

// Every bounded list and every bounded piece of text refuses at its limit and
// accepts just under it, so a limit that was never enforced would fail here.
func TestParseEnforcesItsBoundsAtTheEdge(t *testing.T) {
	pattern := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = `"f` + itoa(i) + `.go"`
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	rules := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = `{"paths": ["f` + itoa(i) + `.go"], "guidance": "g"}`
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	owners := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = `{"subject": "s` + itoa(i) + `", "document": "d.md"}`
		}
		return "[" + strings.Join(out, ",") + "]"
	}
	text := func(n int) string { return `"` + strings.Repeat("x", n) + `"` }

	cases := []struct {
		name string
		key  Key
		doc  func(n int) string
		max  int
	}{
		{"ignore patterns", KeyIgnorePatterns, func(n int) string {
			return `{"ignore_patterns": ` + pattern(n) + `}`
		}, MaxIgnorePatterns},
		{"path rules", KeyReviewPathRules, func(n int) string {
			return `{"review": {"path_rules": ` + rules(n) + `}}`
		}, MaxPathRules},
		{"paths in one rule", KeyReviewPathRules, func(n int) string {
			return `{"review": {"path_rules": [{"paths": ` + pattern(n) + `, "guidance": "g"}]}}`
		}, MaxPathRulePaths},
		{"ownership entries", KeyDocumentOwnership, func(n int) string {
			return `{"document": {"ownership": ` + owners(n) + `}}`
		}, MaxOwnership},
		{"agents", KeyAgent, func(n int) string {
			return `{"agent": ` + pattern(n) + `}`
		}, MaxAgents},
		{"command length", KeyCommandsTest, func(n int) string {
			return `{"commands": {"test": ` + text(n) + `}}`
		}, MaxCommandRunes},
		{"guidance length", KeyReviewPathRules, func(n int) string {
			return `{"review": {"path_rules": [{"paths": ["a.go"], "guidance": ` + text(n) + `}]}}`
		}, MaxGuidanceRunes},
		{"subject length", KeyDocumentOwnership, func(n int) string {
			return `{"document": {"ownership": [{"subject": ` + text(n) + `, "document": "d.md"}]}}`
		}, MaxSubjectRunes},
		{"template length", KeyCommitFixMessage, func(n int) string {
			return `{"commit": {"fix_message": "` + strings.Repeat("x", n-len(FixMessagePlaceholder)) + FixMessagePlaceholder + `"}}`
		}, MaxCommitSubjectRunes},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(OriginTrusted, []byte(c.doc(c.max))); err != nil {
				t.Fatalf("Parse refused %d, which is the limit: %v", c.max, err)
			}
			err := parseError(t, c.doc(c.max+1))
			if err == nil {
				t.Fatalf("Parse accepted %d, which is over the limit of %d", c.max+1, c.max)
			}
			var ke *KeyError
			if !errors.As(err, &ke) || ke.Key != c.key {
				t.Fatalf("error %v does not name %s", err, c.key)
			}
			if !strings.Contains(err.Error(), itoa(c.max)) {
				t.Errorf("error %q does not say what the limit is", err)
			}
		})
	}
}

// parseError parses a document and returns only the error, for tests that do
// not need the layer.
func parseError(t *testing.T, doc string) error {
	t.Helper()
	_, err := Parse(OriginTrusted, []byte(doc))
	return err
}
