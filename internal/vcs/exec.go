package vcs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Option configures a repository handle. Options are supplied when the handle
// is created and are fixed for its lifetime, so every invocation a handle
// makes runs under the same configuration.
type Option func(*settings)

// WithGitBinary names the git executable to run. The default is "git",
// resolved through PATH. A caller that pins a git version, or a test that
// substitutes a stand-in, passes it here.
func WithGitBinary(path string) Option {
	return func(s *settings) { s.git = path }
}

// WithRedactor sets the Redactor applied to arguments and to git's messages
// before either reaches a *CommandError. The default covers only the userinfo
// of a URL that carries a scheme; see the Redactor documentation.
func WithRedactor(r Redactor) Option {
	return func(s *settings) {
		if r != nil {
			s.redactor = r
		}
	}
}

// WithMaxOutput sets the largest standard output, in bytes, that an invocation
// may produce. An invocation that produces more fails with ErrOutputTooLarge
// and its output is discarded rather than truncated. A value of zero or less
// leaves the default in place.
func WithMaxOutput(n int64) Option {
	return func(s *settings) {
		if n > 0 {
			s.maxOutput = n
		}
	}
}

// defaultMaxOutput bounds what one invocation may return. A branch under
// validation chooses the size of its own diff, so an unbounded read is a
// memory exhaustion the branch controls.
const defaultMaxOutput = 64 << 20

// maxStderr bounds the diagnostic text one invocation collects from standard
// error. The bound is applied while the stream is being read, not to the text
// afterwards, so a remote that streams messages cannot grow the buffer past
// it. Git's diagnostics are short; the whole output of a stage lives in that
// stage's log.
const maxStderr = 8 << 10

// killGrace is the deadline given to cmd.WaitDelay, which bounds two waits the
// standard library folds into one setting.
//
// After a context is cancelled it is how long the child gets to exit before
// its pipes are abandoned, so a grandchild holding them open cannot keep a
// cancelled call waiting.
//
// It also bounds the wait for those pipes to close after a child exits on its
// own, and that case has a cost worth stating rather than implying away: a git
// that exits successfully while something else still holds its standard output
// or error open past this deadline is reported as a failure, carrying the
// status git exited with and exec.ErrWaitDelay as the cause. The output
// collected in that case may be missing bytes git wrote, so reporting it as a
// result would be worse, and waiting forever instead would be worse still.
const killGrace = 2 * time.Second

type settings struct {
	git       string
	redactor  Redactor
	maxOutput int64
}

func newSettings(opts []Option) settings {
	s := settings{git: "git", redactor: defaultRedactor{}, maxOutput: defaultMaxOutput}
	for _, o := range opts {
		if o != nil {
			o(&s)
		}
	}
	return s
}

// redirectingVars names the environment variables removed from every child.
// Each is documented by git, and each falls into one of four groups: the ones
// that decide which repository git operates on, the ones that inject
// configuration into it, the ones that name a program git would run, and the
// ones that decide where git's own standard streams go. A process launched
// from a git hook inherits several of these, which is why removing them is a
// correctness requirement and not tidiness.
//
// The list is written by hand, so it covers what is on it and nothing else. A
// variable a later git introduces is not removed until it is added here.
var redirectingVars = []string{
	// Which repository git operates on.
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_COMMON_DIR",
	"GIT_NAMESPACE",
	"GIT_PREFIX",
	"GIT_CEILING_DIRECTORIES",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM",

	// Configuration injected into git.
	"GIT_CONFIG",
	"GIT_CONFIG_PARAMETERS",
	"GIT_CONFIG_COUNT",

	// What a newly created repository is built from. git init copies the
	// template directory's hooks into the repository it creates, so an
	// inherited value chooses the code on the gate repository's push path at
	// the moment it is created, and nothing later can undo it.
	"GIT_TEMPLATE_DIR",

	// Programs git would run. GIT_EXEC_PATH decides where git finds the
	// subcommands and remote helpers it executes, so an inherited one from a
	// different build defeats pinning a binary with WithGitBinary.
	// GIT_EXTERNAL_DIFF names a program git runs while diffing, which is the
	// hazard --no-ext-diff closes on the command line; its TRUST_EXIT_CODE
	// companion is the other half of that one mechanism. GIT_SSH names the
	// transport program, and is otherwise only shadowed by this package
	// setting GIT_SSH_COMMAND, which would be a second mechanism answering
	// the question. GIT_SSH_VARIANT names no program, but it decides how git
	// builds the ssh command line, which is how the BatchMode this package
	// appends reaches ssh at all.
	"GIT_EXEC_PATH",
	"GIT_EXTERNAL_DIFF",
	"GIT_EXTERNAL_DIFF_TRUST_EXIT_CODE",
	"GIT_SSH",
	"GIT_SSH_VARIANT",

	// Where git's own standard streams go. These are Windows-only, and they
	// point a stream at a path of the environment's choosing. Standard output
	// is the result this package parses, so an inherited one turns an empty
	// read into an answer rather than into a failure.
	"GIT_REDIRECT_STDIN",
	"GIT_REDIRECT_STDOUT",
	"GIT_REDIRECT_STDERR",

	// DISPLAY is here because git's askpass fallback consults it before
	// deciding whether a graphical helper is worth running.
	"DISPLAY",
}

