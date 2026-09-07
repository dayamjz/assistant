package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/vcs"
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

// TestTheSeamTheServiceBuildsCarriesItsHomeAndTheRedactorItChose is the
// build-scoped half of what a stage body is handed, and the twin of the test
// above.
//
// driverFor is the one production site that constructs a StageDeps, and until
// this nothing read any of it back. Most of that argument list is held by the
// compiler, since the five parameters have distinct types, but two decisions
// are not. The home is what every isolated copy's path is composed from, and
// the redactor is the one that would fail silently: internal/vcs supplies a
// fallback for a caller that passes none, so dropping vcs.WithRedactor here
// would leave repositories opened through Copy still redacting, just not by
// the owner PRD section 8 gives credential removal to.
//
// So the secret disappearing is not the assertion - the fallback would do that
// much. What separates the two is the marker each leaves behind, which is why
// internal/redact exports Marker for a test to name. This drives the real
// start path to get the deps the service built, opens a copy through it, and
// reads the argument vector back off a failing invocation, where internal/vcs
// documents the redactor as having run.
func TestTheSeamTheServiceBuildsCarriesItsHomeAndTheRedactorItChose(t *testing.T) {
	requiresIdentifiedPeer(t)

	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)

	built := make(chan stages.StageDeps, 1)
	opts := options(t, h)
	opts.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		select {
		case built <- deps:
		default:
		}
		return stages.All(deps)
	}

	var deps stages.StageDeps
	withServiceOptions(t, opts, func(s serviceUnderTest) {
		startRun(t, s.client, subject)
		select {
		case deps = <-built:
		default:
			t.Fatal("the service drove a run without building a stage seam, so what it " +
				"hands a body is unproven")
		}
	})

	if deps.Home == nil {
		t.Fatal("the seam carries no home, so no stage body could locate its isolated copy")
	}
	if deps.Home.Root() != h.Root() {
		t.Fatalf("the seam carries home %s, want the one the service was opened with, %s",
			deps.Home.Root(), h.Root())
	}

	// Copy opens and never creates, and nothing in this build creates one, so
	// the copy this reads through is made here at the path the home names for
	// it rather than by asking the product for something it does not do yet.
	const repositoryID, runID = "subject", "seam-under-test"
	copyPath := h.Worktree(repositoryID, runID)
	if err := os.MkdirAll(copyPath, 0o700); err != nil {
		t.Fatalf("making the isolated copy's directory: %v", err)
	}
	git(t, copyPath, "init", "--quiet", "-b", "main", ".")
	if err := writeFile(filepath.Join(copyPath, "file.txt"), "hello\n"); err != nil {
		t.Fatalf("writing a file in the isolated copy: %v", err)
	}
	git(t, copyPath, "add", "-A")
	git(t, copyPath, "commit", "--quiet", "-m", "first")

	repository, err := deps.Copy(t.Context(), repositoryID, runID)
	if err != nil {
		t.Fatalf("opening the isolated copy through the seam the service built: %v", err)
	}

	const secret = "s3cr3tp4ss"
	_, err = repository.AddWorktree(t.Context(), vcs.WorktreeSpec{
		Path:   filepath.Join(t.TempDir(), "linked"),
		Commit: "HEAD",
		Branch: "https://user:" + secret + "@example.invalid/r.git",
	})
	var failed *vcs.CommandError
	if !errors.As(err, &failed) {
		t.Fatalf("git accepted a URL as a branch name, so nothing reported an argument "+
			"vector to read the redactor off: %v", err)
	}

	reported := strings.Join(failed.Args, " ")
	if strings.Contains(reported, secret) {
		t.Fatalf("a repository opened through the seam reported a credential: %s", reported)
	}
	if !strings.Contains(reported, redact.Marker) {
		t.Errorf("a repository opened through the seam redacted with something other than "+
			"internal/redact, whose marker is %q, so the owner PRD section 8 names is not the "+
			"one wired here: %s", redact.Marker, reported)
	}
}
