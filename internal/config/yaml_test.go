package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// wholeSchemaYAML is TestParseAcceptsTheWholeSchema's document written as
// block YAML, with a comment, so the two formats can be held to one result.
const wholeSchemaYAML = `# the operator's own configuration
agent: [claude, fallback]
roles:
  worker:
    agent: claude
commands:
  test: go test ./...
  lint: make lint
  format: gofmt -w .
  document: make docs
fix_rounds: {review: 0, rebase: 1, test: 2, lint: 3, checks: 4}
run_budget: 12
ignore_patterns:
  - docs/**
  - "*.md"
review:
  path_rules:
    - paths: [internal/**]
      guidance: check the contracts
judge_paths: [spec/**]
document:
  ownership:
    - subject: the schema
      document: docs/prd.html
suppress_project_instructions: true
no_ci: true
checks_timeout: 30m
session_reuse: false
decision_model: enforce
supervision_beacon_interval: 30s
escalation_window: 15m
recheck_window: 3h
stall_inspection_count: 5
relaunch_attempts: 3
stop_grace: 45s
promotion_window: 2h
command_timeout: 45m
commit:
  fix_message: "fix(gate): {summary}"
allow_pushed_commands: true
autonomy:
  merge: [owner/name]
delivery: local
allow_standing_consent: true
session_provider: tmux
usage_ceiling:
  window: 24h
  input_tokens: 1
  output_tokens: 2
  cache_read_tokens: 3
  cache_creation_tokens: 4
notify:
  command: notify-me --channel private
run_usage_budget:
  output_tokens: 5
`

const wholeSchemaJSON = `{
	"agent": ["claude", "fallback"],
	"roles": {"worker": {"agent": "claude"}},
	"commands": {"test": "go test ./...", "lint": "make lint", "format": "gofmt -w .", "document": "make docs"},
	"fix_rounds": {"review": 0, "rebase": 1, "test": 2, "lint": 3, "checks": 4},
	"run_budget": 12,
	"ignore_patterns": ["docs/**", "*.md"],
	"review": {"path_rules": [{"paths": ["internal/**"], "guidance": "check the contracts"}]},
	"judge_paths": ["spec/**"],
	"document": {"ownership": [{"subject": "the schema", "document": "docs/prd.html"}]},
	"suppress_project_instructions": true,
	"no_ci": true,
	"checks_timeout": "30m",
	"session_reuse": false,
	"decision_model": "enforce",
	"supervision_beacon_interval": "30s",
	"escalation_window": "15m",
	"recheck_window": "3h",
	"stall_inspection_count": 5,
	"relaunch_attempts": 3,
	"stop_grace": "45s",
	"promotion_window": "2h",
	"command_timeout": "45m",
	"commit": {"fix_message": "fix(gate): {summary}"},
	"allow_pushed_commands": true,
	"autonomy": {"merge": ["owner/name"]},
	"delivery": "local",
	"allow_standing_consent": true,
	"session_provider": "tmux",
	"usage_ceiling": {"window": "24h", "input_tokens": 1, "output_tokens": 2, "cache_read_tokens": 3, "cache_creation_tokens": 4},
	"notify": {"command": "notify-me --channel private"},
	"run_usage_budget": {"output_tokens": 5}
}`

func resolvedGlobal(t *testing.T, l Layer) Config {
	t.Helper()
	res, err := Resolve(l, Absent(OriginTrusted))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return res.Config
}

// A YAML document and the JSON document that says the same thing set the
// same keys and resolve to the same configuration, across every key in the
// schema, and the JSON document read as YAML does too, so a global document
// written as JSON before YAML was read still means what it meant.
func TestParseYAMLReadsTheWholeSchemaAsParseReadsJSON(t *testing.T) {
	fromJSON, err := Parse(OriginGlobal, []byte(wholeSchemaJSON))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := resolvedGlobal(t, fromJSON)
	for name, doc := range map[string]string{"block YAML": wholeSchemaYAML, "JSON read as YAML": wholeSchemaJSON} {
		l, err := ParseYAML(OriginGlobal, []byte(doc))
		if err != nil {
			t.Fatalf("%s: ParseYAML: %v", name, err)
		}
		if got := len(l.SetKeys()); got != len(Keys()) {
			t.Errorf("%s: set %d keys, want all %d: %v", name, got, len(Keys()), l.SetKeys())
		}
		if got := resolvedGlobal(t, l); !reflect.DeepEqual(got, want) {
			t.Errorf("%s resolved to\n%+v\nwant what the JSON resolves to\n%+v", name, got, want)
		}
	}
}

