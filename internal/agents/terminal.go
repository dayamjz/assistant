package agents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// A terminal worker is a worker hosted in a terminal a person can attach to:
// the agent runs as the one process of a tmux pane, in a tmux server of its
// own, and everything that reaches it is typed into that pane. It is
// CapabilityTerminalWorkers' mechanism, and this file is the whole of it, so
// internal/agents stays the one package that starts an agent process: the
// tmux server starts the agent, and this package is what asks it to, with
// the command line the adapter built and the environment WorkerEnvironment
// built.
//
// # What the server is started with
//
// One server per worker, on the socket Terminal names, started by the tmux
// client this package runs and with no configuration file read (-f
// /dev/null), so neither the operator's ~/.tmux.conf nor anything it would
// run reaches the worker's server. The client runs under WorkerEnvironment's
// output with TMUX and TMUX_PANE removed as well, so a service that itself
// runs inside tmux starts a server of its own rather than a window in the
// operator's. The server is new because the socket must not exist when
// StartInTerminal is called, which is what makes the environment the
// client's: a server already listening there would have kept its own, so a
// socket that exists is refused rather than joined.
//
// What the pane's process then runs under is that environment plus what tmux
// sets for a pane, which this package does not choose and does not list as a
// promise; the test that pins the redirecting variables' absence reads the
// worker's own environment rather than this paragraph.
//
// The command is passed to tmux as separate arguments after "--", never as a
// line a shell would split, and a command line with no arguments after the
// binary is refused, because tmux hands a lone command to a shell to run and
// a shell between the pane and the agent is a process this package did not
// mean to start.
//
// # The process, its group, and its end
//
// The pane's process is the agent itself, which the server starts as the
// leader of its own process group; Group reports its identifier, read off
// tmux's own answer at creation, and its Identity is read straight after, so
// every later question about the process is asked through that identity and
// never through the bare number. tmux's answer can arrive before the process
// leads that group, so StartInTerminal reads the process's lineage until it
// does and refuses, ending the server, if it is gone first, if its lineage
// cannot be read, or if it does not within terminalCommandTimeout; a handle is never returned naming a group
// that is not there yet. Because the process is the server's child
// and not this one's, nothing here can wait on it: Done is closed when a
// poll of the identity, every terminalPoll, reads it absent or replaced, so
// it trails the exit by up to that interval. On a platform with no
// start-time read, StartInTerminal refuses after starting, ends the server,
// and reports ErrIdentityUnreadable, since nothing could then tell the agent
// from a later process given its number.
//
// # The server is the worker's host
//
// Host is the identity of the tmux server, read at start from the pane
// process's own parent and refused unless that parent is the process the
// server reports itself as, so a handle never names a host the kernel did
// not attribute the pane to. It is read because the server is reachable
// from inside the pane: tmux sets TMUX and TMUX_PANE in the pane's
// environment, naming the socket, and the socket is a path under the home
// the worker's route names, so the agent can run "tmux new-window" on its own server
// and the shell that opens is the server's child and not the pane's. A
// caller that places a process by walking its parents to the pane, which
// internal/service's placement does, would reach the server and stop
// short, placing what the worker opened as nobody's; it places the
// server's descendants as the worker's instead, by this identity, which
// internal/session carries in the worker set for that. Nothing here
// removes the variables tmux sets, because an environment is what a
// process states and the socket's path is discoverable without them.
//
// Close ends the group through EndGroup, which signals only after the
// identity verifies the leader, politely and then forcefully after the
// runner's grace, and then ends the server, which also ends any window a
// person opened in it, and removes the socket. A worker that exits on its
// own has its server ended and its socket removed the same way once the poll
// sees it gone, and its group is not signalled, because with the leader gone
// nothing verifies that the group's identifier still names it.
//
// # Typing
//
// Send is typed, not written to a pipe, on the terms of the adapter's
// terminalTyping, which is the adapter's and not the provider's: its framing
// turns the text into the bytes a person would type, which are loaded into a
// tmux buffer of this server and pasted into the pane. The scripted adapter
// pastes every newline as the Enter key, which is what a program reading
// lines needs, and types its instructions the same way. The Claude Code
// adapter pastes each send as one bracketed paste, so a line break inside it
// stays part of the one message, and then types Enter as a key of its own;
// it types only once its reading of the screen says the interface is ready,
// and its instructions are on its command line rather than typed.
// claudeterminal.go states its terms. A start whose adapter reads the screen
// returns only once that reading says the interface is drawn, within ctx and
// terminalReadyTimeout, and is refused with ErrTerminalNotReady otherwise.
//
// That is the same path a person's keystrokes take into the pane, which is
// why typing into an attached window reaches the agent exactly as a Send
// does and why this package cannot tell the two apart afterwards: the pane
// has one input. A send ctx ends returns ctx's error, and the process group
// of the tmux client it was running is ended, which may leave part of the
// text pasted; nothing here takes it back.
//
// # The tmux clients
//
// Every tmux command this package sends is one short-lived client program,
// and each runs through procgroup.Run, PRD section 8's terms for an external
// program: as the leader of a process group of its own, under a deadline,
// with the group ended when the client returns or the deadline passes. The
// deadline is the caller's ctx where there is one and the bound of the
// client's kind, whichever ends first, one bound per kind: tmuxStartBound
// for the client that starts the server, tmuxTypeBound for a send,
// tmuxReadBound for a reading of the pane, and tmuxEndBound for the client
// that ends the server. A client still running at its bound is ended with
// its group and answered as ErrTerminalClientTimedOut, and a start or a Send
// waiting for the screen to read ready stops at the first reading that does
// so, refused with ErrTerminalNotReady beside it. So a wedged server, one
// whose clients do not answer, cannot hold a start, a Send, a Capture or a
// Close forever. A caller whose ctx ends while a client runs is answered
// with ctx's error, the client's own failure kept beside it.
//
// What this does not bound is a server that answers: a screen reading that
// fails at once is read again, and a screen that reads but never reads
// ready, a turn that does not end among them, is waited on until the
// worker ends or ctx does, and a start's wait also until
// terminalReadyTimeout passes. A Send under a ctx that never ends can wait
// that long.
//
// Ending a client's group does not end the server that client started: the
// server is not in that group once the client has answered, which every
// terminal test that types into a worker after starting it rests on, since
// a server ended with its first client would leave nothing to type into.
// The server and the agent in its pane are long-lived, and are ended by
// Close, by the poll once the agent has gone, and by a failed start, as the
// sections above state, never by a client's deadline.
//
// # Capture
//
// Capture is the pane's text, its history and its visible screen, with lines
// tmux wrapped joined back, and trailing blank lines dropped. It is a screen
// and not an output stream: what a program redrew in place reads as it was
// left. It is bounded to the runner's output limit from the end, and what
// that trimmed is counted in Dropped. What the server's own history limit
// let go is not counted, because tmux keeps no count of it; Uncounted says
// when the history had reached that limit, so a caller knows the front may
// be missing. Once the server is gone there is no screen to read, and
// Capture answers the last capture this handle took.
//
// # Residual gaps
//
//   - Done trails the exit by up to terminalPoll, and Member answers in
//     internal/session read it, so a group whose leader exited reads as a
//     worker's for up to that long; a process placed through it in that
//     window would have to hold an identifier the kernel reused within it.
//   - A descendant that moved itself to another process group is not reached
//     by EndGroup, as for a pipe worker; ending the server does not promise
//     to reach it either.
//   - Host names this worker's server and no other. A second tmux server
//     the agent starts on a socket of its own daemonizes away from the
//     pane, as any daemon the agent starts does, and neither this handle
//     nor a placement by ancestry reaches what runs there;
//     internal/service's placement.go names that shape.
//   - A process the agent started that outlives it in its group is left
//     running when the agent exits on its own, for the reason above.
//   - Text typed by a person and text sent through Send arrive on one input,
//     in whatever order they reached the server, and nothing records the
//     person's.
//   - A line longer than the terminal's line-editing limit reaches a program
//     reading lines in that mode cut short; the scripted adapter's framing
//     is what refuses such a line. The Claude Code adapter's paste is not
//     read in that mode, and is not bounded by it.
//   - What a person types is typed as they type it. The leading space the
//     Claude Code framing puts before a "!" or a "/" is put only before a
//     Send, so a person attached to a Claude Code worker's pane can still
//     type "!" into an empty input and run a shell command outside the
//     worker's grant, or "/" and run a slash command.
//   - A long or multi-line send reaches a Claude Code worker's model wrapped
//     in a pasted-content element rather than as the bare text: the CLI
//     collapsed a four-line paste on screen and sent it that way, and took
//     ten kilobytes whole the same way.
//   - The Claude Code typing rests on what one CLI version, 2.1.301, drew
//     and read: the footer and input text claudeScreen reads, the state
//     file's keys, and the approval's twenty characters. A later CLI that
//     draws something else is refused at start or at a send rather than
//     typed at blind; one that draws the same and reads its input otherwise
//     is not noticed here.
//   - A send to a Claude Code worker waits for the turn running when it is
//     made, so a caller whose bound is shorter than a turn has its text
//     refused, nothing typed, rather than queued. The CLI was seen to queue
//     a message pasted while a turn ran; nothing here relies on that.

