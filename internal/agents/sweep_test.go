package agents_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
)

// TestAnUnsupportedSweepCannotReadAsEmpty holds the one answer a platform
// without a listing gives, on every platform: the zero SweepResult is not
// clear, so a caller that ignores the error cannot read it as nothing found,
// and a sweep of a real copy either lists, on Linux, or answers
// ErrSweepUnsupported beside the zero result everywhere else.
func TestAnUnsupportedSweepCannotReadAsEmpty(t *testing.T) {
	if (agents.SweepResult{}).Clear() {
		t.Fatal("the zero SweepResult reads clear, so an unsupported sweep with its error dropped would read as nothing found")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, err := home.Open(base)
	if err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyDir, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	r, err := agents.NewSweeper(h, 0, nil).Sweep(t.Context(), copyDir)
	switch {
	case errors.Is(err, agents.ErrSweepUnsupported):
		if runtime.GOOS == "linux" {
			t.Fatalf("Linux answered %v; its listing is the one this package ships", err)
		}
		if r.Clear() || len(r.Ended) != 0 || r.Dir != "" {
			t.Fatalf("an unsupported sweep returned %+v beside its error, want the zero result", r)
		}
	case err == nil:
		if runtime.GOOS != "linux" {
			t.Fatalf("%s swept %s, but only Linux has a verified listing", runtime.GOOS, copyDir)
		}
	default:
		t.Fatalf("sweeping an empty copy: %v", err)
	}
}
