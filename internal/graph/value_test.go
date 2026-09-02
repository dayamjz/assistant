package graph_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/graph"
)

func TestValueAccessorsAnswerOnlyForTheirOwnKind(t *testing.T) {
	cases := []struct {
		name  string
		value graph.Value
		kind  graph.Kind
	}{
		{"text", graph.TextValue("hello"), graph.KindText},
		{"int", graph.IntValue(-7), graph.KindInt},
		{"bool", graph.BoolValue(true), graph.KindBool},
		{"list", graph.ListValue("a", "b"), graph.KindList},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.value.Kind() != tc.kind {
				t.Fatalf("Kind() = %s, want %s", tc.value.Kind(), tc.kind)
			}
			if tc.kind.String() != tc.name {
				t.Errorf("Kind.String() = %q, want %q", tc.kind.String(), tc.name)
			}
			if _, ok := tc.value.Text(); ok != (tc.kind == graph.KindText) {
				t.Errorf("Text() answered %v for a %s value", ok, tc.kind)
			}
			if _, ok := tc.value.Int(); ok != (tc.kind == graph.KindInt) {
				t.Errorf("Int() answered %v for a %s value", ok, tc.kind)
			}
			if _, ok := tc.value.Bool(); ok != (tc.kind == graph.KindBool) {
				t.Errorf("Bool() answered %v for a %s value", ok, tc.kind)
			}
			if _, ok := tc.value.List(); ok != (tc.kind == graph.KindList) {
				t.Errorf("List() answered %v for a %s value", ok, tc.kind)
			}
			if tc.value.String() == "" {
				t.Error("String() rendered nothing")
			}
		})
	}

	if got, _ := graph.BoolValue(true).Bool(); !got {
		t.Error("BoolValue(true).Bool() = false")
	}
	if got, _ := graph.IntValue(-7).Int(); got != -7 {
		t.Errorf("IntValue(-7).Int() = %d", got)
	}
}

func TestListValuesDoNotAliasWhatTheCallerHolds(t *testing.T) {
	items := []string{"a", "b"}
	v := graph.ListValue(items...)
	items[0] = "tampered"
	got, _ := v.List()
	if !equalStrings(got, []string{"a", "b"}) {
		t.Errorf("mutating the source slice changed the value: %v", got)
	}
	got[1] = "tampered"
	again, _ := v.List()
	if !equalStrings(again, []string{"a", "b"}) {
		t.Errorf("mutating a returned slice changed the value: %v", again)
	}
}

func TestValueEqualityComparesKindAndContents(t *testing.T) {
	cases := []struct {
		name  string
		a, b  graph.Value
		equal bool
	}{
		{"same text", graph.TextValue("x"), graph.TextValue("x"), true},
		{"different text", graph.TextValue("x"), graph.TextValue("y"), false},
		{"same int", graph.IntValue(3), graph.IntValue(3), true},
		{"different int", graph.IntValue(3), graph.IntValue(4), false},
		{"same bool", graph.BoolValue(false), graph.BoolValue(false), true},
		{"different bool", graph.BoolValue(true), graph.BoolValue(false), false},
		{"same list", graph.ListValue("a", "b"), graph.ListValue("a", "b"), true},
		{"reordered list", graph.ListValue("a", "b"), graph.ListValue("b", "a"), false},
		{"shorter list", graph.ListValue("a"), graph.ListValue("a", "b"), false},
		{"different kinds", graph.TextValue("1"), graph.IntValue(1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Equal(tc.b); got != tc.equal {
				t.Errorf("%v.Equal(%v) = %v, want %v", tc.a, tc.b, got, tc.equal)
			}
			if got := tc.b.Equal(tc.a); got != tc.equal {
				t.Errorf("equality is not symmetric for %v and %v", tc.a, tc.b)
			}
		})
	}
}

func TestValuesRoundTripThroughTheirWireFormat(t *testing.T) {
	for _, want := range []graph.Value{
		graph.TextValue(""),
		graph.TextValue(`quotes " and \ backslashes`),
		graph.IntValue(0),
		graph.IntValue(-9007199254740993),
		graph.BoolValue(true),
		graph.ListValue(),
		graph.ListValue("a", "", "c"),
	} {
		encoded, err := json.Marshal(want)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", want, err)
		}
		var got graph.Value
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", encoded, err)
		}
		if !got.Equal(want) {
			t.Errorf("round trip turned %v into %v via %s", want, got, encoded)
		}
	}

	if _, err := json.Marshal(graph.Value{}); err == nil {
		t.Error("encoding a value with no kind was accepted")
	}
}