// TerminalWorkerRunner is a Runner that can be started as a terminal worker,
// which is what CapabilityTerminalWorkers names: an adapter implements it
// exactly when it declares that capability, Resolve refuses one where the two
// disagree, and OpenTerminalWorker is the route to it from a Runner. The
// mechanism and its gaps are the file comment above.
type TerminalWorkerRunner interface {
	Runner

	// StartInTerminal starts one worker in spec.Dir as the process of a new
	// tmux server on t, types spec.Instructions into it, and returns the
	// handle. ctx bounds the start, including the typing, and nothing after
	// it. A spec that cannot be started as written is refused before any
	// process starts, with an error wrapping ErrInvalidInvocation, and so is
	// a Terminal Validate refuses.
	StartInTerminal(ctx context.Context, t Terminal, spec WorkerSpec) (TerminalWorker, error)

	// TerminalLaunchCommandSize reports how many bytes the tmux command that
	// would start spec on t totals, counting each argument with one byte more
	// for its end, as startTerminalWorker does. ok is false when this adapter
	// types spec.Instructions into the pane instead of placing them on that
	// command.
	TerminalLaunchCommandSize(t Terminal, spec WorkerSpec) (size int, ok bool, err error)
}

// Terminal is where a terminal worker is hosted: the tmux binary to run and
// the socket its new server listens on.
type Terminal struct {
	// Tmux is the absolute path of the tmux binary. The caller resolves it,
	// so which binary runs is decided once rather than per worker.
	Tmux string
	// Socket is the absolute path the server listens on. It must not exist,
	// its directory is created owner-only if it is missing, and it must fit
	// the platform's local socket path limit.
	Socket string
}