// The document the operator who found this wrote, which the JSON reader
// refused as not being JSON.
func TestParseYAMLReadsAPlainYAMLLine(t *testing.T) {
	l, err := ParseYAML(OriginGlobal, []byte("session_provider: tmux\n"))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if got := resolvedGlobal(t, l).SessionProvider; got != "tmux" {
		t.Fatalf("SessionProvider = %q, want tmux", got)
	}
}

func TestParseYAMLRequiresAnOrigin(t *testing.T) {
	if _, err := ParseYAML(OriginUnknown, []byte("no_ci: true")); !errors.Is(err, ErrUnknownOrigin) {
		t.Fatalf("ParseYAML with no origin returned %v, want ErrUnknownOrigin", err)
	}
}

// Input with no content is a present document that sets nothing, as Parse
// treats empty input, and a comment alone is no content.
func TestParseYAMLReadsNoContentAsAnEmptyPresentDocument(t *testing.T) {
	for _, doc := range []string{"", "  \n\t\n", "# nothing set yet\n", "{}"} {
		l, err := ParseYAML(OriginGlobal, []byte(doc))
		if err != nil {
			t.Errorf("ParseYAML(%q): %v", doc, err)
			continue
		}
		if !l.Present() || len(l.SetKeys()) != 0 {
			t.Errorf("ParseYAML(%q) = present %v, keys %v; want present and empty", doc, l.Present(), l.SetKeys())
		}
	}
}

