//go:build linux || darwin

package agents_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// Herdr resolves the herdr binary or skips when none is installed. CI that
// requires herdr sets ASSISTANT_REQUIRE_HERDR so a missing binary fails.
func resolveHerdr(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("herdr")
	if err != nil {
		if os.Getenv("ASSISTANT_REQUIRE_HERDR") != "" {
			t.Fatalf("ASSISTANT_REQUIRE_HERDR is set and no herdr binary was on PATH: %v", err)
		}
		t.Skipf("no herdr binary on PATH, so this herdr test was not run; set ASSISTANT_REQUIRE_HERDR to make that a failure")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolving herdr: %v", err)
	}
	return abs
}

func TestHerdrValidateRefusesRelativePathsAndBadSessionNames(t *testing.T) {
	abs := t.TempDir()
	bin := filepath.Join(abs, "herdr")
	cases := []struct {
		h    agents.Herdr
		want string
	}{
		{agents.Herdr{Binary: "herdr", Session: "w", Root: abs}, "Binary"},
		{agents.Herdr{Binary: bin, Session: "w", Root: "relative"}, "Root"},
		{agents.Herdr{Binary: bin, Session: "", Root: abs}, "Session"},
		{agents.Herdr{Binary: bin, Session: ".", Root: abs}, "Session"},
		{agents.Herdr{Binary: bin, Session: "has space", Root: abs}, "Session"},
		{agents.Herdr{Binary: bin, Session: "has/slash", Root: abs}, "Session"},
	}
	for _, c := range cases {
		err := c.h.Validate()
		if err == nil || !errors.Is(err, agents.ErrInvalidInvocation) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%+v) = %v, want ErrInvalidInvocation naming %s", c.h, err, c.want)
		}
	}
	ok := agents.Herdr{Binary: bin, Session: "worker-1.a_b", Root: abs}
	if err := ok.Validate(); err != nil {
		t.Errorf("Validate of a well-formed Herdr: %v", err)
	}
}

func TestRequireHerdrVersionAcceptsThePinnedRelease(t *testing.T) {
	bin := resolveHerdr(t)
	if err := agents.RequireHerdrVersion(t.Context(), bin); err != nil {
		t.Fatalf("RequireHerdrVersion(%s): %v", bin, err)
	}
}

func TestRequireHerdrVersionRefusesAnotherRelease(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "herdr")
	if err := procgroup.WriteProgram(fake, []byte("#!/bin/sh\necho 'herdr 0.0.0'\n"), 0o755); err != nil {
		t.Fatalf("WriteProgram: %v", err)
	}
	err := agents.RequireHerdrVersion(t.Context(), fake)
	if !errors.Is(err, agents.ErrHerdrVersion) {
		t.Fatalf("RequireHerdrVersion = %v, want ErrHerdrVersion", err)
	}
}

func TestWriteHerdrFilesNamesTheLaunchFileAndRunsOnce(t *testing.T) {
	root := t.TempDir()
	agent := filepath.Join(root, "agent")
	if err := procgroup.WriteProgram(agent, []byte("#!/bin/sh\necho ran >>\"$MARKER\"\n"), 0o755); err != nil {
		t.Fatalf("WriteProgram agent: %v", err)
	}
	h := agents.Herdr{Binary: resolveHerdr(t), Session: "files", Root: filepath.Join(root, "worker")}
	if err := agents.WriteHerdrFiles(h, agent, []string{"--flag", "value"}); err != nil {
		t.Fatalf("WriteHerdrFiles: %v", err)
	}
	cfg, err := os.ReadFile(filepath.Join(h.Root, "config.toml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	launch := filepath.Join(h.Root, "launch")
	if !strings.Contains(string(cfg), "default_shell = \""+launch+"\"") {
		t.Errorf("config %q does not name the launch file as default_shell", cfg)
	}
	if !strings.Contains(string(cfg), `shell_mode = "non_login"`) {
		t.Errorf("config %q does not ask for a non-login shell", cfg)
	}
	body, err := os.ReadFile(launch)
	if err != nil {
		t.Fatalf("reading launch: %v", err)
	}
	if !strings.Contains(string(body), "exec \""+agent+"\" \"--flag\" \"value\"") {
		t.Errorf("launch %q does not exec the agent with its args", body)
	}
	info, err := os.Stat(launch)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Errorf("launch mode = %v, want executable", info.Mode())
	}

	marker := filepath.Join(root, "marker")
	cmd := exec.Command(launch)
	cmd.Env = append(os.Environ(), "MARKER="+marker)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first launch run: %v (%s)", err, out)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "ran\n" {
		t.Fatalf("after first run marker = %q (%v), want ran", got, err)
	}
	if out, err := exec.Command(launch).CombinedOutput(); err != nil {
		t.Fatalf("second launch run: %v (%s)", err, out)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "ran\n" {
		t.Fatalf("after second run marker = %q (%v), want still one ran (run-once)", got, err)
	}
}

func TestHerdrServerEnvDropsAmbientHerdrAndSetsThisWorker(t *testing.T) {
	h := agents.Herdr{Binary: "/usr/bin/herdr", Session: "s", Root: "/tmp/worker"}
	got := agents.HerdrServerEnv([]string{
		"PATH=/bin",
		"HERDR_SOCKET_PATH=/person/herdr.sock",
		"HERDR_ENV=1",
		"XDG_CONFIG_HOME=/person/xdg",
		"KEEP=1",
	}, h)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "HERDR_SOCKET_PATH=") || strings.Contains(joined, "HERDR_ENV=") {
		t.Errorf("ambient HERDR_* still present: %q", got)
	}
	if strings.Contains(joined, "XDG_CONFIG_HOME=/person/xdg") {
		t.Errorf("person's XDG_CONFIG_HOME still present: %q", got)
	}
	if !strings.Contains(joined, "HERDR_CONFIG_PATH="+filepath.Join(h.Root, "config.toml")) {
		t.Errorf("missing this worker's HERDR_CONFIG_PATH in %q", got)
	}
	if !strings.Contains(joined, "XDG_CONFIG_HOME="+filepath.Join(h.Root, "xdg")) {
		t.Errorf("missing this worker's XDG_CONFIG_HOME in %q", got)
	}
	if !strings.Contains(joined, "KEEP=1") || !strings.Contains(joined, "PATH=/bin") {
		t.Errorf("unrelated entries lost: %q", got)
	}
}
