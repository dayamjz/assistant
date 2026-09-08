package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/forge"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// TestTheRepositoryAPullRequestWouldLandInComesFromTheRunsOwnRecord is what
// stands between a run and a pull request opened somewhere nobody asked for.
//
// PRD principle P1 makes the push to the gate the consent boundary for the
// pull request that run opens, and nothing else implies that consent. Opening
// a pull request is outward-facing and not undoable, so where it lands may not
// come from a caller's request, from configuration, or from whichever remote a
// working copy happens to carry. It is derived from the upstream URL
// on this run's repository row, which is the record PRD section 8 makes
// authoritative for it, and the derivation is in the one place that has the
// record and cannot be skipped by a call site.
//
// The subject working copy is given a git remote naming a different repository
// on purpose. If anything downstream resolved a repository from the checkout
// rather than from the record, the run would carry that one, and the pull
// request stage would open against it. That is the failure this catches, and
// it is why the two are set to disagree rather than to match.
func TestTheRepositoryAPullRequestWouldLandInComesFromTheRunsOwnRecord(t *testing.T) {
	principles.Cite(t, principles.P1)
	requiresIdentifiedPeer(t)

	const (
		recorded = "https://github.com/dayamjz/assistant.git"
		ambient  = "https://github.com/someone-else/not-this-one.git"
		want     = "dayamjz/assistant"
	)
	seen := observeForgeRepository(t, recorded, ambient)

	if seen != want {
		t.Fatalf("the run acts on %q, want %q: a pull request would be opened against a repository "+
			"the run's own record does not name", seen, want)
	}
}

// TestARunWhoseUpstreamIsNotOnThisHostCarriesNoRepository is the other answer
// the same derivation gives, and the one a run of a repository this build
// cannot address actually reaches.
//
// The specifier is empty rather than guessed at. A remote on another host
// carries an owner and a name too, and reading one out of it would send a pull
// request to the github.com repository that happens to share it, because the
// specifier a provider is given names no host. So the run carries nothing, and
// a provider opened on nothing refuses.
func TestARunWhoseUpstreamIsNotOnThisHostCarriesNoRepository(t *testing.T) {
	requiresIdentifiedPeer(t)

	const elsewhere = "https://gitlab.example.invalid/dayamjz/assistant.git"
	seen := observeForgeRepository(t, elsewhere, elsewhere)

	if seen != "" {
		t.Fatalf("the run acts on %q, want nothing: an upstream on another host is not a repository "+
			"this build can address", seen)
	}
	if _, err := forge.NewGitHubHost(redact.New()).Open(seen); !errors.Is(err, forge.ErrInvalidArgument) {
		t.Fatalf("opening a provider on what that run carries returned %v, want ErrInvalidArgument", err)
	}
}

// observeForgeRepository starts one run of a subject repository whose record
// names upstream and whose checkout's origin remote names remote, and returns
// the code host repository the run's first stage was handed.
//
// It reads the value out of a real run: the stage body is placed through the
// service's own stage hook, so what it sees is what the service put in the
// run's state and not what a helper computed alongside it.
func observeForgeRepository(t *testing.T, upstream, remote string) string {
	t.Helper()

	h := newHome(t)
	subject := newSubject(t)
	git(t, subject, "remote", "add", "origin", remote)
	recordRepositoryWithUpstream(t, h, subject, upstream)

	seen := make(chan string, 1)
	opts := options(t, h)
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Intent = observingStage(seen)
		return all
	}

	var got string
	var read bool
	withServiceOptions(t, opts, func(under serviceUnderTest) {
		startRun(t, under.client, subject)
		select {
		case got, read = <-seen:
		default:
		}
	})
	if !read {
		t.Fatal("the run never reached the stage that reads the repository it acts on")
	}
	return got
}

// observingStage is a stage body that reports the code host repository the run
// carries and reports nothing else, so a run walks straight past it.
func observingStage(seen chan<- string) pipeline.Implementation {
	return pipeline.Implementation{
		Reads: []pipeline.Key{pipeline.KeyForgeRepository},
		NewBody: func() pipeline.Body {
			return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
				value, err := in.State.Get(pipeline.KeyForgeRepository)
				if err != nil {
					return pipeline.Output{}, err
				}
				repository, _ := value.Text()
				select {
				case seen <- repository:
				default:
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "read the repository this run acts on",
				}}, nil
			}
		},
	}
}

// recordRepositoryWithUpstream writes the repository record a run needs,
// naming the upstream the code host repository is derived from.
func recordRepositoryWithUpstream(t *testing.T, h *home.Home, workingPath, upstream string) {
	t.Helper()
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	records, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	defer func() { _ = records.Close() }()
	if _, err := records.UpsertRepository(t.Context(), store.Repository{
		ID:            "subject",
		WorkingPath:   workingPath,
		UpstreamURL:   upstream,
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
}
