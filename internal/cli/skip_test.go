package cli_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// A name that is not one of the nine stages is incorrect usage and names the
// real stage names, not an internal failure code.
func TestSkipWithAnUnknownNameIsIncorrectUsage(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	serve(t, h)
	initialize(t, h, subject)

	got := run(t, h, subject, "--skip", "docs")
	if got.code != machine.ExitUsage {
		t.Fatalf("assistant --skip docs exited %s, want incorrect usage:\n%s%s", got.code, got.stdout, got.stderr)
	}
	if strings.Contains(got.stderr, "(internal)") {
		t.Fatalf("the refusal is reported as internal:\n%s", got.stderr)
	}
	wantNames := strings.Join(stageNames(), ", ")
	if !strings.Contains(got.stderr, wantNames) {
		t.Fatalf("the refusal does not list the stage names %q:\n%s", wantNames, got.stderr)
	}
	if !strings.Contains(got.stderr, string(ipc.CodeInvalidRequest)) {
		t.Fatalf("the refusal does not cross the wire as an invalid request:\n%s", got.stderr)
	}
}

func stageNames() []string {
	names := make([]string, 0, len(pipeline.Order()))
	for _, stage := range pipeline.Order() {
		names = append(names, stage.String())
	}
	return names
}
