package standin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// answer is the line a worker prints for a line this exchange took: Reply,
// the Recorded lines one after another, or for a Run exchange the Ran of the
// command the line carries.
func (e Exchange) answer(line string) (string, error) {
	if e.Refuse != "" {
		return "", errors.New("a Refuse exchange is answered only by a JSON-RPC worker, which has a request to refuse")
	}
	if len(e.Recorded) > 0 {
		return strings.Join(e.Recorded, "\n"), nil
	}
	if e.Write {
		_, rest, _ := strings.Cut(line, e.Contains)
		encoded, err := json.Marshal(writeFile(rest))
		if err != nil {
			return "", fmt.Errorf("encoding what a write did: %w", err)
		}
		return string(encoded), nil
	}
	if !e.Run {
		return e.Reply, nil
	}
	_, command, _ := strings.Cut(line, e.Contains)
	encoded, err := json.Marshal(runCommand(strings.TrimSpace(command)))
	if err != nil {
		return "", fmt.Errorf("encoding what a command did: %w", err)
	}
	return string(encoded), nil
}

// refusal is the Refuse text of the first exchange that answers line, and
// whether that exchange is a Refuse one: a JSON-RPC worker answers the
// request with an error then, and otherwise goes on to answer as it does.
func (w *WorkerScript) refusal(line string) (string, bool) {
	for _, exchange := range w.Exchanges {
		if exchange.answers(line) {
			return exchange.Refuse, exchange.Refuse != ""
		}
	}
	return "", false
}

// valid refuses an exchange whose Run cannot be answered as Run says: one
// with no Contains to find the command after, and one with a Reply the Ran
// would replace; and one whose Recorded lines cannot be played back as
// Recorded says: beside a Reply, a Run or a Write they would replace, or
// carrying a line with a newline in it; and a Refuse exchange beside
// anything its refusal would replace.
func (e Exchange) valid() error {
	if e.Refuse != "" && (e.Reply != "" || e.Run || e.Write || len(e.Recorded) > 0 || e.Ends) {
		return errors.New("a Refuse exchange answers with a JSON-RPC error in place of a Reply, a Run, a Write, Recorded lines or an end, so it may have none of them")
	}
	if len(e.Recorded) > 0 {
		if e.Reply != "" || e.Run || e.Write {
			return errors.New("an exchange playing Recorded lines prints them in place of a Reply, a Run or a Write, so it may have none of them")
		}
		for i, line := range e.Recorded {
			if strings.Contains(line, "\n") {
				return fmt.Errorf("recorded line %d carries a newline, so it would print as two", i)
			}
		}
	}
	if e.Write {
		switch {
		case e.Run:
			return errors.New("an exchange either Runs a command or Writes a file, not both")
		case e.Contains == "":
			return errors.New("a Write exchange needs a Contains, which is what its path follows")
		case e.Reply != "":
			return fmt.Errorf("a Write exchange answers with its Wrote, so its Reply %q would never be printed", e.Reply)
		}
		return nil
	}
	if !e.Run {
		return nil
	}
	if e.Contains == "" {
		return errors.New("a Run exchange needs a Contains, which is what its command follows")
	}
	if e.Reply != "" {
		return fmt.Errorf("a Run exchange answers with its Ran, so its Reply %q would never be printed", e.Reply)
	}
	return nil
}

// runCommand runs command through /bin/sh -c in this process's working
// directory, in a process group of its own, and waits for it, bounded by
// HangFor.
//
// The group is the part modelled on the Claude Code CLI, and it is modelled
// on one observation and no more: #225 saw a hook that the CLI's Bash tool
// ran in a process group other than the CLI's, which is the group a worker
// leads. So the shell is this process's child, outside this process's group,
// and everything it starts stays under it unless it leaves. Not modelled:
// whether the CLI also starts a new session, which the observation did not
// record; any process the CLI puts between itself and the shell; the
// environment the CLI gives a command, for which this passes its own; and a
// command the CLI runs in the background, which this has no way to say. A
// shell started where no process group can be made, or where there is no
// /bin/sh, reports that in the Ran rather than running in this process's
// group, so a test reading Ran.Group is told which shape it got.
//
// What ends the group is this stand-in's own bound and no observation of the
// CLI's: the shell runs through internal/procgroup, so the group is ended
// when the shell exits and before the worker answers, and at HangFor with
// the shell, which is PRD section 8's group the invoker ends when the call
// returns or times out. A child the command left in the background is
// therefore gone by the time its Ran is printed, unless it left the group,
// and procgroup's package comment owns what it does not reach. A shell that
// HangFor ended answers status -1 and says so on its standard error.
func runCommand(command string) Ran {
	if refused := bashCheck(command); refused != "" {
		return Ran{Command: command, Exit: -1, Refused: refused}
	}
	ran := Ran{Command: command, Exit: -1}
	if err := groupRefusal(); err != nil {
		ran.Stderr = err.Error()
		return ran
	}
	ctx, cancel := context.WithTimeout(context.Background(), HangFor)
	defer cancel()
	cmd := exec.Command("/bin/sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// The group is read once the shell has started and before it is reaped,
	// so a shell that has already exited still reports the group it led
	// (#360).
	err := procgroup.RunStarted(ctx, cmd, runGrace, func(pid int) { ran.Group = groupOf(pid) })
	ran.Stdout, ran.Stderr = stdout.String(), stderr.String()
	if cmd.Process == nil {
		ran.Stderr += "standin: starting the command: " + err.Error() + "\n"
		return ran
	}
	if cmd.ProcessState != nil {
		ran.Exit = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		ran.Stderr += "standin: the command ran past " + HangFor.String() + " and was ended with its group\n"
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		ran.Stderr += "standin: waiting for the command: " + err.Error() + "\n"
	}
	return ran
}

// runGrace is the grace procgroup.Run is given for a Run exchange's command:
// how long a process that left the command's group may hold its output open
// after the shell exits before the worker stops waiting for it, and how long
// the shell is given to exit once its group was killed. procgroup.Run says
// each wait it bounds.
const runGrace = time.Second

// bashCheck is the one refusal of the Claude Code CLI's Bash tool the
// stand-in models, and it is modelled on one observation and no more: #267
// saw the CLI's safety check refuse a here-document whose text combined JSON
// braces with quotes, which is the shape a report filed on standard input
// through a here-document has. So a command holding a here-document
// operator, a brace, and a quote is refused without running, with the reason
// this returns; every other command is run. Not modelled: whatever else the
// CLI's check refuses, which the observation did not record, and whether the
// CLI would have asked a person rather than refused where one was present.
func bashCheck(command string) string {
	if strings.Contains(command, "<<") && strings.ContainsAny(command, "{}") && strings.ContainsAny(command, `"'`) {
		return "standin: the command combines a here-document with braces and quotes, which the Claude Code CLI's Bash check refused (#267)"
	}
	return ""
}
