package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Every posture states what it requires, what it publishes and who lands
// it, each from its closed set, and the policy table is the one list: the
// decoded set, the strictness order and the statements are all read off
// it, so no posture is selectable without a statement and no statement
// names a posture that cannot be selected.
func TestEveryPostureStatesValidationPublicationAndLanding(t *testing.T) {
	validations := []Validation{ValidationFullRun, ValidationNone}
	publications := []Publication{PublishBranchAndPullRequest, PublishNothing}
	authorities := []Authority{AuthorityCodeHost, AuthorityApproval, AuthorityRemote}
	rows := Policies()
	if len(rows) == 0 {
		t.Fatal("the policy table is empty, so nothing is held to anything")
	}
	if got := Deliveries(); len(got) != len(rows) {
		t.Fatalf("Deliveries() = %v and the table has %d rows", got, len(rows))
	}
	for i, p := range rows {
		if !slices.Contains(validations, p.Validation) || !slices.Contains(publications, p.Publishes) ||
			!slices.Contains(authorities, p.Lands) {
			t.Errorf("row %q states something outside the closed sets: %+v", p.Delivery, p)
		}
		if Deliveries()[i] != p.Delivery {
			t.Errorf("Deliveries()[%d] = %q, the table's row is %q", i, Deliveries()[i], p.Delivery)
		}
		got, ok := p.Delivery.Policy()
		if !ok || got != p {
			t.Errorf("Policy(%q) = %+v, %v; want the table's row", p.Delivery, got, ok)
		}
		if _, err := ParseDelivery(string(p.Delivery)); err != nil {
			t.Errorf("the table states %q and the key refuses it: %v", p.Delivery, err)
		}
	}
	for _, w := range withheldDeliveries {
		if _, ok := w.Delivery.Policy(); ok || w.Delivery.Recognized() {
			t.Errorf("withheld %q is selectable", w.Delivery)
		}
		if _, err := ParseDelivery(string(w.Delivery)); err == nil || !strings.Contains(err.Error(), w.Reason) {
			t.Errorf("withheld %q decoded as %v, want a refusal giving %q", w.Delivery, err, w.Reason)
		}
	}
	if _, ok := Delivery("fast").Policy(); ok {
		t.Error("a value outside the set has a policy")
	}
}

// The default posture is the validated one: the first row, the default the
// key's row carries, and all nine stages before a pull request whose merge
// is the code host's. A posture lighter than it validates less.
func TestTheDefaultPostureIsTheFullRun(t *testing.T) {
	first := Policies()[0]
	if first.Delivery != DefaultDelivery || Defaults().Delivery != DefaultDelivery {
		t.Fatalf("the table's first row is %q, the default is %q and Defaults holds %q",
			first.Delivery, DefaultDelivery, Defaults().Delivery)
	}
	want := Policy{DeliveryGated, ValidationFullRun, PublishBranchAndPullRequest, AuthorityCodeHost}
	if first != want {
		t.Fatalf("the default posture states %+v, want %+v", first, want)
	}
	for _, p := range Policies()[1:] {
		if !p.Delivery.Lighter(DefaultDelivery) {
			t.Errorf("%q is after the default in the table and not lighter than it", p.Delivery)
		}
	}
}

// No selectable posture shares unvalidated work or lands it without an
// approve answer, which is the line PRD section 12 draws around the direct
// mode. The rule is run over the table and over counterfeit rows shaped
// like a direct mode, so a check that stopped looking would fail on them.
func TestNoPostureSharesOrLandsUnvalidatedWorkUnasked(t *testing.T) {
	for _, p := range Policies() {
		if err := p.check(); err != nil {
			t.Errorf("posture %q: %v", p.Delivery, err)
		}
	}
	counterfeits := []struct {
		row  Policy
		want error
	}{
		{Policy{"direct", ValidationNone, PublishBranchAndPullRequest, AuthorityCodeHost}, errSharesUnvalidated},
		{Policy{"direct", ValidationNone, PublishBranchAndPullRequest, AuthorityApproval}, errSharesUnvalidated},
		{Policy{"unasked", ValidationNone, PublishNothing, AuthorityCodeHost}, errLandsUnasked},
	}
	for _, c := range counterfeits {
		if err := c.row.check(); !errors.Is(err, c.want) {
			t.Errorf("counterfeit %+v came to %v, want %v", c.row, err, c.want)
		}
	}
	if err := (Policy{"gated-like", ValidationFullRun, PublishBranchAndPullRequest, AuthorityCodeHost}).check(); err != nil {
		t.Errorf("a validated posture that publishes was refused: %v", err)
	}
}

// Gated on a repository with no code host lands on the person at the remote,
// not on the code host's merge policy, which is what a run ending pushed
// states and what task answers must carry.
func TestGatedPolicyForNoCodeHostLandsOnTheRemote(t *testing.T) {
	got, ok := DeliveryGated.PolicyForNoCodeHost(true)
	if !ok {
		t.Fatal("gated has no policy")
	}
	want := Policy{DeliveryGated, ValidationFullRun, PublishBranchAndPullRequest, AuthorityRemote}
	if got != want {
		t.Fatalf("PolicyForNoCodeHost(true) = %+v, want %+v", got, want)
	}
	withHost, ok := DeliveryGated.PolicyForNoCodeHost(false)
	if !ok || withHost.Lands != AuthorityCodeHost {
		t.Fatalf("PolicyForNoCodeHost(false) = %+v, want code-host lands", withHost)
	}
}