func TestGraphNewStateZeroesEveryDeclaredKey(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("only").
		Key(graph.Key{Name: "name", Kind: graph.KindText}).
		Key(graph.Key{Name: "count", Kind: graph.KindInt}).
		Key(graph.Key{Name: "ready", Kind: graph.KindBool}).
		Key(graph.Key{Name: "items", Kind: graph.KindList}).
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))

	s := mustState(t, g, nil)
	if !equalStrings(s.Keys(), []string{"count", "items", "name", "ready"}) {
		t.Errorf("Keys() = %v, want every declared key, sorted", s.Keys())
	}
	if text(t, s, "name") != "" || number(t, s, "count") != 0 || len(list(t, s, "items")) != 0 {
		t.Errorf("an unwritten key does not hold its zero value: %v", s.Keys())
	}
	ready, _ := s.Get("ready")
	if got, _ := ready.Bool(); got {
		t.Error("an unwritten bool key is true")
	}
	if _, ok := s.Get("absent"); ok {
		t.Error("Get answered for a key the graph does not declare")
	}
}

func TestGuardsCompareStateTheWayTheyDeclare(t *testing.T) {
	cases := []struct {
		name    string
		key     graph.Key
		guard   graph.Guard
		state   map[string]graph.Value
		routeTo string
	}{
		{
			name:    "equals matches",
			key:     graph.Key{Name: "stage", Kind: graph.KindText},
			guard:   graph.Guard{Key: "stage", Op: graph.OpEquals, Value: graph.TextValue("review")},
			state:   map[string]graph.Value{"stage": graph.TextValue("review")},
			routeTo: "yes",
		},
		{
			name:    "equals misses",
			key:     graph.Key{Name: "stage", Kind: graph.KindText},
			guard:   graph.Guard{Key: "stage", Op: graph.OpEquals, Value: graph.TextValue("review")},
			state:   map[string]graph.Value{"stage": graph.TextValue("lint")},
			routeTo: "no",
		},
		{
			name:    "not-equals matches",
			key:     graph.Key{Name: "stage", Kind: graph.KindText},
			guard:   graph.Guard{Key: "stage", Op: graph.OpNotEquals, Value: graph.TextValue("review")},
			state:   map[string]graph.Value{"stage": graph.TextValue("lint")},
			routeTo: "yes",
		},
		{
			name:    "not-equals misses",
			key:     graph.Key{Name: "stage", Kind: graph.KindText},
			guard:   graph.Guard{Key: "stage", Op: graph.OpNotEquals, Value: graph.TextValue("review")},
			state:   map[string]graph.Value{"stage": graph.TextValue("review")},
			routeTo: "no",
		},
		{
			name:    "less-than matches",
			key:     graph.Key{Name: "rounds", Kind: graph.KindInt},
			guard:   graph.Guard{Key: "rounds", Op: graph.OpLessThan, Value: graph.IntValue(3)},
			state:   map[string]graph.Value{"rounds": graph.IntValue(2)},
			routeTo: "yes",
		},
		{
			name:    "less-than is not less-or-equal",
			key:     graph.Key{Name: "rounds", Kind: graph.KindInt},
			guard:   graph.Guard{Key: "rounds", Op: graph.OpLessThan, Value: graph.IntValue(3)},
			state:   map[string]graph.Value{"rounds": graph.IntValue(3)},
			routeTo: "no",
		},
		{
			name:    "greater-than matches",
			key:     graph.Key{Name: "rounds", Kind: graph.KindInt},
			guard:   graph.Guard{Key: "rounds", Op: graph.OpGreaterThan, Value: graph.IntValue(0)},
			state:   map[string]graph.Value{"rounds": graph.IntValue(1)},
			routeTo: "yes",
		},
		{
			name:    "greater-than misses",
			key:     graph.Key{Name: "rounds", Kind: graph.KindInt},
			guard:   graph.Guard{Key: "rounds", Op: graph.OpGreaterThan, Value: graph.IntValue(0)},
			state:   map[string]graph.Value{"rounds": graph.IntValue(0)},
			routeTo: "no",
		},
		{
			name:    "contains matches",
			key:     graph.Key{Name: "actions", Kind: graph.KindList},
			guard:   graph.Guard{Key: "actions", Op: graph.OpContains, Value: graph.TextValue("ask")},
			state:   map[string]graph.Value{"actions": graph.ListValue("fix", "ask")},
			routeTo: "yes",
		},
		{
			name:    "contains misses",
			key:     graph.Key{Name: "actions", Kind: graph.KindList},
			guard:   graph.Guard{Key: "actions", Op: graph.OpContains, Value: graph.TextValue("ask")},
			state:   map[string]graph.Value{"actions": graph.ListValue("fix", "note")},
			routeTo: "no",
		},
		{
			name:    "an unwritten key is compared as its zero value",
			key:     graph.Key{Name: "rounds", Kind: graph.KindInt},
			guard:   graph.Guard{Key: "rounds", Op: graph.OpEquals, Value: graph.IntValue(0)},
			state:   nil,
			routeTo: "yes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := routedTo(t, tc.key, tc.guard, tc.state); got != tc.routeTo {
				t.Errorf("the run routed to %q, want %q", got, tc.routeTo)
			}
		})
	}
}

