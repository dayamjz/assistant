//go:build unix

package stages_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/stages"
)

// An operator's command line is handed its input on standard input and then
// end of file, its exit status is reported as its own, and what it prints is
// redacted before the caller sees it: the line echoes its input back, so a
// credential in the input reaches the result only through the runner's
// redaction, and the control is the same text with no credential, which
// comes back whole. A non-zero exit is a status and not an error, and a
// command the context ended reports no status of its own.
func TestAnOperatorCommandReadsItsInputAndReportsItsExit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got := stages.RunOperatorCommand(t.Context(), stages.OperatorCommand{
		Line: "cat; exit 3", Dir: dir, Stdin: "see https://user:hunter2@example.test/x and plain words\n", Tail: 4096,
	})
	if !got.Exited || got.Code != 3 || got.Err != nil {
		t.Fatalf("the command answered %+v, want exit status 3 of its own", got)
	}
	if strings.Contains(got.Tail, "hunter2") || !strings.Contains(got.Tail, "plain words") {
		t.Fatalf("the command's output came back as %q, want the credential removed and the rest kept", got.Tail)
	}
	plain := stages.RunOperatorCommand(t.Context(), stages.OperatorCommand{Line: "cat", Dir: dir, Stdin: "only plain words\n", Tail: 4096})
	if !plain.Exited || plain.Code != 0 || plain.Tail != "only plain words" {
		t.Fatalf("the plain command answered %+v, want its input back and status 0", plain)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	ended := stages.RunOperatorCommand(ctx, stages.OperatorCommand{Line: "exec sleep 30", Dir: dir})
	if ended.Exited {
		t.Fatalf("a command the context ended reported status %d as its own", ended.Code)
	}
}
