package journey

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/dayamjz/assistant/internal/fixture"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
)

// ReadyTimeout bounds how long Serve waits for the service to answer a
// readiness check. PRD section 8 makes launch and readiness different states,
// so this waits for the second.
const ReadyTimeout = 30 * time.Second

// readyPoll is how often the wait re-asks.
const readyPoll = 20 * time.Millisecond

// reapGrace bounds how long a kill that reported a failure waits for this
// journey's reaper to account for the process anyway.
//
// It is not a timeout on the kill. The process is either already reaped, in
// which case the reaper closes its channel as soon as the goroutine holding it
// is scheduled and any grace at all is enough, or it is still running, in
// which case no wait would change the answer. What the length buys is only
// that a loaded machine does not turn the first case into the second.
const reapGrace = 5 * time.Second

// Journey is one home, one subject scenario, and the product binary driven
// against them as a process.
//
// Four things a command's answer turns on are stated here rather than left to
// whatever machine is running the harness: the home root, the working
// directory, the front of the PATH the agent is resolved off, and the git
// configuration file the product's own git invocations read. Each of those is
// a channel a developer's own settings would otherwise decide a run through.
//
// The rest of the environment is inherited, which is internal/vcs's approach
// and is taken for the reason that package gives: an environment built from
// nothing drops what git and the processes it starts need, and on Windows they
// do not run without it. So the isolation this offers is the four settings
// above and nothing else. The remainder of PATH in particular is the caller's,
// and a machine with a different git on it is running a different git.
type Journey struct {
	binary     string
	root       string
	scenario   fixture.Scenario
	dir        string
	env        map[string]string
	service    *serving
	agentEntry string
}

// serving is the service process this harness started, together with the
// goroutine that reaps it.
//
// The reaper is the whole of why this is not a bare exec.Cmd. An exec.Cmd
// fills in its ProcessState inside Wait alone, so a harness that waited
// nowhere until it killed the child could not tell a service that is still
// starting from one that is already gone: a service that dies on startup, over
// a home whose lock is held or a configuration it refuses, would be asked for
// its readiness until the timeout ran out and then reported as silence rather
// than as the exit it was.
type serving struct {
	cmd *exec.Cmd
	log *os.File
	// done is closed once Wait has returned, which is what makes err safe to
	// read and ProcessState safe to look at.
	done chan struct{}
	// err is what Wait reported. It is written before done is closed and read
	// only after, so the close is the whole of the ordering it needs.
	err error
}

// exited reports whether the process has ended, and what Wait made of it.
func (s *serving) exited() (bool, error) {
	select {
	case <-s.done:
		return true, s.err
	default:
		return false, nil
	}
}

// end kills the process and returns once the reaper holds its exit.
//
// What the kill itself answered is not what this reports. Asking to kill a
// process this journey's own reaper has already reaped is refused, and which
// refusal it is is not settled here: this code recognized one of them and met
// another on a platform it was not written on. So a caller conditioned on the
// refusal it had seen is conditioned on where it was running rather than on
// what had happened, which is why nothing below reads it and why
// TestKillIsAnsweredByTheReaperAndNotByWhatTheKillReported asserts only that
// there was one.
//
// The reaper is the one thing here that can answer it without that problem: it
// holds the process's exit or it does not. So a kill that reported a failure
// is reported on only when the reaper still has nothing, which is the case
// where something really is still serving.
func (s *serving) end() error {
	killed := s.cmd.Process.Kill()
	if killed == nil {
		<-s.done
		return nil
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(reapGrace):
		return fmt.Errorf("journey: killing the service: %w", killed)
	}
}

