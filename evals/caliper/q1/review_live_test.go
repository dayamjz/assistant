//go:build unix && q1live

package q1_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
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
// agent-ci-review pre-receive, whose review-hook.py starts the claude on PATH
// with its production argv, its filtered environment and a fresh HOME; the
// pusher's environment is the attempt's alone, so the only token it carries is
// the case's: the test's own, an obvious fake, or none. valid and invalid are
// one real call each; absent is none, since the hook refuses before it starts.
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
			if c.token != "" {
				a.env = append(a.env, "CLAUDE_CODE_OAUTH_TOKEN="+c.token)
			}
			goTool, err := exec.LookPath("go")
			must(t, err)
			must(t, os.Remove(filepath.Join(a.root, "stub", "claude"))) // the real claude, not the failing stub
			a.path += ":" + filepath.Dir(goTool) + ":" + filepath.Dir(claude)
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
