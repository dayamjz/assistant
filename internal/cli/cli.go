package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// Environment is everything the command surface reads from the process it is
// running in. It is a parameter rather than a set of package-level reads so
// that a test drives the real surface with its own streams and its own
// directory instead of the process's.
type Environment struct {
	// Args are the arguments after the program name.
	Args []string
	// Stdout is where a structured answer goes.
	Stdout io.Writer
	// Stderr is where progress and human-facing failures go.
	Stderr io.Writer
	// Getenv reads the environment, which is where the home root comes from.
	Getenv func(string) string
	// WorkingDir is the directory the command was run in, which is what
	// locates the working copy every repository verb is about.
	WorkingDir string
	// Executable is the absolute path of this binary. A gate's hooks invoke it
	// by path so a push cannot choose a different one through PATH, and
	// starting the service launches it.
	Executable string
	// Version is what --version reports.
	Version string
}

// usageError marks a failure that is about the command line rather than about
// the system, which is the difference between the two non-zero exit codes.
type usageError struct{ err error }

// Error renders the underlying failure.
func (e usageError) Error() string { return e.err.Error() }

// Unwrap resolves to the failure this wraps.
func (e usageError) Unwrap() error { return e.err }

// usagef reports incorrect usage.
func usagef(format string, a ...any) error {
	return usageError{err: fmt.Errorf(format, a...)}
}

// verb is one command. Every row is a command PRD section 9's table names, and
// the table below is the whole surface.
type verb struct {
	// name is what a caller types, empty for the command with no verb.
	name string
	// summary is the one line the PRD's table gives it.
	summary string
	// run does the work and returns the answer to render.
	run func(context.Context, *invocation) (any, error)
}

// verbs is the command surface. It is PRD section 9's table and nothing else:
// a verb that section does not describe is a finding against the
// specification rather than a row here.
var verbs = []verb{
	{"", "Attach to this branch's active run. With no run, start one.", attachOrStart},
	{"init", "Create or repair the gate for this repository.", initGate},
	{"status", "Repository, gate, service, active run, and local branch state.", reportStatus},
	{"runs", "Recent runs, newest first.", listRuns},
	{"rerun", "Start a fresh run from the last known head, inheriting the recorded intent.", rerun},
	{"sync", "Reconcile your local branch with what the pipeline pushed, or recover work it holds.", syncBranch},
	{"tasks", "List fleet work with its resolved current state.", listTasks},
	{"watch", "Fleet view: everything in flight, everything waiting on you.", watch},
	{"doctor", "Check every dependency, and decisively report whether a run can start at all.", doctor},
	{"service", "Start, stop, restart, status.", serviceVerb},
	{"eject", "Remove the gate and its records.", eject},
}

// invocation is one command being run: the environment, the parsed global
// flags, and the arguments left for the verb.
type invocation struct {
	env Environment
	// args are the arguments after the verb.
	args []string
	// json says the answer is written as a document rather than read out. It
	// is settled by parseGlobal before any verb is reached, so a command line
	// that is wrong is still answered in the shape the caller asked for.
	json bool
	// home is the home root this command acts on.
	home *home.Home
	// flags is the verb's own flag set, built by the verb.
	flags *flag.FlagSet
}

// Run parses the command line, runs the verb, renders the answer, and returns
// the exit code. It is the whole of the binary's behaviour, so a test drives
// the product rather than a rearrangement of it.
func Run(ctx context.Context, env Environment) machine.Code {
	in := &invocation{env: env}
	answer, err := dispatch(ctx, in)
	return render(in, answer, err)
}

// dispatch resolves the verb and runs it.
func dispatch(ctx context.Context, in *invocation) (any, error) {
	args, err := in.parseGlobal()
	if err != nil {
		return nil, err
	}
	name := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = args[0]
		args = args[1:]
	}
	in.args = args
	for _, v := range verbs {
		if v.name != name {
			continue
		}
		return v.run(ctx, in)
	}
	return nil, usagef("%q is not a command. %s", name, usageText())
}

// parseGlobal reads the flags that apply to every verb and returns what is
// left. It stops at the first argument that is not one of them, so a verb's
// own flags are the verb's to parse.
func (in *invocation) parseGlobal() ([]string, error) {
	args := in.env.Args
	root := ""
	for len(args) > 0 {
		switch {
		case args[0] == "--json":
			in.json = true
			args = args[1:]
		case args[0] == "--version":
			return nil, versionAnswer{version: in.env.Version}
		case args[0] == "-h", args[0] == "--help":
			return nil, usagef("%s", usageText())
		case args[0] == "--home":
			if len(args) < 2 {
				return nil, usagef("--home needs a path")
			}
			root, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--home="):
			root, args = strings.TrimPrefix(args[0], "--home="), args[1:]
		default:
			return in.resolveHome(root, args)
		}
	}
	return in.resolveHome(root, args)
}

