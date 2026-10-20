package agents

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
)

// ScriptedName is the name the scripted adapter answers to.
const ScriptedName = "scripted"

// ScriptedFactory returns the Factory for the scripted adapter: the Claude
// Code adapter's one-shot path, which reads the same result envelope and
// opens the same fixer sessions, plus a worker path over plain lines rather
// than the Claude Code adapter's stream-json messages. It is built over a
// binary a test supplies, which is what internal/agents/standin stands up,
// and it ships in no catalog: no real coding agent is known to this build to
// answer plain lines as a worker, so configuration cannot name this adapter
// and "auto" never chooses it.
//
// It exists so that a layer built on workers can run against a scripted
// stand-in whose output is plain lines it can script and read, rather than
// against a double of its own, and so that the conformance suite holds a
// second declarer of CapabilityWorkerSessions to the mechanism. What that
// establishes is a property of this package's worker mechanism, and nothing
// about any real agent.
//
// The options are ClaudeOption because the one-shot path is that adapter's,
// and every option applies to the worker path the same way: the binary, the
// base environment, the termination grace, and the output limit, which bounds
// what a worker's Capture retains. A Recorder given here receives the one-shot
// invocations and not the worker, which produces no Record; the package
// documentation says why.
func ScriptedFactory(opts ...ClaudeOption) Factory {
	return &scriptedFactory{settings: newClaudeSettings(opts)}
}

type scriptedFactory struct{ settings claudeSettings }

// Name returns "scripted".
func (f *scriptedFactory) Name() string { return ScriptedName }

// AutoEligible reports false: this adapter is reachable only by a catalog a
// caller built, and never by "auto".
func (f *scriptedFactory) AutoEligible() bool { return false }

// New reports whether the binary resolves, on the same terms ClaudeFactory's
// New states, and returns a Runner that is also a WorkerRunner when it does.
func (f *scriptedFactory) New(ctx context.Context, args []string) (Runner, error) {
	_ = ctx
	resolved, err := exec.LookPath(f.settings.bin)
	if err != nil {
		return nil, fmt.Errorf("%s is not runnable: %w", f.settings.bin, err)
	}
	return &scriptedRunner{claudeRunner: &claudeRunner{
		name:     ScriptedName,
		settings: f.settings,
		resolved: resolved,
		extra:    slices.Clone(args),
	}}, nil
}

// scriptedRunner is the Claude Code runner with its worker path replaced by
// plain lines. The embedded runner's Fixer is what makes it a SessionRunner,
// and Start below is what makes it a WorkerRunner; Capabilities declares both, and Resolve
// refuses the pair if they ever disagree.
type scriptedRunner struct{ *claudeRunner }

// Capabilities declares resumable sessions, worker sessions and terminal
// workers, because all three mechanisms are here. Instruction suppression is not declared, for the
// reason the Claude Code adapter gives.
func (r *scriptedRunner) Capabilities() Capabilities {
	return Declare(CapabilityResumableSessions, CapabilityWorkerSessions, CapabilityTerminalWorkers)
}

// Start launches the binary as a worker: the configured entry's own
// arguments and nothing managed, with --print absent, and every send written
// as a plain line. It overrides the Claude Code adapter's Start, so this
// adapter's worker is never framed as stream-json. The environment is the one builder's,
// with the redirecting variables removed afterwards, on the terms WorkerSpec
// states.
func (r *scriptedRunner) Start(ctx context.Context, spec WorkerSpec) (Worker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	worker, err := startWorker(ctx, procSpec{
		bin:    r.resolved,
		args:   slices.Clone(r.extra),
		dir:    spec.Dir,
		env:    WorkerEnvironment(r.settings.base, spec.Env),
		grace:  r.settings.grace,
		maxOut: r.settings.maxOut,
	}, spec.Instructions, plainLine, OutputText)
	if err != nil {
		return nil, err
	}
	return worker, nil
}

// StartInTerminal launches the binary as a terminal worker on t: the same
// command line and environment Start uses, hosted on the terms
// TerminalWorkerRunner's mechanism states, with every send typed as plain
// lines through terminalLines. It is what makes this adapter a
// TerminalWorkerRunner.
func (r *scriptedRunner) StartInTerminal(ctx context.Context, t Terminal, spec WorkerSpec) (TerminalWorker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	worker, err := startTerminalWorker(ctx, t, procSpecTerminal{
		bin:    r.resolved,
		args:   slices.Clone(r.extra),
		dir:    spec.Dir,
		env:    WorkerEnvironment(r.settings.base, spec.Env),
		grace:  r.settings.grace,
		maxOut: r.settings.maxOut,
	}, spec.Instructions, terminalTyping{frame: terminalLines})
	if err != nil {
		return nil, err
	}
	return worker, nil
}

func (r *scriptedRunner) TerminalLaunchCommandSize(Terminal, WorkerSpec) (int, bool, error) {
	return 0, false, nil
}

// TerminalLineLimit is the longest line, in bytes and without its newline,
// the scripted adapter's terminal framing types. The stand-in reads lines in
// the terminal's line-editing mode, which bounds a line: on Linux a
// 10000-byte line typed into a tmux pane reached the reading program as its
// first 4095 bytes, which TestATerminalCutsALongLineTheFramingRefuses
// records. On a macOS CI runner the same line never completed at all, and a
// line at this limit arrived whole. The limit is set below what either
// platform kept, so a line is refused here rather than cut or lost there.
const TerminalLineLimit = 1023

// terminalLines is the scripted adapter's terminal framing: plainLine's bytes,
// refused when any line is longer than TerminalLineLimit, with an error
// wrapping ErrInvalidInvocation, before anything is typed.
func terminalLines(text string) ([]byte, error) {
	framed, err := plainLine(text)
	if err != nil {
		return nil, err
	}
	for i, line := range strings.Split(strings.TrimSuffix(string(framed), "\n"), "\n") {
		if len(line) > TerminalLineLimit {
			return nil, &invocationFieldError{
				field: "text",
				reason: fmt.Sprintf("line %d is %d bytes, over the %d a terminal worker's line may be typed with",
					i+1, len(line), TerminalLineLimit),
			}
		}
	}
	return framed, nil
}
