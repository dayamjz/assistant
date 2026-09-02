package vcs_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Every invocation must address its repository explicitly and must be unable
// to wait for a person. Both land in the argument vector and the environment
// of the child process, so the stand-in git is what makes them observable.
func TestEveryInvocationIsExplicitAndNonInteractive(t *testing.T) {
	gitEnvironment(t)
	logPath, exe := useFakeGit(t)

	// An inherited environment that would redirect git elsewhere, inject
	// configuration, or re-enable a prompt. A process launched from a git hook
	// has the first three set.
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "somewhere-else.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "index"))
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath=/tmp/evil'")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/tmp/evil")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	t.Setenv("GIT_EDITOR", "vim")
	t.Setenv("GIT_ASKPASS", "/usr/bin/graphical-askpass")
	t.Setenv("DISPLAY", ":0")

	barePath := fakeBareDir(t)
	repo, err := vcs.OpenBare(ctx(t), barePath, vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenBare against the stand-in git: %v", err)
	}
	if _, err := repo.ResolveCommit(ctx(t), "HEAD"); err != nil {
		t.Fatalf("ResolveCommit against the stand-in git: %v", err)
	}

	for _, call := range readInvocations(t, logPath) {
		args := strings.Join(call.Args, " ")

		// Explicit addressing: the bare repository is named, not discovered.
		if want := "--git-dir=" + repo.Path(); call.Args[0] != want {
			t.Errorf("first argument = %q; want %q (args: %s)", call.Args[0], want, args)
		}
		if !strings.Contains(args, "--no-pager") {
			t.Errorf("invocation is missing --no-pager: %s", args)
		}

		// Nothing inherited may redirect the invocation somewhere else.
		for _, name := range []string{
			"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE",
			"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
			"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "DISPLAY",
		} {
			if v, ok := lookupEnv(call.Env, name); ok {
				t.Errorf("%s reached git as %q; it must be removed", name, v)
			}
		}
		// The configuration file location a caller chose deliberately stays.
		if _, ok := lookupEnv(call.Env, "GIT_CONFIG_GLOBAL"); !ok {
			t.Errorf("GIT_CONFIG_GLOBAL was removed; only redirecting variables should be")
		}

		// Nothing may wait for a person.
		for _, want := range [][2]string{
			{"GIT_TERMINAL_PROMPT", "0"},
			{"GIT_ASKPASS", "echo"},
			{"SSH_ASKPASS", "echo"},
			{"SSH_ASKPASS_REQUIRE", "never"},
			{"GIT_EDITOR", "false"},
			{"GIT_SEQUENCE_EDITOR", "false"},
			{"GIT_MERGE_AUTOEDIT", "no"},
		} {
			if got, ok := lookupEnv(call.Env, want[0]); !ok || got != want[1] {
				t.Errorf("%s = %q (present: %v); want %q", want[0], got, ok, want[1])
			}
		}
		if ssh, _ := lookupEnv(call.Env, "GIT_SSH_COMMAND"); !strings.Contains(ssh, "BatchMode=yes") {
			t.Errorf("GIT_SSH_COMMAND = %q; want it to carry BatchMode=yes", ssh)
		}
		if call.StdinBytes != 0 {
			t.Errorf("git was given %d bytes on standard input; want an immediate end of input", call.StdinBytes)
		}
	}
}

// A working copy is addressed by its root rather than discovered from the
// process working directory.
func TestWorktreeInvocationsNameTheirDirectory(t *testing.T) {
	gitEnvironment(t)
	logPath, exe := useFakeGit(t)

	dir := t.TempDir()
	repo, err := vcs.OpenWorktree(ctx(t), dir, vcs.WithGitBinary(exe))
	if err != nil {
		t.Fatalf("OpenWorktree against the stand-in git: %v", err)
	}
	for _, call := range readInvocations(t, logPath) {
		if len(call.Args) < 2 || call.Args[0] != "-C" || call.Args[1] != repo.Path() {
			t.Errorf("arguments = %v; want them to start with -C %s", call.Args, repo.Path())
		}
	}
}

// A caller's own ssh command survives; BatchMode is appended to it rather than
// replacing it.
func TestAnInheritedSSHCommandIsKept(t *testing.T) {
	gitEnvironment(t)
	logPath, exe := useFakeGit(t)
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /keys/deploy")

	if _, err := vcs.OpenBare(ctx(t), fakeBareDir(t), vcs.WithGitBinary(exe)); err != nil {
		t.Fatalf("OpenBare against the stand-in git: %v", err)
	}
	for _, call := range readInvocations(t, logPath) {
		ssh, _ := lookupEnv(call.Env, "GIT_SSH_COMMAND")
		if !strings.Contains(ssh, "-i /keys/deploy") || !strings.Contains(ssh, "BatchMode=yes") {
			t.Errorf("GIT_SSH_COMMAND = %q; want the inherited command with BatchMode appended", ssh)
		}
	}
}

// Git's change statuses are read rather than guessed at, and output that does
// not have the promised shape is refused rather than partially believed.
func TestChangedFilesRefusesOutputItCannotRead(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   error
	}{
		{"unrecognized status", "Q\x00some/path\x00", vcs.ErrUnknownStatus},
		{"status with no path", "A\x00", vcs.ErrMalformedOutput},
		{"rename with one path", "R100\x00only/one\x00", vcs.ErrMalformedOutput},
		{"empty status field", "\x00some/path\x00", vcs.ErrMalformedOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitEnvironment(t)
			_, exe := useFakeGit(t)
			fakeGitOutput(t, tc.output)

			repo, err := vcs.OpenBare(ctx(t), fakeBareDir(t), vcs.WithGitBinary(exe))
			if err != nil {
				t.Fatalf("OpenBare against the stand-in git: %v", err)
			}
			if _, err := repo.ChangedFiles(ctx(t), "a", "b"); !errors.Is(err, tc.want) {
				t.Fatalf("ChangedFiles of %q = %v; want %v", tc.output, err, tc.want)
			}
		})
	}

	// The accepting path, through the same stand-in, so the refusals above are
	// not simply what this method always does.
	t.Run("well formed output", func(t *testing.T) {
		gitEnvironment(t)
		_, exe := useFakeGit(t)
		fakeGitOutput(t, "M\x00edited.txt\x00R90\x00was.txt\x00is.txt\x00")

		repo, err := vcs.OpenBare(ctx(t), fakeBareDir(t), vcs.WithGitBinary(exe))
		if err != nil {
			t.Fatalf("OpenBare against the stand-in git: %v", err)
		}
		got, err := repo.ChangedFiles(ctx(t), "a", "b")
		if err != nil {
			t.Fatalf("ChangedFiles: %v", err)
		}
		want := []vcs.FileChange{
			{Status: vcs.StatusModified, Path: "edited.txt"},
			{Status: vcs.StatusRenamed, Path: "is.txt", OldPath: "was.txt", Similarity: 90},
		}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("ChangedFiles = %+v; want %+v", got, want)
		}
	})
}
