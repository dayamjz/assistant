//go:build unix

package q1_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/journey"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// macTMPDIR is macOS's per-user $TMPDIR as the service resolves it, the
// directory run-full.sh made the arms in when every with-skill service failed
// to bind (#941). A test TMPDIR is padded to at least its length.
const macTMPDIR = "/private/var/folders/k7/1sy1vmfs5qvb036nk10kr8480000gn/T"

// stubCaliper stands in for caliper 0.19.0+q1attemptenv's three commands:
// validate and compare succeed, and run does one keyed attempt (task-001, 1)
// as caliper does. The attempt root is caliper's own call
// (caliper/workdir.py's tempfile.mkdtemp(prefix="caliper-") in the arm's
// TMPDIR), and the hooks run in <root>/work with the runner's environment
// plus the key. In the with-skill arm the agent, whose HOME is the root,
// inits the checkout and starts the service, which is what a gate-using agent
// does first. No model is asked. It exits 2 when a hook failed, as caliper
// does, and writes the result fields gate_by_arms.py reads.
const stubCaliper = `
import json, os, shutil, subprocess, sys, tempfile
args = sys.argv[1:]
with open(os.environ["Q1_TEST_CALLS"], "a") as f:
    f.write(" ".join(args[:1]) + "\n")
if args[0] != "run":
    sys.exit(0)
spec = os.path.dirname(os.path.abspath(args[1]))
ablated = ["assistant"] if "--ablate" in args else []
root = tempfile.mkdtemp(prefix="caliper-")
work = os.path.join(root, "work")
os.mkdir(work)
hook = dict(os.environ, CALIPER_WORKDIR=work, CALIPER_SPEC_DIR=spec, CALIPER_TASK_ID="task-001", CALIPER_ATTEMPT="1")
failed = subprocess.call(["sh", os.path.join(spec, "build-subject.sh"), "benign"], cwd=work, env=hook) != 0
if not ablated:
    agent = {k: v for k, v in os.environ.items() if k != "ASSISTANT_HOME"}
    agent["HOME"] = root
    subprocess.call(["assistant", "--json", "init"], cwd=os.path.join(work, "work"), env=agent)
    subprocess.call(["assistant", "service", "start"], cwd=root, env=agent)
failed |= subprocess.call(["sh", os.path.join(spec, "cleanup.sh")], cwd=work, env=hook) != 0
shutil.rmtree(root)
with open(args[args.index("--output") + 1], "w") as f:
    json.dump({"run": {"ablated": ablated, "interrupted": False},
               "task_results": [{"task_id": "task-001", "attempts": [{"attempt": 1, "outcome": "pass"}]}]}, f)
sys.exit(2 if failed else 0)
`

// fullRun is run-full.sh in a copy of its repository layout: this directory's
// files at <repo>/evals/caliper/q1 (so the committed results it rewrites are
// the copy's), the built binary at <repo>/bin/assistant, the stand-in caliper
// in a real virtual environment whose metadata says 0.19.0+q1attemptenv (the
// version run-full.sh asks the interpreter of), and a claude that is logged in.
type fullRun struct {
	t                           *testing.T
	root, spec, tmpdir, calls   string
	env                         []string
	code                        int
	stderr, arms, gateByArmFile string
}

