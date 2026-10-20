package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// A home that names no per-run usage budget has none, the operator's own
// file sets one, zero stays none, and a negative budget is refused rather
// than read as none.
func TestTheRunUsageBudgetIsOffUntilTheOperatorSetsOne(t *testing.T) {
	t.Parallel()
	if got := Defaults().RunUsageBudget; got != 0 {
		t.Fatalf("the default per-run usage budget is %d, want none", got)
	}
	for doc, want := range map[string]int64{
		`{"run_usage_budget": {"output_tokens": 0}}`:     0,
		`{"run_usage_budget": {"output_tokens": 50000}}`: 50000,
	} {
		if got := resolve(t, mustParse(t, OriginGlobal, doc), Absent(OriginTrusted)).Config.RunUsageBudget; got != want {
			t.Errorf("%s resolved to %d, want %d", doc, got, want)
		}
	}
	_, err := Parse(OriginGlobal, []byte(`{"run_usage_budget": {"output_tokens": -1}}`))
	var ke *KeyError
	if !errors.As(err, &ke) || ke.Key != KeyRunUsageBudgetOutputTokens {
		t.Fatalf("a negative budget parsed as %v, want a refusal naming the key", err)
	}
}

// PRD principle P7 for the per-run usage budget: a repository copy, trusted
// or pushed, naming it is refused when it is parsed, so neither copy can
// raise or lift the budget the operator's file set, and a run resolved over a
// pushed copy setting other keys keeps the operator's budget. The control is
// the operator's own document, which parses.
func TestARepositoryCopyCannotSetTheRunUsageBudget(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	doc := `{"run_usage_budget": {"output_tokens": 1000000000}}`
	for _, origin := range []Origin{OriginTrusted, OriginPushed} {
		_, err := Parse(origin, []byte(doc))
		var ke *KeyError
		if !errors.As(err, &ke) || ke.Key != KeyRunUsageBudgetOutputTokens || !strings.Contains(err.Error(), "global-only") {
			t.Errorf("Parse(%v, %s) = %v, want a global-only refusal of %s", origin, doc, err, KeyRunUsageBudgetOutputTokens)
		}
	}
	if _, err := Parse(OriginGlobal, []byte(doc)); err != nil {
		t.Fatalf("the operator's own document naming the budget was refused: %v", err)
	}
	global := mustParse(t, OriginGlobal, `{"run_usage_budget": {"output_tokens": 10}}`)
	pushed := mustParse(t, OriginPushed, `{"fix_rounds": {"test": 1}}`)
	if got := resolveRun(t, global, Absent(OriginTrusted), pushed).Config.RunUsageBudget; got != 10 {
		t.Errorf("a run resolved its usage budget to %d, want the operator's 10", got)
	}
}
