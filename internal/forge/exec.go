package forge

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxOutput bounds what one provider invocation may print on standard
// output. A pull request under validation influences how much a provider has
// to say about it, most of all in a check list, so an unbounded read is memory
// exhaustion a contributor controls.
const DefaultMaxOutput = 8 << 20

// maxStderr bounds the diagnostic text one invocation collects from standard
// error. It is applied while the stream is read rather than to the text
// afterwards, so a provider that streams messages cannot grow the buffer past
// it.
const maxStderr = 8 << 10

// stderrTruncatedMarker ends collected provider text that stopped at
// maxStderr, so a reader is never shown a fragment that looks whole.
const stderrTruncatedMarker = "[provider message truncated]"

// DefaultProviderGrace is the deadline given to cmd.WaitDelay on every
// invocation, which bounds two waits the standard library folds into one
// setting.
//
// After a context ends and the provider is killed, it is how long the call
// waits for the output pipes before abandoning them, so a descendant that
// inherited one and holds it open cannot keep a cancelled call waiting past
// it. Without that bound the caller's checks_timeout could not bound a check
// read, because the wait for those pipes is not the wait a context ends.
//
// It also bounds the wait for those pipes to close after a provider exits on
// its own, and that case has a cost worth stating rather than implying away:
// an invocation whose output was still held open at the deadline is refused
// rather than reported, because what was collected may be missing bytes the
// provider wrote, and a partial answer read as a whole one is the failure this
// package exists to prevent.
const DefaultProviderGrace = 2 * time.Second

// providerEnv is applied over the base environment of every invocation.
//
// It states what the child is given, which is what a reader can check here,
// rather than what the provider does with it. Each entry is present so that an
// operator's preference about paging, color, or update notices cannot decide
// what this package parses, and so that a child that would otherwise wait for
// a person fails instead: there is nobody to answer a prompt inside a
// pipeline, and standard input carries either the body of a request or
// nothing.
var providerEnv = map[string]string{
	"GH_PAGER":              "",
	"PAGER":                 "",
	"GH_NO_UPDATE_NOTIFIER": "1",
	"GH_PROMPT_DISABLED":    "1",
	"NO_COLOR":              "1",
	"CLICOLOR":              "0",
}

// Option configures a provider adapter. Options are supplied at construction
// and are fixed for the adapter's lifetime, so every call it makes runs under
// the same configuration.
type Option func(*settings)

type settings struct {
	bin     string
	dir     string
	repo    string
	base    []string
	baseSet bool
	maxOut  int64
	grace   time.Duration
}

// WithBinary names the provider executable to run. The default is "gh",
// resolved through PATH. A caller that pins an installation, or a test that
// substitutes a stand-in, passes it here. An empty path leaves the default in
// place.
func WithBinary(path string) Option {
	return func(s *settings) {
		if path != "" {
			s.bin = path
		}
	}
}

// WithDirectory sets the working directory every invocation runs in, which is
// how the provider resolves which repository is being addressed when no
// repository is named. It is normally the run's isolated copy.
//
// An adapter needs a directory or a repository, and NewGitHub refuses one
// given neither: a provider left to resolve a repository from this process's
// own working directory would be addressing something the run never chose.
func WithDirectory(dir string) Option {
	return func(s *settings) { s.dir = dir }
}

// WithRepository names the repository to address, as owner/name. It is checked
// at construction, and a value carrying a scheme or userinfo is refused there,
// so no credentialed URL reaches a provider command line through this package.
func WithRepository(repo string) Option {
	return func(s *settings) { s.repo = repo }
}

// WithBaseEnvironment sets the environment every invocation starts from,
// before providerEnv is applied over it. The default is this process's
// environment; an empty or nil slice given here is a deliberately empty base
// rather than a request for the default.
//
// Nothing is filtered out of it. A provider's credentials legitimately arrive
// this way, and what keeps them out of an error is that provider text is
// redacted and the argument vector is never copied into one.
func WithBaseEnvironment(env []string) Option {
	return func(s *settings) {
		s.base = append([]string(nil), env...)
		s.baseSet = true
	}
}

// WithMaxOutput bounds what one invocation may print on standard output. Over
// the bound the output is discarded rather than truncated, because a truncated
// check list read as a whole one is worse than no check list. A value of zero
// or less leaves DefaultMaxOutput in place.
func WithMaxOutput(n int64) Option {
	return func(s *settings) {
		if n > 0 {
			s.maxOut = n
		}
	}
}

// WithProviderGrace sets how long an invocation waits for the provider's
// output pipes once the provider has exited or its context has ended. The
// default is DefaultProviderGrace, and what the deadline buys and costs is
// stated there.
//
// A value of zero or less leaves the default in place. A zero WaitDelay is the
// standard library's way of asking for no deadline at all, which is exactly
// the unbounded wait this setting exists to prevent, so it is not something a
// caller can ask for by passing nothing.
func WithProviderGrace(d time.Duration) Option {
	return func(s *settings) {
		if d > 0 {
			s.grace = d
		}
	}
}

