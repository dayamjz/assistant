package agents_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
)

// plantOnPath makes each named executable resolvable and nothing else, so what
// the default catalog finds is established rather than whatever this machine
// has installed. PATH is process state, so the tests using it stay serial.
func plantOnPath(t *testing.T, names ...string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("planting a bare executable name on PATH needs a script this test cannot portably make on windows")
	}
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("planting %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)
}

func TestAutoPicksClaudeWhenSeveralAdaptersAreOnPath(t *testing.T) {
	plantOnPath(t, agents.CursorName, agents.ClaudeName, agents.OpenAIName, agents.GrokName)
	got, err := agents.Resolve(t.Context(), []string{agents.AutoEntry}, agents.DefaultCatalog())
	if err != nil {
		t.Fatalf("auto refused with every adapter on PATH: %v", err)
	}
	if got.Name != agents.ClaudeName {
		t.Errorf("auto resolved %q, want %q", got.Name, agents.ClaudeName)
	}
}

func TestAutoNeverPicksANamedOnlyAdapter(t *testing.T) {
	plantOnPath(t, agents.CursorName, agents.OpenAIName, agents.GrokName)
	_, err := agents.Resolve(t.Context(), []string{agents.AutoEntry}, agents.DefaultCatalog())
	if err == nil {
		t.Fatal("auto resolved an adapter with only named-only adapters on PATH")
	}
}

func TestAnExplicitNamedOnlyAdapterStillResolves(t *testing.T) {
	plantOnPath(t, agents.CursorName, agents.ClaudeName)
	for _, entry := range []string{agents.CursorName, "cursor --model x"} {
		got, err := agents.Resolve(t.Context(), []string{entry}, agents.DefaultCatalog())
		if err != nil {
			t.Fatalf("explicit %q refused: %v", entry, err)
		}
		if got.Name != agents.CursorName {
			t.Errorf("explicit %q resolved %q", entry, got.Name)
		}
	}
	// An explicit fallback list may mix: the absent named one is passed over,
	// and auto inside the list still skips named-only adapters.
	got, err := agents.Resolve(t.Context(), []string{"openai", agents.AutoEntry}, agents.DefaultCatalog())
	if err != nil {
		t.Fatalf("fallback list refused: %v", err)
	}
	if got.Name != agents.ClaudeName {
		t.Errorf("fallback list resolved %q, want %q after the absent openai", got.Name, agents.ClaudeName)
	}
}

// TestOnlyClaudeIsDeclaredAutoEligible pins the declaration PRD section 12
// implies. It shows what each factory declares, not that any adapter's parsing
// matches its real CLI: nothing in this repository drives a real CLI.
func TestOnlyClaudeIsDeclaredAutoEligible(t *testing.T) {
	plantOnPath(t, agents.CursorName, agents.ClaudeName, agents.CodexName, agents.OpenAIName, agents.GrokName)
	standings := agents.DefaultCatalog().Survey(t.Context())
	if len(standings) != 5 || standings[0].Name != agents.ClaudeName {
		t.Fatalf("survey is %+v, want five adapters with claude first", standings)
	}
	for _, s := range standings {
		if !s.Runnable {
			t.Errorf("%s is on PATH and the survey says it is not runnable: %v", s.Name, s.Err)
		}
		if want := s.Name == agents.ClaudeName; s.AutoEligible != want {
			t.Errorf("%s auto-eligible=%v, want %v", s.Name, s.AutoEligible, want)
		}
	}
}

func TestAutoSkipsANamedOnlyFactoryInACustomCatalog(t *testing.T) {
	namedOnly := &stubFactory{name: "first", available: true, namedOnly: true}
	eligible := &stubFactory{name: "second", available: true}
	got, err := agents.Resolve(t.Context(), []string{agents.AutoEntry}, agents.NewCatalog(namedOnly, eligible))
	if err != nil {
		t.Fatalf("auto refused: %v", err)
	}
	if got.Name != "second" {
		t.Errorf("auto resolved %q, want the auto-eligible \"second\"", got.Name)
	}
	if namedOnly.args != nil {
		t.Error("auto built the named-only adapter")
	}
}
