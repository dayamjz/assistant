package service_test

import (
	"context"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/stages"
)

// TestAStartedRunCarriesTheRecordsOwnIdentifiers binds the run's record to the
// two run-scoped keys a stage body locates its isolated copy from.
//
// Service.begin is the one production site that fills them in, and until this
// nothing read them back. internal/stages establishes the other half - that
// Copy derives the path from whatever those keys hold - but it does so from a
// pipeline.Start it builds itself, so the assignment here was never on any
// test's path. Swapping the two lines compiles, passes go vet, and would put
// every run's copy at worktrees/<run>/<repository>, which nothing observes
// today only because nothing creates or reclaims a copy yet.
//
// So this drives the real start path, reads the keys from inside a stage body
// the way a real body will, and compares them against the record the same call
// returned rather than against literals the test states twice. A test that
// restated the two identifiers would agree with a swap that restated them the
// same way.
func TestAStartedRunCarriesTheRecordsOwnIdentifiers(t *testing.T) {
	requiresIdentifiedPeer(t)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	type identifiers struct{ repository, run string }
	read := make(chan identifiers, 1)

	opts := options(t, h)
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		all := stages.All(deps)
		all.Intent = pipeline.Implementation{
			Reads: []pipeline.Key{pipeline.KeyRepository, pipeline.KeyRun},
			NewBody: func() pipeline.Body {
				return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
					repository, err := stateText(in, pipeline.KeyRepository)
					if err != nil {
						return pipeline.Output{}, err
					}
					run, err := stateText(in, pipeline.KeyRun)
					if err != nil {
						return pipeline.Output{}, err
					}
					select {
					case read <- identifiers{repository: repository, run: run}:
					default:
					}
					return pipeline.Output{Report: findings.Report{
						Summary: "read the identifiers the run was started with",
					}}, nil
				}
			},
		}
		return all
	}

	var started machine.Run
	withServiceOptions(t, opts, func(s serviceUnderTest) {
		started = startRun(t, s.client, subject)
	})

	var got identifiers
	select {
	case got = <-read:
	default:
		t.Fatal("no stage body ran, so what a body is given for this run is unproven")
	}
	if started.Record.RepositoryID == started.Record.ID {
		t.Fatalf("this run's repository and run identifiers are both %q, so a swapped "+
			"assignment would satisfy the assertions below", started.Record.ID)
	}
	if got.repository != started.Record.RepositoryID {
		t.Errorf("a stage body is given repository %q, want the record's %q",
			got.repository, started.Record.RepositoryID)
	}
	if got.run != started.Record.ID {
		t.Errorf("a stage body is given run %q, want the record's %q",
			got.run, started.Record.ID)
	}
}

// stateText reads one declared text key, so a body above states each read once.
func stateText(in pipeline.Input, key pipeline.Key) (string, error) {
	v, err := in.State.Get(key)
	if err != nil {
		return "", err
	}
	s, _ := v.Text()
	return s, nil
}
