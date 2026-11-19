//go:build unix

package q1_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
// the operator's global config as a 0444 file at rel under the run's HOME:
// ~/.config/git/config is where the Mac Studio's is. HOME itself stays
// writable, so git can still replace the file rather than refuse to write it;
// snapshot's inode is what shows that it did. It returns the file's path.
func (r *fullRun) operatorConfig(rel, content string) []string {
	r.t.Helper()
	env := r.env[:0:0]
	for _, kv := range r.env {
		if !strings.HasPrefix(kv, "GIT_CONFIG_GLOBAL=") {
			env = append(env, kv)
		}
	}
	r.env = env
	file := filepath.Join(r.root, "home", rel)
	must(r.t, os.MkdirAll(filepath.Dir(file), 0o755))
	must(r.t, os.WriteFile(file, []byte(content), 0o444))
	return []string{file}
}

// snapshot is each path's inode, mode, link target and bytes: a file git
// replaced has a new inode even when its bytes and mode came back.
func snapshot(t *testing.T, paths []string) []string {
	t.Helper()
	var got []string
	for _, p := range paths {
		info, err := os.Lstat(p)
		must(t, err)
		target, _ := os.Readlink(p)
		data, err := os.ReadFile(p)
		must(t, err)
		got = append(got, fmt.Sprintf("%s inode %d %v -> %q: %q", p, info.Sys().(*syscall.Stat_t).Ino, info.Mode(), target, data))
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

// TestRunFullLeavesAReadOnlyGlobalConfigAlone is a#942 with the operator's
// global config a read-only (0444) ~/.config/git/config. Every setup hook's
// git config --global wrote that file: git replaced it (a new inode, its bytes
// back after cleanup). Where the file cannot be replaced, as behind the Mac
// Studio's read-only link, the write failed and no attempt ran (the a#939
// proof run); that shape needs a read-only directory, which this test does
// not make. Now both arms' setup succeeds, its git reads the operator's
// config through the run's scratch file and takes the subject rewrite from
// it, the operator's file is the same file with the same bytes, and the
// scratch file is gone.
func TestRunFullLeavesAReadOnlyGlobalConfigAlone(t *testing.T) {
	r := newFullRun(t)
	r.standInGraders()
	real := r.operatorConfig(filepath.Join(".config", "git", "config"), operatorMarker)
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
			real := r.operatorConfig(".gitconfig", operatorMarker)
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
			real := r.operatorConfig(".gitconfig", c.content)
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
