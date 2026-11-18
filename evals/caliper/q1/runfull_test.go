//go:build unix

package q1_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
// plus the key. In the full-gate arm the agent runs with the environment
// caliper builds for it and nothing else (caliper/harness/base.py
// _isolated_env: HOME is the root, then PATH and env_passthrough, with
// claude_code.py's token added). It writes what its git resolves origin to
// and where the rewrite came from to $Q1_TEST_AGENT, inits the checkout and
// starts the service, which is what a gate-using agent does first, and, only
// when init recorded the local remote as the upstream so github.com is never
// asked, starts a run, which the service fetches that upstream's main for. No
// model is asked. It exits 2 when a hook failed, as caliper does, and writes
// the result fields gate_by_arms.py reads. With Q1_TEST_HOOK set, it appends
// what the setup hook's git saw to that file once setup returns; with
// Q1_TEST_AFTER_SETUP set it then stops without cleanup, exiting 3 ("crash")
// or, for "interrupt", after sending run-full.sh the SIGTERM an operator's
// kill sends (runfull_gitconfig_test.go).
const stubCaliper = `
import json, os, shutil, signal, subprocess, sys, tempfile
args = sys.argv[1:]
with open(os.environ["Q1_TEST_CALLS"], "a") as f:
    f.write(" ".join(args) + "\n")
if args[0] != "run":
    sys.exit(0)
spec = os.path.dirname(os.path.abspath(args[1]))
ablated = ["assistant"] if "--ablate" in args else []
root = tempfile.mkdtemp(prefix="caliper-")
work = os.path.join(root, "work")
os.mkdir(work)
hook = dict(os.environ, CALIPER_WORKDIR=work, CALIPER_SPEC_DIR=spec, CALIPER_TASK_ID="task-001", CALIPER_ATTEMPT="1")
failed = subprocess.call(["sh", os.path.join(spec, "build-subject.sh"), "benign"], cwd=work, env=hook) != 0
if "Q1_TEST_HOOK" in os.environ:
    def hook_git(*argv):
        return subprocess.run(["git", *argv], cwd=work, env=hook, stdin=subprocess.DEVNULL, capture_output=True, text=True).stdout.strip()
    with open(os.environ["Q1_TEST_HOOK"], "a") as f:
        f.write(json.dumps({"global": os.environ.get("GIT_CONFIG_GLOBAL", ""), "marker": hook_git("config", "--get", "q1test.marker"),
                            "remote": os.path.realpath(os.path.join(work, "remote.git")),
                            "url": os.path.realpath(hook_git("ls-remote", "--get-url", "https://github.com/dayamjz/caliper-q1.git") or "/")}) + "\n")
if os.environ.get("Q1_TEST_AFTER_SETUP"):
    if os.environ["Q1_TEST_AFTER_SETUP"] == "interrupt":
        os.kill(os.getppid(), signal.SIGTERM)
    shutil.rmtree(root)
    sys.exit(3)
if not ablated:
    agent = {"HOME": root, "PATH": os.environ["PATH"]}
    agent.update({k: os.environ[k] for k in ("LANG", "LC_ALL", "TERM", "TMPDIR", "CLAUDE_CODE_OAUTH_TOKEN") if k in os.environ})
    checkout = os.path.join(work, "work")
    def out(argv, cwd=checkout, env=agent):
        p = subprocess.run(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True)
        return {"exit": p.returncode, "stdout": p.stdout.strip(), "stderr": p.stderr.strip()}
    seen = {"remote": os.path.realpath(os.path.join(work, "remote.git")),
            "main": out(["git", "rev-parse", "main"], cwd=os.path.join(work, "remote.git"), env=dict(os.environ))["stdout"],
            "url": out(["git", "ls-remote", "--get-url", "origin"]),
            "origins": out(["git", "config", "--show-origin", "--get-regexp", r"^url\."])}
    # Resolved while the attempt root exists: it is removed below, as caliper removes it.
    seen["url"]["real"] = os.path.realpath(seen["url"]["stdout"] or "/")
    seen["origins"]["real"] = [os.path.realpath(line.split("\t")[0].removeprefix("file:"))
                               for line in seen["origins"]["stdout"].splitlines()]
    seen["attempt_gitconfig"] = os.path.realpath(os.path.join(root, ".gitconfig"))
    seen["init"] = out(["assistant", "--json", "init"])
    try:
        seen["init"]["upstream"] = os.path.realpath(json.loads(seen["init"]["stdout"])["repository"]["upstream_url"])
    except (ValueError, KeyError, TypeError):
        seen["init"]["upstream"] = None
    subprocess.call(["assistant", "service", "start"], cwd=root, env=agent)
    if seen["init"]["upstream"] == seen["remote"]:
        seen["intent"] = out(["assistant", "--json", "--intent", "ship the branch"])
    with open(os.environ["Q1_TEST_AGENT"], "w") as f:
        json.dump(seen, f)
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
	agent                       string // what the stand-in agent saw (stubCaliper)
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
	r := &fullRun{t: t, root: root, spec: filepath.Join(root, "repo", "evals", "caliper", "q1"), calls: filepath.Join(root, "calls"), agent: filepath.Join(root, "agent.json")}
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
	// The subject's fix role names cursor: a stand-in that fails at once, as
	// cleanup_test.go's, so the gate admits a run and no model is asked.
	must(t, procgroup.WriteProgram(filepath.Join(root, "stub", "cursor"), []byte("#!/bin/sh\nexit 1\n"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "gitconfig"), nil, 0o644))
	r.env = []string{"HOME=" + filepath.Join(root, "home"), "TMPDIR=" + r.tmpdir, "CALIPER_VENV=" + venv, "Q1_K=1", "Q1_TEST_CALLS=" + r.calls, "Q1_TEST_AGENT=" + r.agent,
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

// standInGraders replaces the copy's verify_graders.py with a stand-in that
// records each call and passes, for a test whose subject is not the graders:
// the real script builds eight subjects cold (README "CI"), and
// TestRunFullRunsEachArmUnderItsOwnTMPDIR runs it.
func (r *fullRun) standInGraders() {
	r.t.Helper()
	calls := filepath.Join(r.root, "graders.calls")
	must(r.t, os.WriteFile(filepath.Join(r.spec, "verify_graders.py"),
		[]byte("import os\nwith open("+strconv.Quote(calls)+", \"a\") as f:\n    f.write(os.getcwd() + \"\\n\")\n"), 0o644))
}

// wantGradersCalled checks that run-full.sh ran the copy's verify_graders.py,
// the stand-in standInGraders wrote, once and from the copy.
func (r *fullRun) wantGradersCalled() {
	r.t.Helper()
	data, err := os.ReadFile(filepath.Join(r.root, "graders.calls"))
	if err != nil && !os.IsNotExist(err) {
		r.t.Fatal(err)
	}
	want(r.t, "verify_graders.py calls (each its cwd)", string(data), r.spec+"\n")
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
// (ran: the agent's run is held, its head checked while the checkout existed),
// and the run exits 0.
func TestRunFullStartsTheAgentsServiceUnderALongTMPDIR(t *testing.T) {
	r := newFullRun(t)
	r.standInGraders()
	r.run()
	if r.code != 0 || r.gateByArmFile == "" {
		t.Fatalf("run-full.sh exit %d, want 0; output:\n%s", r.code, r.stderr)
	}
	r.wantGradersCalled()
	var report struct {
		AsAssigned map[string]struct{ Attempts []armAttempt } `json:"as_assigned"`
	}
	data, err := os.ReadFile(r.gateByArmFile)
	must(t, err)
	must(t, json.Unmarshal(data, &report))
	for arm, state := range map[string]string{"full-gate": "ran", "agent-ci": "no-home"} {
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
// The why is the start's own refusal, kept in the arms' probe.log: since #946
// a start refuses a home whose socket path is too long before it launches or
// creates anything, naming the path, its length and the platform's limit, so
// there is no probe home or service log to keep.
func TestRunFullRefusesArmsWhereAServiceCannotListen(t *testing.T) {
	r := newFullRun(t)
	r.standInGraders()
	r.run("Q1_ARMS_ROOT=" + r.tmpdir)
	if r.code != 2 || !strings.Contains(r.stderr, "did not start") {
		t.Fatalf("run-full.sh exit %d, want 2 with the refusal; output:\n%s", r.code, r.stderr)
	}
	r.wantGradersCalled()
	calls, err := os.ReadFile(r.calls)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains("\n"+string(calls), "\nrun ") {
		t.Errorf("caliper was asked to run after the refusal; calls:\n%s", calls)
	}
	arms, err := filepath.Glob(filepath.Join(r.tmpdir, "q1-arms.*"))
	must(t, err)
	if len(arms) != 1 {
		t.Fatalf("arms directories under Q1_ARMS_ROOT: %v", arms)
	}
	roots, _ := filepath.Glob(filepath.Join(arms[0], "full-gate", "caliper-????????"))
	if len(roots) != 1 {
		t.Fatalf("the refused probe's root is not kept for inspection: %v", roots)
	}
	limit, ok := map[string]int{"darwin": 103, "linux": 107}[runtime.GOOS]
	if !ok {
		t.Skipf("%s is not a platform the product serves a local socket on", runtime.GOOS)
	}
	socket := filepath.Join(roots[0], ".assistant", "socket")
	named := fmt.Sprintf("%s is %d bytes, and this platform allows at most %d", socket, len(socket), limit)
	if log, err := os.ReadFile(filepath.Join(arms[0], "probe.log")); err != nil || !strings.Contains(string(log), named) {
		t.Errorf("the probe's start did not say why it refused, %q (%v):\n%s", named, err, log)
	}
}

// TestRunFullGivesTheAgentAndItsServiceTheSubjectRemote is #941's finding B.
// The agent runs with only the environment caliper gives it (stubCaliper),
// and the operator side with a scratch HOME and GIT_CONFIG_GLOBAL, so nothing
// here reads or writes the operator's real git configuration. The agent's git
// resolves origin to the attempt's local remote, from <attempt
// root>/.gitconfig alone. The service does not read the rewrite: init, run by
// the agent, records origin as git expands it, so the upstream the run's
// configuration is fetched from is that remote, and the run resolves its
// configuration against that remote's main. Before the rewrite
// was written to the agent's home, origin resolved to github.com and the run
// was refused fetching main from the upstream (task-003 of the a#945 proof).
// The operator's own GIT_CONFIG_GLOBAL is only read (a#942): it is as empty
// after the run as before, and the operator HOME gets no config.
func TestRunFullGivesTheAgentAndItsServiceTheSubjectRemote(t *testing.T) {
	r := newFullRun(t)
	r.standInGraders()
	r.run()
	if r.code != 0 || r.arms == "" {
		t.Fatalf("run-full.sh exit %d, want 0; output:\n%s", r.code, r.stderr)
	}
	r.wantGradersCalled()
	var seen struct {
		Remote, Main         string
		AttemptGitconfig     string `json:"attempt_gitconfig"`
		URL, Origins, Intent *struct {
			Exit           int
			Stdout, Stderr string
			Real           any // the resolved path, or for origins each file's
		}
		Init *struct {
			Exit     int
			Stderr   string
			Upstream *string // upstream_url as init recorded it, resolved
		}
	}
	data, err := os.ReadFile(r.agent)
	must(t, err)
	must(t, json.Unmarshal(data, &seen))
	if seen.URL == nil || seen.URL.Exit != 0 || seen.URL.Stdout == "" {
		t.Fatalf("the agent's git did not resolve origin: %+v", seen.URL)
	}
	if seen.URL.Real != seen.Remote {
		t.Fatalf("the agent's git resolves origin to %q, want the attempt's remote %q", seen.URL.Stdout, seen.Remote)
	}
	if fmt.Sprint(seen.Origins.Real) != fmt.Sprint([]string{seen.AttemptGitconfig}) {
		t.Errorf("the agent's url config comes from %v, want only the attempt's %s:\n%s", seen.Origins.Real, seen.AttemptGitconfig, seen.Origins.Stdout)
	}
	if seen.Init == nil || seen.Init.Upstream == nil || *seen.Init.Upstream != seen.Remote {
		t.Fatalf("init did not record the attempt's remote %q as the upstream: %+v", seen.Remote, seen.Init)
	}
	if seen.Intent == nil || seen.Intent.Exit != 0 {
		t.Fatalf("the agent's run did not start: %+v", seen.Intent)
	}
	var started struct{ Record struct{ ID string } }
	must(t, json.Unmarshal([]byte(seen.Intent.Stdout), &started))
	records, err := filepath.Glob(filepath.Join(r.armsWithSkill(), "caliper-q1-gate", "caliper-*.record.json"))
	must(t, err)
	if len(records) != 1 {
		t.Fatalf("with-skill records: %v", records)
	}
	var rec struct {
		Runs struct {
			Output struct {
				Runs []struct {
					ID, Base         string
					ConfigRejections *[]string `json:"config_rejections"`
					NoCodeHost       bool      `json:"no_code_host"`
				}
			}
		}
	}
	data, err = os.ReadFile(records[0])
	must(t, err)
	must(t, json.Unmarshal(data, &rec))
	runs := rec.Runs.Output.Runs
	// A known config_rejections is a run whose own configuration resolved, from
	// main as the service fetched it; base is that main, this attempt's commit.
	if len(runs) != 1 || runs[0].ID != started.Record.ID || runs[0].ConfigRejections == nil || runs[0].Base != seen.Main {
		t.Fatalf("runs = %+v, want run %s with its configuration resolved against main %s", runs, started.Record.ID, seen.Main)
	}
	// The recorded upstream is a local path, so the run has no code host
	// (README "Subject remote"): the price of a rewrite init can see.
	want(t, "no_code_host", runs[0].NoCodeHost, true)
	if data, err := os.ReadFile(filepath.Join(r.root, "gitconfig")); err != nil || len(data) != 0 {
		t.Errorf("the operator's GIT_CONFIG_GLOBAL was written: %v\n%s", err, data)
	}
	if _, err := os.Stat(filepath.Join(r.root, "home", ".gitconfig")); !os.IsNotExist(err) {
		t.Errorf("the operator HOME got a .gitconfig: %v", err)
	}
}

// armsWithSkill is the full-gate arm's TMPDIR, where caliper made its attempt root.
func (r *fullRun) armsWithSkill() string { return filepath.Join(r.arms, "full-gate") }

// TestRunFullRunsEachArmUnderItsOwnTMPDIR: run-full.sh runs caliper once per arm
// of README "Arms", full-gate with the skill and agent-ci with it ablated, each
// on the owner's pinned model (a#896) and under its own TMPDIR with Q1_ARM set,
// so each arm's record says its arm, carries the CI evidence its setup wrote
// (no push, so not invoked), and the report has exactly those arms. It is the
// one run-full.sh test that runs the real verify_graders.py, which must pass
// before any arm runs; the others stand it in (standInGraders).
func TestRunFullRunsEachArmUnderItsOwnTMPDIR(t *testing.T) {
	r := newFullRun(t)
	r.run()
	if r.code != 0 || r.gateByArmFile == "" {
		t.Fatalf("run-full.sh exit %d, want 0; output:\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "all grader checks passed") {
		t.Errorf("run-full.sh did not run the real verify_graders.py to its end; output:\n%s", r.stderr)
	}
	calls, err := os.ReadFile(r.calls)
	must(t, err)
	var runs []string
	for _, line := range strings.Split(string(calls), "\n") {
		if strings.HasPrefix(line, "run ") {
			runs = append(runs, line)
		}
	}
	model := "--model claude-code:claude-sonnet-5-5 --judge-model claude-code:claude-sonnet-5-5"
	want(t, "caliper runs", runs, []string{
		"run q1.eval.yaml --k 1 --timeout 1200 --workers 1 " + model + " --output result-q1-full-gate.json",
		"run q1.eval.yaml --k 1 --timeout 1200 --workers 1 " + model + " --ablate assistant --output result-q1-agent-ci.json"})
	var report struct {
		AsAssigned map[string]struct {
			Attempts []struct {
				CIExecution struct{ Verdict string } `json:"ci_execution"`
			}
		} `json:"as_assigned"`
	}
	data, err := os.ReadFile(r.gateByArmFile)
	must(t, err)
	must(t, json.Unmarshal(data, &report))
	want(t, "report arms", len(report.AsAssigned), 2)
	for _, arm := range []string{"agent-ci", "full-gate"} {
		records, err := filepath.Glob(filepath.Join(r.arms, arm, "caliper-q1-gate", "caliper-*.record.json"))
		must(t, err)
		var rec struct{ Arm string }
		if len(records) == 1 {
			data, err = os.ReadFile(records[0])
			must(t, err)
			must(t, json.Unmarshal(data, &rec))
		}
		want(t, arm+" records, their arm", []any{len(records), rec.Arm}, []any{1, arm})
		got := report.AsAssigned[arm].Attempts
		if len(got) != 1 || got[0].CIExecution.Verdict != "not invoked" {
			t.Errorf("%s attempts = %+v, want one whose CI was not invoked", arm, got)
		}
		if _, err := os.Stat(filepath.Join(r.spec, "result-q1-"+arm+".json")); err != nil {
			t.Errorf("%s result: %v", arm, err)
		}
	}
}
