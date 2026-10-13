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

func TestAutoNeverPicksAnUnprovenAdapter(t *testing.T) {
	plantOnPath(t, agents.CursorName, agents.OpenAIName, agents.GrokName)
	_, err := agents.Resolve(t.Context(), []string{agents.AutoEntry}, agents.DefaultCatalog())
	if err == nil {
		t.Fatal("auto resolved an adapter with only unproven adapters on PATH")
	}
}

func TestAnExplicitUnprovenAdapterStillResolves(t *testing.T) {
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
	// and auto inside the list still skips unproven adapters.
	got, err := agents.Resolve(t.Context(), []string{"openai", agents.AutoEntry}, agents.DefaultCatalog())
	if err != nil {
		t.Fatalf("fallback list refused: %v", err)
	}
	if got.Name != agents.ClaudeName {
		t.Errorf("fallback list resolved %q, want %q after the absent openai", got.Name, agents.ClaudeName)
	}
}

func TestOnlyClaudeIsDeclaredProven(t *testing.T) {
	plantOnPath(t, agents.CursorName, agents.ClaudeName, agents.OpenAIName, agents.GrokName)
	standings := agents.DefaultCatalog().Survey(t.Context())
	if len(standings) != 4 || standings[0].Name != agents.ClaudeName {
		t.Fatalf("survey is %+v, want four adapters with claude first", standings)
	}
	for _, s := range standings {
		if !s.Runnable {
			t.Errorf("%s is on PATH and the survey says it is not runnable: %v", s.Name, s.Err)
		}
		if want := s.Name == agents.ClaudeName; s.Proven != want {
			t.Errorf("%s proven=%v, want %v", s.Name, s.Proven, want)
		}
	}
}

func TestAutoSkipsAnUnprovenFactoryInACustomCatalog(t *testing.T) {
	unproven := &stubFactory{name: "first", available: true, unproven: true}
	proven := &stubFactory{name: "second", available: true}
	got, err := agents.Resolve(t.Context(), []string{agents.AutoEntry}, agents.NewCatalog(unproven, proven))
	if err != nil {
		t.Fatalf("auto refused: %v", err)
	}
	if got.Name != "second" {
		t.Errorf("auto resolved %q, want the proven \"second\"", got.Name)
	}
	if unproven.args != nil {
		t.Error("auto built the unproven adapter")
	}
}