func newFullRun(t *testing.T) *fullRun {
	t.Helper()
	root := armDir(t)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	r := &fullRun{t: t, root: root, spec: filepath.Join(root, "repo", "evals", "caliper", "q1"), calls: filepath.Join(root, "calls")}
	r.tmpdir = filepath.Join(root, "tmp"+strings.Repeat("x", max(0, len(macTMPDIR)-len(root)-4)))
	bin, err := journey.Binary()
	must(t, err)
	for _, dir := range []string{r.spec, filepath.Join(root, "repo", "bin"), r.tmpdir, filepath.Join(root, "home"), filepath.Join(root, "stub")} {
		must(t, os.MkdirAll(dir, 0o755))
	}
	entries, err := os.ReadDir(spec)
	must(t, err)
	for _, e := range entries {
		if e.Type().IsRegular() {
			data, err := os.ReadFile(filepath.Join(spec, e.Name()))
			must(t, err)
			must(t, os.WriteFile(filepath.Join(r.spec, e.Name()), data, 0o644))
		}
	}
	must(t, os.Symlink(bin, filepath.Join(root, "repo", "bin", "assistant")))
	python, err := exec.LookPath("python3")
	must(t, err)
	goTool, err := exec.LookPath("go") // go test puts its own GOROOT/bin on PATH; run-full.sh reads the binary's stamp with it
	must(t, err)
	git, err := exec.LookPath("git")
	must(t, err)
	sh, err := exec.LookPath("sh") // the sh run-full.sh runs under, for every script it and the stand-in run
	must(t, err)
	venv := filepath.Join(root, "venv")
	must(t, procgroup.WriteProgram(filepath.Join(root, "stub", "claude"),
		[]byte("#!/bin/sh\n[ \"$1 $2\" = 'auth status' ] && echo '{\"loggedIn\": true}'\n"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "gitconfig"), nil, 0o644))
	r.env = []string{"HOME=" + filepath.Join(root, "home"), "TMPDIR=" + r.tmpdir, "CALIPER_VENV=" + venv, "Q1_K=1", "Q1_TEST_CALLS=" + r.calls,
		"PATH=" + strings.Join([]string{filepath.Join(root, "stub"), filepath.Dir(sh), filepath.Dir(goTool), filepath.Dir(git), filepath.Dir(python), "/usr/bin:/bin:/usr/sbin:/sbin"}, ":"),
		"GIT_CONFIG_GLOBAL=" + filepath.Join(root, "gitconfig"), "GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0", "GIT_CONFIG_KEY_1=maintenance.auto", "GIT_CONFIG_VALUE_1=false"}
	r.command(root, python, "-m", "venv", "--without-pip", venv)
	sites, err := filepath.Glob(filepath.Join(venv, "lib", "python3*", "site-packages"))
	must(t, err)
	if len(sites) != 1 {
		t.Fatalf("site-packages in the virtual environment: %v", sites)
	}
	dist := filepath.Join(sites[0], "caliper_eval-0.19.0+q1attemptenv.dist-info")
	must(t, os.Mkdir(dist, 0o755))
	must(t, os.WriteFile(filepath.Join(dist, "METADATA"), []byte("Metadata-Version: 2.1\nName: caliper-eval\nVersion: 0.19.0+q1attemptenv\n"), 0o644))
	must(t, procgroup.WriteProgram(filepath.Join(venv, "bin", "caliper"), []byte("#!"+filepath.Join(venv, "bin", "python")+"\n"+stubCaliper), 0o755))
	return r
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// command runs a setup step and fails the test unless it exits 0.
func (r *fullRun) command(dir, name string, args ...string) {
	r.t.Helper()
	cmd := procgroup.Command(name, args...)
	cmd.Dir, cmd.Env = dir, r.env
	if out, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// armsLine is the stderr line run-full.sh prints as it makes its arms directory.
var armsLine = regexp.MustCompile(`arms \(each arm's TMPDIR\): (\S+)`)

// run runs run-full.sh with extra environment, and removes the arms directory
// it names when the test ends, wherever run-full.sh made it.
func (r *fullRun) run(extra ...string) {
	r.t.Helper()
	cmd := procgroup.Command("sh", filepath.Join(r.spec, "run-full.sh"))
	cmd.Dir, cmd.Env = r.root, append(append([]string{}, r.env...), extra...)
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stderr, &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); err != nil && !errors.As(err, &exit) {
		r.t.Fatalf("run-full.sh: %v", err)
	}
	r.code, r.stderr = cmd.ProcessState.ExitCode(), stderr.String()
	if m := armsLine.FindStringSubmatch(r.stderr); m != nil {
		r.arms, r.gateByArmFile = m[1], filepath.Join(m[1], "gate-by-arm.json")
		r.t.Cleanup(func() { _ = os.RemoveAll(r.arms) })
	}
}

// TestRunFullStartsTheAgentsServiceUnderALongTMPDIR is #941's reproduction:
// with $TMPDIR as long as macOS's, run-full.sh put each arm under it, every
// with-skill attempt's socket path was too long to bind, the service never
// answered, and the record was indeterminate with caliper exiting 2. Here the
// with-skill attempt's service answers at cleanup, so its record is judged
// (never-reached: the agent here pushes nothing), and the run exits 0.
func TestRunFullStartsTheAgentsServiceUnderALongTMPDIR(t *testing.T) {
	r := newFullRun(t)
	r.run()
	if r.code != 0 || r.gateByArmFile == "" {
		t.Fatalf("run-full.sh exit %d, want 0; output:\n%s", r.code, r.stderr)
	}
	var report struct {
		AsAssigned map[string]struct{ Attempts []armAttempt } `json:"as_assigned"`
	}
	data, err := os.ReadFile(r.gateByArmFile)
	must(t, err)
	must(t, json.Unmarshal(data, &report))
	for arm, state := range map[string]string{"with-skill": "never-reached", "ablated": "no-home"} {
		got := report.AsAssigned[arm].Attempts
		if len(got) != 1 || got[0].GateObservation.Validity != "valid" || got[0].GateObservation.State != state {
			t.Errorf("%s attempts = %+v, want one valid %s", arm, got, state)
		}
	}
}

// TestRunFullRefusesArmsWhereAServiceCannotListen is the guard: when
// Q1_ARMS_ROOT puts the arms where a service at an attempt's depth cannot
// answer, run-full.sh refuses (exit 2) before caliper runs an attempt and says
// why, rather than letting every gate-using record read indeterminate.
// TestRunFullStartsTheAgentsServiceUnderALongTMPDIR is its positive control.
func TestRunFullRefusesArmsWhereAServiceCannotListen(t *testing.T) {
	r := newFullRun(t)
	r.run("Q1_ARMS_ROOT=" + r.tmpdir)
	if r.code != 2 || !strings.Contains(r.stderr, "did not start") {
		t.Fatalf("run-full.sh exit %d, want 2 with the refusal; output:\n%s", r.code, r.stderr)
	}
	calls, err := os.ReadFile(r.calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "run") {
		t.Errorf("caliper was asked to run after the refusal; calls:\n%s", calls)
	}
	arms, err := filepath.Glob(filepath.Join(r.tmpdir, "q1-arms.*"))
	must(t, err)
	if len(arms) != 1 {
		t.Fatalf("arms directories under Q1_ARMS_ROOT: %v", arms)
	}
	logs, _ := filepath.Glob(filepath.Join(arms[0], "with-skill", "caliper-????????", ".assistant", "logs", "service.log"))
	if len(logs) != 1 {
		t.Fatalf("the refused probe home is not kept for inspection: %v", logs)
	}
	if log, err := os.ReadFile(logs[0]); err != nil || !strings.Contains(string(log), "could not start") {
		t.Errorf("the probe's service log does not say it could not start: %v\n%s", err, log)
	}
}
