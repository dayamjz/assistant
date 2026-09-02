package findings_test

import (
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

// Every action, plus the ones no one recognizes, against the one predicate the
// fix loop is gated on.
func TestFixEligibleIsTrueForFixAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		action findings.Action
		want   bool
	}{
		{findings.ActionFix, true},
		{findings.ActionAsk, false},
		{findings.ActionNote, false},
		{findings.ActionUnset, false},
		{findings.Action("FIX"), false},
		{findings.Action(" fix"), false},
		{findings.Action("fixable"), false},
		{findings.Action("unknown"), false},
	} {
		f := findings.Finding{Action: tc.action, Description: "d"}
		if got := f.FixEligible(); got != tc.want {
			t.Errorf("Finding{Action: %q}.FixEligible() = %v, want %v", tc.action, got, tc.want)
		}
	}
}

func TestParksIsTrueForAskOnly(t *testing.T) {
	for _, tc := range []struct {
		action findings.Action
		want   bool
	}{
		{findings.ActionAsk, true},
		{findings.ActionFix, false},
		{findings.ActionNote, false},
		{findings.ActionUnset, false},
		{findings.Action("ASK"), false},
	} {
		f := findings.Finding{Action: tc.action, Description: "d"}
		if got := f.Parks(); got != tc.want {
			t.Errorf("Finding{Action: %q}.Parks() = %v, want %v", tc.action, got, tc.want)
		}
	}
}

func TestFixableNeverReturnsAnAskOrANote(t *testing.T) {
	in := []findings.Finding{
		{ID: "1", Action: findings.ActionAsk, Description: "judgment"},
		{ID: "2", Action: findings.ActionFix, Description: "typo"},
		{ID: "3", Action: findings.ActionNote, Description: "noted"},
		{ID: "4", Action: findings.ActionUnset, Description: "no action given"},
		{ID: "5", Action: findings.Action("fix-it"), Description: "unrecognized"},
		{ID: "6", Action: findings.ActionFix, Description: "another typo"},
	}
	got := findings.Fixable(in)
	want := []string{"2", "6"}
	if len(got) != len(want) {
		t.Fatalf("Fixable returned %d findings, want %d: %+v", len(got), len(want), got)
	}
	for i, f := range got {
		if f.ID != want[i] {
			t.Errorf("Fixable()[%d].ID = %q, want %q", i, f.ID, want[i])
		}
		if !f.FixEligible() {
			t.Errorf("Fixable()[%d] is not fix-eligible: %+v", i, f)
		}
	}
}

func TestFixableOnEmptyAndNil(t *testing.T) {
	if got := findings.Fixable(nil); len(got) != 0 {
		t.Errorf("Fixable(nil) = %+v, want empty", got)
	}
	if got := findings.Fixable([]findings.Finding{}); len(got) != 0 {
		t.Errorf("Fixable(empty) = %+v, want empty", got)
	}
}

func TestFixableDoesNotAliasItsInput(t *testing.T) {
	in := []findings.Finding{{ID: "1", Action: findings.ActionFix, Description: "typo"}}
	got := findings.Fixable(in)
	got[0].Description = "rewritten"
	if in[0].Description != "typo" {
		t.Fatalf("Fixable aliased its input: the source finding now reads %q", in[0].Description)
	}
}

func TestParkedReturnsOnlyAsks(t *testing.T) {
	in := []findings.Finding{
		{ID: "1", Action: findings.ActionFix, Description: "typo"},
		{ID: "2", Action: findings.ActionAsk, Description: "judgment"},
		{ID: "3", Action: findings.ActionNote, Description: "noted"},
	}
	got := findings.Parked(in)
	if len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("Parked = %+v, want just the ask", got)
	}
}

func TestLocationUnmarshalAcceptsObjectAndText(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want findings.Location
	}{
		{`{"path":"internal/graph/node.go","line":42}`, findings.Location{Path: "internal/graph/node.go", Line: 42}},
		{`{"path":"internal/graph/node.go"}`, findings.Location{Path: "internal/graph/node.go"}},
		{`"internal/graph/node.go:42"`, findings.Location{Path: "internal/graph/node.go", Line: 42}},
		{`"internal/graph/node.go"`, findings.Location{Path: "internal/graph/node.go"}},
		{`"the pull request body"`, findings.Location{Path: "the pull request body"}},
		{`"a.go:notaline"`, findings.Location{Path: "a.go:notaline"}},
		{`"a.go:0"`, findings.Location{Path: "a.go:0"}},
		// The file:line:column form every common compiler and linter prints.
		// The line is the middle segment; the column has nowhere to go.
		{`"internal/gate/push.go:88:12"`,
			findings.Location{Path: "internal/gate/push.go", Line: 88}},
		// A colon in the path is still not truncated, because the segment
		// before the line does not parse as a positive integer.
		{`"weird:dir/a.go:88"`, findings.Location{Path: "weird:dir/a.go", Line: 88}},
		{`"a.go:notaline:12"`, findings.Location{Path: "a.go:notaline", Line: 12}},
		{`""`, findings.Location{}},
		{`null`, findings.Location{}},
		{`{}`, findings.Location{}},
		// Each part of the object form is read on its own terms, so one part
		// nobody can read does not take the other part with it.
		{`{"path":"a.go","line":"42"}`, findings.Location{Path: "a.go"}},
		{`{"path":"a.go","line":{"of":42}}`, findings.Location{Path: "a.go"}},
		{`{"path":42,"line":7}`, findings.Location{Line: 7}},
		{`{"path":null,"line":null}`, findings.Location{}},
	} {
		var got findings.Location
		if err := json.Unmarshal([]byte(tc.raw), &got); err != nil {
			t.Errorf("Unmarshal(%s) returned %v", tc.raw, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Unmarshal(%s) = %+v, want %+v", tc.raw, got, tc.want)
		}
	}
}

// A value that is neither an object, nor a string, nor null says nothing
// readable at all, so it is refused rather than guessed at.
func TestLocationUnmarshalRefusesOtherShapes(t *testing.T) {
	for _, raw := range []string{`42`, `true`, `["a.go", 3]`} {
		var got findings.Location
		if err := json.Unmarshal([]byte(raw), &got); err == nil {
			t.Errorf("Unmarshal(%s) = %+v, want a refusal", raw, got)
		}
	}
}

// json.Unmarshal validates a whole document before any field's decoder runs,
// so this reaches the string branch's refusal the way only a direct call can.
func TestLocationUnmarshalRefusesAnInvalidStringLiteral(t *testing.T) {
	var got findings.Location
	if err := got.UnmarshalJSON([]byte(`"a\qb"`)); err == nil {
		t.Fatalf("UnmarshalJSON accepted an invalid string literal as %+v", got)
	}
}

func TestLocationString(t *testing.T) {
	for _, tc := range []struct {
		in   findings.Location
		want string
	}{
		{findings.Location{Path: "a.go", Line: 3}, "a.go:3"},
		{findings.Location{Path: "a.go"}, "a.go"},
		{findings.Location{}, ""},
		{findings.Location{Line: 3}, ""},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("Location%+v.String() = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !(findings.Location{}).Empty() {
		t.Error("the zero Location must report Empty")
	}
	if (findings.Location{Path: "a.go"}).Empty() {
		t.Error("a location with a path must not report Empty")
	}
}
