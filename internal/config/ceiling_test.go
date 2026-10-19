package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// Zero tokens is no ceiling, and a document that sets the key without a
// window keeps the usage view's window rather than an empty one.
func TestAZeroUsageCeilingIsOffAndAnOmittedWindowIsTheDefault(t *testing.T) {
	cases := []struct {
		doc  string
		want UsageCeiling
	}{
		{`{"usage_ceiling": {"input_tokens": 0}}`, UsageCeiling{Window: DefaultUsageCeilingWindow}},
		{`{"usage_ceiling": {}}`, UsageCeiling{Window: DefaultUsageCeilingWindow}},
		{`{"usage_ceiling": {"window": ""}}`, UsageCeiling{Window: DefaultUsageCeilingWindow}},
		{`{"usage_ceiling": {"window": "24h", "input_tokens": 5}}`, UsageCeiling{Window: "24h", InputTokens: 5}},
		{`{"usage_ceiling": {"window": " 90m "}}`, UsageCeiling{Window: "90m"}},
	}
	for _, c := range cases {
		l := mustParse(t, OriginGlobal, c.doc)
		got := resolve(t, l, Absent(OriginTrusted)).Config.UsageCeiling
		if got != c.want {
			t.Errorf("%s resolved to %+v, want %+v", c.doc, got, c.want)
		}
	}
	if got := Defaults().UsageCeiling; got != defaultUsageCeiling {
		t.Errorf("Defaults().UsageCeiling = %+v, want %+v", got, defaultUsageCeiling)
	}
}

// Two unrecognized fields are refused at the first in sorted order, so the
// refusal names the same field every time.
func TestAUsageCeilingNamesTheFirstUnrecognizedField(t *testing.T) {
	_, err := Parse(OriginGlobal, []byte(`{"usage_ceiling": {"zzz": 1, "price": 2}}`))
	if err == nil {
		t.Fatal("Parse accepted unrecognized fields")
	}
	if !strings.Contains(err.Error(), `"price"`) || strings.Contains(err.Error(), `"zzz"`) {
		t.Errorf("error %q, want it to name price and not zzz", err)
	}
}

// A repository file cannot raise the ceiling, from either copy. The operator's
// own file is what a run keeps, including when the pushed copy sets other keys
// and leaves this one out.
func TestARepositoryFileCannotRaiseTheUsageCeiling(t *testing.T) {
	principles.Cite(t, principles.P7)
	doc := `{"usage_ceiling": {"window": "24h", "input_tokens": 1000000}}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		_, err := Parse(origin, []byte(doc))
		if err == nil {
			t.Fatalf("Parse(%v) accepted a usage ceiling", origin)
		}
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyUsageCeiling || !strings.Contains(err.Error(), "global-only") {
			t.Errorf("Parse(%v) = %v, want a global-only refusal of usage_ceiling", origin, err)
		}
	}
	global := mustParse(t, OriginGlobal, `{"usage_ceiling": {"input_tokens": 10}}`)
	pushed := mustParse(t, OriginPushed, `{"run_budget": 20}`)
	got := resolveRun(t, global, Absent(OriginTrusted), pushed).Config.UsageCeiling
	if got != (UsageCeiling{Window: DefaultUsageCeilingWindow, InputTokens: 10}) {
		t.Errorf("a run resolved to %+v, want the operator's ceiling of 10", got)
	}
}