// maxSocketPath is the longest local socket path this package passes to
// tmux: one less than the 104 bytes macOS gives a socket path, which is also
// within Linux's 108, so a path is refused here rather than truncated or
// refused by the server in a message about something else.
const maxSocketPath = 103

// Validate refuses a Terminal that cannot be started as written, wrapping
// ErrInvalidInvocation and naming the field.
func (t Terminal) Validate() error {
	if !filepath.IsAbs(t.Tmux) {
		return &invocationFieldError{field: "Tmux", reason: fmt.Sprintf("%q is not an absolute path", t.Tmux)}
	}
	if !filepath.IsAbs(t.Socket) {
		return &invocationFieldError{field: "Socket", reason: fmt.Sprintf("%q is not an absolute path", t.Socket)}
	}
	if len(t.Socket) > maxSocketPath {
		return &invocationFieldError{
			field:  "Socket",
			reason: fmt.Sprintf("%d bytes exceeds the local socket path limit of %d", len(t.Socket), maxSocketPath),
		}
	}
	return nil
}

// ErrTerminalOccupied is StartInTerminal's refusal of a socket path that
// already exists: a server there would keep its own environment and its own
// windows, so the worker is never started into one.
var ErrTerminalOccupied = errors.New("agents: the terminal socket already exists")

// TerminalWorker is a Worker hosted in a terminal: everything Worker states,
// on the terms the terminal mechanism states for Send, Capture, Done and
// Close, and the one thing a pipe worker cannot offer, a way for a person to
// attach. It is a handle over a process and nothing else, as Worker is.
type TerminalWorker interface {
	Worker

	// Attach is the command line a person runs to attach to the worker's
	// terminal, the tmux binary first. What they type there reaches the
	// worker as a Send does, which the mechanism's documentation states.
	Attach() []string

	// Host is the identity of the tmux server hosting the worker: the pane
	// process's parent, read at start and checked against the process the
	// server reports itself as. Every window opened on the server, by the
	// worker through the socket its environment names or by a person, is
	// the server's child, so a caller placing a process by its ancestry
	// places the server's descendants as this worker's; the mechanism's
	// documentation says why.
	Host() Identity
}

// OpenTerminalWorker starts a terminal worker on r, hosted on t. It is the
// only route to one this package offers a caller holding a Runner, and it
// reads the declaration first, on the terms OpenWorker states for a worker:
// an adapter that has not declared CapabilityTerminalWorkers is refused with
// a *CapabilityError naming it, one that declared it and carries no
// mechanism with an *AdapterError, and a spec or Terminal that cannot be
// started with an error wrapping ErrInvalidInvocation.
func OpenTerminalWorker(ctx context.Context, r Runner, t Terminal, spec WorkerSpec) (TerminalWorker, error) {
	if !r.Capabilities().Has(CapabilityTerminalWorkers) {
		return nil, &CapabilityError{
			Agent:      r.Name(),
			Capability: CapabilityTerminalWorkers,
			Path:       "the terminal worker",
		}
	}
	terminals, ok := r.(TerminalWorkerRunner)
	if !ok {
		return nil, declaredWithoutMechanism(r.Name(), CapabilityTerminalWorkers)
	}
	return terminals.StartInTerminal(ctx, t, spec)
}

// OpenTerminalSubordinate is OpenSubordinate for a terminal-hosted
// subordinate coordinator, on OpenTerminalWorker's terms: the same
// mechanism, under GrantSubordinate, which only it and OpenSubordinate
// select. An adapter whose terminal interface was not observed under that
// grant refuses it, as the Claude Code adapter does with
// ErrTerminalGrantUnobserved.
func OpenTerminalSubordinate(ctx context.Context, r Runner, t Terminal, spec WorkerSpec) (TerminalWorker, error) {
	spec.subordinate = true
	return OpenTerminalWorker(ctx, r, t, spec)
}

// terminalPoll is how often a terminal worker's identity is read to learn
// that its process has gone.
const terminalPoll = 100 * time.Millisecond

