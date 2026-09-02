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
		{"pattern not a string", `{"ignore_patterns": ["a.go", 42]}`, KeyIgnorePatterns, "42"},
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
	pattern := listOf
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

// A dotted key has one spelling. The flat form is refused rather than accepted
// alongside the nested form, because a document that carries both would take
// effect one way and read the other way to whoever reviews it.
func TestParseRefusesTheFlatDottedSpelling(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		flat string
	}{
		{"on its own", `{"fix_rounds.review": 2}`, "fix_rounds.review"},
		{"shadowing the nested form", `{"fix_rounds": {"review": 1}, "fix_rounds.review": 2}`, "fix_rounds.review"},
		{"inside a section", `{"fix_rounds": {"review.extra": 1}}`, "review.extra"},
		{"unrecognized but dotted", `{"nope.nope": 1}`, "nope.nope"},
	}
	for _, c := range cases {
		_, err := Parse(OriginTrusted, []byte(c.doc))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse(%s) returned %v, want ErrInvalid", c.name, c.doc, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, c.flat) {
			t.Errorf("%s: message %q does not name the flat spelling", c.name, msg)
		}
		parts := strings.Split(c.flat, ".")
		for _, part := range parts {
			if !strings.Contains(msg, `{"`+part+`": `) {
				t.Errorf("%s: message %q does not show the nested spelling of %q", c.name, msg, part)
			}
		}
	}
	// The nested spelling of the same key is still accepted and still merges
	// key by key.
	res, err := Resolve(mustParse(t, OriginTrusted, `{"fix_rounds": {"review": 2}}`), Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Config.FixRounds.Review != 2 {
		t.Errorf("FixRounds.Review = %d, want 2", res.Config.FixRounds.Review)
	}
	if res.Config.FixRounds.Test != DefaultFixRoundsStage {
		t.Errorf("FixRounds.Test = %d, want the default", res.Config.FixRounds.Test)
	}
}

// A member name written twice in one object is refused. JSON decoding keeps
// the last of them, so accepting it would resolve a document to a value a
// reader of the file cannot see.
func TestParseRefusesARepeatedMemberAtEveryDepth(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		key  string
	}{
		{"top level", `{"no_ci": true, "no_ci": false}`, "no_ci"},
		{"in commands", `{"commands": {"test": "a", "test": "b"}}`, "commands.test"},
		{"in fix_rounds", `{"fix_rounds": {"review": 1, "review": 2}}`, "fix_rounds.review"},
		{"in review", `{"review": {"path_rules": [], "path_rules": []}}`, "review.path_rules"},
		{"in commit", `{"commit": {"fix_message": "fix: {summary}", "fix_message": "fix: {summary}"}}`, "commit.fix_message"},
		{"in a path rule", `{"review": {"path_rules": [{"paths": ["a.go"], "guidance": "g", "guidance": "h"}]}}`,
			"review.path_rules[0].guidance"},
		{"in a later path rule", `{"review": {"path_rules": [{"paths": ["a.go"], "guidance": "g"},` +
			`{"paths": ["b.go"], "paths": ["c.go"], "guidance": "g"}]}}`, "review.path_rules[1].paths"},
		{"in an ownership entry", `{"document": {"ownership": [{"subject": "s", "subject": "t", "document": "d.md"}]}}`,
			"document.ownership[0].subject"},
		{"in a section object", `{"document": {"ownership": []}, "document": {"ownership": []}}`, "document"},
	}
	for _, c := range cases {
		_, err := Parse(OriginTrusted, []byte(c.doc))
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse(%s) returned %v, want ErrInvalid", c.name, c.doc, err)
			continue
		}
		var ke *KeyError
		if !errors.As(err, &ke) {
			t.Errorf("%s: error %v is not a KeyError", c.name, err)
			continue
		}
		if string(ke.Key) != c.key {
			t.Errorf("%s: refusal names %q, want %q", c.name, ke.Key, c.key)
		}
		if !strings.Contains(ke.Detail, "more than once") {
			t.Errorf("%s: detail %q does not say the key is repeated", c.name, ke.Detail)
		}
	}
}

