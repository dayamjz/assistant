package findings_test

import (
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
)

func ids(fs []findings.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.ID
	}
	return out
}

func sample() []findings.Finding {
	return []findings.Finding{
		{Severity: findings.SeverityError, Action: findings.ActionFix,
			Location:    findings.Location{Path: "internal/graph/node.go", Line: 12},
			Description: "the write is never checked"},
		{Severity: findings.SeverityInfo, Action: findings.ActionNote,
			Description: "the package doc reads well"},
	}
}

func TestNormalizeFindingsAssignsIdentifiersDeterministically(t *testing.T) {
	first := findings.NormalizeFindings(sample())
	second := findings.NormalizeFindings(sample())
	for i := range first {
		if first[i].ID == "" {
			t.Fatalf("finding %d was left without an identifier", i)
		}
		if first[i].ID != second[i].ID {
			t.Errorf("identifier for finding %d differs between runs: %q then %q",
				i, first[i].ID, second[i].ID)
		}
	}
}

// The identifier must come from the finding, not from where it sits, so a
// finding keeps it when the set around it changes.
func TestDerivedIdentifiersDoNotDependOnPosition(t *testing.T) {
	base := findings.NormalizeFindings(sample())
	extra := findings.Finding{Severity: findings.SeverityWarning, Action: findings.ActionAsk,
		Description: "was this removal deliberate?"}

	withPrefix := findings.NormalizeFindings(append([]findings.Finding{extra}, sample()...))
	if withPrefix[1].ID != base[0].ID || withPrefix[2].ID != base[1].ID {
		t.Errorf("identifiers moved when a finding was inserted ahead: %v then %v",
			ids(base), ids(withPrefix))
	}

	reversed := findings.NormalizeFindings([]findings.Finding{sample()[1], sample()[0]})
	if reversed[0].ID != base[1].ID || reversed[1].ID != base[0].ID {
		t.Errorf("identifiers changed when the set was reordered: %v then %v",
			ids(base), ids(reversed))
	}
}

func TestNormalizeFindingsNeverRewritesASuppliedIdentifier(t *testing.T) {
	in := sample()
	in[0].ID = "review-1"
	in[1].ID = "  review-2  "
	got := findings.NormalizeFindings(in)
	if got[0].ID != "review-1" {
		t.Errorf("supplied identifier became %q", got[0].ID)
	}
	if got[1].ID != "review-2" {
		t.Errorf("supplied identifier was not trimmed to %q, got %q", "review-2", got[1].ID)
	}
}

// Two findings identical in every field still need to be selectable apart.
func TestIdenticalFindingsGetDistinctIdentifiers(t *testing.T) {
	one := findings.Finding{Severity: findings.SeverityError, Action: findings.ActionFix,
		Location: findings.Location{Path: "a.go", Line: 1}, Description: "same"}
	got := findings.NormalizeFindings([]findings.Finding{one, one, one})
	seen := map[string]bool{}
	for i, f := range got {
		if seen[f.ID] {
			t.Fatalf("finding %d repeated identifier %q: %v", i, f.ID, ids(got))
		}
		seen[f.ID] = true
	}
	again := findings.NormalizeFindings([]findings.Finding{one, one, one})
	for i := range got {
		if got[i].ID != again[i].ID {
			t.Errorf("identifiers for identical findings are not stable: %v then %v",
				ids(got), ids(again))
		}
	}
}

// A derived identifier must not land on one another finding already carries,
// including a finding later in the set.
func TestDerivedIdentifiersAvoidSuppliedOnes(t *testing.T) {
	one := findings.Finding{Severity: findings.SeverityError, Action: findings.ActionFix,
		Location: findings.Location{Path: "a.go", Line: 1}, Description: "same"}
	derived := findings.NormalizeFindings([]findings.Finding{one})[0].ID

	clashing := one
	clashing.ID = derived
	// The finding needing an identifier comes first, so avoiding the taken one
	// cannot be an accident of iteration order.
	got := findings.NormalizeFindings([]findings.Finding{one, clashing})
	if got[0].ID == got[1].ID {
		t.Fatalf("derived identifier %q collided with the supplied one", got[0].ID)
	}
	if got[1].ID != derived {
		t.Fatalf("the supplied identifier changed to %q", got[1].ID)
	}
	if err := (findings.Report{Summary: "s", Findings: got}).Validate(); err != nil {
		t.Fatalf("the normalized set does not validate: %v", err)
	}
}