// procResult is what one provider invocation produced. It reports what
// happened rather than deciding what it means.
type procResult struct {
	stdout []byte
	stderr string
	// code is the exit status, or -1 when the process never reported one.
	code int
	// over is true when the process printed more than the configured bound, in
	// which case stdout is empty because the output was discarded.
	over bool
	// startErr is the error from starting the process, and is set only when
	// the process never ran.
	startErr error
	// waitErr is what waiting on the process reported. A failing status is
	// reported here as an *exec.ExitError and takes precedence over anything
	// else the wait found, so the value worth testing for is
	// exec.ErrWaitDelay: it means the wait for the output pipes was abandoned
	// at the grace deadline, which is how a caller tells output that was
	// collected whole from output that may be missing bytes the provider
	// wrote.
	waitErr error
}

// runProvider runs one provider invocation to completion and collects what it
// produced. It writes stdin to the child and closes it, so a child reading
// there reaches the end of the input rather than waiting for a person.
func runProvider(ctx context.Context, s *settings, env []string, stdin string, args []string) procResult {
	cmd := exec.CommandContext(ctx, s.bin, args...)
	cmd.Dir = s.dir
	cmd.Env = env
	// With nothing to carry, standard input stays os.DevNull, which is the end
	// of the input immediately.
	cmd.Stdin = nil
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out := &capWriter{limit: s.maxOut}
	// Standard error is diagnostic text rather than a result, so it is kept up
	// to its bound and marked, instead of being discarded whole the way an
	// over-bound standard output is.
	errBuf := &capWriter{limit: maxStderr, truncate: true}
	cmd.Stdout = out
	cmd.Stderr = errBuf
	// WaitDelay bounds how long this call waits for the output pipes after the
	// provider has exited or its context has ended, so a descendant that
	// inherited one and holds it open costs at most the grace period and then
	// ends the invocation, rather than keeping a cancelled call waiting on a
	// pipe nobody is going to close.
	cmd.WaitDelay = s.grace

	if err := cmd.Start(); err != nil {
		return procResult{code: -1, startErr: err}
	}
	waitErr := cmd.Wait()

	res := procResult{stderr: errBuf.text(), code: -1, over: out.over, waitErr: waitErr}
	if cmd.ProcessState != nil {
		res.code = cmd.ProcessState.ExitCode()
	}
	if !out.over {
		res.stdout = out.buf
	}
	return res
}

// environment builds the environment for one invocation from base with extra
// applied over it. A name present in both takes the value from extra.
//
// The result is ordered: base entries in their original order, then the extra
// names sorted, so two invocations built from the same inputs produce the same
// environment.
//
// It is never nil, including for an empty base and no extra. A nil environment
// is os/exec's way of asking for this process's own, so returning one for an
// empty base would hand the provider every variable that base was chosen to
// withhold.
func environment(base []string, extra map[string]string) []string {
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	override := make(map[string]struct{}, len(extra))
	for _, name := range names {
		override[name] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(names))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, replaced := override[name]; replaced {
			continue
		}
		out = append(out, entry)
	}
	for _, name := range names {
		out = append(out, name+"="+extra[name])
	}
	return out
}

// baseEnvironment returns the environment an adapter starts from: what the
// caller supplied, or this process's own when the caller supplied nothing.
func baseEnvironment(s *settings) []string {
	if s.baseSet {
		return s.base
	}
	return os.Environ()
}

// capWriter collects output up to a limit. Over the limit it either stops and
// marks itself, when truncate is set, or refuses the whole thing by setting
// over, so a caller can discard rather than read a fragment as a whole.
type capWriter struct {
	limit    int64
	truncate bool
	buf      []byte
	over     bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	if w.over {
		return len(p), nil
	}
	room := w.limit - int64(len(w.buf))
	if int64(len(p)) <= room {
		w.buf = append(w.buf, p...)
		return len(p), nil
	}
	if w.truncate {
		if room > 0 {
			w.buf = append(w.buf, p[:room]...)
		}
		w.over = true
		return len(p), nil
	}
	w.over = true
	w.buf = nil
	return len(p), nil
}

// text renders a truncating writer's contents, marking them when the writer
// stopped short.
//
// When something was lost to the bound the final line goes with it, because
// the cut may have landed inside it, and half of a credentialed URL is exactly
// what a Redactor cannot recognize: the userinfo pattern it matches needs the
// "@" that the cut left on the other side. A writer that overran before any
// newline arrived therefore renders as the marker alone.
func (w *capWriter) text() string {
	s := string(w.buf)
	if w.over {
		if i := strings.LastIndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		} else {
			s = ""
		}
	}
	s = strings.TrimRight(s, "\n")
	if w.over {
		if s != "" {
			s += "\n"
		}
		s += stderrTruncatedMarker
	}
	return s
}

// exitDetail renders a provider's exit status and its message into one
// sentence. The text is the caller's to redact before it reaches a Refusal.
func exitDetail(code int, stderr string) string {
	var b strings.Builder
	if code >= 0 {
		b.WriteString("the provider exited " + strconv.Itoa(code))
	} else {
		b.WriteString("the provider reported no exit status")
	}
	if stderr != "" {
		b.WriteString(": " + stderr)
	}
	return b.String()
}
