package service

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// The variables an earlier service is started with. They travel in the
// environment rather than on the command line because standin.Main reads
// this binary's arguments first, and the stand-in's own control argument on
// the command line would make this process the stand-in rather than the
// service that launches one.
const (
	earlierHomeVar  = "ASSISTANT_TEST_EARLIER_HOME"
	earlierRunVar   = "ASSISTANT_TEST_EARLIER_RUN"
	earlierAgentVar = "ASSISTANT_TEST_EARLIER_AGENT"
)

// EarlierServiceMain lets this binary act as an earlier service of a home,
// for a test whose subject is what such a service leaves behind when it is
// killed: it opens a real service over the home, launches one invocation of
// the production adapter over the stand-in through this service's own
// containment wrapper, under the run it was named, and prints the started
// group's identifier once Started has returned, which is after the run
// process record has been written. It then waits on the invocation, which the
// stand-in holds, until it is killed. It returns at once in every process not
// started for that.
//
// It exists only in the tests. The launch goes through containRunner rather
// than through a stage body because a stage body would need a whole run
// driven to it, and what this is about is the seam the wrapper owns, which
// every stage body's invocation passes through on the product's path.
func EarlierServiceMain() {
	root := os.Getenv(earlierHomeVar)
	if root == "" {
		return
	}
	fail := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "earlier service: "+format+"\n", args...)
		os.Exit(2)
	}
	ctx := context.Background()
	h, err := home.Open(root)
	if err != nil {
		fail("opening the home: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		fail("reading the build: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		fail("finding this binary: %v", err)
	}
	runner, err := agents.ClaudeFactory(agents.WithBinary(self)).
		New(ctx, strings.Split(os.Getenv(earlierAgentVar), "\n"))
	if err != nil {
		fail("building the adapter over the stand-in: %v", err)
	}
	s, err := Open(ctx, Options{
		Home:      h,
		NewStages: stages.All,
		NewFixer:  stages.Fix,
		Build:     build,
		Catalog:   agents.NewCatalog(fixedRunner{runner: runner}, cursorFactory{runner: runner}),
		LockWait:  -1,
	})
	if err != nil {
		fail("opening the service: %v", err)
	}
	wrapped := s.containRunner(announcing{inner: runner})
	_, err = wrapped.Run(withAdvancing(ctx, os.Getenv(earlierRunVar)), agents.PurposeReview, holdInvocation())
	fail("the held invocation returned, which only a kill was meant to end: %v", err)
}

// holdInvocation is the invocation every process in these tests is started
// with: a text answer, so the stand-in's Hang step answers it, run outside any
// directory the test removes.
func holdInvocation() agents.Invocation {
	return agents.Invocation{Prompt: "hold until ended", Shape: agents.ShapeText, Dir: os.TempDir()}
}

// announcing passes every call to the adapter it wraps and, inside the
// Started value the containment wrapper set, prints the group the adapter
// reported once that value has returned. It produces nothing of an
// invocation's own: the process, its output and its result are the
// adapter's, so the only thing this adds is the moment a test synchronizes on.
type announcing struct{ inner agents.Runner }

func (a announcing) Name() string                      { return a.inner.Name() }
func (a announcing) Capabilities() agents.Capabilities { return a.inner.Capabilities() }
func (a announcing) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	registered := inv.Started
	inv.Started = func(pgid int) func() {
		ended := registered(pgid)
		fmt.Printf("started %d\n", pgid)
		return ended
	}
	return a.inner.Run(ctx, purpose, inv)
}
