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
	// summary is PRD section 9's line for the verb, narrowed where this build
	// does something narrower or more precise than the table describes. A
	// reader comparing the two finds a summary that says what the verb does
	// rather than one that repeats a line the verb does not answer to: rerun
	// says it acts on the branch you are standing on, which the table leaves
	// open and this build settles, and sync leaves out the table's plan and
	// confirmation, which this build has no custody module to carry out.
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
	{"rerun", "Start a fresh run of this branch from its last known head, inheriting the recorded intent.", rerun},
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
	// that is wrong is still answered in the shape the caller asked for, and
	// it is settled again by the verb's own flag set so that the same flag
	// after the verb reaches the same field.
	json bool
	// root is the home root --home named, empty when the environment settles
	// it. It is the string rather than an open home because --home is accepted
	// after the verb as well as before it, so which home a command acts on is
	// not settled until the verb's flags have been parsed.
	root string
	// home is the home root this command acts on. It is opened by parseFlags,
	// so a verb reads it after it has parsed its own flags and not before.
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

// parseGlobal reads the flags that apply to every verb where they may appear
// before one, and returns what is left. It stops at the first argument that is
// not one of them, so a verb's own flags are the verb's to parse; the same
// flags after the verb are declared on the verb's own set by parseFlags.
func (in *invocation) parseGlobal() ([]string, error) {
	args := in.env.Args
	for len(args) > 0 {
		switch {
		case args[0] == "--json":
			in.json = true
			args = args[1:]
		case args[0] == "--version":
			return nil, versionAnswer{version: in.env.Version}
		case args[0] == "-h", args[0] == "--help":
			return nil, helpAnswer{text: usageText()}
		case args[0] == "--home":
			if len(args) < 2 {
				return nil, usagef("--home needs a path")
			}
			in.root, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--home="):
			in.root, args = strings.TrimPrefix(args[0], "--home="), args[1:]
		default:
			return args, nil
		}
	}
	return args, nil
}

// resolveHome settles which home this command acts on: the one --home named,
// wherever on the command line it was, or the one internal/home resolves from
// the environment.
func (in *invocation) resolveHome() error {
	root := in.root
	var err error
	if root == "" {
		if root, err = home.Resolve(in.env.Getenv); err != nil {
			return err
		}
	}
	in.home, err = home.Open(root)
	return err
}

// versionAnswer is what --version reports. It is an error value so that the
// flag short-circuits the verb without a second path through dispatch, and it
// renders as an answer rather than as a failure.
type versionAnswer struct{ version string }

// Error renders the version, which is what a caller reading this as an error
// would print.
func (v versionAnswer) Error() string { return v.version }

// helpAnswer is the command list, or one verb's flags, asked for rather than
// stumbled into. It travels the same way versionAnswer does and for the same
// reason, and it is answered rather than refused: PRD section 9 gives
// machine.ExitUsage the meaning "incorrect usage", and asking what the
// commands are is not that.
type helpAnswer struct{ text string }

// Error renders the help, which is what a caller reading this as an error
// would print.
func (h helpAnswer) Error() string { return h.text }

// The two flags every verb accepts, named so that the declaration below and
// the reordering that hoists them read the same two names rather than two
// lists that could drift apart.
const (
	jsonFlagName = "json"
	homeFlagName = "home"
)

// parseFlags gives the verb its own flag set.
//
// The two global flags are declared on every verb's set as well as read before
// the verb, so --json and --home mean the same thing wherever they appear.
// They are read back into the invocation rather than left on the set, because
// the output shape is decided from that field and a --json the verb parsed
// would otherwise be a flag that was accepted and ignored. They are read back
// before a parse failure is returned, so a command line that is wrong is still
// answered in the shape the caller asked for.
//
// Nothing here writes to a stream. The flag package's own reports would land
// on standard error beside the one render writes, and an explicit request for
// help would land there rather than on standard output, so the set is silent
// and what it refuses is returned.
func (in *invocation) parseFlags(name string, declare func(*flag.FlagSet)) error {
	return in.parseArgs(name, 0, declare)
}

// parseArgs is parseFlags for a verb that takes arguments of its own, at most
// limit of them.
//
// Refusing what is left over is here rather than in each verb because a verb
// that forgot the check accepted a mistyped word and acted anyway, which on a
// surface whose third exit code means "your arguments were wrong" is the one
// answer a driving agent cannot recover from. A verb that takes an argument
// says so by coming through here; every other verb takes none by default.
func (in *invocation) parseArgs(name string, limit int, declare func(*flag.FlagSet)) error {
	set := flag.NewFlagSet(commandName(name), flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.Usage = func() {}
	asJSON := in.json
	root := in.root
	set.BoolVar(&asJSON, jsonFlagName, asJSON, "write the answer as one document rather than reading it out")
	set.StringVar(&root, homeFlagName, root, "the home root this command acts on")
	declare(set)
	err := set.Parse(reorder(set, in.args))
	in.json, in.root = asJSON, root
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return helpAnswer{text: verbUsage(name, set)}
		}
		return usageError{err: err}
	}
	in.flags = set
	in.args = set.Args()
	if len(in.args) > limit {
		return usagef("%s takes %s, and was given %d: %s",
			commandName(name), arguments(limit), len(in.args), strings.Join(in.args, " "))
	}
	return in.resolveHome()
}