// Options are the parts of a Journey a caller settles.
type Options struct {
	// Scenario is the subject the product is pointed at. It is required. A
	// caller that will change anything about the scenario takes it with Claim
	// first; one that only reads it, or that points Dir at a clone of its
	// origin, does not.
	Scenario fixture.Scenario
	// Dir is the directory commands run in, empty for the scenario's own
	// working copy. It is what locates the working copy every repository verb
	// is about.
	Dir string
	// Env is written over the environment every command and the service run
	// with. A value that is empty removes the variable, which is how a test
	// says a channel is closed rather than pointed elsewhere.
	Env map[string]string
	// AgentArguments are the words the resolved agent entry carries after its
	// name, which is the seam a stand-in is reached through. Empty leaves the
	// home carrying an empty configuration document, so a run resolves whatever
	// agent the machine has rather than the stand-in.
	//
	// Each element has to be one word. An agent entry is one string on the
	// wire that both internal/config and internal/agents split on whitespace,
	// so an element carrying any would arrive as several arguments and an
	// empty one would vanish, and Open refuses either rather than handing the
	// run a command line nobody asked for.
	AgentArguments []string
}

// Open creates a home, installs the agent seam, and returns the Journey.
//
// The home root is made short deliberately. A home holds a unix domain socket,
// whose path is bounded by the operating system at a length a temporary
// directory under a long parent exceeds, and the failure that produces is the
// service refusing to bind with an error about an invalid argument that names
// nothing about length.
func Open(opts Options) (*Journey, error) {
	binary, err := Binary()
	if err != nil {
		return nil, err
	}
	if opts.Scenario.Name == "" {
		return nil, errors.New("journey: a journey needs a scenario to point the product at")
	}
	if err := oneWordEach(opts.AgentArguments); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		return nil, fmt.Errorf("journey: making a home root: %w", err)
	}
	// Nothing else removes this root until Close, and a caller holding an
	// error holds no Journey to close. So every failure below takes the root
	// with it rather than leaving a directory, sometimes a created home, under
	// the temporary directory of whatever machine ran the suite.
	opened := false
	defer func() {
		if !opened {
			_ = os.RemoveAll(root)
		}
	}()
	dir := opts.Dir
	if dir == "" {
		dir = opts.Scenario.WorkingCopy
	}
	j := &Journey{
		binary:   binary,
		root:     root,
		scenario: opts.Scenario,
		dir:      dir,
		env:      map[string]string{},
	}
	shim, err := Shims()
	if err != nil {
		return nil, err
	}
	j.env["PATH"] = shim + string(os.PathListSeparator) + os.Getenv("PATH")
	j.env[home.Var] = root
	// The product's own git invocations read the configuration file the
	// fixture wrote and no system file, so neither of the two files a
	// developer's own git configuration arrives in decides what a run does.
	// internal/vcs deliberately keeps GIT_CONFIG_GLOBAL, which is the channel
	// the hostile-template conditions are planted against, so this is a
	// default a test overrides rather than something it works around.
	if config, ok := opts.Scenario.Paths[fixture.GitConfigKey]; ok {
		j.env["GIT_CONFIG_GLOBAL"] = config
	}
	j.env["GIT_CONFIG_NOSYSTEM"] = "1"
	j.env["GIT_TERMINAL_PROMPT"] = "0"
	for name, value := range opts.Env {
		j.env[name] = value
	}
	if len(opts.AgentArguments) > 0 {
		j.agentEntry = strings.Join(append([]string{AgentShimName}, opts.AgentArguments...), " ")
	}
	if err := j.WriteConfiguration(nil); err != nil {
		return nil, err
	}
	opened = true
	return j, nil
}

// oneWordEach reports why an agent argument cannot survive the entry it is
// written into, and nil when every one of them can.
//
// The entry is a single configuration string, and both readers of it split on
// whitespace: internal/config walks the words to find a reserved flag, and
// internal/agents takes the first as the agent's name and the rest as its
// arguments. So an element carrying a space arrives as two arguments and an
// empty one arrives as none, and in this build no run carries a stage body
// through an agent launch, which means neither would produce a symptom. Refusing is
// what keeps that from being a silently truncated path on a machine whose
// temporary directory happens to have a space in it.
func oneWordEach(arguments []string) error {
	for i, argument := range arguments {
		if argument == "" {
			return fmt.Errorf("journey: agent argument %d is empty, and an agent entry is one string "+
				"its readers split on whitespace, so an empty word is dropped rather than passed", i)
		}
		if at := strings.IndexFunc(argument, unicode.IsSpace); at >= 0 {
			return fmt.Errorf("journey: agent argument %d, %q, carries whitespace at byte %d, and an "+
				"agent entry is one string its readers split on whitespace, so it would reach the "+
				"agent as several arguments rather than as this one", i, argument, at)
		}
	}
	return nil
}

