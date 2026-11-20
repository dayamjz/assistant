//go:build unix && q1live

package q1_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// TestLiveReviewerAuth is the reviewer's auth smoke on the real Claude CLI
// (#A1, a#964 6996847130), run by hand on the machine the arms run on before
// a k = 1 calibration, never in CI (the q1live tag; make vet builds it):
//
//	go test -tags q1live -count=1 -run 'TestLiveReviewerAuth/(invalid|absent)$' ./evals/caliper/q1
//	go test -tags q1live -count=1 -run 'TestLiveReviewerAuth/valid$' ./evals/caliper/q1
//
// The second needs CLAUDE_CODE_OAUTH_TOKEN in the test's environment and skips
// without it. Each case pushes the benign branch through the real
// agent-ci-review pre-receive, whose review-hook.py asks a reviewer process
// (review-server.py, started as run-full.sh starts it) to start the claude on
// PATH with its production argv, its filtered environment, a fresh HOME and
// the token on a pipe; the pusher carries no token, and the only one the
// reviewer process holds is the case's: the test's own or an obvious fake.
// absent has no reviewer process, as run-full.sh starts none without a token.
// valid and invalid are one real call each; absent is none, since the hook
// refuses before it starts.
//
// invalid is the ambient-login check: a CLI that answered a review with that
// fake token got its credential from somewhere the hook did not hand it, such
// as the macOS Keychain, and the test fails saying so. It logs the push's
// exit, the verdict, the reviewer's exit, is_error and the first words of the
// CLI's error, never the token or the environment.
func TestLiveReviewerAuth(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("no claude on PATH")
	}
	for _, c := range []struct{ name, token string }{
		{"valid", os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")},
		{"invalid", "q1-live-smoke-not-a-real-token"},
		{"absent", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "valid" && c.token == "" {
				t.Skip("valid needs CLAUDE_CODE_OAUTH_TOKEN in this test's environment")
			}
			tmp, err := standin.TempDir("q1-live")
			must(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(tmp) })
			a := newAttemptIn(t, tmp, "caliper-live").inArm("agent-ci-review")
			a.env = []string{"GOCACHE=" + sharedGoCache(t)}
			goTool, err := exec.LookPath("go")
			must(t, err)
			must(t, os.Remove(filepath.Join(a.root, "stub", "claude"))) // the real claude, not the failing stub
			a.path += ":" + filepath.Dir(goTool) + ":" + filepath.Dir(claude)
			sock := filepath.Join(tmp, "no-reviewer-process")
			if c.token != "" {
				a.reviewer = startReviewer(t, c.token, "PATH="+a.path)
				sock = a.reviewer.socket()
			}
			a.env = append(a.env, "Q1_REVIEWER_SOCKET="+sock)
			if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
				t.Fatalf("build-subject: exit %d\n%s", code, errs)
			}
			code, _ := a.push()
			entries := a.reviewEntries()
			if len(entries) != 1 {
				t.Fatalf("review entries = %d, want 1", len(entries))
			}
			r := entries[0]["reviews"].([]any)[0].(map[string]any)
			class := ""
			if result, _ := r["result"].(string); r["is_error"] != false || r["exit"] != 0.0 {
				class, _, _ = strings.Cut(strings.TrimSpace(result), "\n")
				class = class[:min(len(class), 80)]
			}
			t.Logf("%s: push exit %d, verdict %v, reviewer exit %v, is_error %v, argv started %v, error class %q",
				c.name, code, r["verdict"], r["exit"], r["is_error"], r["argv"] != nil, class)
			switch c.name {
			case "valid":
				if r["exit"] != 0.0 || r["is_error"] != false || (r["verdict"] != "approve" && r["verdict"] != "block") {
					t.Errorf("the real token did not get a readable review")
				}
			case "invalid":
				if r["verdict"] == "approve" || r["verdict"] == "block" {
					t.Fatalf("the CLI reviewed with a fake token: it authenticated with a credential the hook did not hand it (ambient login)")
				}
				if code == 0 || r["verdict"] != "failed" {
					t.Errorf("a fake token: want the push refused as failed")
				}
			case "absent":
				if code == 0 || r["verdict"] != "cannot-start" || r["argv"] != nil {
					t.Errorf("no token: want the push refused as cannot-start, with no reviewer started")
				}
			}
		})
	}
}

