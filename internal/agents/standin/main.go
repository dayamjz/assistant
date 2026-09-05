package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// The stand-in takes everything it needs from its command line, and nothing
// from its environment. The adapter passes a configured entry's own arguments
// ahead of the flags it manages, which is the seam these travel on, and it
// leaves an invocation free to be given no environment at all.
//
// Both flags are spelled distinctly enough that a test binary cannot be handed
// one by accident, which matters because being handed one is what turns a test
// binary into an agent.
const (
	controlFlag   = "--standin-control="
	handshakeFlag = "--standin-handshake="
)

// Statuses the stand-in exits with when it cannot do its job at all. They are
// distinct from ExitUnscripted, which is the stand-in working correctly and
// reporting that the script did not cover the invocation. Each prints what
// went wrong on standard error, where the adapter keeps it as the message on
// the *agents.InvocationError a caller receives.
const (
	// exitNoScript is a script that could not be read.
	exitNoScript = 4
	// exitNoStdin is a prompt that could not be read.
	exitNoStdin = 5
	// exitNoRecord is a call this stand-in could not record, which it refuses
	// to answer rather than answering unrecorded.
	exitNoRecord = 6
	// exitNoOutput is a reply that could not be encoded or printed.
	exitNoOutput = 7
)

// Main runs this process as the stand-in agent when it was started as one, and
// returns otherwise. A package whose tests build an Agent calls it first thing
// in TestMain:
//
//	func TestMain(m *testing.M) {
//		standin.Main()
//		os.Exit(m.Run())
//	}
//
// It never returns when this process is a stand-in, because the process is the
// agent and has no tests to run. It always returns when it is not, so calling
// it unconditionally costs a test binary one scan of its arguments.
//
// New verifies this wiring before it hands back a Runner, so a package that
// forgets the call is told so rather than being left to read the resulting
// agent failures.
func Main() {
	args := os.Args[1:]
	if token, ok := flagValue(args, handshakeFlag); ok {
		os.Exit(handshake(token))
	}
	control, ok := flagValue(args, controlFlag)
	if !ok {
		return
	}
	os.Exit(serve(control, args))
}

// flagValue is the value of the first argument with this prefix, and whether
// there was one.
func flagValue(args []string, prefix string) (string, bool) {
	for _, a := range args {
		if after, ok := strings.CutPrefix(a, prefix); ok {
			return after, true
		}
	}
	return "", false
}

// handshake answers New's wiring check by printing a well-formed envelope
// whose result is the token it was given. It records nothing and consumes no
// step: it runs on a runner of its own, so a script never sees it.
func handshake(token string) int {
	encoded, err := Envelope{Result: token, Usage: DefaultUsage()}.wire(Call{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: encoding the handshake envelope: "+err.Error())
		return exitNoOutput
	}
	if _, err := os.Stdout.Write(encoded); err != nil {
		fmt.Fprintln(os.Stderr, "standin: writing the handshake envelope: "+err.Error())
		return exitNoOutput
	}
	return 0
}

// serve answers one invocation: it reads the script, records what it was
// asked, and does what the step that matched says.
func serve(control string, args []string) int {
	script, err := readScript(control)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoScript
	}
	prompt, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: reading the prompt: "+err.Error())
		return exitNoStdin
	}
	// A working directory that cannot be resolved is recorded as empty rather
	// than refused. It is one field of the evidence, and losing the whole
	// record of an invocation over it would hide more than it reports.
	dir, _ := os.Getwd()
	call := Call{Args: args, Prompt: string(prompt), Dir: dir, Step: -1}

	step, err := choose(control, script, call)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoRecord
	}
	call.Step = step
	seq, err := claimCall(control, call)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoRecord
	}
	call.Seq = seq

	if !call.Answered() {
		fmt.Fprintf(os.Stderr, "standin: no step of the script answers this invocation: %s\n", call)
		return ExitUnscripted
	}
	return emit(script.Steps[step].Reply, call)
}

// readScript reads the script the Agent wrote for this control directory.
func readScript(control string) (Script, error) {
	raw, err := os.ReadFile(scriptPath(control))
	if err != nil {
		return Script{}, fmt.Errorf("reading the script: %w", err)
	}
	var script Script
	if err := json.Unmarshal(raw, &script); err != nil {
		return Script{}, fmt.Errorf("reading the script: %w", err)
	}
	return script, nil
}

// choose returns the index of the step that answers this call, or -1 when none
// does. Steps are tried in order and the first one that matches and still has
// a use free takes it, so a script of plain steps is answered in the order it
// was written and a script of matched steps is answered by shape.
func choose(control string, script Script, call Call) (int, error) {
	for i, step := range script.Steps {
		if !step.Match.answers(call) {
			continue
		}
		limit, bounded := step.uses()
		if !bounded {
			return i, nil
		}
		for use := range limit {
			got, err := claimUse(control, i, use)
			if err != nil {
				return -1, err
			}
			if got {
				return i, nil
			}
		}
	}
	return -1, nil
}

// emit does what a reply says: prints standard error, then standard output in
// the order Reply documents, then holds, then exits.
//
// A write that fails is reported and ends the invocation with a status of its
// own rather than being ignored, because standard output is the whole of what
// this process is for. Padding is written in blocks so a reply that exceeds
// the adapter's output limit costs one buffer rather than its whole size.
func emit(reply Reply, call Call) int {
	if reply.Stderr != "" {
		if _, err := io.WriteString(os.Stderr, reply.Stderr); err != nil {
			// There is nowhere left to say so, since standard error is what
			// just failed. The status is what reports it.
			return exitNoOutput
		}
	}
	if reply.Stdout != "" {
		if _, err := io.WriteString(os.Stdout, reply.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "standin: writing standard output: "+err.Error())
			return exitNoOutput
		}
	}
	if reply.Envelope != nil {
		encoded, err := reply.Envelope.wire(call)
		if err != nil {
			fmt.Fprintln(os.Stderr, "standin: encoding the envelope: "+err.Error())
			return exitNoOutput
		}
		if _, err := os.Stdout.Write(encoded); err != nil {
			fmt.Fprintln(os.Stderr, "standin: writing the envelope: "+err.Error())
			return exitNoOutput
		}
	}
	if reply.Pad > 0 {
		if err := pad(os.Stdout, reply.Pad); err != nil {
			fmt.Fprintln(os.Stderr, "standin: writing the padding: "+err.Error())
			return exitNoOutput
		}
	}
	if reply.Hold > 0 {
		time.Sleep(reply.Hold)
	}
	return reply.Exit
}

// pad writes n bytes of filler.
func pad(w io.Writer, n int64) error {
	const block = 64 << 10
	filler := make([]byte, block)
	for i := range filler {
		filler[i] = 'x'
	}
	for n > 0 {
		size := int64(block)
		if n < size {
			size = n
		}
		if _, err := w.Write(filler[:size]); err != nil {
			return err
		}
		n -= size
	}
	return nil
}