// WriteConfiguration writes the home's own configuration document, which is
// the operator's global layer, carrying the agent entry this journey was
// opened with and whatever else the caller names.
//
// The agent entry is carried on every write rather than left to the caller,
// because it is what reaches the stand-in: internal/agents resolves an entry's
// first word against the catalog and hands the rest to the adapter, and the
// adapter looks that name up on PATH, where a copy of the harness binary
// stands. A caller writing a configuration without it would silently move the
// run onto whatever agent the machine happens to have installed.
//
// It refuses to write a key the caller named that would replace the agent
// entry, because a caller doing that has asked for two different agents at
// once and taking one silently is the failure this refuses.
//
// A document that composes to nothing is written as an empty document rather
// than skipped, so a caller clearing a key clears it. Returning early there
// would leave whatever was written before standing, and the caller that asked
// for nothing would go on running against the document it meant to remove.
func (j *Journey) WriteConfiguration(document map[string]any) error {
	whole := map[string]any{}
	for name, value := range document {
		whole[name] = value
	}
	if entry := j.agentEntry; entry != "" {
		if named, taken := whole["agent"]; taken {
			return fmt.Errorf("journey: this journey resolves the agent %q and the configuration being "+
				"written names %v; a journey has one agent", entry, named)
		}
		whole["agent"] = entry
	}
	body, err := json.Marshal(whole)
	if err != nil {
		return fmt.Errorf("journey: rendering the home's configuration: %w", err)
	}
	opened, err := home.Open(j.root)
	if err != nil {
		return err
	}
	if err := opened.Create(); err != nil {
		return err
	}
	if err := os.WriteFile(opened.ConfigFile(), append(body, '\n'), 0o600); err != nil {
		return fmt.Errorf("journey: writing the home's configuration: %w", err)
	}
	return nil
}

// Root is the home this journey acts on.
func (j *Journey) Root() string { return j.root }

// Scenario is the subject the product is pointed at.
func (j *Journey) Scenario() fixture.Scenario { return j.scenario }

// Dir is the directory commands run in.
func (j *Journey) Dir() string { return j.dir }

// Environment is the whole environment a command runs with: this process's
// own, with the journey's own settings written over it. A setting whose value
// is empty is removed rather than set to nothing, which is how a test closes a
// channel rather than pointing it somewhere harmless.
func (j *Journey) Environment() []string {
	return writeOver(os.Environ(), j.env)
}

// writeOver returns base with these settings written over it, removing the
// ones whose value is empty so that a caller can close a channel rather than
// point it somewhere harmless.
func writeOver(base []string, over map[string]string) []string {
	if len(over) == 0 {
		return base
	}
	out := make([]string, 0, len(base)+len(over))
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, replaced := over[name]; replaced {
			continue
		}
		out = append(out, entry)
	}
	for name, value := range over {
		if value == "" {
			continue
		}
		out = append(out, name+"="+value)
	}
	return out
}

// Answer is one invocation of the product binary: what it exited with and what
// it wrote to each stream.
type Answer struct {
	// Command is the whole command line, for a failure that says what was run.
	Command string
	// Code is the exit code, which internal/machine owns the meaning of.
	Code machine.Code
	// Stdout is the structured answer, one document under --json.
	Stdout string
	// Stderr is progress and human-facing failure text.
	Stderr string
	// Ended is whether this harness stopped the command rather than the
	// command finishing. It is set for a verb that runs until it is
	// interrupted, and it says that Code describes how the command was ended
	// rather than what it decided.
	Ended bool
}

// String renders the invocation and everything it produced, which is what a
// failing assertion has to print to be diagnosable.
func (a Answer) String() string {
	return fmt.Sprintf("%s\n  exit %d (%s)\n  stdout: %s\n  stderr: %s",
		a.Command, int(a.Code), a.Code, strings.TrimSpace(a.Stdout), strings.TrimSpace(a.Stderr))
}

