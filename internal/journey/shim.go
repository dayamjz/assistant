package journey

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The names this harness stands on a PATH, and the environment variable each
// one reads.
//
// Both are executables a component of this product resolves by name and
// invokes as a process, which is the only seam either offers: internal/agents
// looks the configured agent up on PATH, and internal/forge invokes a provider
// command. A harness in another process cannot substitute a Go value into
// either, so it substitutes the executable.
const (
	// AgentShimName is what internal/agents resolves the Claude Code adapter
	// by. A copy of this test binary standing here is answered by
	// internal/agents/standin, which is what makes the stand-in reachable from
	// a separate process.
	AgentShimName = "claude"
	// ProviderShimName is what internal/forge resolves its GitHub provider by.
	ProviderShimName = "gh"
	// ProviderAnswerVariable names the file the provider shim prints. The
	// harness writes that file, which is where the run's own head is
	// substituted for the one the fixture recorded at build time.
	ProviderAnswerVariable = "JOURNEY_PROVIDER_ANSWER"
)

// ExitShimUnprepared is what a shim exits with when it was invoked and this
// harness had not prepared an answer for it.
//
// It is a distinct status because the two failures it separates need different
// fixes: a shim that answered wrongly is a harness bug, and a shim that was
// invoked at all where the harness expected no invocation is the product
// reaching for something the test did not know it would.
const ExitShimUnprepared = 97

// shims is the directory of copies, made once per process. Which binary
// answers as which name is a fact about this process, and copying the test
// binary twice per journey would cost tens of megabytes for nothing.
var shims struct {
	once sync.Once
	dir  string
	err  error
}

// Shims returns a directory holding a copy of this test binary under every
// name a component of this product resolves off PATH, for a caller to put in
// front of a process's own PATH.
//
// The copies are of this binary rather than of a script, because a script is
// not executable on every platform this module targets and PATH resolution on
// Windows goes through an extension list. What makes one copy behave as an
// agent and another as a provider is ActAsShim, which reads the name it was
// invoked under.
func Shims() (string, error) {
	shims.once.Do(func() {
		shims.dir, shims.err = installShims()
	})
	return shims.dir, shims.err
}

// installShims is Shims without the memoization.
func installShims() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("journey: locating this test binary, which is what stands in for the "+
			"executables this product resolves off PATH: %w", err)
	}
	dir, err := os.MkdirTemp("", "assistant-shims")
	if err != nil {
		return "", fmt.Errorf("journey: making a directory for the shims: %w", err)
	}
	for _, name := range []string{AgentShimName, ProviderShimName} {
		if err := copyExecutable(self, filepath.Join(dir, name+exeSuffix())); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// copyExecutable writes a copy of src at dst that this process may execute.
func copyExecutable(src, dst string) error {
	from, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("journey: reading %s to copy it: %w", src, err)
	}
	defer func() { _ = from.Close() }()
	to, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("journey: creating the shim %s: %w", dst, err)
	}
	if _, err := io.Copy(to, from); err != nil {
		_ = to.Close()
		return fmt.Errorf("journey: writing the shim %s: %w", dst, err)
	}
	if err := to.Close(); err != nil {
		return fmt.Errorf("journey: writing the shim %s: %w", dst, err)
	}
	return nil
}

// ActAsShim runs this process as whichever shim it was invoked as, and returns
// when it was not invoked as one.
//
// A consuming package calls it from TestMain, after internal/agents/standin's
// own Main, which is what answers an agent invocation carrying the stand-in's
// control flag:
//
//	func TestMain(m *testing.M) {
//		standin.Main()
//		journey.ActAsShim()
//		os.Exit(m.Run())
//	}
//
// The agent name is not served here, and the refusal it gets instead is the
// point of that ordering. An agent invocation that reached this line carried no
// control flag, so nothing scripted it, and running the test suite as an agent
// would look to the caller like an agent that talked for several minutes and
// then failed. It exits with a message the adapter keeps as the failure's own
// text instead.
func ActAsShim() {
	switch shimName() {
	case AgentShimName:
		fmt.Fprintf(os.Stderr, "journey: this binary was invoked as %s with no stand-in control flag, "+
			"so nothing scripted what it should answer. The agent entry a run resolves has to carry the "+
			"arguments internal/agents/standin's Agent.Arguments reports.\n", AgentShimName)
		os.Exit(ExitShimUnprepared)
	case ProviderShimName:
		os.Exit(serveProviderAnswer())
	}
}

// shimName is the name this process was invoked under, with the platform's
// executable suffix and any directory removed, or empty when it was not
// invoked as one of the shims.
func shimName() string {
	base := strings.ToLower(filepath.Base(os.Args[0]))
	base = strings.TrimSuffix(base, strings.ToLower(exeSuffix()))
	for _, name := range []string{AgentShimName, ProviderShimName} {
		if base == name {
			return name
		}
	}
	return ""
}

// serveProviderAnswer prints the answer the harness prepared and returns the
// status to exit with.
//
// It prints a file rather than composing anything, because what the provider
// answers is the fixture's recorded bytes with the run's own head substituted,
// and the substitution is the harness's to make where it can be checked. A
// shim that composed an answer would be a second author of the condition.
func serveProviderAnswer() int {
	path := os.Getenv(ProviderAnswerVariable)
	if path == "" {
		fmt.Fprintf(os.Stderr, "journey: this binary was invoked as %s and %s names no answer, so this "+
			"harness had prepared none. Something asked the code host a question the test did not expect.\n",
			ProviderShimName, ProviderAnswerVariable)
		return ExitShimUnprepared
	}
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "journey: reading the prepared provider answer %s: %v\n", path, err)
		return ExitShimUnprepared
	}
	if _, err := os.Stdout.Write(body); err != nil {
		fmt.Fprintf(os.Stderr, "journey: writing the prepared provider answer: %v\n", err)
		return ExitShimUnprepared
	}
	return 0
}

// ErrNoRecordedHead reports an answer that does not name the commit the
// fixture recorded, so there is nothing in it to substitute.
var ErrNoRecordedHead = errors.New("journey: the recorded answer names no head to substitute")

// SubstituteHead writes the fixture's recorded provider answer with the run's
// own head in place of the one the build left, and returns the file it wrote.
//
// The substitution is not a convenience. internal/fixture records that the
// answer carries Commits["branch-head"] as the build left it, that a run
// rebases and may add fix commits, and that internal/forge distinguishes an
// answer naming a commit the run did not push from an empty check list. Served
// unchanged, the answer reports a stale check list rather than the condition
// that was planted, so a harness that skipped this would observe something
// else and report the condition as met.
//
// It refuses an answer that does not name the recorded head, because the
// substitution silently doing nothing is exactly the failure above with no
// symptom.
func SubstituteHead(recorded, actual, answerPath, into string) (string, error) {
	body, err := os.ReadFile(answerPath)
	if err != nil {
		return "", fmt.Errorf("journey: reading the recorded provider answer %s: %w", answerPath, err)
	}
	if recorded == "" || !strings.Contains(string(body), recorded) {
		return "", fmt.Errorf("%w: %s does not carry %q", ErrNoRecordedHead, answerPath, recorded)
	}
	substituted := strings.ReplaceAll(string(body), recorded, actual)
	if err := os.WriteFile(into, []byte(substituted), 0o600); err != nil {
		return "", fmt.Errorf("journey: writing the substituted provider answer %s: %w", into, err)
	}
	return into, nil
}
