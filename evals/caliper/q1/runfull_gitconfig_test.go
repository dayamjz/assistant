//go:build unix

package q1_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are a#942: run-full.sh never writes the operator's global git
// config. Each runs run-full.sh (newFullRun, with the stand-in caliper and
// claude, no model, no network) with GIT_CONFIG_GLOBAL unset, so git finds the
// operator's global config under the run's HOME as it does on a machine. Every
// process a run starts is stopped before it returns (cleanup.sh stops the
// attempt's service, run-full.sh its probe's), and TestMain's
// standin.RunOwned fails the package if one is still running.

// operatorMarker is a key only the operator's own config sets, so a hook's
// git that reads it reads that config through the run's scratch file.
const operatorMarker = "[q1test]\n\tmarker = operator\n"

// operatorConfig drops GIT_CONFIG_GLOBAL from the run's environment and writes
// the operator's global config: with homeManager, as a home-manager install
// does, a link at ~/.config/git/config into a read-only directory (the Mac
// Studio's, where every setup hook's write failed); otherwise a 0444
// ~/.gitconfig in a writable HOME, which git rewrites in place, so only the
// bytes tell. It returns the config's paths for snapshot.
func (r *fullRun) operatorConfig(homeManager bool, content string) []string {
	r.t.Helper()
	env := r.env[:0:0]
	for _, kv := range r.env {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
			env = append(env, kv)
		}
	}
	r.env = env
	home := filepath.Join(r.root, "home")
	if !homeManager {
		must(r.t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(content), 0o444))
		return []string{filepath.Join(home, ".gitconfig")}
	}
	store, link := filepath.Join(r.root, "store"), filepath.Join(home, ".config", "git", "config")
	must(r.t, os.MkdirAll(store, 0o755))
	must(r.t, os.MkdirAll(filepath.Dir(link), 0o755))
	must(r.t, os.WriteFile(filepath.Join(store, "git-config"), []byte(content), 0o444))
	takeWritesOff(r.t, store)
	must(r.t, os.Symlink(filepath.Join(store, "git-config"), link))
	return []string{link, filepath.Join(store, "git-config")}
}