// The same name in two different objects is not a repetition, so a document
// that uses it is still accepted and still resolves both values.
func TestParseAcceptsTheSameNameInDifferentObjects(t *testing.T) {
	doc := `{"commands": {"test": "go test ./..."}, "fix_rounds": {"test": 2},
		"review": {"path_rules": [{"paths": ["a.go"], "guidance": "g"},
		                          {"paths": ["b.go"], "guidance": "h"}]}}`
	res, err := Resolve(mustParse(t, OriginTrusted, doc), Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Config.Commands.Test != "go test ./..." {
		t.Errorf("Commands.Test = %q", res.Config.Commands.Test)
	}
	if res.Config.FixRounds.Test != 2 {
		t.Errorf("FixRounds.Test = %d, want 2", res.Config.FixRounds.Test)
	}
	if len(res.Config.ReviewPathRules) != 2 {
		t.Errorf("ReviewPathRules has %d rules, want 2", len(res.Config.ReviewPathRules))
	}
}

// A document with more than one repeated member reports the same one every
// time, the same determinism a document with several broken keys has.
func TestParseRepeatedMemberRefusalIsDeterministic(t *testing.T) {
	doc := []byte(`{"no_ci": true, "no_ci": false, "run_budget": 1, "run_budget": 2, "agent": []}`)
	first := ""
	for range 50 {
		_, err := Parse(OriginTrusted, doc)
		if err == nil {
			t.Fatal("Parse accepted a document with repeated keys")
		}
		if first == "" {
			first = err.Error()
			continue
		}
		if err.Error() != first {
			t.Fatalf("Parse reported %q then %q for the same document", first, err)
		}
	}
	if !strings.Contains(first, string(KeyNoCI)) {
		t.Errorf("expected the first repetition in document order, got %q", first)
	}
}

// The repeated-member scan has to survive every value shape a document Parse
// accepts can contain, and say so loudly when it cannot. A number outside
// float64 range is the case that defeated an earlier scan: it decoded fine for
// Parse, which reads numbers as json.Number, and errored for a scan that did
// not, which then reported no repetition for the rest of the document.
func TestParseRefusesARepeatedMemberPastAwkwardValues(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		key  string
	}{
		{"after a number outside float64 range",
			`{"run_budget": 1e400, "no_ci": false, "no_ci": true}`, "no_ci"},
		{"the repeated key is itself the huge number",
			`{"ignore_patterns": 1e400, "ignore_patterns": ["*.md"], "no_ci": false}`, "ignore_patterns"},
		{"after a number with hundreds of digits",
			`{"run_budget": ` + strings.Repeat("9", 400) + `, "no_ci": false, "no_ci": true}`, "no_ci"},
		{"after a deeply negative exponent",
			`{"run_budget": -1e-400, "no_ci": false, "no_ci": true}`, "no_ci"},
		{"after a string with escapes and non-ASCII text",
			`{"commands": {"test": "a\"b\\cé😀\n"}, "no_ci": false, "no_ci": true}`, "no_ci"},
		{"after a null and a nested empty list",
			`{"agent": null, "review": {"path_rules": []}, "no_ci": false, "no_ci": true}`, "no_ci"},
		{"after a value of the wrong type for its key",
			`{"no_ci": {"a": [1, 2, {"b": "c"}]}, "no_ci": true}`, "no_ci"},
	}
	for _, c := range cases {
		_, err := Parse(OriginTrusted, []byte(c.doc))
		var ke *KeyError
		if !errors.As(err, &ke) || !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Parse returned %v, want a KeyError for a repeated member", c.name, err)
			continue
		}
		if string(ke.Key) != c.key {
			t.Errorf("%s: refusal names %q, want %q", c.name, ke.Key, c.key)
		}
		if !strings.Contains(ke.Detail, "more than once") {
			t.Errorf("%s: detail %q does not say the key is repeated", c.name, ke.Detail)
		}
	}
	// The same awkward values are still accepted, or refused on their own
	// merits, when nothing is repeated: the scan does not invent faults.
	if _, err := Parse(OriginTrusted, []byte(`{"commands": {"test": "a\"b\\cé😀\t"}}`)); err != nil {
		t.Errorf("Parse refused a document with an escaped string: %v", err)
	}
	_, err := Parse(OriginTrusted, []byte(`{"run_budget": 1e400}`))
	var ke *KeyError
	if !errors.As(err, &ke) || ke.Key != KeyRunBudget {
		t.Errorf("Parse returned %v, want a KeyError naming %q", err, KeyRunBudget)
	}
}

