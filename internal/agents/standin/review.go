package standin

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/dayamjz/assistant/internal/findings"
)

// A review report is the one answer a script cannot state in full.
//
// internal/findings refuses a review report whose revision is not the commit
// the run asked about, findings and all, and a script is written before the
// run exists: the commit a test would have to name has not been created yet,
// and for a run started by a push it is created by the test itself after the
// stand-in was built. So Review states everything but the revision and the
// stand-in completes it from the invocation, which is what a reviewer does -
// the prompt is the only place either of them is told which commit to report.
//
// Where the revision sits in the prompt is derived from
// findings.Demand.Guidance rather than spelled here. That function is the one
// owner of the text a reviewer is held to, and a literal anchor in this file
// would be a second statement of it that drifts silently: a reworded demand
// would leave this reading nothing while the demand still asked for a
// revision, and every review a script answered would be refused for naming the
// wrong commit. Deriving it means such a rewording fails here instead, naming
// what it could not find.

// revisionSentinel stands where a revision does in the guidance this reads its
// anchors out of. It is not a commit and could not be one, so a guidance that
// echoed it back somewhere else would be visible rather than matching a real
// revision by accident.
const revisionSentinel = "standin-revision-sentinel"

// sentinelPath is the one path the sampled demand declares touched. A demand
// naming none is refused by findings.Demand.Validate, and what it names does
// not reach either anchor.
const sentinelPath = "internal/standin/sentinel.go"

// errNoDemand reports that a prompt carries no evidence demand this can read a
// revision out of, so a review reply has no commit to report.
var errNoDemand = errors.New("this invocation's prompt carries no evidence demand naming a revision")

// demandAnchors is the text findings.Demand.Guidance writes immediately before
// and after the revision it demands, computed once per process.
var demandAnchors = sync.OnceValues(func() (anchors, error) {
	guidance, err := (findings.Demand{Revision: revisionSentinel, Touched: []string{sentinelPath}}).Guidance()
	if err != nil {
		return anchors{}, fmt.Errorf("building the evidence demand this reads its anchors out of: %w", err)
	}
	before, after, found := strings.Cut(guidance, revisionSentinel)
	if !found {
		return anchors{}, errors.New("the evidence demand no longer writes the revision it asks for, " +
			"so there is nothing here to read a review's revision out of")
	}
	lead := before[strings.LastIndex(before, "\n")+1:]
	trail, _, _ := strings.Cut(after, "\n")
	if strings.TrimSpace(lead) == "" || strings.TrimSpace(trail) == "" {
		return anchors{}, fmt.Errorf("the evidence demand writes the revision with nothing either side of "+
			"it on its line (lead %q, trail %q), so an anchor on it would match anywhere", lead, trail)
	}
	return anchors{lead: lead, trail: trail}, nil
})

// anchors is what surrounds the revision on the demand's own line.
type anchors struct {
	// lead is the rest of that line before the revision, and trail is the rest
	// of it after. Both are whole enough to be found once: the demand writes
	// that sentence nowhere else.
	lead, trail string
}

// revisionAskedAbout is the commit the prompt's evidence demand names.
//
// It reads the first demand in the prompt. The review stage assembles one
// demand per prompt, so there is one to find; a prompt with none is refused
// rather than answered with something this guessed at, because a review reply
// to an invocation that asked for no review is a shape no script described.
func revisionAskedAbout(prompt string) (string, error) {
	around, err := demandAnchors()
	if err != nil {
		return "", err
	}
	at := strings.Index(prompt, around.lead)
	if at < 0 {
		return "", fmt.Errorf("%w: it does not carry %q", errNoDemand, around.lead)
	}
	rest := prompt[at+len(around.lead):]
	end := strings.Index(rest, around.trail)
	if end < 0 {
		return "", fmt.Errorf("%w: it asks for a revision and the sentence is not finished with %q",
			errNoDemand, around.trail)
	}
	revision := strings.TrimSpace(rest[:end])
	if revision == "" {
		return "", fmt.Errorf("%w: it asks for a revision and names none", errNoDemand)
	}
	return revision, nil
}

// reviewResult is the bytes a review reply prints: the report as the caller
// wrote it, with the revision the invocation asked about filled in.
func reviewResult(report findings.Report, prompt string) (string, error) {
	revision, err := revisionAskedAbout(prompt)
	if err != nil {
		return "", err
	}
	report.Revision = revision
	return Report(report).Envelope.Result, nil
}
