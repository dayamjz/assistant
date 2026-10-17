package agents

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
)

func TestClaudeTranslatesEveryAnthropicCellAndNothingElse(t *testing.T) {
	for _, cell := range config.ModelCatalog() {
		id, err := Translate(ClaudeName, cell.Provider, cell.Type, cell.Version)
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
		if _, err := Translate(CursorName, cell.Provider, cell.Type, cell.Version); !errors.Is(err, ErrUndeclaredModel) {
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