// A document Parse accepts was scanned all the way to its end. Appending a
// repeated member to an accepted document must therefore always be caught: if
// the scan had stopped early, the appended repetition would slip through.
func TestParseScansAcceptedDocumentsToTheEnd(t *testing.T) {
	accepted := []string{
		`{}`,
		`{"no_ci": true}`,
		`{"commands": {"test": "go test ./...", "lint": "", "format": "gofmt -l ."}}`,
		`{"agent": ["claude", "codex"], "fix_rounds": {"review": 0, "test": 3}}`,
		`{"ignore_patterns": ["*.md", "docs/**", "**/vendor/**"], "run_budget": 40}`,
		`{"review": {"path_rules": [{"paths": ["a.go"], "guidance": "g"},` +
			`{"paths": ["b/**", "c.go"], "guidance": "h"}]}}`,
		`{"document": {"ownership": [{"subject": "s", "document": "d.md"},` +
			`{"subject": "t", "document": "e.md"}]}}`,
		`{"checks_timeout": "168h", "session_reuse": false, "commit": {"fix_message": "fix: {summary}"}}`,
		`{"suppress_project_instructions": true, "allow_pushed_commands": true, "no_ci": false}`,
	}
	for _, doc := range accepted {
		if _, err := Parse(OriginTrusted, []byte(doc)); err != nil {
			t.Errorf("Parse(%s) was expected to be accepted: %v", doc, err)
			continue
		}
		withRepeat := strings.TrimSuffix(doc, "}") + `,"no_ci": true, "no_ci": false}`
		withRepeat = strings.Replace(withRepeat, "{,", "{", 1)
		_, err := Parse(OriginTrusted, []byte(withRepeat))
		var ke *KeyError
		if !errors.As(err, &ke) || !strings.Contains(ke.Detail, "more than once") {
			t.Errorf("Parse(%s) returned %v, want the appended repetition refused", withRepeat, err)
			continue
		}
		if ke.Key != KeyNoCI {
			t.Errorf("Parse(%s) named %q, want %q", withRepeat, ke.Key, KeyNoCI)
		}
	}
}

// An entry with more than one unrecognized field names the same one every
// time. Map iteration order is randomized, so an unsorted scan reports a
// different field per run and a fix chases a moving target.
func TestParseUnknownEntryFieldRefusalIsDeterministic(t *testing.T) {
	cases := []struct {
		name  string
		doc   string
		field string
	}{
		{"path rule", `{"review": {"path_rules": [{"paths": ["a.go"], "guidance": "g",` +
			` "why": 1, "how": 2, "zeta": 3}]}}`, `"how"`},
		{"ownership entry", `{"document": {"ownership": [{"subject": "s", "document": "d.md",` +
			` "why": 1, "how": 2, "zeta": 3}]}}`, `"how"`},
	}
	for _, c := range cases {
		first := ""
		for range 50 {
			_, err := Parse(OriginTrusted, []byte(c.doc))
			if err == nil {
				t.Fatalf("%s: Parse accepted an entry with unrecognized fields", c.name)
			}
			if first == "" {
				first = err.Error()
				continue
			}
			if err.Error() != first {
				t.Fatalf("%s: Parse reported %q then %q for the same document", c.name, first, err)
			}
		}
		if !strings.Contains(first, "unrecognized field "+c.field) {
			t.Errorf("%s: expected the first unknown field in sorted order, got %q", c.name, first)
		}
	}
}

