package forge

import (
	"context"
	"strconv"
)

// PullRequestState is where a pull request stands on the provider.
//
// The zero value is PullRequestStateUnknown, so a value nobody filled in
// reports that nothing is known rather than that the pull request is open.
type PullRequestState uint8

const (
	// PullRequestStateUnknown is the provider did not say, or said something
	// this package does not recognize. It is the zero value.
	PullRequestStateUnknown PullRequestState = iota
	// PullRequestStateOpen is the pull request is open.
	PullRequestStateOpen
	// PullRequestStateMerged is the pull request was merged.
	PullRequestStateMerged
	// PullRequestStateClosed is the pull request was closed without being
	// merged.
	PullRequestStateClosed
)

// String returns the state's name, which is what appears in a report.
func (s PullRequestState) String() string {
	switch s {
	case PullRequestStateOpen:
		return "open"
	case PullRequestStateMerged:
		return "merged"
	case PullRequestStateClosed:
		return "closed"
	case PullRequestStateUnknown:
		return "unknown"
	default:
		return "pull-request-state(" + strconv.Itoa(int(s)) + ")"
	}
}

// Mergeability is whether the provider can merge a pull request into its base,
// judged on the content of the two branches alone. It is a separate answer
// from the checks on the head, and this package never blends the two: a
// provider summary that folds check status into mergeability is not what the
// adapters read.
//
// The zero value is MergeabilityUnknown, so a value nobody filled in does not
// read as mergeable.
type Mergeability uint8

const (
	// MergeabilityUnknown is the provider has not answered. On a provider that
	// computes this asynchronously it is the ordinary answer for a pull
	// request that was just opened or just updated, so a caller re-reads
	// rather than concluding anything.
	MergeabilityUnknown Mergeability = iota
	// MergeabilityMergeable is the provider reports the branches merge.
	MergeabilityMergeable
	// MergeabilityConflicted is the provider reports the branches conflict, so
	// the merge needs a person or a rebase.
	MergeabilityConflicted
)

// String returns the value's name, which is what appears in a report.
func (m Mergeability) String() string {
	switch m {
	case MergeabilityMergeable:
		return "mergeable"
	case MergeabilityConflicted:
		return "conflicted"
	case MergeabilityUnknown:
		return "unknown"
	default:
		return "mergeability(" + strconv.Itoa(int(m)) + ")"
	}
}

// PullRequest is one pull request as this package models it. Every field is
// either a plain fact about the change or a typed value declared here, except
// URL, which names the host it lives on and is there for a person.
type PullRequest struct {
	// Number identifies the pull request within its repository. It is what
	// every later call addresses it by.
	Number int
	// URL is where a person looks at it.
	URL string
	// Title is its title.
	Title string
	// State is where it stands.
	State PullRequestState
	// Mergeability is whether the provider can merge it, which is a separate
	// question from whether its checks passed.
	Mergeability Mergeability
	// Head is the branch being merged.
	Head string
	// HeadCommit is the commit the head branch stood at when the provider
	// answered. It is what the checks on this pull request were run against,
	// so a caller comparing it with ChecksReport.HeadCommit can tell a stale
	// check list from a current one.
	HeadCommit string
	// Base is the branch being merged into.
	Base string
	// Draft reports whether the pull request is a draft.
	Draft bool
}

// OpenSpec describes a pull request to open.
type OpenSpec struct {
	// Head is the branch to merge. Required.
	Head string
	// Base is the branch to merge into. Required, because letting the provider
	// pick a default would make the target of the change depend on a setting
	// this run never read.
	Base string
	// Title is the pull request title. Required.
	Title string
	// Body is the pull request body, written for a reviewer who was not
	// present. It may be empty and it may be long; it does not travel on a
	// command line.
	Body string
	// Draft opens the pull request as a draft.
	Draft bool
}

// Provider is a code host. It is the whole of what the rest of this product
// may depend on about one, and PRD section 8 fixes it at one interface with
// one adapter per provider.
//
// Every method returns a *Refusal on failure and never reports a failure as an
// empty answer. In particular a provider that could not be run, or that
// reported this caller is not authenticated, is a refusal naming which, not a
// pull request that does not exist and not a check list with nothing in it.
//
// An implementation must be safe for concurrent use.
type Provider interface {
	// Find returns the open pull request whose head is the named branch, and
	// reports whether there was one. A branch with no open pull request is a
	// false second result and a nil error, because that is an answer rather
	// than a failure. A branch carrying more than one open pull request is a
	// refusal with ReasonAmbiguous rather than a choice made here.
	//
	// A merged or closed pull request for the same branch is not found. The
	// pull request stage opens a new one in that case, which is what a caller
	// wants: the old one is finished.
	Find(ctx context.Context, head string) (PullRequest, bool, error)

	// Open opens a pull request and returns it as the provider then reports
	// it. It does not check whether one already exists for the head; Submit
	// is the operation that does.
	Open(ctx context.Context, spec OpenSpec) (PullRequest, error)

	// UpdateBody replaces the body of the pull request with the given number
	// and returns it as the provider then reports it. Nothing else about the
	// pull request is changed.
	UpdateBody(ctx context.Context, number int, body string) (PullRequest, error)

	// Get returns where the pull request with the given number stands and
	// whether the provider can merge it.
	Get(ctx context.Context, number int) (PullRequest, error)

	// Checks returns the checks registered on the pull request's head, and the
	// commit they were read against. It reports what the provider says now and
	// waits for nothing.
	//
	// A report returned without an error names that commit. An implementation
	// whose provider answered without one refuses with ReasonMalformed rather
	// than returning a report nobody can place on a head.
	//
	// An empty check list is an answer, not a failure: it means no check is
	// registered. What that means for the run is ChecksReport.Evaluate's
	// question, and the answer is not "green".
	Checks(ctx context.Context, number int) (ChecksReport, error)
}

// Submit is PRD section 5's pull request stage: it creates the pull request
// for a head, or updates the body of the one that is already there. It never
// opens a second pull request for a head that already has one, so a stage that
// runs twice produces one pull request.
//
// A pull request that already exists keeps its title, its base, and its draft
// state. Those are things a person may have changed on purpose after the run
// opened it, and the stage's business is the body: what it says is the pull
// request stage's to decide, and this function writes the text it was handed,
// every time.
func Submit(ctx context.Context, p Provider, spec OpenSpec) (PullRequest, error) {
	existing, found, err := p.Find(ctx, spec.Head)
	if err != nil {
		return PullRequest{}, err
	}
	if !found {
		return p.Open(ctx, spec)
	}
	return p.UpdateBody(ctx, existing.Number, spec.Body)
}