// Decode reads the answer's document into v.
//
// It refuses standard output that is not exactly one document, because the
// contract this harness holds the surface to is one document per invocation:
// a caller that decoded the first of several would be reading a surface that
// had already broken the contract and reporting it as working.
func (a Answer) Decode(v any) error {
	decoder := json.NewDecoder(strings.NewReader(a.Stdout))
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("journey: reading the answer of %s: %w", a, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("journey: %s wrote more than one document, and the surface writes one per "+
			"invocation: the second was %s", a, extra)
	}
	return nil
}

// Documents is how many whole documents an answer wrote on standard output.
//
// It reports an error for output that is not a sequence of documents, which is
// the shape the machine interface promises: a consumer reading one document at
// a time must never be handed a fragment, and a verb that answers nothing
// writes nothing rather than something that decodes to nothing.
//
// Most verbs write one. assistant watch writes one per thing it sees, because
// it reports what is in flight and then follows a stream, and a view that only
// printed once the stream ended would be a log.
func (a Answer) Documents() (int, error) {
	decoder := json.NewDecoder(strings.NewReader(a.Stdout))
	count := 0
	for {
		var document json.RawMessage
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return count, nil
		}
		if err != nil {
			return count, fmt.Errorf("journey: %s wrote something that is not a document after %d of "+
				"them: %w", a, count, err)
		}
		count++
	}
}

// Failure is the failure document an answer carries, and whether it carried
// one. A refusal is reported as a document under --json whether or not it
// succeeded, so this is how a test reads what the product refused with.
func (a Answer) Failure() (machine.Failure, bool) {
	var failure machine.Failure
	if err := a.Decode(&failure); err != nil || failure.Error == "" {
		return machine.Failure{}, false
	}
	return failure, true
}

// Message is the text the product refused with, wherever it wrote it: the
// failure document under --json, and standard error otherwise. It is what a
// condition's recorded substrings are checked against.
func (a Answer) Message() string {
	if failure, ok := a.Failure(); ok {
		return failure.Error + "\n" + failure.NextAction
	}
	return a.Stderr
}

// Command runs the product binary with the journey's home named before the
// verb, and returns everything it produced.
//
// The home and the output shape are written before the verb because that order
// is honoured everywhere on this surface, and the order after --version or
// --help is not. That is a limit of the binary rather than of this harness;
// README.md says so and nothing here works around it.
func (j *Journey) Command(args ...string) Answer {
	return j.CommandIn(j.dir, args...)
}

// CommandIn is Command run from another directory, for a verb whose subject is
// a working copy other than the journey's own.
func (j *Journey) CommandIn(dir string, args ...string) Answer {
	return j.CommandWith(dir, nil, args...)
}

// CommandWith is CommandIn with more written over the environment for this one
// command.
//
// It exists for the conditions whose whole plant is a variable or a
// configuration file reaching one process rather than every process: a
// template offered to the initialization that creates a gate and not to the
// one that repairs it, for instance. A journey whose whole environment carried
// it would be exercising a different condition.
func (j *Journey) CommandWith(dir string, env map[string]string, args ...string) Answer {
	return j.command(dir, env, 0, args...)
}

// CommandBounded is Command for a verb that runs until something stops it.
//
// assistant watch is the one this surface has: it reports what is in flight
// and then follows the service's event stream, so it ends by being
// interrupted rather than by finishing. The answer carries Ended, so a caller
// reads the exit code as how the command was stopped rather than as what it
// decided.
func (j *Journey) CommandBounded(dir string, within time.Duration, args ...string) Answer {
	return j.command(dir, nil, within, args...)
}

// CommandExactly runs the binary with the command line exactly as given,
// prepending nothing.
//
// Every other entry point writes --json and --home ahead of the verb, because
// that order is honoured everywhere on this surface. This one exists for the
// test whose subject is where on the command line an argument may appear, and
// which therefore cannot have the harness deciding that for it. The home still
// reaches the process, through the environment this journey sets.
func (j *Journey) CommandExactly(dir string, args ...string) Answer {
	return j.exec(dir, nil, 0, args, args)
}

// command runs the binary and collects everything it produced. A non-zero
// within bounds how long it may run before this harness ends it.
func (j *Journey) command(dir string, env map[string]string, within time.Duration, args ...string) Answer {
	return j.exec(dir, env, within, append([]string{"--json", "--home", j.root}, args...), args)
}