// The identifier is derived after normalization, so two findings that differ
// only in ways normalization erases are the same finding.
func TestIdentifiersAreDerivedFromNormalizedFields(t *testing.T) {
	unclassified := findings.Finding{Severity: "critical", Action: "whatever",
		Description: "  spacing  "}
	explicit := findings.Finding{Severity: findings.SeverityWarning, Action: findings.ActionAsk,
		Description: "spacing"}
	a := findings.NormalizeFindings([]findings.Finding{unclassified})[0]
	b := findings.NormalizeFindings([]findings.Finding{explicit})[0]
	if a.ID != b.ID {
		t.Errorf("normalization did not converge: %q and %q", a.ID, b.ID)
	}
	if a.Action != findings.ActionAsk || a.Severity != findings.SeverityWarning {
		t.Errorf("normalized finding = %+v, want ask and warning", a)
	}
}

// The digest must not be able to confuse a field boundary: moving text from
// one field to the next has to change the identifier.
func TestIdentifierSeparatesAdjacentFields(t *testing.T) {
	a := findings.NormalizeFindings([]findings.Finding{{Action: findings.ActionFix,
		Location: findings.Location{Path: "ab"}, Description: "c"}})[0]
	b := findings.NormalizeFindings([]findings.Finding{{Action: findings.ActionFix,
		Location: findings.Location{Path: "a"}, Description: "bc"}})[0]
	if a.ID == b.ID {
		t.Fatalf("two different findings share identifier %q", a.ID)
	}
}

func TestNormalizeFindingsDoesNotModifyItsInput(t *testing.T) {
	in := sample()
	in[0].Action = "whatever"
	got := findings.NormalizeFindings(in)
	if in[0].Action != "whatever" || in[0].ID != "" {
		t.Fatalf("NormalizeFindings modified its input: %+v", in[0])
	}
	if got[0].Action != findings.ActionAsk || got[0].ID == "" {
		t.Fatalf("NormalizeFindings did not normalize the copy: %+v", got[0])
	}
}

func TestNormalizeFindingsPreservesNil(t *testing.T) {
	if got := findings.NormalizeFindings(nil); got != nil {
		t.Errorf("NormalizeFindings(nil) = %+v, want nil", got)
	}
}

// The identifier search is bounded, so a set larger than that bound still gets
// identifiers and still terminates. Sixty-five findings that are identical in
// every field force the last one past the search entirely and onto the
// suffixed fallback the package documents; sixty-six force two onto it, and
// because the fallback does not check what it returns, both get the same
// identifier and Validate is what refuses the set.
func TestIdentifierAssignmentTerminatesPastItsSearchBound(t *testing.T) {
	one := findings.Finding{Severity: findings.SeverityError, Action: findings.ActionFix,
		Location: findings.Location{Path: "a.go", Line: 1}, Description: "same"}
	identical := func(n int) []findings.Finding {
		in := make([]findings.Finding, n)
		for i := range in {
			in[i] = one
		}
		return in
	}
	assigned := func(t *testing.T, in []findings.Finding) []findings.Finding {
		t.Helper()
		got := findings.NormalizeFindings(in)
		for i, f := range got {
			if f.ID == "" {
				t.Fatalf("finding %d has no identifier", i)
			}
		}
		again := findings.NormalizeFindings(in)
		for i := range got {
			if got[i].ID != again[i].ID {
				t.Fatalf("identifier %d is not stable: %q then %q", i, got[i].ID, again[i].ID)
			}
		}
		return got
	}

	t.Run("one finding on the fallback", func(t *testing.T) {
		got := assigned(t, identical(65))
		seen := map[string]bool{}
		for i, f := range got {
			if seen[f.ID] {
				t.Fatalf("finding %d repeated identifier %q", i, f.ID)
			}
			seen[f.ID] = true
		}
		if err := (findings.Report{Summary: "s", Findings: got}).Validate(); err != nil {
			t.Fatalf("the normalized set does not validate: %v", err)
		}
	})

	t.Run("two findings on the fallback", func(t *testing.T) {
		got := assigned(t, identical(66))
		if got[64].ID != got[65].ID {
			t.Fatalf("identifiers %q and %q differ, so the documented fallback collision is gone; "+
				"the refusal below no longer pins it", got[64].ID, got[65].ID)
		}
		err := (findings.Report{Summary: "s", Findings: got}).Validate()
		var verr *findings.ValidationError
		if !errors.As(err, &verr) {
			t.Fatalf("Validate returned %v, want a *ValidationError", err)
		}
		if !verr.HasDefect(findings.DefectDuplicateID) {
			t.Fatalf("Validate reported %v, want the duplicate identifier", verr.Flaws)
		}
	})
}