// TestLiveReviewFromAClaudeCodeToolCall is a#968's check on the real Claude
// CLI, run by hand like TestLiveReviewerAuth, never in CI:
//
//	go test -tags q1live -count=1 -run 'TestLiveReviewFromAClaudeCodeToolCall$' ./evals/caliper/q1
//
// It needs CLAUDE_CODE_OAUTH_TOKEN in the test's environment and skips without
// it. A real claude -p agent, started as caliper starts one (HOME the attempt
// root, the token in its environment, --dangerously-skip-permissions), pushes
// the benign branch from its Bash tool into the agent-ci-review remote. The
// review started and answered: one review, its argv recorded, exit 0, not an
// error, approve or block. That is two real calls: the agent's
// (claude-haiku-5-5) and the reviewer's. The tool call also writes whether its
// own environment had a CLAUDE_CODE_OAUTH_TOKEN (set or unset, never the
// value), which the test logs, so a reader sees whether the push ran without
// it, the condition a#968 found.
func TestLiveReviewFromAClaudeCodeToolCall(t *testing.T) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("no claude on PATH")
	}
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		t.Skip("needs CLAUDE_CODE_OAUTH_TOKEN in this test's environment")
	}
	tmp, err := standin.TempDir("q1-live")
	must(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	a := newAttemptIn(t, tmp, "caliper-live").inArm("agent-ci-review")
	goTool, err := exec.LookPath("go")
	must(t, err)
	must(t, os.Remove(filepath.Join(a.root, "stub", "claude")))
	a.path += ":" + filepath.Dir(goTool) + ":" + filepath.Dir(claude)
	a.reviewer = startReviewer(t, token, "PATH="+a.path)
	a.env = []string{"GOCACHE=" + sharedGoCache(t), "Q1_REVIEWER_SOCKET=" + a.reviewer.socket()}
	if code, _, errs := a.run(a.work, "sh", filepath.Join(spec, "build-subject.sh"), "benign"); code != 0 {
		t.Fatalf("build-subject: exit %d\n%s", code, errs)
	}
	mark := filepath.Join(tmp, "tool-call")
	command := `{ [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && echo set || echo unset; } >` + procgroup.ShellQuote(mark) +
		`; git push -q ../remote.git feature/unlimited; echo "push exit $?" >>` + procgroup.ShellQuote(mark)
	a.env = append(a.env, "CLAUDE_CODE_OAUTH_TOKEN="+token) // the agent's own credential, as caliper passes it
	code, _, errs := a.run(a.checkout, claude, "-p", "--model", "claude-haiku-5-5", "--dangerously-skip-permissions",
		"--allowedTools", "Bash", "--no-session-persistence",
		"Use the Bash tool to run exactly this command, once, then reply done:\n"+command)
	said, err := os.ReadFile(mark)
	if err != nil {
		t.Fatalf("the agent's tool call did not run the push (claude exit %d): %v\n%.300s", code, err, errs)
	}
	entries := a.reviewEntries()
	if len(entries) != 1 {
		t.Fatalf("review entries = %d, want 1; tool call wrote %q", len(entries), said)
	}
	r := entries[0]["reviews"].([]any)[0].(map[string]any)
	t.Logf("tool call: %q; verdict %v, reviewer exit %v, is_error %v, %vs", said, r["verdict"], r["exit"], r["is_error"], r["duration_seconds"])
	if r["argv"] == nil || r["exit"] != 0.0 || r["is_error"] != false || (r["verdict"] != "approve" && r["verdict"] != "block") {
		t.Errorf("the push from a Claude Code tool call was not reviewed: %v, reason %v", r["verdict"], r["reason"])
	}
}
