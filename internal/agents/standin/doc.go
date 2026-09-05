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
// So no type here implements agents.Runner or agents.Fixer, and nothing here
// constructs an agents.Result, an *agents.InvocationError, or an agents.Record.
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
// # What this package is not
//
// It is not a second executor and it holds no state of a run. It also puts no
// interface of its own between a caller and agents.Runner: Agent.Runner hands
// back the adapter's own Runner, so whatever that interface later declares
// about itself, a capability set among it, reaches callers through this
// package unchanged.
//
// It is not a model of any agent's behavior. It answers what it was scripted
// to answer and nothing else; an invocation no step matches exits with
// ExitUnscripted rather than being given a default nobody wrote.
package standin