// arguments says how many arguments a verb takes, for a refusal that says what
// was expected rather than only that what arrived was wrong.
func arguments(limit int) string {
	switch limit {
	case 0:
		return "no arguments"
	case 1:
		return "at most one argument"
	default:
		return fmt.Sprintf("at most %d arguments", limit)
	}
}

// reorder puts a verb's arguments in the order flag.FlagSet.Parse needs.
//
// Parse stops at the first argument that is not a flag, so a flag written
// after one that is not would be left in the arguments rather than parsed:
// assistant runs <id> --json would refuse for having been given two names
// instead of answering with a document. This moves every flag ahead of every
// argument that is not one, so where a flag stands on the command line stops
// deciding whether it is read at all, for every verb rather than for the two
// that take a name today.
//
// The two global flags go first among the flags. They decide how an answer is
// rendered and which home it is about, so a refusal raised over one of the
// verb's own flags must not come before them.
//
// set is the authority for which flags take the argument behind them, so
// nothing here holds a second list of what a verb accepts. A flag the set does
// not declare is moved as it stands and refused by Parse. Everything after a
// bare "--" is left alone, which is what that terminator means.
func reorder(set *flag.FlagSet, args []string) []string {
	var global, flags, rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			rest = append(rest, arg)
			continue
		}
		name, _, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		written := []string{arg}
		if !hasValue && takesValue(set, name) && i+1 < len(args) {
			i++
			written = append(written, args[i])
		}
		if name == jsonFlagName || name == homeFlagName {
			global = append(global, written...)
			continue
		}
		flags = append(flags, written...)
	}
	ordered := make([]string, 0, len(args))
	ordered = append(ordered, global...)
	ordered = append(ordered, flags...)
	return append(ordered, rest...)
}

// takesValue reports whether a flag the set declares reads the argument behind
// it as its value.
//
// A boolean flag does not, which is the distinction the flag package itself
// makes and which is read off the declared value rather than restated here. A
// flag the set does not declare is reported as taking nothing, so Parse
// refuses the flag rather than this quietly swallowing whatever followed it.
func takesValue(set *flag.FlagSet, name string) bool {
	declared := set.Lookup(name)
	if declared == nil {
		return false
	}
	boolean, ok := declared.Value.(interface{ IsBoolFlag() bool })
	return !ok || !boolean.IsBoolFlag()
}

// flagWasSet reports whether a caller wrote a flag, as opposed to it standing
// at its default.
//
// Reading the value cannot answer that: a flag written with the text its
// default already holds is indistinguishable from one nobody wrote. Visit
// reports the flags that were actually set and nothing else, which is what
// lets a verb take a caller at their word when they contradict a default it
// would otherwise infer.
func flagWasSet(set *flag.FlagSet, name string) bool {
	written := false
	set.Visit(func(f *flag.Flag) {
		if f.Name == name {
			written = true
		}
	})
	return written
}

// commandName is how a command names itself, which for the command with no
// verb is the program.
func commandName(verb string) string {
	if verb == "" {
		return "assistant"
	}
	return "assistant " + verb
}

// verbUsage is one verb's own flags, read off the set that would have parsed
// them so it cannot drift from what the verb accepts.
func verbUsage(name string, set *flag.FlagSet) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: %s [flags]\n\nFlags:\n", commandName(name))
	set.SetOutput(&b)
	set.PrintDefaults()
	return strings.TrimRight(b.String(), "\n")
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
	b.WriteString("\n  --json      Write the answer as one document. It is accepted before or after the command.\n")
	b.WriteString("  --home PATH The home root to act on. It is accepted before or after the command.\n")
	b.WriteString("  --version   Report the build.\n")
	b.WriteString("  --help      Print this.")
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
		return in.answer(machine.Version{Version: version.version}, version.version)
	}
	var help helpAnswer
	if errors.As(err, &help) {
		return in.answer(machine.Help{Usage: help.text}, help.text)
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
		// A verb that answers nothing writes nothing. assistant watch is the
		// one that does, and a consumer reading one document per line must not
		// be handed a trailing document that decodes to nothing.
		if answer == nil {
			return exitFor(answer)
		}
		if err := encoder.Encode(answer); err != nil {
			writeln(env.Stderr, err)
			return machine.ExitFailure
		}
		return exitFor(answer)
	}
	readOut(env.Stdout, answer)
	return exitFor(answer)
}

// answer writes something that is neither a failure nor a verb's result: the
// version and the help, which are asked for rather than produced by a verb.
//
// Both go to standard output and exit successfully, and both honour --json,
// because the contract is one document per invocation whatever the caller
// asked about. A caller decoding standard output would otherwise get a decode
// error from the two commands it is most likely to try first.
func (in *invocation) answer(document any, text string) machine.Code {
	if in.json {
		if err := machine.NewEncoder(in.env.Stdout).Encode(document); err != nil {
			writeln(in.env.Stderr, err)
			return machine.ExitFailure
		}
		return machine.ExitOK
	}
	writeln(in.env.Stdout, text)
	return machine.ExitOK
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
