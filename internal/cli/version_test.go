package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/dayamjz/assistant/internal/buildinfo"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/machine"
)

// --version reports exactly the build the process boundary handed the
// surface, in both renderings, and nothing else on standard output. The line
// is internal/buildinfo's, which cmd/assistant passes as Environment.Version;
// internal/buildinfo's own test reads it back through a stamped binary, so
// this pins only that the surface adds and drops nothing. The value used is
// buildinfo.Line for this test binary, so the shape checked is one the real
// read produces rather than one written for the test.
func TestVersionReportsTheBuildItWasHandedAndNothingElse(t *testing.T) {
	t.Parallel()
	line := buildinfo.Line()
	versionOf := func(args ...string) (string, string, machine.Code) {
		var out, errs bytes.Buffer
		code := cli.Run(context.Background(), cli.Environment{
			Args:       args,
			Stdout:     &out,
			Stderr:     &errs,
			Getenv:     func(string) string { return "" },
			WorkingDir: t.TempDir(),
			Executable: "/nonexistent/assistant",
			Version:    line,
		})
		return out.String(), errs.String(), code
	}

	out, errs, code := versionOf("--version")
	if code != machine.ExitOK || errs != "" {
		t.Fatalf("--version exited %s with standard error %q", code, errs)
	}
	if out != line+"\n" {
		t.Fatalf("--version wrote %q, want exactly %q", out, line+"\n")
	}

	out, errs, code = versionOf("--json", "--version")
	if code != machine.ExitOK || errs != "" {
		t.Fatalf("--json --version exited %s with standard error %q", code, errs)
	}
	var document machine.Version
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatalf("--json --version wrote no document: %v\n%s", err, out)
	}
	if document.Version != line {
		t.Fatalf("--json --version names %q, want %q", document.Version, line)
	}
}