// The scan refuses rather than reporting "no repetition" when it cannot read a
// document to its end. Parse rejects these documents before the scan sees
// them, so this drives the scan directly: the property is that no path through
// it returns success for bytes it did not finish reading.
func TestRepeatedMemberScanRefusesWhatItCannotFinish(t *testing.T) {
	for _, doc := range []string{`{"a": 1`, `{"a": [1, 2`, `{"a": {`, `{"a": 1} trailing`} {
		err := checkNoRepeatedNames([]byte(doc))
		if !errors.Is(err, ErrMalformed) {
			t.Errorf("checkNoRepeatedNames(%s) = %v, want a refusal wrapping ErrMalformed", doc, err)
		}
	}
	if err := checkNoRepeatedNames([]byte(`{"a": {"b": [1, {"c": 2}]}, "d": 1e400}`)); err != nil {
		t.Errorf("checkNoRepeatedNames refused a complete document: %v", err)
	}
}

// A refusal raised while decoding one element of a list says which element it
// came from. Without that an author has to find the offending rule or entry by
// counting through the list by hand.
func TestParseListRefusalsLocateTheElement(t *testing.T) {
	rule := func(body string) string {
		return `{"review": {"path_rules": [{"paths": ["a.go"], "guidance": "g"}, ` + body + `]}}`
	}
	owner := func(body string) string {
		return `{"document": {"ownership": [{"subject": "s", "document": "d.md"}, ` + body + `]}}`
	}
	long := strings.Repeat("x", MaxGuidanceRunes+1)
	longSubject := strings.Repeat("x", MaxSubjectRunes+1)
	manyPaths := listOf(MaxPathRulePaths + 1)
	control := `a\u0001b`

	cases := []struct {
		name  string
		doc   string
		where string
	}{
		{"guidance over the length limit", rule(`{"paths": ["b.go"], "guidance": "` + long + `"}`), "rule 1"},
		{"guidance with a control character", rule(`{"paths": ["b.go"], "guidance": "` + control + `"}`), "rule 1"},
		{"guidance of the wrong type", rule(`{"paths": ["b.go"], "guidance": 7}`), "rule 1"},
		{"a malformed pattern in a rule", rule(`{"paths": ["/bad"], "guidance": "g"}`), "rule 1"},
		{"a non-string pattern in a rule", rule(`{"paths": [7], "guidance": "g"}`), "rule 1"},
		{"paths that are not a list", rule(`{"paths": "b.go", "guidance": "g"}`), "rule 1"},
		{"too many paths in a rule", rule(`{"paths": ` + manyPaths + `, "guidance": "g"}`), "rule 1"},
		{"a rule that is not an object", rule(`"nope"`), "rule 1"},
		{"a rule with an unknown field", rule(`{"paths": ["b.go"], "guidance": "g", "why": 1}`), "rule 1"},
		{"a rule with no guidance", rule(`{"paths": ["b.go"]}`), "rule 1"},
		{"a rule with empty guidance", rule(`{"paths": ["b.go"], "guidance": " "}`), "rule 1"},
		{"a rule with no paths at all", rule(`{"guidance": "g"}`), "rule 1"},
		{"a rule scoped to nothing", rule(`{"paths": [], "guidance": "g"}`), "rule 1"},
		{"subject over the length limit", owner(`{"subject": "` + longSubject + `", "document": "d.md"}`), "entry 1"},
		{"document with a control character", owner(`{"subject": "t", "document": "` + control + `"}`), "entry 1"},
		{"subject of the wrong type", owner(`{"subject": 7, "document": "d.md"}`), "entry 1"},
		{"an empty document path", owner(`{"subject": "t", "document": " "}`), "entry 1"},
		{"an entry with no document", owner(`{"subject": "t"}`), "entry 1"},
		{"an entry that is not an object", owner(`"nope"`), "entry 1"},
		{"an entry with an unknown field", owner(`{"subject": "t", "document": "d.md", "why": 1}`), "entry 1"},
		{"a second claim on a subject", owner(`{"subject": "s", "document": "e.md"}`), "entry 1"},
		{"a malformed ignore pattern", `{"ignore_patterns": ["a.go", "b.go", "/bad"]}`, "pattern 2"},
		{"a non-string ignore pattern", `{"ignore_patterns": ["a.go", 7]}`, "pattern 1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := parseError(t, c.doc)
			var ke *KeyError
			if !errors.As(err, &ke) || !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse returned %v, want a KeyError", err)
			}
			if !strings.Contains(ke.Detail, c.where) {
				t.Errorf("detail %q does not say it came from %s", ke.Detail, c.where)
			}
		})
	}
	// The same shapes are accepted when nothing is wrong with them, so the
	// locator is not coming from a check that refuses everything.
	for _, doc := range []string{
		rule(`{"paths": ["b.go", "c/**"], "guidance": "g"}`),
		owner(`{"subject": "t", "document": "e.md"}`),
		`{"ignore_patterns": ["a.go", "b.go", "docs/**"]}`,
	} {
		if _, err := Parse(OriginTrusted, []byte(doc)); err != nil {
			t.Errorf("Parse refused a valid document %s: %v", doc, err)
		}
	}
}

