// Package standin is the scripted agent every test that needs a coding agent
// runs against. A test writes a Script saying what the agent prints, what it
// exits with, and whether it lingers; New builds a real agents.Runner over it;
// and Calls reports what the stand-in processes recorded of what they were
// asked, on the terms that method states.
//
// # The rule this package exists to obey
//
// A stand-in may not produce a shape the real adapter cannot. This repository
// has paid for the alternative once: a fake in internal/safety stated typed
// values directly, including one the real read could never return, and a guard
// against that value looked alive while being dead. Deleting the guard failed
// the test, so mutation testing said nothing about it.
//
// So no type here implements agents.Runner, agents.Fixer, or agents.Worker,
// and nothing here constructs an agents.Result, an *agents.InvocationError, an
// agents.Record, or an agents.Capture.
// Agent.Runner hands back the adapter's own Runner, which is the one of those
// this package passes along rather than makes. It writes bytes on standard
// output and standard error, exits with a status, and lets
// agents.ClaudeFactory read all of that. Every Result, every InvocationError,
// and every Record a test sees was built by the production adapter out of a
// process's output, on the path a real Claude Code installation is read on.
// Even declared usage counts are not handed over: they are marshalled into the
// envelope and read back by the adapter's own reader.
//
// Two residual gaps come with that, and neither is closed here.
//
// This bounds what a test can hold to what the adapter can build. It does not
// bound it to what a real agent would print. An envelope reporting the agent's
// own failure while carrying a valid report is writable here, and nothing in
// this repository says an agent writes one. What the bound buys is that no
// test holds a Result the product's own path could not have built; it is not
// evidence that anything produces the bytes behind it.
//
// The envelope this package writes and the envelope internal/agents reads are
// two spellings of one wire contract, and Go checks neither against the other.
// A test stands in for that check: TestAdapterReadsBackEveryStatedField
// declares a result, a session, a model, all five counts, and the subtype an
// agent-reported failure carrying no result says about itself, and asserts the
// adapter reports each one back, so one of those keys renamed on one side and
// not the other fails there rather than quietly reading as unreported. The one
// key that is outside this is "type", which the adapter's reader does not name
// at all; it is written for realism and nothing can fail on it.
//
// # Wiring
//
// The stand-in is the test binary re-executed, which is what keeps it free of
// a build step and identical on every platform this module targets. That costs
// one line in the consuming package:
//
//	func TestMain(m *testing.M) {
//		standin.Main()
//		os.Exit(m.Run())
//	}
//
// Main returns immediately unless this process was started as the stand-in, so
// it is safe to call unconditionally and nothing else in TestMain has to know
// about it.
//
// A package that forgets it would otherwise read the resulting agent failures
// as the code under test misbehaving. The first New in a process refuses
// instead: it puts one handshake invocation through the adapter, and a binary
// that does not answer it is a fatal error naming the missing TestMain.
//
// AtTerminal is the second TestMain call, for a package whose tests push to
// a gate from the test's own process. The gate places such a push by a
// terminal's foreground on the pushing chain, which a CI runner has none of,
// so AtTerminal runs the binary again on a pseudo-terminal of its own when
// it reads no foreground; it is called after every verb that makes this
// binary another process, and its doc comment states what it establishes
// and what it does not, including how the binary run again ends with the
// outer one.
//
// A process a test starts must end with the test binary that started it
// (P15), and owner_unix.go is the one owner watch that ties the two: the
// binary AtTerminal runs again watches its outer, and a package whose tests
// start lasting processes of its binary calls WatchOwner first in TestMain
// and runs its tests through RunOwned after AtTerminal. A process running
// another program cannot watch, so RunOwned also starts an owner sweep that
// ends what carries the owner's identity once the owner is gone; a tmux
// server is the case that needs it, since it leaves the test's group and
// session, so RunOwned puts a tmux first on PATH that puts the identity back
// whatever environment a test starts it with, and Tmux refuses a test that
// does not run inside RunOwned. The check RunOwned runs once the tests have
// returned fails the package naming every process of its binary, and every
// process carrying its identity, still running. Before it, RunOwned has the
// binary start no further process, and the check lists again until a
// listing comes back empty, so what an exiting process starts is listed
// too; only then, on a pass, does RunOwned end the sweep and remove the
// wrapper's directory itself, so neither outlives a binary whose check
// passed, and the sweep stays for a binary that failed its check or never
// reached it. What neither the check nor the sweep can see is not ended
// either way: a process running another program that is not read as
// carrying the identity, which on macOS includes Apple's /bin/sh, /bin/bash,
// /bin/zsh and /bin/sleep, whose environment the read cannot find. Nor is
// any file a test leaves removed: the wrapper's directory is the only path
// RunOwned removes. RunOwned's doc comment owns those gaps.
//
// That wiring serves a caller who can be handed a Runner. One in another
// process cannot be: internal/agents resolves the configured agent by name off
// PATH and builds the command line itself, so reaching the stand-in from there
// means standing a copy of this binary at that name. Agent.Arguments is the
// seam for that, and its doc comment states what those words carry and what
// they deliberately do not.
//
// Every invocation is a process, so what a script costs is what starting this
// binary costs, times the invocations in it.
//
// # What is recorded, and what P4 can be asked of it
//
// A Call is what reached the agent: the command line the adapter built, the
// prompt on standard input, the working directory the process resolved, and
// which script step answered. The stand-in process writes it, so it is
// evidence off the wire rather than the adapter's account of its own behavior,
// which is the point of asking it about P4 at all.
//
// The command line is where a session is visible. Call.Session is the value of
// --resume, and it is empty for every invocation Runner.Run makes, because Run
// has no session to pass; a fixer's second round carries the reference its
// first round reported.
//
// What the wire does not carry is the purpose. The adapter tells the agent
// what to do, not what role it is playing, so a Call cannot say on its own
// that it was a review. A test names the invocation some other way, by its
// prompt or by the step that answered it, or reads the purpose from an
// agents.Recorder it installed, which is a second and separate account of the
// same invocation.
//
// The environment is deliberately not recorded. It may carry an agent's
// credentials, and a Call is written to a file, which is exactly where those
// should not be. internal/agents keeps them out of a Record by having no field
// they fit in; this keeps them out of a Call the same way.
//
// # The worker modes, and what a worker's Call proves
//
// A stand-in is started as a worker in one of four modes, each chosen by the
// command line the adapter built, which is what the wire carries.
//
// Stream mode is the Claude Code adapter's pipe worker: --print with --input-format
// stream-json. New builds that adapter, and a test reaches its worker
// through agents.OpenWorker exactly as it reaches a fixer through
// agents.OpenFixer. The worker reads one JSON message per line and answers a
// user message's text as Script.Worker says, in stream-json lines; the rules
// it applies to a line are stated on serveStreamWorker. They are modelled on
// two sources and no more. The input shape is what Anthropic's Python Agent
// SDK writes to the CLI for a string prompt. The reactions are what the
// Claude Code CLI did when it was driven this way by hand: it stayed running
// after answering a message and answered a second one, exited 0 when its
// input closed, exited 1 on a line that was not JSON with its input still
// open, and passed over a JSON line that was not a user message. That is an
// observation of one version, recorded in the pull request that added this
// mode, and nothing re-checks it: no test here runs the real CLI, so a later
// CLI that reacts differently is not noticed here. The stand-in also answers
// with three lines per turn where the CLI prints more kinds of line than
// that; a reader of a worker's output that relied on exactly three would be
// relying on the stand-in.
//
// App-server mode is the Codex adapter's pipe worker: the argument
// app-server. NewCodex builds that adapter. The worker reads one JSON
// message per line and answers a turn/start's text as Script.Worker says.
// The frames are the ones Codex CLI 0.163.0 emitted for initialize,
// thread/start, an in-progress turn/start result, item/completed
// agentMessage, and thread/status/changed idle, recorded in
// internal/agents/testdata. A command a Run exchange names is asked as
// item/commandExecution/requestApproval and run only after accept. A
// handshake that is not that recorded order is refused, so an adapter that
// drifts from it fails here. An exchange's Recorded frames replace the
// agent message and the idle, which is how a test plays back the failed
// turns recorded beside that fixture, and with Ends a partial turn is an
// app-server that dies mid-turn. An exchange's Refuse answers the
// turn/start itself with a JSON-RPC error and prints nothing after it; the
// error's code is the protocol's generic internal error, chosen here and
// not recorded from a CLI, because the adapter reads only the message.
//
// The same runner's one-shot invocations are `codex exec --json`, which
// carries no --print: the stand-in tells it apart by exec and --json before
// the "--", reads the prompt as the one word after it, and writes a
// scripted envelope as Codex JSONL events rather than as one result object,
// on the terms serveCodexExec and codexWire state. Call.Session reads the
// thread after its resume subcommand.
//
// Line mode is the scripted adapter's: no --print at all, and no screen
// reader flag. NewWorker builds
// agents.ScriptedFactory, which starts one. The worker reads its standard
// input line by line, instructions first, and answers each line as
// Script.Worker says; a line nothing answers draws a note on standard error
// and nothing on standard output, so silence is the stand-in's and never a
// default. No real agent is known to this repository to answer plain lines
// as a worker; the mode exists so a layer built on workers can script plain
// lines and read them back.
//
// Interactive mode is the Claude Code adapter's terminal worker:
// --ax-screen-reader among the flags before "--", and no --print. New's
// runner starts one through agents.OpenTerminalWorker, in a tmux pane. It
// models the CLI's interactive interface as it was observed in a tmux pane,
// on version 2.1.301 with that flag, against a local server standing in
// for the model (#254), and serveInteractiveWorker and input state its
// rules. In short: it puts its terminal in raw mode and asks for bracketed
// paste; it opens on a first-run screen, and never leaves it, unless the
// .claude.json in its CLAUDE_CONFIG_DIR records onboarding, trust for the
// directory it runs in exactly, and, where ANTHROPIC_API_KEY is set, an
// approval naming that key's last twenty characters; it draws nothing for
// Startup and drops an Enter typed before it draws; it answers the one word
// after "--" as its first message, and a command line without one is
// refused; it keeps a bracketed paste whole and takes a carriage return
// outside one as Enter; it takes a "!" or a "/" typed into an empty input as
// a shell or a slash command, which it records and runs nothing for; it
// shows a turn as running for Turn, queueing what is submitted meanwhile;
// and its footer and input line are the text the CLI drew, redrawn in place.
// What it does not model is everything else the CLI draws and does: the
// model's reply is the script's, a "!" command is not run, a paste is not
// collapsed or wrapped, the footer is modelled in acceptEdits mode only,
// and no first-run screen answers a key. Like the stream mode's sources,
// this is an observation of one version that nothing here re-checks;
// internal/agents runs the same probe against an installed CLI where there
// is one, and says what the model rests on where there is not. The
// environment is still not recorded: the key is read to check an approval
// and is written nowhere, the configuration directory is a path, which
// Call.ConfigDir carries, and of the two credential variables the CLI reads,
// ANTHROPIC_API_KEY and CLAUDE_CODE_OAUTH_TOKEN, only the names of those set
// are kept, in Call.Credentials. A token draws no first-run screen, which is
// what the installed CLI was observed to do with one (#320).
//
// In any of the three modes an exchange may Run what the line carries as a
// shell command, which is how a test has a worker do something rather than say
// something: push to a gate, or call the service. The command runs where a
// command the Claude Code CLI's Bash tool runs was observed to, in a process
// group the worker does not lead, so whatever reaches the service from it
// arrives from outside the worker's group, as a real worker's push does; a
// rule placing a worker's descendants by the worker's group alone cannot pass
// a test that drives the worker this way. runCommand says what of the CLI it
// does not model, and bashCheck the one refusal it does: a here-document
// carrying braces and quotes, which #267 saw the CLI refuse. An exchange may
// instead Write what the line carries as a file in the worker's directory,
// which is how a test has a worker use the CLI's file editing tool, and a
// path outside that directory is refused, which #267 saw needed an approval
// nobody was there to give.
//
// A worker's Call is the exception to "complete or absent": it is written
// when the worker starts and rewritten whole, under the same number, as each
// line arrives, so a reader sees a record complete up to some line and
// possibly behind the worker. What it proves is what reached the process,
// in order, and that the record was taken off the wire by the process
// itself; what it cannot say is which lines were the instructions and which
// were sent afterwards, because nothing on the wire marks that, and a test
// that needs the distinction scripts lines it can tell apart.
//
// Being a worker is not on the Call either, except as the command line the
// stand-in itself read: Call.Worker and Call.Stream are the process's own
// account of how it decided, not a second observation.
//
// # What this package is not
//
// It is not a second executor and it holds no state of a run. It also puts no
// interface of its own between a caller and agents.Runner: Agent.Runner hands
// back the adapter's own Runner, so whatever that interface declares about
// itself, its capability set among that, reaches callers through this package
// unchanged.
//
// It is not a model of any agent's behavior. It answers what it was scripted
// to answer and nothing else; an invocation no step matches exits with
// ExitUnscripted rather than being given a default nobody wrote. The one
// answer a script states without stating its text is Reviewed, and what it
// derives comes off the invocation's own prompt on the wire, never from
// anything the test process knew; its doc comment says why that is not a
// default.
package standin
