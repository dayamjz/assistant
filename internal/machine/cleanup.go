package machine

import (
	"strings"

	"github.com/dayamjz/assistant/internal/store"
)

// ProofFinding is what one of PRD principle P12's checks found about a local
// copy's work when it did not hold: which check, and what it found, in the
// sentence internal/tasks wrote for it with the commits it read as evidence.
type ProofFinding struct {
	// Proof is the check.
	Proof store.CleanupProof
	// Found is what it found, as the cleanup decision stated it.
	Found string
}

// proofQuestion names each of P12's checks as the question it asks, in a
// person's words, keyed by the proof the cleanup record would carry.
var proofQuestion = map[store.CleanupProof]string{
	store.CleanupProofRemoteBranch:      "whether the work is on the branch kept for it where it was sent",
	store.CleanupProofMergedPullRequest: "whether the work is in the pull request that was merged",
	store.CleanupProofDefaultBranch:     "whether the work is already in the shared branch",
}

// proofQuestionOf is the question a check asks, and a placeholder for a
// check this table has no row for, so a finding is never printed without
// saying it is one.
func proofQuestionOf(p store.CleanupProof) string {
	if q, ok := proofQuestion[p]; ok {
		return q
	}
	return "one of the checks"
}

// foundPlaceholder fills a finding into the table's own copy of the row.
const foundPlaceholder = "what it found"

// CopyUnprovenPhrase is what a person reads about a task's local copy the
// cleanup decision kept because none of P12's checks held: every check named
// by the question it asks, each followed by what it found, so the line says
// which check said what rather than only that none held. What it claims is
// what the decision does: nothing was removed, and only a check that holds,
// or the person, removes the copy.
func CopyUnprovenPhrase(found []ProofFinding) Phrase {
	parts := make([]string, 0, len(found))
	for _, f := range found {
		text := strings.TrimSpace(f.Found)
		if text == "" {
			text = "nothing it could state"
		}
		parts = append(parts, proofQuestionOf(f.Proof)+" was not shown: "+text)
	}
	outcome := "This work's local copy was kept, because nothing showed the work is safely somewhere else."
	if len(parts) > 0 {
		outcome = "This work's local copy was kept, because nothing showed the work is safely somewhere else; " +
			strings.Join(parts, "; ") + "."
	}
	return Phrase{
		Outcome:     outcome,
		Consequence: "Nothing in the copy was removed, and it stays until one of those is shown or you remove it yourself.",
		Decision:    "If the work is already in, compare each finding with where it went before removing anything; otherwise nothing to do.",
	}
}

// cleanupRows is this file's rows for TablePhrases: the row with each check
// it can name, filled with the placeholder, and the row for a refusal that
// carried no finding.
func cleanupRows() []Phrase {
	var found []ProofFinding
	for _, p := range store.CleanupProofs() {
		if _, ok := proofQuestion[p]; ok {
			found = append(found, ProofFinding{Proof: p, Found: foundPlaceholder})
		}
	}
	return []Phrase{CopyUnprovenPhrase(found), CopyUnprovenPhrase(nil)}
}