// A refusal about one element carries that element and not the rest of the
// list, so a fault in a long list does not arrive with every other entry
// attached to it.
func TestParseListRefusalDoesNotCarryTheOtherElements(t *testing.T) {
	guidance := strings.Repeat("g", 200)
	rules := make([]string, MaxPathRules)
	for i := range rules {
		rules[i] = `{"paths": ["keep` + itoa(i) + `.go"], "guidance": "` + guidance + `"}`
	}
	rules[MaxPathRules-1] = `{"paths": ["broken.go"]}`

	err := parseError(t, `{"review": {"path_rules": [`+strings.Join(rules, ",")+`]}}`)
	var ke *KeyError
	if !errors.As(err, &ke) {
		t.Fatalf("Parse returned %v, want a KeyError", err)
	}
	if !strings.Contains(ke.Value, "broken.go") {
		t.Errorf("value %q does not carry the offending rule", ke.Value)
	}
	if strings.Contains(ke.Value, "keep0.go") {
		t.Errorf("value carries an unrelated rule: %q", ke.Value)
	}
	if len(ke.Value) > 200 {
		t.Errorf("value is %d bytes for a fault in one rule of %d", len(ke.Value), MaxPathRules)
	}

	owners := make([]string, MaxOwnership)
	for i := range owners {
		owners[i] = `{"subject": "keep` + itoa(i) + `", "document": "d.md"}`
	}
	owners[MaxOwnership-1] = `{"subject": "` + strings.Repeat("x", MaxSubjectRunes+1) + `", "document": "d.md"}`
	err = parseError(t, `{"document": {"ownership": [`+strings.Join(owners, ",")+`]}}`)
	if !errors.As(err, &ke) {
		t.Fatalf("Parse returned %v, want a KeyError", err)
	}
	if strings.Contains(ke.Value, "keep0") {
		t.Errorf("value carries an unrelated ownership entry: %q", ke.Value)
	}

	// A fault about the list itself still carries the list, because that is
	// what it is about.
	err = parseError(t, `{"ignore_patterns": `+listOf(MaxIgnorePatterns+1)+`}`)
	if !errors.As(err, &ke) {
		t.Fatalf("Parse returned %v, want a KeyError", err)
	}
	if !strings.Contains(ke.Value, `"f0.go"`) {
		t.Errorf("a refusal about the whole list must carry it, got %q", ke.Value)
	}
}

// listOf builds a JSON list of n distinct pattern strings.
func listOf(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = `"f` + itoa(i) + `.go"`
	}
	return "[" + strings.Join(out, ",") + "]"
}