// Every refusal the YAML reader adds, each beside the corrected document,
// which is accepted, so no case passes because the reader refuses
// everything near it. A key-level refusal names where it stands.
func TestParseYAMLRefusesWhatWouldMeanSomethingOtherThanItSays(t *testing.T) {
	cases := []struct {
		name      string
		doc       string
		corrected string
		malformed bool
		key       string
		detail    string
	}{
		{name: "a repeated top-level key", doc: "no_ci: true\nno_ci: false\n", corrected: "no_ci: false\n",
			key: "no_ci", detail: "more than once"},
		{name: "a repeated nested key", doc: "commands:\n  test: a\n  test: b\n", corrected: "commands:\n  test: b\n",
			key: "commands.test", detail: "more than once"},
		{name: "a repeated key in a flow mapping", doc: `{"fix_rounds": {"review": 1, "review": 2}}`,
			corrected: `{"fix_rounds": {"review": 2}}`, key: "fix_rounds.review", detail: "more than once"},
		{name: "a repeated key in a list element",
			doc:       "review:\n  path_rules:\n    - paths: [a.go]\n      guidance: g\n    - paths: [b.go]\n      paths: [c.go]\n      guidance: g\n",
			corrected: "review:\n  path_rules:\n    - paths: [a.go]\n      guidance: g\n    - paths: [c.go]\n      guidance: g\n",
			key:       "review.path_rules[1].paths", detail: "more than once"},
		{name: "a repeated section", doc: "document: {ownership: []}\ndocument: {ownership: []}\n",
			corrected: "document: {ownership: []}\n", key: "document", detail: "more than once"},
		{name: "yes for true", doc: "no_ci: yes\n", corrected: "no_ci: true\n", key: "no_ci", detail: "true or false"},
		{name: "on for true", doc: "no_ci: on\n", corrected: "no_ci: true\n", key: "no_ci", detail: "true or false"},
		{name: "a quoted number", doc: "run_budget: \"12\"\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "whole number"},
		{name: "a hexadecimal number", doc: "run_budget: 0x0c\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "decimal"},
		{name: "an octal number", doc: "run_budget: 0o14\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "decimal"},
		{name: "a leading plus", doc: "run_budget: +12\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "decimal"},
		{name: "infinity", doc: "run_budget: .inf\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "decimal"},
		{name: "a number where text is expected", doc: "delivery: 12\n", corrected: "delivery: local\n", key: "delivery", detail: "string"},
		{name: "an explicit tag", doc: "run_budget: !!int \"12\"\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "explicit tag"},
		{name: "an anchor and an alias", doc: "judge_paths: &p [a/**]\nignore_patterns: *p\n",
			corrected: "judge_paths: [a/**]\nignore_patterns: [a/**]\n", key: "judge_paths", detail: "anchor"},
		{name: "a merge key", doc: "commands:\n  <<: {test: a}\n", corrected: "commands:\n  test: a\n", key: "commands.<<", detail: "merge key"},
		{name: "a timestamp", doc: "checks_timeout: 2026-10-15\n", corrected: "checks_timeout: 30m\n", key: "checks_timeout", detail: "timestamp"},
		{name: "a key that is not text", doc: "1: x\n", corrected: "no_ci: true\n", key: "1", detail: "plain text"},
		{name: "a null", doc: "run_budget: ~\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "value is required"},
		{name: "an empty value", doc: "run_budget:\n", corrected: "run_budget: 12\n", key: "run_budget", detail: "value is required"},
		{name: "an unrecognized key", doc: "sesion_provider: tmux\n", corrected: "session_provider: tmux\n",
			key: "sesion_provider", detail: "unrecognized"},
		{name: "a flat dotted key", doc: "fix_rounds.review: 1\n", corrected: "fix_rounds:\n  review: 1\n",
			key: "fix_rounds.review", detail: "flat dotted form"},
		{name: "two documents", doc: "no_ci: true\n---\nno_ci: false\n", corrected: "no_ci: true\n", malformed: true,
			detail: "more than one"},
		{name: "a list at the top", doc: "- no_ci\n", corrected: "no_ci: true\n", malformed: true, detail: "mapping"},
		{name: "text at the top", doc: "tmux\n", corrected: "session_provider: tmux\n", malformed: true, detail: "mapping"},
		{name: "the JSON escape for a slash, which YAML does not have", doc: `{"commands": {"test": "go test .\/..."}}`,
			corrected: `{"commands": {"test": "go test ./..."}}`, malformed: true, detail: "not valid YAML"},
		{name: "a syntax error", doc: "supervision_beacon_interval: [not a duration\n",
			corrected: "supervision_beacon_interval: 1s\n", malformed: true, detail: "not valid YAML"},
	}
	for _, c := range cases {
		if _, err := ParseYAML(OriginGlobal, []byte(c.corrected)); err != nil {
			t.Errorf("%s: the corrected document %q was refused: %v", c.name, c.corrected, err)
		}
		_, err := ParseYAML(OriginGlobal, []byte(c.doc))
		if c.malformed {
			var de *DocumentError
			if !errors.Is(err, ErrMalformed) || !errors.As(err, &de) || !strings.Contains(de.Error(), c.detail) {
				t.Errorf("%s: ParseYAML(%q) = %v, want a malformed document saying %q", c.name, c.doc, err, c.detail)
			}
			continue
		}
		var ke *KeyError
		if !errors.Is(err, ErrInvalid) || !errors.As(err, &ke) {
			t.Errorf("%s: ParseYAML(%q) = %v, want a refusal naming %q", c.name, c.doc, err, c.key)
			continue
		}
		if string(ke.Key) != c.key || !strings.Contains(ke.Detail, c.detail) {
			t.Errorf("%s: refusal names %q saying %q, want %q saying %q", c.name, ke.Key, ke.Detail, c.key, c.detail)
		}
	}
}

// A repository origin is held to the global-only rule when the document is
// YAML too, because the rule is the walk's and not the format's.
func TestParseYAMLRefusesAGlobalOnlyKeyFromARepositoryOrigin(t *testing.T) {
	doc := []byte("session_provider: tmux\n")
	if _, err := ParseYAML(OriginGlobal, doc); err != nil {
		t.Fatalf("the operator's own document was refused: %v", err)
	}
	for _, o := range []Origin{OriginTrusted, OriginPushed} {
		var ke *KeyError
		if _, err := ParseYAML(o, doc); !errors.As(err, &ke) || ke.Key != KeySessionProvider {
			t.Errorf("ParseYAML(%v) = %v, want the global-only key refused", o, err)
		}
	}
}