// resolveHome settles which home this command acts on: the one --home names,
// or the one internal/home resolves from the environment.
func (in *invocation) resolveHome(root string, args []string) ([]string, error) {
	var err error
	if root == "" {
		if root, err = home.Resolve(in.env.Getenv); err != nil {
			return nil, err
		}
	}
	if in.home, err = home.Open(root); err != nil {
		return nil, err
	}
	return args, nil
}

// versionAnswer is what --version reports. It is an error value so that the
// flag short-circuits the verb without a second path through dispatch, and it
// renders as an answer rather than as a failure.
type versionAnswer struct{ version string }

// Error renders the version, which is what a caller reading this as an error
// would print.
func (v versionAnswer) Error() string { return v.version }

// parseFlags gives the verb its own flag set, with usage written to the
// command's own error stream rather than to the process's.
func (in *invocation) parseFlags(name string, declare func(*flag.FlagSet)) error {
	set := flag.NewFlagSet("assistant "+name, flag.ContinueOnError)
	set.SetOutput(in.env.Stderr)
	declare(set)
	if err := set.Parse(in.args); err != nil {
		return usageError{err: err}
	}
	in.flags = set
	in.args = set.Args()
	return nil
}

// workingCopy resolves the working copy the command was run in, and returns
// its root.
//
// The root rather than the directory the command was run in is what matters: a
// gate is filed under a hash of the working copy's path, so the same command
// run from a subdirectory has to be about the same gate.
func (in *invocation) workingCopy(ctx context.Context) (string, error) {
	working, err := vcs.OpenWorktree(ctx, in.env.WorkingDir)
	if err != nil {
		return "", fmt.Errorf("%s is not inside a working copy: %w", in.env.WorkingDir, err)
	}
	return working.WorkingRoot(ctx)
}

// openStore opens the home's database for a verb that acts on the working copy
// rather than on a run.
//
// It opens the same file the service holds open, which is safe and is not the
// home's lock: internal/home says what that lock does and does not cover, and
// internal/store serializes its own writers.
func (in *invocation) openStore(ctx context.Context) (*store.Store, error) {
	if err := in.home.Create(); err != nil {
		return nil, err
	}
	return openRecords(ctx, in.home)
}

// progressf writes a line to standard error. Progress belongs there, per PRD
// section 9, so that a caller redirecting standard output gets answers only.
func (in *invocation) progressf(format string, a ...any) {
	writef(in.env.Stderr, format+"\n", a...)
}

// usageText is the command list, which is the verb table read out.
func usageText() string {
	var b strings.Builder
	b.WriteString("Usage: assistant [--json] [--home PATH] [command]\n\nCommands:\n")
	for _, v := range verbs {
		name := v.name
		if name == "" {
			name = "(none)"
		}
		fmt.Fprintf(&b, "  %-9s %s\n", name, v.summary)
	}
	b.WriteString("\n  --version   Report the build.")
	return b.String()
}

// render writes the answer and returns the exit code.
//
// A structured answer goes to standard output whether or not it reports a
// failure, because a driving agent parses one document per invocation and a
// failure it has to read out of prose is one it will read wrong.
func render(in *invocation, answer any, err error) machine.Code {
	env := in.env
	var version versionAnswer
	if errors.As(err, &version) {
		writeln(env.Stdout, version.version)
		return machine.ExitOK
	}
	encoder := machine.NewEncoder(env.Stdout)
	if err != nil {
		code := machine.ExitFailure
		var usage usageError
		if errors.As(err, &usage) {
			code = machine.ExitUsage
		}
		failure := machine.Failure{Error: err.Error(), Code: failureCode(err), NextAction: nextAction(err)}
		if in.json {
			_ = encoder.Encode(failure)
			return code
		}
		writeln(env.Stderr, failure.Error)
		if failure.NextAction != "" {
			writeln(env.Stderr, failure.NextAction)
		}
		return code
	}
	if in.json {
		if err := encoder.Encode(answer); err != nil {
			writeln(env.Stderr, err)
			return machine.ExitFailure
		}
		return exitFor(answer)
	}
	readOut(env.Stdout, answer)
	return exitFor(answer)
}

// exitFor is the code an answer ends with. A run that stopped for a decision
// is a success; a run that ended without a verdict is an operational failure.
func exitFor(answer any) machine.Code {
	switch v := answer.(type) {
	case machine.Run:
		if v.Outcome == machine.OutcomeFailed {
			return machine.ExitFailure
		}
	case machine.Doctor:
		if !v.CanStartRun {
			return machine.ExitFailure
		}
	case machine.Lifecycle:
		if !v.Accepted {
			return machine.ExitFailure
		}
	case machine.Plan:
		if !v.Applied {
			return machine.ExitFailure
		}
	}
	return machine.ExitOK
}
