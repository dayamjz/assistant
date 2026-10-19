package knowledge_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

func TestAnAssertionIsNotARuleUnlessAnOperatorRecordAdoptsIt(t *testing.T) {
	principles.Cite(t, principles.P8)
	rows := []store.Knowledge{
		{ID: 1, Author: store.AuthoredByTask(), Body: "the venue allows a later curfew"},
		{ID: 2, Author: store.AuthoredByOperator(), Body: "that assertion holds", Adopts: store.Known[int64](1)},
		{ID: 3, Author: store.AuthoredByTask(), Body: "and the deposit is optional"},
	}
	got := knowledge.Rules(rows)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("rules are %+v, want the adopted assertion and the operator row, not the newest assertion", got)
	}
	// A task row that carries an adoption still does not make another row a rule.
	forged := []store.Knowledge{
		{ID: 1, Author: store.AuthoredByTask(), Body: "assertion", Adopts: store.Known[int64](2)},
		{ID: 2, Author: store.AuthoredByTask(), Body: "other"},
	}
	if rules := knowledge.Rules(forged); len(rules) != 0 {
		t.Fatalf("a task row's adoption produced %+v", rules)
	}
}

func TestARepositoryFileCannotReplaceTheTrustedDocument(t *testing.T) {
	principles.Cite(t, principles.P7)
	const trusted = "operator policy: do not merge without the gate\n"
	const repo = "{\n  \"no_ci\": true,\n  \"allow_standing_consent\": true\n}\n"
	got, err := knowledge.Compose(knowledge.Material{
		Trusted:        trusted,
		RepositoryFile: repo,
		Knowledge: []store.Knowledge{
			{ID: 1, Author: store.AuthoredByTask(), Body: "agent says no_ci is fine"},
			{ID: 2, Author: store.AuthoredByOperator(), Body: "keep the gate"},
		},
		Budget: knowledge.DefaultBudget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Text, trusted) {
		t.Fatalf("the prompt does not start with the trusted document:\n%s", got.Text)
	}
	if strings.Contains(got.Text, "no_ci") || strings.Contains(got.Text, "allow_standing_consent") || strings.Contains(got.Text, "agent says") {
		t.Fatalf("the repository file or the unadopted assertion replaced or entered the prompt:\n%s", got.Text)
	}
	if !strings.Contains(got.Text, "keep the gate") {
		t.Fatalf("the operator rule is missing:\n%s", got.Text)
	}
	trustedAt := strings.Index(got.Text, "operator policy")
	generatedAt := strings.Index(got.Text, "keep the gate")
	if trustedAt < 0 || generatedAt < trustedAt {
		t.Fatalf("generated text is not after the trusted document:\n%s", got.Text)
	}
	if _, err := knowledge.Compose(knowledge.Material{
		Trusted: repo, RepositoryFile: repo, Budget: knowledge.DefaultBudget,
	}); !errors.Is(err, knowledge.ErrRepositoryOverrides) {
		t.Fatalf("using the repository file as the trusted document was %v", err)
	}
	absent, err := knowledge.Compose(knowledge.Material{
		RepositoryFile: repo,
		Knowledge:      []store.Knowledge{{ID: 2, Author: store.AuthoredByOperator(), Body: "keep the gate"}},
		Budget:         knowledge.DefaultBudget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(absent.Text, "no_ci") || strings.HasPrefix(absent.Text, repo) {
		t.Fatalf("an absent operator document was filled from the repository file:\n%s", absent.Text)
	}
}

func TestCompactionDropsOldestKnowledgeAndKeepsPins(t *testing.T) {
	pins := []knowledge.Pin{
		{Kind: knowledge.PinHold, ID: "venue-deposit", Text: "decide the venue deposit"},
		{Kind: knowledge.PinTask, ID: "abcdefghijkl", Text: "dispatched"},
	}
	rules := []store.Knowledge{
		{ID: 1, Author: store.AuthoredByOperator(), Body: strings.Repeat("O", 80) + "OLDEST"},
		{ID: 2, Author: store.AuthoredByOperator(), Body: strings.Repeat("M", 80) + "MIDDLE"},
		{ID: 3, Author: store.AuthoredByOperator(), Body: strings.Repeat("N", 80) + "NEWEST"},
	}
	zero, err := knowledge.Compose(knowledge.Material{
		Trusted: "policy\n", Knowledge: rules, Pinned: pins, Budget: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(zero.Text, "policy\n") || !strings.Contains(zero.Text, "hold venue-deposit") || !strings.Contains(zero.Text, "task abcdefghijkl") {
		t.Fatalf("a zero budget dropped a pin or the trusted prefix:\n%s", zero.Text)
	}
	if strings.Contains(zero.Text, "OLDEST") || strings.Contains(zero.Text, "NEWEST") {
		t.Fatalf("a zero budget kept a rule:\n%s", zero.Text)
	}
	if len(zero.Dropped) != 3 || zero.Dropped[0] != 1 || zero.Dropped[2] != 3 {
		t.Fatalf("dropped %v, want the rules oldest first", zero.Dropped)
	}

	var sawNewestWithoutOldest bool
	for budget := 1; budget < 4000; budget += 5 {
		got, err := knowledge.Compose(knowledge.Material{
			Trusted: "policy\n", Knowledge: rules, Pinned: pins, Budget: budget,
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Text, "hold venue-deposit") || !strings.Contains(got.Text, "decide the venue deposit") {
			t.Fatalf("budget %d dropped a pin:\n%s", budget, got.Text)
		}
		hasOld := strings.Contains(got.Text, "OLDEST")
		hasNew := strings.Contains(got.Text, "NEWEST")
		if hasOld && !hasNew {
			t.Fatalf("budget %d kept the oldest rule and dropped the newest:\n%s", budget, got.Text)
		}
		if hasNew && !hasOld {
			sawNewestWithoutOldest = true
			if len(got.Dropped) == 0 || got.Dropped[0] != 1 {
				t.Fatalf("budget %d dropped %v, want the oldest first", budget, got.Dropped)
			}
		}
	}
	if !sawNewestWithoutOldest {
		t.Fatal("no budget kept the newest rule while dropping the oldest")
	}
}

func TestKnowledgeIsNotPipelineState(t *testing.T) {
	for _, key := range pipeline.Keys() {
		if strings.Contains(strings.ToLower(string(key)), "knowledge") {
			t.Fatalf("pipeline key %q is knowledge, which would put it in the convergence fingerprint", key)
		}
	}
}

func TestComposeRefusesAPinItCannotKeep(t *testing.T) {
	if _, err := knowledge.Compose(knowledge.Material{
		Trusted: "policy\n",
		Pinned:  []knowledge.Pin{{Kind: "note", ID: "x", Text: "y"}},
		Budget:  knowledge.DefaultBudget,
	}); !errors.Is(err, knowledge.ErrPin) {
		t.Fatalf("an unknown pin kind was %v", err)
	}
	if _, err := knowledge.Compose(knowledge.Material{Budget: -1}); !errors.Is(err, knowledge.ErrBudget) {
		t.Fatalf("a negative budget was %v", err)
	}
}
