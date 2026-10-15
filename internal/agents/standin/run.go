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
)

// answer is the line a worker prints for a line this exchange took: Reply,
// or for a Run exchange the Ran of the command the line carries.
func (e Exchange) answer(line string) (string, error) {
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

// valid refuses an exchange whose Run cannot be answered as Run says: one
// with no Contains to find the command after, and one with a Reply the Ran
// would replace.
func (e Exchange) valid() error {
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
func runCommand(command string) Ran {
	ctx, cancel := context.WithTimeout(context.Background(), HangFor)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process the command left running in the background may hold its
	// output open after the shell exits; the worker stops waiting for it
	// after this rather than when it exits.
	cmd.WaitDelay = time.Second
	ran := Ran{Command: command, Exit: -1}
	if err := ownGroup(cmd); err != nil {
		ran.Stderr = err.Error()
		return ran
	}
	if err := cmd.Start(); err != nil {
		ran.Stderr = "standin: starting the command: " + err.Error()
		return ran
	}
	ran.Group = groupOf(cmd.Process.Pid)
	err := cmd.Wait()
	ran.Stdout, ran.Stderr = stdout.String(), stderr.String()
	if cmd.ProcessState != nil {
		ran.Exit = cmd.ProcessState.ExitCode()
	}
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		ran.Stderr += "standin: waiting for the command: " + err.Error() + "\n"
	}
	return ran
}
