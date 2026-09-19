package forge_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
)

// The probe answers with the executable the host's adapters would run, so a
// caller reporting it reports the invocation a run would make rather than a
// restatement of the configuration.
func TestProbeResolvesTheProviderCommandLine(t *testing.T) {
	t.Parallel()
	host := forge.NewGitHubHost(testRedactor, forge.WithBinary(os.Args[0]))
	resolved, err := host.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if resolved == "" {
		t.Fatal("the probe resolved the command line and reports no path")
	}
}

// A command line that does not resolve is refused the way every other
// provider failure is: a typed refusal, unavailable, saying the command line
// could not be run - the same fact a run's own call would otherwise be the
// first to report.
func TestProbeRefusesACommandLineThatDoesNotResolve(t *testing.T) {
	t.Parallel()
	host := forge.NewGitHubHost(testRedactor,
		forge.WithBinary(filepath.Join(t.TempDir(), "no-such-provider")))
	resolved, err := host.Probe()
	if err == nil {
		t.Fatalf("the probe resolved %q for a command line that does not exist", resolved)
	}
	if !errors.Is(err, forge.ErrRefused) {
		t.Fatalf("the probe's failure is not a refusal: %v", err)
	}
	var refusal *forge.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("the probe's failure is not a *Refusal: %v", err)
	}
	if refusal.Reason != forge.ReasonUnavailable {
		t.Fatalf("the probe refused with %q, want %q", refusal.Reason, forge.ReasonUnavailable)
	}
	if !strings.Contains(refusal.Detail, "could not be run") {
		t.Fatalf("the refusal does not say the command line could not be run: %q", refusal.Detail)
	}
}

// The probe establishes resolution and nothing past it, which is the gap its
// contract names: an executable that resolves and could never start still
// probes as runnable. The planted command is a file that is executable and is
// no program at all, so a probe that started what it resolved could not
// answer this way.
func TestProbeEstablishesResolutionAndNotAStart(t *testing.T) {
	t.Parallel()
	name := "gh"
	if runtime.GOOS == "windows" {
		name = "gh.exe"
	}
	planted := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(planted, []byte("not a program\n"), 0o755); err != nil {
		t.Fatalf("planting an executable that is no program: %v", err)
	}
	host := forge.NewGitHubHost(testRedactor, forge.WithBinary(planted))
	resolved, err := host.Probe()
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if resolved != planted {
		t.Fatalf("the probe resolved %q, want the planted command %q", resolved, planted)
	}
}
