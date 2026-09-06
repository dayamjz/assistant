package runs_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestMain lets this binary act as the stand-in agent. Every test here that
// reaches an agent does it through internal/agents/standin, so what a Result,
// a refusal, or a recorded call is built from is a process's output read by
// the production adapter.
func TestMain(m *testing.M) {
	standin.Main()
	os.Exit(m.Run())
}

// seamUserinfo is the shape internal/store's redaction seam covers. A store
// refuses to open without a redactor and refuses one that does nothing, so a
// test supplies one of that shape rather than reaching into another package.
var seamUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)

func redactor() vcs.Redactor {
	return vcs.RedactorFunc(func(s string) string {
		return seamUserinfo.ReplaceAllString(s, "${1}REDACTED@")
	})
}

// openStore opens a fresh store in a temporary directory and returns it with
// its path, so a test that wants a restart can reopen the same database.
func openStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	return openStoreAt(t, path), path
}

func openStoreAt(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(t.Context(), path, store.WithRedactor(redactor()))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seedRun records a repository and a run through the service, which is how a
// run begins, and returns it.
func seedRun(t *testing.T, s *store.Store, svc *runs.Service, id string) store.Run {
	t.Helper()
	ctx := t.Context()
	if _, err := s.UpsertRepository(ctx, store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	r, err := svc.Create(ctx, store.Run{
		ID: id, RepositoryID: "repo-1", Branch: "topic",
		SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push",
		Build:        store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		ConfigDigest: "cfg-1",
	})
	if err != nil {
		t.Fatalf("Create %s: %v", id, err)
	}
	return r
}

// resolution is the stand-in's runner as a caller holds it after
// agents.Resolve: the runner, its name, and the declaration that function
// verified against its type.
func resolution(ag *standin.Agent) agents.Resolution {
	runner := ag.Runner()
	return agents.Resolution{
		Runner:       runner,
		Name:         runner.Name(),
		Capabilities: runner.Capabilities(),
		Entry:        runner.Name(),
	}
}

// service builds a run service or fails the test.
func service(t *testing.T, s *store.Store, agent agents.Resolution, reuse bool) *runs.Service {
	t.Helper()
	svc, err := runs.New(runs.Options{Store: s, Agent: agent, SessionReuse: reuse})
	if err != nil {
		t.Fatalf("runs.New: %v", err)
	}
	return svc
}

// fixInvocation is a runnable fix invocation. Its prompt is what a test names
// a call by, since the wire carries no purpose.
func fixInvocation(t *testing.T, prompt string) agents.Invocation {
	t.Helper()
	return agents.Invocation{Prompt: prompt, Shape: agents.ShapeText, Dir: t.TempDir()}
}

// answering is a reply reporting the session identifier it names, which is how
// a test says which conversation the agent answered in.
func answering(result, session string) standin.Reply {
	reply := standin.Text(result)
	reply.Envelope.Session = standin.SessionID(session)
	return reply
}

// sessionOf reads a run's recorded fixer session, and says whether one is
// there at all rather than substituting an empty string for absent.
func sessionOf(t *testing.T, s *store.Store, id string) (string, bool) {
	t.Helper()
	r, err := s.Run(context.Background(), id)
	if err != nil {
		t.Fatalf("reading run %s: %v", id, err)
	}
	return r.FixerSession.Get()
}