// exec starts the binary with whole as its command line and collects
// everything it produced. named is what the answer reports having been asked,
// which is the caller's own words rather than what was written around them.
func (j *Journey) exec(dir string, env map[string]string, within time.Duration, whole, named []string) Answer {
	cmd := exec.Command(j.binary, whole...)
	cmd.Dir = dir
	cmd.Env = writeOver(j.Environment(), env)
	var out, errs strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errs
	answer := Answer{Command: "assistant " + strings.Join(named, " ") + "  (in " + dir + ")"}
	if err := cmd.Start(); err != nil {
		answer.Code = machine.ExitFailure
		answer.Stderr = "journey: the binary did not run: " + err.Error()
		return answer
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if within > 0 {
		select {
		case <-done:
		case <-time.After(within):
			answer.Ended = true
			_ = cmd.Process.Kill()
			<-done
		}
	} else {
		<-done
	}
	answer.Code = machine.Code(cmd.ProcessState.ExitCode())
	answer.Stdout, answer.Stderr = out.String(), errs.String()
	return answer
}

// Serve starts the service in a process this harness owns and waits until it
// answers a readiness check.
//
// It runs the service in the foreground of a child rather than letting the
// binary launch a detached one, because a harness that cannot name the process
// cannot kill it, and killing it at a stage boundary is P6's own stated
// verification criterion. The flag it uses is the product's own.
//
// A Serve that reported a failure after the child started still leaves this
// journey serving, and the caller owes it a Kill. That is deliberate: the
// readiness check failing says the service never answered, never that no
// process is there, and a Serve that cleared the handle on its way out would
// leave a running child nothing in this harness could name. So the two
// failures are told apart by what the caller does next rather than by the
// error: a second Serve is refused, and Kill is what ends whatever is there
// and releases the log this one opened.
func (j *Journey) Serve() error {
	if j.service != nil {
		return errors.New("journey: this journey is already serving")
	}
	opened, err := home.Open(j.root)
	if err != nil {
		return err
	}
	if err := opened.Create(); err != nil {
		return err
	}
	log, err := os.OpenFile(j.harnessLog(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("journey: opening the service log: %w", err)
	}
	cmd := exec.Command(j.binary, "--home", j.root, "service", "start", "--foreground")
	cmd.Dir = j.dir
	cmd.Env = j.Environment()
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return fmt.Errorf("journey: starting the service: %w", err)
	}
	reaped := &serving{cmd: cmd, log: log, done: make(chan struct{})}
	go func() {
		reaped.err = cmd.Wait()
		close(reaped.done)
	}()
	j.service = reaped
	if err := j.WaitReady(); err != nil {
		return err
	}
	return nil
}

// WaitReady blocks until the service answers a readiness check, or reports why
// it never did.
//
// It asks the surface rather than looking for a process or a socket file,
// because PRD section 8 makes only a real answer proof of readiness, and
// because a service that died on startup leaves both of the other two behind.
func (j *Journey) WaitReady() error {
	deadline := time.Now().Add(ReadyTimeout)
	var last Answer
	for time.Now().Before(deadline) {
		last = j.Command("service", "status")
		var state machine.Service
		if err := last.Decode(&state); err == nil && state.Running {
			return nil
		}
		if j.service != nil {
			if ended, err := j.service.exited(); ended {
				return fmt.Errorf("journey: the service exited before it was ready: %s\n%s",
					describeExit(j.service.cmd, err), j.ServiceLog())
			}
		}
		time.Sleep(readyPoll)
	}
	return fmt.Errorf("journey: the service did not answer a readiness check within %s: %s\n%s",
		ReadyTimeout, last, j.ServiceLog())
}

// Kill ends the serving process the way a crash does, and reports the run
// records left behind by doing so.
//
// It is a kill rather than a stop on purpose. A service asked to stop unwinds,
// and what P6 is about is the service that did not get the chance: the run's
// position has to be recoverable from what was already durable, not from
// anything the process wrote on its way out.
//
// It promises two things, and it keeps the second whatever became of the
// first. Nothing is serving this home afterwards, which end establishes. And
// this harness holds nothing of the home open afterwards, which is why the log
// is released and the journey stops serving on the way out of every path
// rather than only the one where the kill went as expected: a handle this
// process still holds is a home that cannot be removed on a platform where an
// open file is not unlinkable, so a Kill that reported a failure and kept the
// handle would turn one failure into two and lose the first behind the second.
//
// The residual gap is on the first promise, and it is what that ordering
// costs. A failure from end is exactly the case where this journey's reaper
// still holds nothing, so something really is still serving, and by then this
// journey has already let the process go: no later Kill, Close or Cleanup can
// reach it, and Close removes the home underneath it regardless. The error
// this returns is therefore the only notice a caller gets that a process
// outlived its home, and there is nothing here for it to retry.
func (j *Journey) Kill() error {
	if j.service == nil {
		return errors.New("journey: this journey is not serving")
	}
	service := j.service
	j.service = nil
	var errs []error
	if err := service.end(); err != nil {
		errs = append(errs, err)
	}
	if service.log != nil {
		if err := service.log.Close(); err != nil {
			errs = append(errs, fmt.Errorf("journey: closing the service log %s: %w", j.harnessLog(), err))
		}
	}
	return errors.Join(errs...)
}

// describeExit renders how a serving process ended, for a failure that says
// what happened rather than that nothing answered.
func describeExit(cmd *exec.Cmd, waited error) string {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.String()
	}
	if waited != nil {
		return waited.Error()
	}
	return "for a reason the operating system did not report"
}

