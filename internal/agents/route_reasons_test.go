package agents_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

// heldFactory is a catalog entry over a runner the test already holds, so
// Route reaches a scripted adapter through the catalog the way it reaches a
// shipped one.
type heldFactory struct {
	name   string
	runner agents.Runner
}

func (f heldFactory) Name() string { return f.name }

func (heldFactory) AutoEligible() bool { return true }

func (f heldFactory) Declares() agents.Capabilities { return f.runner.Capabilities() }

func (f heldFactory) New(context.Context, []string) (agents.Runner, error) { return f.runner, nil }

// Every entry Route and Resolve pass over carries a stable reason from the
// skip table, one for each way an entry is passed over, in the order the
// entries were considered. The cases are derived from the table in both
// directions: a reason the table lists that no case produces fails, and so
// does a case producing a reason the table does not list.
func TestEverySkippedEntryCarriesAReasonFromTheTable(t *testing.T) {
	t.Parallel()
	codex := &stubFactory{name: "codex", available: false}
	cursor := &stubFactory{
		name: "cursor", available: true,
		declares: []agents.Capability{agents.CapabilityResumableSessions},
		sessions: true,
	}
	claude := &stubFactory{
		name: "claude", available: true,
		declares: []agents.Capability{agents.CapabilityResumableSessions, agents.CapabilityWorkerSessions},
		sessions: true, workers: true,
	}
	catalog := agents.NewCatalog(codex, cursor, claude)

	routed, err := agents.Route(t.Context(), catalog, []string{"  ", "nosuch", "codex", "cursor", "claude"}, agents.NeedForWorker(false), "")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	want := []agents.Unavailability{
		{Name: "", Reason: agents.SkipNamesNoAgent},
		{Name: "nosuch", Reason: agents.SkipNotInBuild},
		{Name: "codex", Reason: agents.SkipCannotRun},
		{Name: "cursor", Reason: agents.SkipMissingCapability},
	}
	if got := reasonsOf(routed.Resolution.Skipped); !slices.Equal(got, reasonsOf(want)) {
		t.Fatalf("Route skipped %v, want %v", got, reasonsOf(want))
	}
	var undeclared *agents.CapabilityError
	if !errors.As(routed.Resolution.Skipped[3].Err, &undeclared) || undeclared.Capability != agents.CapabilityWorkerSessions {
		t.Fatalf("the missing-capability skip carries %v, want the *CapabilityError naming %s", routed.Resolution.Skipped[3].Err, agents.CapabilityWorkerSessions)
	}

	resolved, err := agents.Resolve(t.Context(), []string{"  ", "nosuch", "codex", "claude"}, catalog)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := reasonsOf(resolved.Skipped); !slices.Equal(got, reasonsOf(want[:3])) {
		t.Fatalf("Resolve skipped %v, want %v", got, reasonsOf(want[:3]))
	}

	// A refusal carries the same reasons in what it says was tried.
	_, err = agents.Resolve(t.Context(), []string{"nosuch", "codex"}, catalog)
	var nothing *agents.ResolutionError
	if !errors.As(err, &nothing) || len(nothing.Tried) != 2 ||
		nothing.Tried[0].Reason != agents.SkipNotInBuild || nothing.Tried[1].Reason != agents.SkipCannotRun {
		t.Fatalf("Resolve refused with %v, want both entries tried with their reasons", err)
	}

	produced := map[string]bool{}
	for _, u := range routed.Resolution.Skipped {
		produced[u.Reason] = true
	}
	for _, reason := range agents.SkipReasons() {
		if !produced[reason] {
			t.Errorf("the skip table lists %q and no case here produced it", reason)
		}
		delete(produced, reason)
	}
	for reason := range produced {
		t.Errorf("Route produced %q, which the skip table does not list", reason)
	}
}

func reasonsOf(skipped []agents.Unavailability) []string {
	out := make([]string, 0, len(skipped))
	for _, u := range skipped {
		out = append(out, u.Name+":"+u.Reason)
	}
	return out
}

// A fallback selection has a reason only for the two failures that reach a
// backup, and the selection table is exactly Route's two reasons and those
// two. Every other failure category is refused rather than given a reason
// nothing could have caused.
func TestAFallbackReasonExistsOnlyForAFailureThatReachesABackup(t *testing.T) {
	t.Parallel()
	reached := map[agents.Failure]string{
		agents.FailureUnavailable: agents.ReasonFallbackUnavailable,
		agents.FailureQuota:       agents.ReasonFallbackQuota,
	}
	for _, f := range []agents.Failure{
		agents.FailureNone, agents.FailureProcess, agents.FailureExit, agents.FailureAgent,
		agents.FailureCancelled, agents.FailureTimeout, agents.FailureOversize, agents.FailureOutput,
		agents.FailureUnavailable, agents.FailureQuota,
	} {
		got, err := agents.FallbackReason(f)
		if want, ok := reached[f]; ok {
			if err != nil || got != want {
				t.Errorf("FallbackReason(%q) = %q, %v, want %q", f, got, err, want)
			}
			continue
		}
		if err == nil {
			t.Errorf("FallbackReason(%q) = %q, want a refusal", f, got)
		}
	}
	want := []string{agents.ReasonOverride, agents.ReasonOrdered, agents.ReasonFallbackUnavailable, agents.ReasonFallbackQuota}
	if got := agents.SelectionReasons(); !slices.Equal(got, want) {
		t.Fatalf("SelectionReasons() = %v, want %v", got, want)
	}
}