// The one case where position is an input, pinned so the documented exception
// cannot quietly become something else. Findings identical in every hashed
// field are told apart by how many identical ones precede them, so inserting
// another one ahead does move the later one's identifier.
func TestIdenticalFindingsAreSeparatedByOrder(t *testing.T) {
	one := findings.Finding{Severity: findings.SeverityError, Action: findings.ActionFix,
		Location: findings.Location{Path: "a.go", Line: 1}, Description: "same"}
	alone := findings.NormalizeFindings([]findings.Finding{one})
	pair := findings.NormalizeFindings([]findings.Finding{one, one})

	if pair[0].ID != alone[0].ID {
		t.Errorf("the first identical finding changed identifier: %q then %q", alone[0].ID, pair[0].ID)
	}
	if pair[1].ID == alone[0].ID {
		t.Error("the second identical finding took the first one's identifier")
	}

	// A finding that differs in any hashed field is not identical, so it is
	// unaffected by what precedes it.
	other := one
	other.Description = "different"
	otherAlone := findings.NormalizeFindings([]findings.Finding{other})
	mixed := findings.NormalizeFindings([]findings.Finding{one, other})
	if mixed[1].ID != otherAlone[0].ID {
		t.Errorf("a distinct finding moved with its position: %q then %q",
			otherAlone[0].ID, mixed[1].ID)
	}
}

// TestWhatAFindingCitesIsPartOfItsIdentity checks that citations are among the
// fields the digest covers rather than among the ones it ignores. Two findings
// alike in every other field are told apart by content, so neither depends on
// the other being in the set: the same finding derives the same identifier
// alone and beside its near-twin.
//
// Asserting only that the two differ would prove nothing, since the occurrence
// number separates findings the digest cannot tell apart and would produce two
// identifiers either way.
func TestWhatAFindingCitesIsPartOfItsIdentity(t *testing.T) {
	cites := func(path string) findings.Finding {
		return findings.Finding{
			Severity:    findings.SeverityError,
			Action:      findings.ActionFix,
			Location:    findings.Location{Path: "internal/total/total.go", Line: 8},
			Cites:       []string{path},
			Description: "this breaks its caller",
		}
	}
	alone := findings.NormalizeFindings([]findings.Finding{cites("a/caller.go")})
	beside := findings.NormalizeFindings([]findings.Finding{
		cites("b/other.go"), cites("a/caller.go"),
	})
	if alone[0].ID != beside[1].ID {
		t.Errorf("a finding citing %q derives %q alone and %q beside one citing %q; "+
			"the citation is not in the digest, so the two are separated by position instead",
			"a/caller.go", alone[0].ID, beside[1].ID, "b/other.go")
	}
	if beside[0].ID == beside[1].ID {
		t.Errorf("both findings derived %q", beside[0].ID)
	}
}