// redirectingPrefixes are variable name prefixes removed for the same reason
// as redirectingVars. GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n are the
// numbered halves of the GIT_CONFIG_COUNT mechanism.
var redirectingPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// nonInteractive is the environment every invocation runs under. Setting it
// here rather than at call sites is the whole point of this package owning git
// invocation: a call site cannot forget what it never writes.
//
// GIT_SSH_COMMAND is not in this list because it is derived from the inherited
// value; envFor appends to it.
var nonInteractive = [][2]string{
	{"GIT_TERMINAL_PROMPT", "0"},
	// false exits non-zero without writing a line, so git treats the askpass
	// as having failed and falls back to the terminal, which
	// GIT_TERMINAL_PROMPT=0 refuses. Setting these at all is what keeps a
	// graphical helper named in the environment or in git configuration from
	// being run in its place.
	{"GIT_ASKPASS", "false"},
	{"SSH_ASKPASS", "false"},
	{"SSH_ASKPASS_REQUIRE", "never"},
	{"GIT_EDITOR", "false"},
	{"GIT_SEQUENCE_EDITOR", "false"},
	{"GIT_MERGE_AUTOEDIT", "no"},
	// Git's own diagnostics are what a *CommandError carries, so pin the
	// locale rather than reporting whatever the operator's shell was set to.
	{"LC_ALL", "C"},
}

// envFor builds the environment for one invocation from base, which is
// normally os.Environ(). It removes the variables named by redirectingVars and
// redirectingPrefixes, then applies the settings in nonInteractive, overriding
// any inherited value, and appends BatchMode to whatever ssh command was
// inherited.
func envFor(base []string) []string {
	drop := make(map[string]struct{}, len(redirectingVars)+len(nonInteractive))
	for _, k := range redirectingVars {
		drop[k] = struct{}{}
	}
	for _, kv := range nonInteractive {
		drop[kv[0]] = struct{}{}
	}
	drop["GIT_SSH_COMMAND"] = struct{}{}

	ssh := "ssh"
	out := make([]string, 0, len(base)+len(nonInteractive)+1)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if name == "GIT_SSH_COMMAND" {
			if v := entry[len(name)+1:]; strings.TrimSpace(v) != "" {
				ssh = v
			}
			continue
		}
		if _, skip := drop[name]; skip {
			continue
		}
		if hasAnyPrefix(name, redirectingPrefixes) {
			continue
		}
		out = append(out, entry)
	}
	for _, kv := range nonInteractive {
		out = append(out, kv[0]+"="+kv[1])
	}
	// BatchMode makes ssh fail rather than ask for a passphrase or a host key
	// confirmation. It is appended so a caller's own ssh command survives.
	out = append(out, "GIT_SSH_COMMAND="+ssh+" -o BatchMode=yes")
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// globalArgs are the git-level options every invocation carries, before the
// repository addressing and the subcommand.
//
// core.fsmonitor is disabled because a repository-local setting would start a
// long-lived helper process for what are short one-shot invocations.
// core.quotePath is disabled so paths come back as bytes rather than as git's
// C-style escapes, which matters for the operations that do not use a
// NUL-separated format.
var globalArgs = []string{
	// --no-pager is what keeps an invocation from waiting on a pager. It is
	// the only mechanism used for that; GIT_PAGER is deliberately left alone,
	// so a caller's configuration is not two things this package disagrees
	// with itself about.
	"--no-pager",
	"-c", "core.quotePath=false",
	"-c", "core.fsmonitor=false",
	"-c", "advice.detachedHead=false",
}