// terminalCommandTimeout bounds the two waits on a terminal this package
// makes that are not a tmux client: a new pane's process coming to lead its
// own group, and Close's wait for the poll once the server was ended.
const terminalCommandTimeout = 10 * time.Second

// The bound of each kind of tmux client, on the terms the file comment's
// section on the clients states. Each kind took between ten and forty
// milliseconds against a live tmux 3.7b server when these were chosen, a
// ten kilobyte send included, so each bound is several hundred times what
// a healthy server takes, and a client still running at it is taken as
// wedged rather than slow.
const (
	// tmuxStartBound bounds the client that starts a worker's server, which
	// answers once the server is listening and the pane's process has
	// started. It is ten seconds, as the other kinds are: starting the
	// server was the slowest kind observed, at forty milliseconds.
	tmuxStartBound = 10 * time.Second
	// tmuxTypeBound bounds one send: loading the framed text into a buffer,
	// pasting it, and, for a bracketed paste, typing Enter. It is ten
	// seconds because the text is bounded by the framing and a ten kilobyte
	// send took twenty milliseconds; a screen read before typing is a
	// reading, under tmuxReadBound, and not part of it.
	tmuxTypeBound = 10 * time.Second
	// tmuxReadBound bounds one reading of the pane, its visible screen, its
	// whole history, or its history's size and limit. It is ten seconds
	// because a reading of the whole history took twenty milliseconds, and
	// a Capture makes two readings, each under a bound of its own.
	tmuxReadBound = 10 * time.Second
	// tmuxEndBound bounds the client that ends a worker's server. It is ten
	// seconds because ending a server took twenty milliseconds, and Close
	// and a failed start each wait for it before they return.
	tmuxEndBound = 10 * time.Second
)

// tmuxClientGrace is the grace procgroup.Run is given for a tmux client: how
// long a client sent SIGKILL gets to exit, and how long its output pipes are
// waited on once it has, before the call answers without them. A client
// holds nothing that outlasts a SIGKILL, so the grace is short; a call
// returns within its bound and three graces at most, as procgroup.Run
// states.
const tmuxClientGrace = time.Second

// ErrTerminalClientTimedOut is the answer of a tmux client that was still
// running at the bound of its kind, whose process group was then ended. It
// is never a caller's own deadline: a call whose ctx ended answers an error
// wrapping ctx's error instead, which runTmux decides in one place.
var ErrTerminalClientTimedOut = errors.New("agents: a tmux client ran past its bound and its process group was ended")

// runTmux runs one tmux client, cmd, built with exec.Command and not yet
// started, through procgroup.Run, under ctx and bound, whichever ends first.
// A client that failed is answered with an error wrapping ctx's error when
// ctx has ended, since the caller's cancellation is then the explanation,
// with an error wrapping ErrTerminalClientTimedOut when bound passed and ctx
// had not ended, and otherwise with procgroup.Run's answer; the first two
// keep that answer beside them. A client that answered in full is its answer
// even when ctx or bound ended as it did. what names the client in the
// error.
func runTmux(ctx context.Context, bound time.Duration, what string, cmd *exec.Cmd) error {
	bounded, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	err := procgroup.Run(bounded, cmd, tmuxClientGrace)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("%s ended with its caller's context: %w: %w", what, ctx.Err(), err)
	case bounded.Err() != nil:
		return fmt.Errorf("%w: %s ran past %s: %w", ErrTerminalClientTimedOut, what, bound, err)
	}
	return err
}

