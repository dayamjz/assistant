package agents

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

func TestClaudeTranslatesEveryAnthropicCellAndNothingElse(t *testing.T) {
	for _, cell := range config.ModelCatalog() {
		id, err := Translate(ClaudeName, cell.Provider, cell.Type, cell.Version, "")
		if cell.Provider == "anthropic" {
			want := "claude-" + cell.Type + "-" + strings.ReplaceAll(cell.Version, ".", "-")
			if err != nil || id != want {
				t.Errorf("claude %s %s %s = %q, %v; want %s", cell.Provider, cell.Type, cell.Version, id, err, want)
			}
			continue
		}
		if !errors.Is(err, ErrUndeclaredModel) {
			t.Errorf("claude translated %s %s %s to %q (%v)", cell.Provider, cell.Type, cell.Version, id, err)
		}
		if _, err := Translate(CursorName, cell.Provider, cell.Type, cell.Version, ""); !errors.Is(err, ErrUndeclaredModel) {
			t.Errorf("cursor translated %s %s %s", cell.Provider, cell.Type, cell.Version)
		}
	}
	if err := AcceptsEffort(ClaudeName, "high"); err != nil {
		t.Errorf("claude refused high: %v", err)
	}
	if err := AcceptsEffort(CursorName, "high"); !errors.Is(err, ErrUndeclaredModel) {
		t.Errorf("cursor accepted an effort: %v", err)
	}
	if err := AcceptsEffort(CursorName, ""); err != nil {
		t.Errorf("an unnamed effort was refused: %v", err)
	}
}

func TestCodexTranslatesOnlyOpenAICells(t *testing.T) {
	for _, cell := range config.ModelCatalog() {
		id, err := Translate(CodexName, cell.Provider, cell.Type, cell.Version, "")
		if cell.Provider == "openai" {
			if err != nil || id != "gpt-"+cell.Version {
				t.Errorf("%+v: %q, %v", cell, id, err)
			}
		} else if !errors.Is(err, ErrUndeclaredModel) {
			t.Errorf("translated %+v: %q, %v", cell, id, err)
		}
	}
	for _, effort := range []string{"", "low", "medium", "high"} {
		if err := AcceptsEffort(CodexName, effort); err != nil {
			t.Errorf("%q: %v", effort, err)
		}
	}
	if err := AcceptsEffort(CodexName, "max"); !errors.Is(err, ErrUndeclaredModel) {
		t.Fatalf("accepted unsupported effort: %v", err)
	}
}

func TestTheDefaultFixerIsTheCursorAdapter(t *testing.T) {
	if got := config.Defaults().Roles[config.RoleFix].Agent; got != CursorName {
		t.Fatalf("default fixer %q, want %s", got, CursorName)
	}
}

// Claude starts every anthropic cell at the standard window as the identifier
// it starts with no window named, and at 1m only on a cell it declared, as
// that identifier with "[1m]". Every other cell and window is refused, so a
// launch the adapter cannot express is never started at the standard window
// in its place.
func TestClaudeStartsADeclaredCellAt1MAndRefusesTheRest(t *testing.T) {
	for _, cell := range config.ModelCatalog() {
		if cell.Provider != "anthropic" {
			continue
		}
		plain := "claude-" + cell.Type + "-" + strings.ReplaceAll(cell.Version, ".", "-")
		if id, err := Translate(ClaudeName, cell.Provider, cell.Type, cell.Version, config.ContextStandard); err != nil || id != plain {
			t.Errorf("%+v at standard = %q, %v; want %s", cell, id, err, plain)
		}
		id, err := Translate(ClaudeName, cell.Provider, cell.Type, cell.Version, config.Context1M)
		if claudeLongWindowCells[cell] {
			if err != nil || id != plain+"[1m]" {
				t.Errorf("%+v at 1m = %q, %v; want %s[1m]", cell, id, err, plain)
			}
			continue
		}
		if !errors.Is(err, ErrUndeclaredModel) || id != "" {
			t.Errorf("%+v at 1m = %q, %v; want ErrUndeclaredModel and nothing started", cell, id, err)
		}
	}
	if id, err := Translate(ClaudeName, "anthropic", "opus", "5.5", config.Context1M); err != nil || id != "claude-opus-5-5[1m]" {
		t.Fatalf("opus 5.5 at 1m = %q, %v; want claude-opus-5-5[1m]", id, err)
	}
	if id, err := Translate(ClaudeName, "anthropic", "haiku", "4.5", config.Context1M); !errors.Is(err, ErrUndeclaredModel) {
		t.Fatalf("haiku 4.5 at 1m = %q, %v; want a refusal", id, err)
	}
}

// A window outside the set, a window with no triple, and any window on an
// adapter that declares none are refused, never dropped.
func TestAWindowNoAdapterDeclaredIsRefused(t *testing.T) {
	for _, tc := range []struct{ adapter, provider, typ, version, window string }{
		{ClaudeName, "anthropic", "opus", "5.5", "2m"},
		{ClaudeName, "anthropic", "opus", "5.5", "1M"},
		{ClaudeName, "", "", "", config.Context1M},
		{ClaudeName, "", "", "", config.ContextStandard},
		{CodexName, "openai", "gpt", "6-astra", config.ContextStandard},
		{CodexName, "openai", "gpt", "6-astra", config.Context1M},
		{CursorName, "anthropic", "opus", "5.5", config.Context1M},
	} {
		if id, err := Translate(tc.adapter, tc.provider, tc.typ, tc.version, tc.window); !errors.Is(err, ErrUndeclaredModel) || id != "" {
			t.Errorf("%+v = %q, %v; want ErrUndeclaredModel", tc, id, err)
		}
	}
}

// Every window an adapter declares is a member of config.ContextWindows, the
// set's one owner: a declaration cannot introduce a window of its own.
func TestEveryDeclaredWindowIsDrawnFromTheConfiguredSet(t *testing.T) {
	principles.Cite(t, principles.P14)
	declared := 0
	for _, adapter := range []string{ClaudeName, CodexName, CursorName} {
		for _, cell := range config.ModelCatalog() {
			for _, window := range []string{config.ContextStandard, config.Context1M, "2m", "200k", "long"} {
				if _, err := Translate(adapter, cell.Provider, cell.Type, cell.Version, window); err != nil {
					continue
				}
				declared++
				if !config.KnownContextWindow(window) {
					t.Errorf("%s declares %+v at %q, which is not in config.ContextWindows", adapter, cell, window)
				}
			}
		}
	}
	for cell := range claudeLongWindowCells {
		if !config.KnownModel(cell.Provider, cell.Type, cell.Version) {
			t.Errorf("claude declares 1m on %+v, which is not in the model table", cell)
		}
	}
	if declared == 0 {
		t.Fatal("no adapter declared any window, so this test checked nothing")
	}
}