// run invokes git for op with the given subcommand arguments and returns its
// standard output. The repository addressing and the global options are added
// here, so a caller passes only the subcommand and its arguments.
func (r *Repository) run(ctx context.Context, op string, args ...string) ([]byte, error) {
	addr := r.addressing()
	full := make([]string, 0, len(addr)+len(globalArgs)+len(args))
	full = append(full, addr...)
	full = append(full, globalArgs...)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, r.set.git, full...)
	cmd.Dir = r.path
	cmd.Env = envFor(os.Environ())
	// A nil Stdin is os.DevNull, so anything git reads from standard input
	// sees EOF rather than waiting for a person.
	cmd.Stdin = nil
	cmd.WaitDelay = killGrace

	stdout := &capWriter{limit: r.set.maxOutput}
	// Standard error is diagnostic text rather than a result, so it is kept up
	// to the limit and marked instead of being refused whole the way an
	// over-limit standard output is.
	stderr := &capWriter{limit: maxStderr, truncate: true}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	if stdout.over {
		// The output is refused whole, but what git reported about the run is
		// not thrown away with it: an invocation that overran the limit and
		// then also failed has an exit status and a message, and they are the
		// most wanted facts about it.
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		msg, truncated := stderr.collected()
		return nil, r.commandError(op, full, code, msg, truncated, ErrOutputTooLarge)
	}
	if runErr != nil {
		// The status git exited with is on the process state whether or not
		// the error describing the run is the one that carries it, so a
		// failure raised around a git that exited cleanly still names it. It
		// stays -1 when git never ran or never reported a status.
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		var exit *exec.ExitError
		if errors.As(runErr, &exit) && code >= 0 {
			// An exit status is the ordinary way git reports a failure, so it
			// is not also carried as a wrapped process error. A negative
			// status means there was none, because a signal ended the process,
			// and then the process error is the only description of what
			// happened.
			runErr = nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			// The context ended the call, so report that rather than the
			// signal git died of.
			runErr = ctxErr
		}
		msg, truncated := stderr.collected()
		return stdout.buf.Bytes(), r.commandError(op, full, code, msg, truncated, runErr)
	}
	return stdout.buf.Bytes(), nil
}

// runExpecting is run for a command that answers with its exit status. The
// caller lists the non-zero statuses that are answers rather than failures,
// and gets the status back to classify. Any other status is a failure and is
// returned as a *CommandError, so a command that fails for a reason the caller
// did not anticipate cannot be read as one of the answers it did.
func (r *Repository) runExpecting(ctx context.Context, op string, expected []int, args ...string) ([]byte, int, error) {
	out, err := r.run(ctx, op, args...)
	if err == nil {
		return out, 0, nil
	}
	var ce *CommandError
	if errors.As(err, &ce) && ce.Err == nil {
		for _, code := range expected {
			if ce.ExitCode == code {
				return out, code, nil
			}
		}
	}
	return nil, -1, err
}

func (r *Repository) commandError(op string, args []string, code int, stderrText string, truncated bool, cause error) *CommandError {
	red := make([]string, len(args))
	for i, a := range args {
		red[i] = r.set.redactor.Redact(a)
	}
	// Redaction runs on whole lines. A cut made while collecting drops the
	// partial line it ended in, so the redactor is never handed half of a
	// credentialed URL it would no longer recognize.
	msg := r.set.redactor.Redact(strings.TrimSpace(stderrText))
	if truncated {
		msg = strings.TrimSpace(msg + "\n" + stderrTruncatedMarker)
	}
	return &CommandError{
		Op:       op,
		Repo:     r.path,
		Args:     red,
		ExitCode: code,
		Stderr:   msg,
		Err:      cause,
	}
}

// stderrTruncatedMarker is appended to a *CommandError message that lost text
// to the collection limit, so a reader can tell a short message from a cut
// one.
const stderrTruncatedMarker = "[git message truncated]"

// capWriter collects output up to limit bytes and records whether more was
// offered. The limit is enforced as the bytes arrive, so a stream that never
// stops cannot grow the buffer past it.
//
// With truncate false, everything collected is dropped once the limit is
// passed, because the result is refused whole. With truncate true, the bytes
// that fit are kept, because the result is diagnostic text a caller is better
// off seeing part of than none of.
type capWriter struct {
	limit    int64
	truncate bool
	n        int64
	over     bool
	buf      bytes.Buffer
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.over {
		return len(p), nil
	}
	if w.n+int64(len(p)) > w.limit {
		w.over = true
		if !w.truncate {
			w.buf.Reset()
			return len(p), nil
		}
		if room := w.limit - w.n; room > 0 {
			w.n = w.limit
			if _, err := w.buf.Write(p[:room]); err != nil {
				return 0, err
			}
		}
		return len(p), nil
	}
	w.n += int64(len(p))
	return w.buf.Write(p)
}

// collected returns the text kept for a *CommandError and whether anything was
// lost to the limit. When something was, the final line is dropped because the
// cut may have landed inside it, and half of a credentialed URL is exactly
// what a redactor cannot recognize.
func (w *capWriter) collected() (string, bool) {
	text := w.buf.String()
	if !w.over {
		return text, false
	}
	if i := strings.LastIndexByte(text, '\n'); i >= 0 {
		return text[:i], true
	}
	return "", true
}
