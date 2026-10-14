package cli_test

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
)

// unwatchedHome is a home with one task in flight, an instance file and a
// fresh beacon, and no process holding its lock: the stop guard blocks there
// unless the host supplies its re-entry signal.
func unwatchedHome(t *testing.T) *home.Home {
	t.Helper()
	h := createdHome(t)
	oneTaskInFlight(t, h)
	writeInstance(t, h)
	writeBeacon(t, h, time.Now())
	return h
}

// turnEnd is the turn-end event in the shape the host documents for a Stop
// hook's standard input, the members the Agent SDK's StopHookInput declares
// and the last message the host's own build adds, with the re-entry member
// set to active. It is encoded here rather than written as a literal, so a
// case that wants another shape says so by building one.
func turnEnd(t *testing.T, active any) string {
	t.Helper()
	event := map[string]any{
		"session_id":             "abc123",
		"transcript_path":        "/tmp/transcript.jsonl",
		"cwd":                    "/tmp/work",
		"hook_event_name":        "Stop",
		"stop_hook_active":       active,
		"last_assistant_message": "Done.",
	}
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// P10's re-entry signal as the host this program installs for supplies it:
// stop_hook_active true in the JSON on the hook's standard input allows the
// one invocation it was given to, exactly as ASSISTANT_HOST_REENTRY does,
// and the variable still works on its own for a host that sets it.
//
// The blocked cases are the positive control. The same home blocks with no
// input, with the member false, with an object that lacks it and with a
// member spelled otherwise, so the allowed case is allowed by the member's
// value and by nothing the setup left lying around; a build that read
// nothing on standard input - the bridge missing - answers the field case
// blocked and fails here.
func TestHostStopReadsTheHostsReentryFieldOnStandardInput(t *testing.T) {
	principles.Cite(t, principles.P10)
	vocabtest.Control(t)
	h := unwatchedHome(t)

	for _, c := range []struct {
		name  string
		stdin io.Reader
	}{
		{"no standard input at all", nil},
		{"empty standard input", strings.NewReader("")},
		{"whitespace only", strings.NewReader(" \n")},
		{"the member false", strings.NewReader(turnEnd(t, false))},
		{"an object without the member", strings.NewReader(`{"hook_event_name":"Stop","session_id":"abc123"}`)},
		{"the member spelled otherwise", strings.NewReader(`{"Stop_Hook_Active":true}`)},
	} {
		got := hostStopWith(t, h, nil, c.stdin, "--json")
		answer := decodeHostStop(t, got)
		if answer.Allowed || answer.ReEntered || got.code == machine.ExitOK {
			t.Fatalf("%s: the answer is %+v and exited %s, want blocked", c.name, answer, got.code)
		}
		if answer.InputUnread != "" {
			t.Fatalf("%s: input that carries no signal was reported unread: %q", c.name, answer.InputUnread)
		}
		if !strings.HasPrefix(got.stderr, machine.PhraseTurnBlocked) {
			t.Fatalf("%s: standard error does not say the turn cannot end:\n%s", c.name, got.stderr)
		}
	}
	blocked := decodeHostStop(t, hostStop(t, h, nil, "--json"))

	got := hostStopWith(t, h, nil, strings.NewReader(turnEnd(t, true)), "--json")
	byField := decodeHostStop(t, got)
	if !byField.Allowed || !byField.ReEntered || got.code != machine.ExitOK {
		t.Fatalf("with the member true the answer is %+v and exited %s, want allowed", byField, got.code)
	}
	if byField.InputUnread != "" || byField.Supervision.Reason != blocked.Supervision.Reason {
		t.Fatalf("the member changed more than the re-entry: %+v against %+v", byField, blocked)
	}
	if !strings.HasPrefix(got.stderr, machine.PhraseTurnReEntered) {
		t.Fatalf("standard error does not say why the turn may end:\n%s", got.stderr)
	}
	vocabtest.Assert(t, got.stderr)
	// The human rendering is answered the same way, from the same input.
	if plain := hostStopWith(t, h, nil, strings.NewReader(turnEnd(t, true))); plain.code != machine.ExitOK || plain.stdout != "" {
		t.Fatalf("without --json the re-entered turn exited %s with %q", plain.code, plain.stdout)
	}

	// The variable is still the signal for a host that sets it, whatever the
	// input says.
	signalled := map[string]string{cli.HostReentryVar: "1"}
	got = hostStopWith(t, h, signalled, strings.NewReader(turnEnd(t, false)), "--json")
	if byEnvironment := decodeHostStop(t, got); !byEnvironment.Allowed || !byEnvironment.ReEntered || got.code != machine.ExitOK {
		t.Fatalf("with the variable set the answer is %+v and exited %s, want allowed", byEnvironment, got.code)
	}

	// Once: the next invocation with neither is judged afresh.
	if again := decodeHostStop(t, hostStop(t, h, nil, "--json")); again.Allowed {
		t.Fatalf("after a re-entered turn the next unsignalled one is %+v, want blocked", again)
	}
}

// Input that is there and cannot be read as the turn-end event is no signal:
// the turn is blocked, and the answer says why the input was not read, so an
// unreadable event never allows and a block it causes is not mistaken for
// the guard's alone. The cases are the ways the member could be taken as true
// by a reading that relaxed one rule: a string or a number for the boolean,
// the member twice with the last one true, a true inside input that does not
// end or does not parse, one past the size bound, a read that fails, and a
// stream nobody closes, which the wait gives up on. With the variable set
// beside it the turn is allowed, by the variable, and the input is still
// reported, which shows the report is about the input and not the verdict.
func TestHostStopReadsNoSignalFromInputItCannotRead(t *testing.T) {
	principles.Cite(t, principles.P10)
	vocabtest.Control(t)
	h := unwatchedHome(t)
	oversize := `{"stop_hook_active":true,"last_assistant_message":"` + strings.Repeat("x", 4<<20) + `"}`

	cases := []struct {
		name  string
		stdin func(t *testing.T) io.Reader
	}{
		{"not JSON", text("stop_hook_active: true")},
		{"an array", text(`[{"stop_hook_active":true}]`)},
		{"the member as a string", text(`{"stop_hook_active":"true"}`)},
		{"the member as a number", text(`{"stop_hook_active":1}`)},
		{"the member null", text(`{"stop_hook_active":null}`)},
		{"the member twice, the last true", text(`{"stop_hook_active":false,"stop_hook_active":true}`)},
		{"the member twice, both true", text(`{"stop_hook_active":true,"stop_hook_active":true}`)},
		{"an object that does not end", text(`{"stop_hook_active":true`)},
		{"a second object after the first", text(`{"stop_hook_active":true}{"stop_hook_active":true}`)},
		{"more than the size bound", text(oversize)},
		{"a read that fails", func(*testing.T) io.Reader { return iotest.ErrReader(io.ErrClosedPipe) }},
		{"a stream nobody closes", func(t *testing.T) io.Reader {
			r, w := io.Pipe()
			go func() { _, _ = w.Write([]byte(`{"stop_hook_active":true}`)) }()
			t.Cleanup(func() { _ = w.Close() })
			return r
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := hostStopWith(t, h, nil, c.stdin(t), "--json")
			answer := decodeHostStop(t, got)
			if answer.Allowed || answer.ReEntered || got.code == machine.ExitOK {
				t.Fatalf("the answer is %+v and exited %s, want blocked", answer, got.code)
			}
			if answer.InputUnread == "" {
				t.Fatalf("unreadable input was not reported: %+v", answer)
			}
			if !strings.HasPrefix(got.stderr, machine.PhraseTurnBlocked) {
				t.Fatalf("standard error does not say the turn cannot end:\n%s", got.stderr)
			}

			signalled := map[string]string{cli.HostReentryVar: "1"}
			got = hostStopWith(t, h, signalled, c.stdin(t), "--json")
			answer = decodeHostStop(t, got)
			if !answer.Allowed || !answer.ReEntered || got.code != machine.ExitOK || answer.InputUnread == "" {
				t.Fatalf("with the variable set the answer is %+v and exited %s, want allowed with the input reported", answer, got.code)
			}
		})
	}
}

// text is a case's standard input that holds s.
func text(s string) func(*testing.T) io.Reader {
	return func(*testing.T) io.Reader { return strings.NewReader(s) }
}