// takeWritesOff makes dir, a directory, read-only until the test ends, when
// its write permission is put back so armDir's RemoveAll can empty it.
func takeWritesOff(t *testing.T, dir string) {
	t.Helper()
	must(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// snapshot is each path's mode, link target and bytes.
func snapshot(t *testing.T, paths []string) []string {
	t.Helper()
	var got []string
	for _, p := range paths {
		info, err := os.Lstat(p)
		must(t, err)
		target, _ := os.Readlink(p)
		data, err := os.ReadFile(p)
		must(t, err)
		got = append(got, fmt.Sprintf("%s %v -> %q: %q", p, info.Mode(), target, data))
	}
	return got
}

// hookSaw is one line the stand-in caliper appended to Q1_TEST_HOOK: what the
// setup hook's git saw once setup returned.
type hookSaw struct{ Global, Marker, Remote, URL string }

// hooks reads Q1_TEST_HOOK's lines, and checks that each hook's global config
// was this run's scratch file under TMPDIR, so wantScratchGone looks where
// run-full.sh made it.
func (r *fullRun) hooks(file string) []hookSaw {
	r.t.Helper()
	f, err := os.Open(file)
	must(r.t, err)
	defer f.Close()
	var saw []hookSaw
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		var h hookSaw
		must(r.t, json.Unmarshal(lines.Bytes(), &h))
		if !strings.HasPrefix(h.Global, filepath.Join(r.tmpdir, "q1-gitconfig.")) {
			r.t.Errorf("the hook's GIT_CONFIG_GLOBAL is %q, not a scratch file under %s", h.Global, r.tmpdir)
		}
		saw = append(saw, h)
	}
	must(r.t, lines.Err())
	return saw
}

// wantScratchGone checks that no scratch global config is left under TMPDIR.
func (r *fullRun) wantScratchGone() {
	r.t.Helper()
	left, err := filepath.Glob(filepath.Join(r.tmpdir, "q1-gitconfig.*"))
	must(r.t, err)
	if len(left) != 0 {
		r.t.Errorf("scratch global config left after run-full.sh exited: %v", left)
	}
}

// TestRunFullLeavesAReadOnlyGlobalConfigAlone is a#942's reproduction: with
// the operator's global config a link into a read-only directory, every
// setup hook's git config --global failed, so no attempt ran (the a#939 proof
// run). Now both arms' setup succeeds, its git reads the operator's config
// through the run's scratch file and takes the subject rewrite from it, the
// operator's config is byte for byte as it was, and the scratch file is gone.
func TestRunFullLeavesAReadOnlyGlobalConfigAlone(t *testing.T) {
	r := newFullRun(t)
	r.standInGraders()
	real := r.operatorConfig(true, operatorMarker)
	before := snapshot(t, real)
	hookFile := filepath.Join(r.root, "hooks.jsonl")
	r.run("Q1_TEST_HOOK=" + hookFile)
	if r.code != 0 {
		t.Fatalf("run-full.sh exit %d, want 0; output:\n%s", r.code, r.stderr)
	}
	saw := r.hooks(hookFile)
	want(t, "setup hooks run (one per arm)", len(saw), 2)
	for _, h := range saw {
		want(t, "the operator's key as the hook's git reads it", h.Marker, "operator")
		want(t, "the subject URL as the hook's git rewrites it", h.URL, h.Remote)
	}
	want(t, "the operator's global config", snapshot(t, real), before)
	r.wantScratchGone()
}

// TestRunFullLeavesTheGlobalConfigAloneWhenARunStops: a run whose attempt
// stops after setup and before cleanup, by a crash or by an operator's
// SIGTERM to run-full.sh, left the subject rewrite in the operator's global
// config, which a 0444 mode does not stop git rewriting. Now that config's
// bytes are unchanged and the scratch file is gone either way; an interrupted
// run-full.sh exits 143 through its trap.
func TestRunFullLeavesTheGlobalConfigAloneWhenARunStops(t *testing.T) {
	for stop, code := range map[string]int{"crash": 1, "interrupt": 143} {
		t.Run(stop, func(t *testing.T) {
			r := newFullRun(t)
			r.standInGraders()
			real := r.operatorConfig(false, operatorMarker)
			before := snapshot(t, real)
			hookFile := filepath.Join(r.root, "hooks.jsonl")
			r.run("Q1_TEST_HOOK="+hookFile, "Q1_TEST_AFTER_SETUP="+stop)
			if r.code != code {
				t.Errorf("run-full.sh exit %d, want %d; output:\n%s", r.code, code, r.stderr)
			}
			if saw := r.hooks(hookFile); len(saw) == 0 || saw[0].URL != saw[0].Remote {
				t.Errorf("setup did not install the subject rewrite before the stop: %+v", saw)
			}
			want(t, "the operator's global config", snapshot(t, real), before)
			r.wantScratchGone()
		})
	}
}

// TestRunFullRefusesARewriteInTheOperatorsConfig: a rewrite of the subject URL
// that a run before a#942 left in the operator's own config would win over
// each attempt's, and clearing it would write that config, so run-full.sh
// refuses (exit 2) before graders or caliper run and names it. A config git
// cannot read is refused the same way rather than run past.
// TestRunFullLeavesAReadOnlyGlobalConfigAlone is the positive control.
func TestRunFullRefusesARewriteInTheOperatorsConfig(t *testing.T) {
	for name, c := range map[string]struct{ content, says string }{
		"stale rewrite": {"[url \"/gone/remote.git\"]\n\tinsteadOf = https://github.com/dayamjz/caliper-q1.git\n", "remove it and rerun"},
		"unreadable":    {"[q1test\n", "cannot read your global git config"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newFullRun(t)
			r.standInGraders()
			real := r.operatorConfig(false, c.content)
			before := snapshot(t, real)
			r.run()
			if r.code != 2 || !strings.Contains(r.stderr, c.says) {
				t.Errorf("run-full.sh exit %d, want 2 saying %q; output:\n%s", r.code, c.says, r.stderr)
			}
			if name == "stale rewrite" && !strings.Contains(r.stderr, "file:"+real[0]) {
				t.Errorf("the refusal does not name %s:\n%s", real[0], r.stderr)
			}
			for _, ran := range []string{"graders.calls", "calls"} {
				if _, err := os.Stat(filepath.Join(r.root, ran)); !os.IsNotExist(err) {
					t.Errorf("%s exists after the refusal: %v", ran, err)
				}
			}
			want(t, "the operator's global config", snapshot(t, real), before)
			r.wantScratchGone()
		})
	}
}
