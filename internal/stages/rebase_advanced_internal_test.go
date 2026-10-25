package stages

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/vcs"
)

// TestARebaseStoppedOnTheSameCommitIsNotAdvanced stops a real rebase on a
// conflict and stages the resolution without continuing, so it is stopped
// with nothing unmerged on the commit it stopped on. Measured from that
// commit it has not moved past the round's step, and rebaseAdvanced refuses
// it. Measured from another commit, the same stop is a later step whose
// resolution is already staged, and rebaseAdvanced accepts it: that is the
// positive control that the refusal turns on the commit and not on the
// index alone.
func TestARebaseStoppedOnTheSameCommitIsNotAdvanced(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Test\n\temail = test@example.invalid\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	run := func(ok bool, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if ok && err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "clash.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		run(true, "add", "clash.txt")
		run(true, "commit", "--quiet", "-m", message)
		return run(true, "rev-parse", "HEAD")
	}
	run(true, "init", "--quiet", "-b", "main", ".")
	base := commit("base\n", "base")
	run(true, "checkout", "--quiet", "-b", "topic")
	topic := commit("topic\n", "topic")
	run(true, "checkout", "--quiet", "main")
	commit("main\n", "main")
	run(true, "checkout", "--quiet", "topic")
	run(false, "rebase", "--merge", "main")
	if err := os.WriteFile(filepath.Join(dir, "clash.txt"), []byte("topic and main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(true, "add", "clash.txt")
	if unmerged := run(true, "ls-files", "-u"); unmerged != "" {
		t.Fatalf("the resolution left paths unmerged:\n%s", unmerged)
	}

	copied, err := vcs.OpenWorktree(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if on, err := rebaseStep(t.Context(), copied); err != nil || on != topic {
		t.Fatalf("the rebase is stopped on %q, %v; want %s", on, err, topic)
	}
	if _, err := rebaseAdvanced(t.Context(), copied, pipeline.StageRebase, topic); err == nil ||
		!strings.Contains(err.Error(), "nothing unmerged") {
		t.Fatalf("a stop on the round's own commit with nothing unmerged: %v, want a refusal", err)
	}
	if _, err := rebaseAdvanced(t.Context(), copied, pipeline.StageRebase, base); err != nil {
		t.Fatalf("the same stop measured from another commit: %v, want it accepted as a later step", err)
	}
}