// tmuxOutput is runTmux for a client whose standard output is its answer.
func tmuxOutput(ctx context.Context, bound time.Duration, what string, cmd *exec.Cmd) ([]byte, error) {
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := runTmux(ctx, bound, what, cmd); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// terminalTarget is the session, and so the pane, a worker's server holds.
// The server is the worker's own, so one fixed name addresses it.
const terminalTarget = "worker"

// terminalBuffer is the tmux buffer a send is loaded into before it is
// pasted, deleted by the paste.
const terminalBuffer = "assistant-send"

// ErrTerminalNotReady is the refusal of a terminal worker whose screen did
// not read as ready for what was asked: at start, an interface that was not
// drawn within the bound, and at a send, one that did not read ready to be
// typed into before the send's ctx ended; at either, a screen whose reading
// ran past tmuxReadBound. Nothing is typed into a screen that has not read
// ready, so a send refused with it typed nothing. It is wrapped together
// with ctx's error where ctx is what ended the wait, and with
// ErrTerminalClientTimedOut where a reading did.
var ErrTerminalNotReady = errors.New("agents: the terminal worker's screen did not read ready")

// screenState is what an adapter's reader makes of a terminal worker's
// screen.
type screenState int

const (
	// screenNotDrawn is a screen that does not show the agent's interface:
	// before it is drawn, or a screen of another kind, a first-run screen
	// among them.
	screenNotDrawn screenState = iota
	// screenDrawn is the interface drawn and not ready to be typed into: a
	// turn is running, or its input is not empty.
	screenDrawn
	// screenReady is the interface drawn with no turn running and nothing
	// in its input, which is the one state a send types into.
	screenReady
)

// terminalTyping is how an adapter types into a terminal worker: the bytes
// one send is typed as, how they are pasted, and how its screen is read. It
// is the adapter's and never the provider's, so the scripted adapter's line
// framing and the Claude Code adapter's paste are each one value.
type terminalTyping struct {
	// frame turns one send into the bytes pasted for it, or refuses it with
	// an error wrapping ErrInvalidInvocation before anything is typed.
	frame func(text string) ([]byte, error)
	// bracketed pastes the bytes as one bracketed paste and then types Enter
	// as a key of its own, so a line break inside the bytes stays part of
	// the one input. Unset, every newline is pasted as the Enter key, which
	// is what a program reading lines needs.
	bracketed bool
	// read is the adapter's reading of the pane's visible screen. Set, the
	// start returns only once it reads other than screenNotDrawn, and a send
	// types only once it reads screenReady. Nil, nothing is read and every
	// send is typed at once.
	read func(screen string) screenState
}

// tmuxWorker is the TerminalWorker over one tmux server this package
// started.
type tmuxWorker struct {
	terminal Terminal
	identity Identity
	host     Identity
	grace    time.Duration
	// hurry is the hurry channel the worker was started under, on
	// WithHurry's terms; nil never hurries.
	hurry   <-chan struct{}
	maxOut  int64
	typing  terminalTyping
	cleanup func()

	done      chan struct{}
	closeOnce sync.Once
	endOnce   sync.Once

	// sendMu serializes sends, the instructions among them, so text arrives
	// in the order it was sent.
	sendMu sync.Mutex

	captureMu sync.Mutex
	last      Capture
}

// procSpecTerminal is what an adapter hands startTerminalWorker: the
// command, where it runs, and under what.
type procSpecTerminal struct {
	bin    string
	args   []string
	dir    string
	env    []string
	grace  time.Duration
	maxOut int64
	// cleanup is run once the worker has ended, after its server: what the
	// adapter made for this worker alone and nothing after it reads. It is
	// not run when the start fails, which the adapter answers for itself.
	cleanup func()
}

// TerminalCommandLimit is the most bytes the arguments of the tmux command
// that starts a server may total, each counted with one byte more for its
// end. tmux 3.4 refused a new-session command whose arguments totalled
// about 16.3 KB with "command too long" and accepted one a little under, so
// the limit is set below that and a command over it is refused here, before
// anything starts, rather than by the server in a message about something
// else. Another tmux may draw the line elsewhere; this is where the one it
// was observed on did.
const TerminalCommandLimit = 16000

// startTerminalWorker starts spec as a terminal worker on t and, when
// instructions is not empty, types them, on the terms the mechanism's
// documentation states. An adapter that puts its instructions on the command
// line instead passes none.
func startTerminalWorker(ctx context.Context, t Terminal, spec procSpecTerminal, instructions string,
	typing terminalTyping) (*tmuxWorker, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	if len(spec.args) == 0 {
		return nil, &invocationFieldError{
			field:  "command",
			reason: "a terminal worker's command line needs an argument after the binary, or tmux would run it through a shell",
		}
	}
	// A send that cannot be framed is refused before any process exists, as
	// startWorker does.
	if instructions != "" {
		if _, err := typing.frame(instructions); err != nil {
			return nil, fmt.Errorf("framing the worker's instructions: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(t.Socket), 0o700); err != nil {
		return nil, fmt.Errorf("creating the terminal socket's directory: %w", err)
	}
	if _, err := os.Lstat(t.Socket); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrTerminalOccupied, t.Socket)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("looking for an existing terminal socket: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	args, err := tmuxNewSessionArgs(t, spec.dir, spec.bin, spec.args)
	if err != nil {
		return nil, err
	}
	if size := commandSize(args); size > TerminalCommandLimit {
		return nil, &invocationFieldError{
			field: "command",
			reason: fmt.Sprintf("the command that starts the terminal is %d bytes, over the %d tmux is given, so the instructions it carries are too long for a terminal worker",
				size, TerminalCommandLimit),
		}
	}
	cmd := exec.Command(t.Tmux, args...)
	cmd.Dir = spec.dir
	cmd.Env = withoutTmux(spec.env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := runTmux(ctx, tmuxStartBound, "the client starting the server", cmd); err != nil {
		killTerminalServer(t)
		return nil, fmt.Errorf("starting the terminal worker's server: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// The server answers the pane's process and its own in one line, so the
	// two numbers describe one creation.
	pid, server, err := parseTerminalAnswer(stdout.String())
	if err != nil {
		killTerminalServer(t)
		return nil, err
	}
	identity, found, err := Identify(pid)
	switch {
	case err != nil:
		killTerminalServer(t)
		return nil, fmt.Errorf("reading the identity of terminal worker process %d: %w", pid, err)
	case !found:
		killTerminalServer(t)
		return nil, fmt.Errorf("terminal worker process %d was not running when looked for", pid)
	}
	lineage, err := awaitGroupLeader(ctx, identity)
	if err != nil {
		killTerminalServer(t)
		return nil, err
	}
	// The host is the parent the kernel attributes the pane to, and it must
	// be the server that answered, or the handle would name as host a
	// process whose descendants are not this server's windows.
	if lineage.Parent != server {
		killTerminalServer(t)
		return nil, fmt.Errorf("terminal worker process %d has parent %d, and the terminal server answering on %s is process %d",
			pid, lineage.Parent, t.Socket, server)
	}
	host, found, err := Identify(server)
	switch {
	case err != nil:
		killTerminalServer(t)
		return nil, fmt.Errorf("reading the identity of terminal server process %d: %w", server, err)
	case !found:
		killTerminalServer(t)
		return nil, fmt.Errorf("terminal server process %d was not running when looked for", server)
	}

	w := &tmuxWorker{
		terminal: t,
		identity: identity,
		host:     host,
		grace:    spec.grace,
		hurry:    hurryOf(ctx),
		maxOut:   spec.maxOut,
		typing:   typing,
		done:     make(chan struct{}),
	}
	go w.poll()
	go w.closeOnHurry()
	if typing.read != nil {
		bound, cancel := context.WithTimeout(ctx, terminalReadyTimeout)
		err := w.await(bound, func(s screenState) bool { return s != screenNotDrawn })
		cancel()
		if err != nil {
			w.Close()
			return nil, fmt.Errorf("waiting for the terminal worker's interface: %w", err)
		}
	}
	if instructions != "" {
		if err := w.Send(ctx, instructions); err != nil {
			w.Close()
			return nil, fmt.Errorf("typing the worker's instructions: %w", err)
		}
	}
	// The cleanup is set once the start has succeeded, so a start that
	// fails leaves it to the adapter, which made what it removes.
	w.cleanup = spec.cleanup
	return w, nil
}

// terminalReadyTimeout bounds how long a start waits for an adapter's
// interface to be drawn, whatever ctx allows, so a worker that opens on a
// screen nothing here answers is refused rather than waited on for ever.
// The Claude Code CLI took about three seconds to draw its interface
// against a local server when it was observed.
const terminalReadyTimeout = 60 * time.Second

// commandSize is the bytes args total, each with one more for its end.
func commandSize(args []string) int {
	n := 0
	for _, a := range args {
		n += len(a) + 1
	}
	return n
}

// tmuxNewSessionArgs is the argument vector startTerminalWorker passes to tmux
// when starting a worker in dir with bin and workerArgs.
func tmuxNewSessionArgs(t Terminal, dir, bin string, workerArgs []string) ([]string, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return append([]string{"-S", t.Socket, "-f", os.DevNull,
		"new-session", "-d", "-s", terminalTarget, "-x", "200", "-y", "50", "-c", dir,
		"-P", "-F", "#{pane_pid} #{pid}", "--", bin}, workerArgs...), nil
}

// TmuxStartCommandSize is commandSize of the tmux arguments tmuxNewSessionArgs
// builds for the same dir, bin, and workerArgs.
func TmuxStartCommandSize(t Terminal, dir, bin string, workerArgs []string) (int, error) {
	args, err := tmuxNewSessionArgs(t, dir, bin, workerArgs)
	if err != nil {
		return 0, err
	}
	return commandSize(args), nil
}

// await reads the visible screen through the adapter's reader, every
// terminalPoll, until ok accepts what it reads. It returns ErrWorkerEnded
// once the worker has ended, an error wrapping ErrTerminalNotReady and
// ctx's error when ctx ends first, and one wrapping ErrTerminalNotReady and
// ErrTerminalClientTimedOut when a reading ran past tmuxReadBound: a server
// that does not answer one reading within that bound is taken as wedged, so
// the wait ends there rather than starting another reading of it. A screen
// that cannot be read for any other reason is read again rather than taken
// for any state.
func (w *tmuxWorker) await(ctx context.Context, ok func(screenState) bool) error {
	tick := time.NewTicker(terminalPoll)
	defer tick.Stop()
	for {
		screen, err := w.screen(ctx)
		switch {
		case err == nil && ok(w.typing.read(screen)):
			return nil
		case errors.Is(err, ErrTerminalClientTimedOut):
			return fmt.Errorf("%w: %w", ErrTerminalNotReady, err)
		case ctx.Err() != nil:
			// ctx is read once the reading has returned and before any
			// select, so a tick already waiting can never start another
			// reading under the ended ctx. A reading ctx cut short is
			// runTmux's answer, which already wraps ctx's error and keeps
			// the client's failure beside it, so it is returned whole.
			if err != nil && errors.Is(err, ctx.Err()) {
				return fmt.Errorf("%w: %w", ErrTerminalNotReady, err)
			}
			return fmt.Errorf("%w: %w", ErrTerminalNotReady, ctx.Err())
		}
		select {
		case <-w.done:
			return ErrWorkerEnded
		case <-ctx.Done():
			// No reading was cut here: ctx ended while the wait was idle
			// between readings.
			return fmt.Errorf("%w: %w", ErrTerminalNotReady, ctx.Err())
		case <-tick.C:
		}
	}
}

// screen is the pane's visible screen, with lines tmux wrapped joined back.
func (w *tmuxWorker) screen(ctx context.Context) (string, error) {
	out, err := tmuxOutput(ctx, tmuxReadBound, "the client reading the screen", exec.Command(w.terminal.Tmux, "-S", w.terminal.Socket,
		"capture-pane", "-p", "-J", "-t", terminalTarget))
	return string(out), err
}

// parseTerminalAnswer reads the line the server prints for a new session,
// the pane's process identifier and the server's own, and refuses anything
// else: a line that is not two identifiers is not a creation this package
// can hold a handle over.
func parseTerminalAnswer(answer string) (pane, server int, err error) {
	fields := strings.Fields(answer)
	if len(fields) == 2 {
		pane, perr := strconv.Atoi(fields[0])
		server, serr := strconv.Atoi(fields[1])
		if perr == nil && serr == nil && pane > 0 && server > 0 {
			return pane, server, nil
		}
	}
	return 0, 0, fmt.Errorf("the terminal server answered %q for the worker's process and its own, not two process identifiers",
		strings.TrimSpace(answer))
}

// awaitGroupLeader returns the process's lineage once the process id names
// leads its own process group, which is what Group reports and what EndGroup signals. tmux answers
// with the pane's process identifier once that process exists, and the
// answer has been observed to arrive while the process was still in the
// server's group (#273), so a handle returned then would name a group that
// did not exist yet: a Close in that window would signal nothing and read
// the group as gone. The wait is on the process's own lineage, read until
// it leads its group, and it ends in an error when the process is gone or
// replaced, when the lineage cannot be read, which is every platform but
// Linux and macOS, when ctx ends, or when terminalCommandTimeout passes
// without it leading one, since an agent that put itself in another group
// is not a worker Group could answer for.
func awaitGroupLeader(ctx context.Context, id Identity) (Lineage, error) {
	bound := time.NewTimer(terminalCommandTimeout)
	defer bound.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		lineage, found, err := ReadLineage(id.PID)
		switch {
		case err != nil:
			return Lineage{}, fmt.Errorf("reading the process group of terminal worker process %d: %w", id.PID, err)
		case !found || lineage.Identity != id:
			return Lineage{}, fmt.Errorf("terminal worker process %d ended before it led its own process group", id.PID)
		case lineage.Group == id.PID:
			return lineage, nil
		}
		select {
		case <-ctx.Done():
			return Lineage{}, ctx.Err()
		case <-bound.C:
			return Lineage{}, fmt.Errorf("terminal worker process %d was still in process group %d after %v, not leading its own",
				id.PID, lineage.Group, terminalCommandTimeout)
		case <-tick.C:
		}
	}
}

// withoutTmux is env without the two variables that tell a tmux client it is
// running inside a tmux pane, so the client this package runs starts a
// server of its own wherever the service itself runs.
func withoutTmux(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "TMUX" || name == "TMUX_PANE" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// poll ends the worker once the identity reads the process gone, or once
// end has run for another reason.
func (w *tmuxWorker) poll() {
	tick := time.NewTicker(terminalPoll)
	defer tick.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-tick.C:
		}
		presence, err := w.identity.Presence()
		if err != nil || presence == PresencePresent {
			continue
		}
		w.end()
		return
	}
}

// end records that the process has gone: the last screen is kept, the
// server ended, the socket removed, and done closed. It runs once.
func (w *tmuxWorker) end() {
	w.endOnce.Do(func() {
		killTerminalServer(w.terminal)
		if w.cleanup != nil {
			w.cleanup()
		}
		close(w.done)
	})
}

// killTerminalServer ends the server on t and removes its socket, under
// tmuxEndBound. A server already gone, or a client that ran past its bound,
// is not an error worth reporting: the socket is removed either way.
func killTerminalServer(t Terminal) {
	_ = runTmux(context.Background(), tmuxEndBound, "the client ending the server", exec.Command(t.Tmux, "-S", t.Socket, "kill-server"))
	_ = os.Remove(t.Socket)
}

// Send types text into the pane on the terms TerminalWorkerRunner's
// mechanism states.
func (w *tmuxWorker) Send(ctx context.Context, text string) error {
	framed, err := w.typing.frame(text)
	if err != nil {
		return err
	}
	w.sendMu.Lock()
	defer w.sendMu.Unlock()
	select {
	case <-w.done:
		return ErrWorkerEnded
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.typing.read != nil {
		if err := w.await(ctx, func(s screenState) bool { return s == screenReady }); err != nil {
			return err
		}
	}
	args := []string{"-S", w.terminal.Socket,
		"load-buffer", "-b", terminalBuffer, "-", ";",
		"paste-buffer", "-d", "-b", terminalBuffer, "-t", terminalTarget}
	if w.typing.bracketed {
		args = []string{"-S", w.terminal.Socket,
			"load-buffer", "-b", terminalBuffer, "-", ";",
			"paste-buffer", "-p", "-d", "-b", terminalBuffer, "-t", terminalTarget, ";",
			"send-keys", "-t", terminalTarget, "Enter"}
	}
	cmd := exec.Command(w.terminal.Tmux, args...)
	cmd.Stdin = bytes.NewReader(framed)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := runTmux(ctx, tmuxTypeBound, "the client typing a send", cmd); err != nil {
		// runTmux's answer is returned whole rather than replaced, so the
		// client's own failure stays beside the caller's cancellation; a ctx
		// that ended after the client failed for its own reason is put in
		// front of that failure the same way.
		if ctxErr := ctx.Err(); ctxErr != nil {
			if !errors.Is(err, ctxErr) {
				return fmt.Errorf("typing into the terminal: %w: %w", ctxErr, err)
			}
			return err
		}
		if errors.Is(err, ErrTerminalClientTimedOut) {
			return err
		}
		if presence, perr := w.identity.Presence(); perr == nil && presence != PresencePresent {
			return ErrWorkerEnded
		}
		return fmt.Errorf("typing into the terminal: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Capture reads the pane on the terms the mechanism states.
func (w *tmuxWorker) Capture() Capture {
	w.captureMu.Lock()
	defer w.captureMu.Unlock()
	select {
	case <-w.done:
		return w.last
	default:
	}
	ctx := context.Background()
	screen, err := tmuxOutput(ctx, tmuxReadBound, "the client reading the history", exec.Command(w.terminal.Tmux, "-S", w.terminal.Socket,
		"capture-pane", "-p", "-J", "-S", "-", "-E", "-", "-t", terminalTarget))
	if err != nil {
		return w.last
	}
	history, err := tmuxOutput(ctx, tmuxReadBound, "the client reading the history's size", exec.Command(w.terminal.Tmux, "-S", w.terminal.Socket,
		"display-message", "-p", "-t", terminalTarget, "#{history_size} #{history_limit}"))
	uncounted := false
	if err == nil {
		size, limit, _ := strings.Cut(strings.TrimSpace(string(history)), " ")
		s, serr := strconv.Atoi(size)
		l, lerr := strconv.Atoi(limit)
		uncounted = serr != nil || lerr != nil || s >= l
	} else {
		uncounted = true
	}
	text := strings.TrimRight(string(screen), " \n")
	if text != "" {
		text += "\n"
	}
	var dropped int64
	if over := int64(len(text)) - w.maxOut; w.maxOut > 0 && over > 0 {
		dropped = over
		text = text[over:]
	}
	w.last = Capture{Text: text, Dropped: dropped, Uncounted: uncounted, Format: OutputText}
	return w.last
}

// Group is the pane process's identifier, which leads its own group.
func (w *tmuxWorker) Group() int { return w.identity.PID }

// Host is the server's identity, on TerminalWorker's terms.
func (w *tmuxWorker) Host() Identity { return w.host }

// Done is closed once the poll has read the process gone and the server has
// been ended, or once Close stopped waiting for that read, as Close states.
func (w *tmuxWorker) Done() <-chan struct{} { return w.done }

// Attach is the command a person runs to attach to the worker's terminal.
func (w *tmuxWorker) Attach() []string {
	return []string{w.terminal.Tmux, "-S", w.terminal.Socket, "attach-session", "-t", terminalTarget}
}

// closeOnHurry is procWorker.closeOnHurry for a terminal-hosted worker: the
// worker is closed the moment its hurry is closed. The group it ends is
// named by the identity read when the worker started and verified again by
// endGroup before anything is signalled.
func (w *tmuxWorker) closeOnHurry() {
	if w.hurry == nil {
		return
	}
	select {
	case <-w.hurry:
		w.Close()
	case <-w.done:
	}
}

// Close ends the worker on the terms the mechanism states. It is
// idempotent and returns once done is closed: when the poll reads the
// process gone, or, if it has not within the grace and the command timeout
// after the group and the server were ended, then, with done closed though
// nothing read the process gone. That fallback exists so a process whose
// identity cannot be read cannot hold a caller's Close forever; it is the
// one way done closes without the read, and the gaps above count it.
func (w *tmuxWorker) Close() {
	w.closeOnce.Do(func() {
		select {
		case <-w.done:
			return
		default:
		}
		// The screen is read before the end, so a Capture after Close
		// answers what the worker last showed rather than nothing.
		w.Capture()
		_, _ = endGroup(w.identity, w.grace, w.hurry)
		killTerminalServer(w.terminal)
		timer := time.NewTimer(w.grace + terminalCommandTimeout)
		defer timer.Stop()
		select {
		case <-w.done:
		case <-timer.C:
			w.end()
		}
	})
	<-w.done
}