// The two operator documents that describe worker routing name every
// reason the selection and skip tables hold, as code, so an operator reading
// a recorded reason finds it. The check is held to a positive control: the
// same document with one reason taken out is reported missing that reason,
// so a check that stopped reading the text fails here rather than passing.
func TestTheRoutingDocumentsNameEveryRecordedReason(t *testing.T) {
	t.Parallel()
	reasons := append(agents.SelectionReasons(), agents.SkipReasons()...)
	for _, doc := range []string{"operators.md", "multi-agent-support.md"} {
		text, err := os.ReadFile(filepath.Join("..", "..", "docs", doc))
		if err != nil {
			t.Fatalf("reading docs/%s: %v", doc, err)
		}
		if missing := unnamedReasons(string(text), reasons); len(missing) != 0 {
			t.Errorf("docs/%s does not name %v", doc, missing)
		}
		for _, reason := range reasons {
			cut := strings.ReplaceAll(string(text), "`"+reason+"`", "")
			if missing := unnamedReasons(cut, reasons); !slices.Equal(missing, []string{reason}) {
				t.Errorf("docs/%s with %q taken out reports %v missing, want only it", doc, reason, missing)
			}
		}
	}
}

// unnamedReasons is the reasons text does not name as inline code.
func unnamedReasons(text string, reasons []string) []string {
	var missing []string
	for _, reason := range reasons {
		if !strings.Contains(text, "`"+reason+"`") {
			missing = append(missing, reason)
		}
	}
	return missing
}

// A worker routing reaches after a refusal and a fallthrough runs under the
// worker grant grant.go gives the role, exactly as a worker started on the
// same adapter without routing does: routing chooses an adapter and decides
// no grant. The pin to an adapter that cannot host a worker is refused, the
// ordered list then skips it for the next adapter, and the command line the
// stand-in recorded for the started worker carries the Claude adapter's
// translation of GrantWorker for the spec's route and nothing wider. The
// check is shown to fail on counterfeits of that command line widened the
// ways a fallback path could widen it.
func TestAWorkerReachedAfterARefusalRunsUnderTheRolesGrant(t *testing.T) {
	t.Parallel()
	const route = "/opt/assistant/bin/assistant --home /var/lib/assistant worker"
	unrouted := func(t *testing.T) []string {
		t.Helper()
		agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
			agents.WithTerminationGrace(300*time.Millisecond))
		worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{
			Dir: t.TempDir(), Instructions: "Work on the task in this copy.", Route: route,
		})
		if err != nil {
			t.Fatalf("starting the worker: %v", err)
		}
		defer worker.Close()
		return workerArgs(t, agent)
	}

	agent := standin.New(t, standin.Script{Worker: &standin.WorkerScript{}},
		agents.WithTerminationGrace(300*time.Millisecond))
	cursor := &stubFactory{
		name: "cursor", available: true, namedOnly: true,
		declares: []agents.Capability{agents.CapabilityResumableSessions},
		sessions: true,
	}
	catalog := agents.NewCatalog(cursor, heldFactory{name: "claude", runner: agent.Runner()})
	need := agents.NeedForWorker(false)

	if _, err := agents.Route(t.Context(), catalog, []string{"cursor"}, need, "cursor"); !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Fatalf("the pin to an adapter with no worker answered %v, want a refusal", err)
	}
	selection, err := agents.Route(t.Context(), catalog, []string{"cursor", "claude"}, need, "")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if selection.Resolution.Name != "claude" || selection.Reason != agents.ReasonOrdered ||
		len(selection.Resolution.Skipped) != 1 || selection.Resolution.Skipped[0].Reason != agents.SkipMissingCapability {
		t.Fatalf("Route chose %+v, want claude by order past cursor's missing capability", selection)
	}
	worker, err := agents.OpenWorker(t.Context(), selection.Resolution.Runner, agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "Work on the task in this copy.", Route: route,
	})
	if err != nil {
		t.Fatalf("starting the routed worker: %v", err)
	}
	routed := workerArgs(t, agent)
	worker.Close()

	role := grantOf(agents.ClaudeGrantFlags(agents.GrantWorker, route))
	if err := heldToGrant(routed, role); err != nil {
		t.Fatalf("the routed worker: %v", err)
	}
	if control := grantOf(unrouted(t)); control != grantOf(routed) {
		t.Fatalf("the routed worker ran under %+v and an unrouted one under %+v", grantOf(routed), control)
	}

	for name, widen := range map[string]func([]string) []string{
		"bypassed permissions": func(args []string) []string { return setFlag(args, "--permission-mode", "bypassPermissions") },
		"the whole shell": func(args []string) []string {
			return setFlag(args, "--allowedTools", flagValue(args, "--allowedTools")+",Bash")
		},
		"the read grant's list": func(args []string) []string {
			return setFlag(args, "--allowedTools", "Bash(git:*),Bash("+route+":*),Edit,Write")
		},
	} {
		if heldToGrant(widen(slices.Clone(routed)), role) == nil {
			t.Errorf("a command line widened to %s passed the grant check", name)
		}
	}
}

// heldToGrant is nil when args carry exactly the grant want: the permission
// mode and both tool lists.
func heldToGrant(args []string, want grantFlags) error {
	if got := grantOf(args); got != want {
		return fmt.Errorf("ran under %+v, want the role's %+v (command line %q)", got, want, args)
	}
	return nil
}

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func setFlag(args []string, name, value string) []string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			args[i+1] = value
			return args
		}
	}
	return append(args, name, value)
}