// ServicePID is the operating system's identifier for the process serving this
// home, and zero when none is.
//
// It is what a test records to have evidence that a service it killed was
// replaced by another process rather than by the same one carrying on. A
// harness that recorded only what it intended to do would report a run
// recovered across a kill that never happened, which is the check proving
// nothing while looking like it proved the most.
func (j *Journey) ServicePID() int {
	if j.service == nil || j.service.cmd.Process == nil {
		return 0
	}
	return j.service.cmd.Process.Pid
}

// ServiceLog is everything the serving processes of this home have printed,
// which is where a failure that happened inside the service is explained.
//
// It reads two files: the one this harness redirects a child's streams into,
// which is its own and is named here, and the product's own lifecycle log,
// whose path is internal/home's and is asked of it. A layout spelled here
// instead would go quietly empty the day that package moved it, which is
// exactly when a failing P6 or concurrency test needs it most.
func (j *Journey) ServiceLog() string {
	var b strings.Builder
	paths := []string{j.harnessLog()}
	if opened, err := home.Open(j.root); err == nil {
		paths = append(paths, opened.ServiceLog())
	} else {
		b.WriteString("--- the home " + j.root + " could not be opened to find its own log: " +
			err.Error() + " ---\n")
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil || len(body) == 0 {
			continue
		}
		b.WriteString("--- " + path + " ---\n")
		b.Write(body)
	}
	return b.String()
}

// harnessLog is the file this harness redirects a serving child's streams
// into. It is this package's own file rather than part of PRD section 8's
// layout, which is why it is the one path under the root spelled here.
func (j *Journey) harnessLog() string {
	return filepath.Join(j.root, "journey-service.log")
}

// Database is the file internal/store opens for this home, asked of the
// package that owns the layout.
//
// A test that opened a path it composed itself would create a fresh empty
// database the day the layout moved, and report on records nobody wrote rather
// than reporting that there are none.
func (j *Journey) Database() (string, error) {
	opened, err := home.Open(j.root)
	if err != nil {
		return "", err
	}
	return opened.Database(), nil
}

// Close ends the serving process and removes the home.
//
// The subject is left standing. It is the fixture's, this journey only pointed
// at it, and a harness that removed it would take the evidence of what a run
// did along with it.
func (j *Journey) Close() error {
	var errs []error
	if j.service != nil {
		errs = append(errs, j.Kill())
	}
	if err := os.RemoveAll(j.root); err != nil {
		errs = append(errs, fmt.Errorf("journey: removing the home %s: %w", j.root, err))
	}
	return errors.Join(errs...)
}