// routedTo runs a two-way branch guarded by guard and reports which branch ran.
func routedTo(t *testing.T, key graph.Key, guard graph.Guard, initial map[string]graph.Value) string {
	t.Helper()
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("pick").
		Key(key).
		Node(graph.Node{Name: "pick", NewBody: noteOnly(rec, "pick")}).
		Node(graph.Node{Name: "yes", NewBody: noteOnly(rec, "yes")}).
		Node(graph.Node{Name: "no", NewBody: noteOnly(rec, "no")}).
		Edge(graph.Edge{From: "pick", To: "yes", Guard: &guard}).
		Edge(graph.Edge{From: "pick", To: "no"}))

	exec := mustExecutor(t, g, graph.NewMemoryStore(), 10)
	if _, err := exec.Run(context.Background(), "run", mustState(t, g, initial)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	order := rec.order()
	return order[len(order)-1]
}

func TestARunEndsWhenNoGuardedEdgeMatches(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("pick").
		Key(graph.Key{Name: "go", Kind: graph.KindBool}).
		Node(graph.Node{Name: "pick", NewBody: noteOnly(rec, "pick")}).
		Node(graph.Node{Name: "onward", NewBody: noteOnly(rec, "onward")}).
		Edge(graph.Edge{From: "pick", To: "onward", Guard: &graph.Guard{
			Key: "go", Op: graph.OpEquals, Value: graph.BoolValue(true),
		}}))

	exec := mustExecutor(t, g, graph.NewMemoryStore(), 10)
	got, err := exec.Run(context.Background(), "run", mustState(t, g, nil))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Status != graph.StatusCompleted || got.Position != "" {
		t.Errorf("Status = %s at %q, want a completed run", got.Status, got.Position)
	}
	if rec.count("onward") != 0 {
		t.Error("a node behind a failing guard ran")
	}
}

func TestStatusParkedNamesTheStatusesThatNeedAPerson(t *testing.T) {
	parked := []graph.Status{
		graph.StatusHalted, graph.StatusRoundsExhausted,
		graph.StatusBudgetExhausted, graph.StatusConverged,
	}
	for _, s := range parked {
		if !s.Parked() {
			t.Errorf("%s.Parked() = false", s)
		}
	}
	for _, s := range []graph.Status{graph.StatusRunning, graph.StatusCompleted} {
		if s.Parked() {
			t.Errorf("%s.Parked() = true", s)
		}
	}
}

func TestExecutorExposesTheGraphItWalks(t *testing.T) {
	rec := &recorder{}
	g := mustBuild(t, graph.NewBuilder().
		Start("only").
		Node(graph.Node{Name: "only", NewBody: noteOnly(rec, "only")}))
	exec := mustExecutor(t, g, graph.NewMemoryStore(), 10)
	if exec.Graph() != g {
		t.Error("Graph() returned a different graph")
	}
}

func TestBuildErrorRendersEveryViolation(t *testing.T) {
	rec := &recorder{}
	_, err := graph.NewBuilder().
		Start("check").
		Key(graph.Key{Name: "findings", Kind: graph.KindInt}).
		Node(graph.Node{Name: "check", Writes: []string{"findings"}, NewBody: noteOnly(rec, "check")}).
		Node(graph.Node{Name: "fix", Writes: []string{"findings"}, NewBody: noteOnly(rec, "fix")}).
		Edge(graph.Edge{From: "check", To: "fix"}).
		Edge(graph.Edge{From: "fix", To: "check"}).
		Build()

	be := requireBuildError(t, err, graph.RuleSingleWriter, graph.RuleBoundedCycle)
	message := be.Error()
	for _, want := range []string{"2 construction rules broken", string(graph.RuleSingleWriter), string(graph.RuleBoundedCycle)} {
		if !strings.Contains(message, want) {
			t.Errorf("Error() = %q, want it to mention %q", message, want)
		}
	}
	if lines := strings.Count(message, "\n"); lines != len(be.Violations) {
		t.Errorf("Error() has %d continuation lines for %d violations", lines, len(be.Violations))
	}
}
