package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/machine"
)

// PRD section 8's host wake: the service types a notice into the tmux pane
// the coordinator's host runs in, so a coordinator holding neither a stream
// nor a wait still gets a turn when something needs it.
//
// It is two halves of one line. hostInstall, run from inside the pane,
// offers the line: this binary's absolute path, "host wake", and what it
// read of the pane, which is tmux's own two variables for the socket and
// the pane, the tmux binary on the PATH it was given, and one reading of the
// pane for its server's process and start time and for its program, which
// it refuses to offer when that program is the installer itself. The
// operator sets that line as
// notify.command, and internal/service's notifier runs it once per queued
// wake, on that file's terms: at most once, recorded, never on the loop.
// hostWake is what runs then: it reads the pane again, and types the notice
// only when the server answering on the socket has the process and the
// start time the installer read and the pane's program is the one it read,
// so a pane identifier a later server reused, under a reused process
// identifier too, or a host that exited back to its shell, is refused with
// nothing typed; so is a pane internal/agents' TypeIntoPane refuses, one in
// copy mode, synchronized with others, or with its input disabled. The
// notice is machine.HostWakeNotice, the sentence listen prints for a queued
// wake. Its tmux clients run in host wake's own process group, so the
// notifier ending the command by its group ends them with it.
//
// The gaps, beside internal/agents' Pane's (the screen is not read, so a
// person's half-typed input is sent with the notice, and the reading and
// the paste are two clients, so a change between them is not seen):
//
//   - One notice per wake: notify.command runs once per wake, so a burst of
//     wakes types a notice for each, and what a host does with input typed
//     while it is busy is the host's.
//   - notify.command is one command line, so the host wake replaces an
//     operator's private channel unless both are chained in one shell line.
//   - The program is compared by the name tmux reports, so another program
//     of the same name in the same pane is typed into.
//   - Of the programs that can lead the pane's foreground process group,
//     the installer refuses only itself, and records whichever leads it
//     while it runs. Run by a wrapper of its own, a script
//     or sh -c, it records the wrapper, so every wake is refused while the
//     host runs; run in the background of the pane's shell, it records that
//     shell, and a notice is typed into that shell while it is the pane's
//     foreground program.
//   - The installer runs in the pane only as far as it descends from the
//     pane's first process: a host that runs its commands where the kernel
//     reparents them away from that process is offered no line.
//   - The start time is tmux's, in whole seconds, so a later server given
//     the same process identifier within the same second as the one the
//     installer read is not told apart from it.
//   - The wake reaches a host in a tmux pane and no other; PRD section 8's
//     host wake table says which hosts that leaves without one.

// The flags hostWake takes, which hostWakeLine writes.
const (
	hostWakeTmuxFl    = "tmux"
	hostWakeSocketFl  = "socket"
	hostWakePaneFl    = "pane"
	hostWakeServerFl  = "server-pid"
	hostWakeStartFl   = "server-start"
	hostWakeProgramFl = "program"
)

// tmux's own variables, as tmux sets them in every pane's environment: the
// socket, the server's process and the session's index joined by commas,
// and the pane's identifier.
const (
	tmuxVar     = "TMUX"
	tmuxPaneVar = "TMUX_PANE"
)

// hostWake types the notice into the pane its flags name, on this file's
// terms. Every flag is required; a missing or malformed one is incorrect
// usage. A pane that cannot be read, one whose server or program is not the
// one recorded, and a paste that fails are each an operational failure with
// the reason, which the notifier records.
func hostWake(ctx context.Context, in *invocation) (any, error) {
	var pane agents.Pane
	var server int
	var start int64
	var program string
	if err := in.parseFlags("host wake", func(set *flag.FlagSet) {
		set.StringVar(&pane.Tmux, hostWakeTmuxFl, "", "absolute path of the tmux binary")
		set.StringVar(&pane.Socket, hostWakeSocketFl, "", "absolute path of the tmux server's socket")
		set.StringVar(&pane.ID, hostWakePaneFl, "", "the coordinator's pane, as tmux names it")
		set.IntVar(&server, hostWakeServerFl, 0, "process of the tmux server the installer read")
		set.Int64Var(&start, hostWakeStartFl, 0, "start time of the tmux server the installer read, in seconds since the epoch")
		set.StringVar(&program, hostWakeProgramFl, "", "program the installer read in the pane")
	}); err != nil {
		return nil, err
	}
	if err := pane.Validate(); err != nil {
		return nil, usageError{err: err}
	}
	if server <= 0 || start <= 0 || program == "" {
		return nil, usageError{err: fmt.Errorf("--%s, --%s and --%s name what host install read, and all three are required",
			hostWakeServerFl, hostWakeStartFl, hostWakeProgramFl)}
	}
	facts, err := agents.ReadPane(ctx, pane)
	if err != nil {
		return nil, fmt.Errorf("nothing was typed, because the coordinator's pane could not be read: %w", err)
	}
	if facts.ServerPID != server {
		return nil, fmt.Errorf("nothing was typed, because the tmux server on %s is process %d, not process %d that host install read", pane.Socket, facts.ServerPID, server)
	}
	if facts.ServerStart != start {
		return nil, fmt.Errorf("nothing was typed, because the tmux server on %s started at %d, not at %d as the one host install read", pane.Socket, facts.ServerStart, start)
	}
	if facts.Program != program {
		return nil, fmt.Errorf("nothing was typed, because pane %s runs %q, not %q that host install read", pane.ID, facts.Program, program)
	}
	notice := machine.HostWakeNotice()
	if err := agents.TypeIntoPane(ctx, pane, notice); err != nil {
		var refused *agents.PaneRefusal
		if errors.As(err, &refused) {
			return nil, fmt.Errorf("nothing was typed, because %w", err)
		}
		return nil, err
	}
	return machine.HostWake{Pane: pane.ID, Program: facts.Program, Typed: notice}, nil
}

// offerHostWake fills in the installer's answer with the host wake line for
// the pane it runs in, or with why it offers none. It never fails the
// installer: the stop guard's line stands whether or not a wake is offered.
func offerHostWake(ctx context.Context, in *invocation, answer *machine.HostInstall) {
	line, program, err := hostWakeLine(ctx, in)
	if err != nil {
		answer.WakeAbsent = err.Error()
		answer.WakeInstallerForeground = errors.Is(err, errInstallerForeground)
		return
	}
	answer.WakeCommand, answer.WakeProgram = line, program
}

// errInstallerForeground is hostWakeLine's refusal when this process leads
// the foreground process group of the pane's terminal, which is what a shell
// gives a command typed at its prompt. tmux names that group's leader as the
// pane's program, so the program read is the installer and not a host.
var errInstallerForeground = errors.New("this command leads the pane's foreground process group, so tmux names it, not the coordinator's host, as the pane's program")

// hostWakeLine is the notify.command line for the pane this invocation runs
// in, and the program it read there. tmux's variables are what this process
// was given, not where it runs, since every process started from a pane
// inherits them wherever it runs, so it offers a line only when this process
// descends from the pane's first process. It refuses, with errInstallerForeground,
// when the program read is this installer: the kernel's foreground process
// group of the pane's terminal, read from the pane's first process, is the
// group this process leads. A program leading the pane's foreground that
// runs the installer as a child, without handing it the terminal, keeps
// that group, so it is the program read; and the terminal read is the
// pane's, not the installer's own, so an installer leading a terminal some
// program made for it is not taken for the pane's.
func hostWakeLine(ctx context.Context, in *invocation) (line, program string, err error) {
	getenv := in.env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	joined, paneID := getenv(tmuxVar), getenv(tmuxPaneVar)
	if joined == "" || paneID == "" {
		return "", "", errors.New("this command did not run inside a tmux pane: " + tmuxVar + " or " + tmuxPaneVar + " is not set")
	}
	socket, server, ok := splitTmuxVar(joined)
	if !ok {
		return "", "", fmt.Errorf("%s is %q, which is not the socket, server and session tmux sets", tmuxVar, joined)
	}
	tmux := lookPath("tmux", getenv("PATH"))
	if tmux == "" {
		return "", "", errors.New("no tmux binary was found on PATH")
	}
	pane := agents.Pane{Tmux: tmux, Socket: socket, ID: paneID}
	facts, err := agents.ReadPane(ctx, pane)
	if err != nil {
		return "", "", err
	}
	if facts.ServerPID != server {
		return "", "", fmt.Errorf("%s names server process %d, and the server on %s is process %d", tmuxVar, server, socket, facts.ServerPID)
	}
	first, found, err := agents.ReadLineage(facts.PanePID)
	if err != nil {
		return "", "", fmt.Errorf("reading the foreground of pane %s through its first process %d: %w", paneID, facts.PanePID, err)
	}
	if !found || first.TerminalGroup == 0 {
		return "", "", fmt.Errorf("the foreground of pane %s could not be read: its first process %d is not running or has no terminal", paneID, facts.PanePID)
	}
	if first.TerminalGroup == os.Getpid() {
		return "", "", fmt.Errorf("pane %s runs %q: %w", paneID, facts.Program, errInstallerForeground)
	}
	inPane, err := agents.DescendsFrom(os.Getpid(), facts.PanePID)
	if err != nil {
		return "", "", fmt.Errorf("reading whether this command runs in pane %s: %w", paneID, err)
	}
	if !inPane {
		return "", "", fmt.Errorf("this command does not descend from pane %s's first process %d, so %s names a pane it does not run in", paneID, facts.PanePID, tmuxPaneVar)
	}
	line = strings.Join([]string{shellQuote(in.env.Executable), hostVerbName, "wake",
		"--" + hostWakeTmuxFl, shellQuote(tmux), "--" + hostWakeSocketFl, shellQuote(socket),
		"--" + hostWakePaneFl, shellQuote(paneID), "--" + hostWakeServerFl, strconv.Itoa(server),
		"--" + hostWakeStartFl, strconv.FormatInt(facts.ServerStart, 10),
		"--" + hostWakeProgramFl, shellQuote(facts.Program)}, " ")
	return line, facts.Program, nil
}

// splitTmuxVar reads tmux's TMUX variable, the socket, the server's process
// and the session's index joined by commas. It splits from the right, so a
// socket path holding a comma still reads whole.
func splitTmuxVar(joined string) (socket string, server int, ok bool) {
	session := strings.LastIndexByte(joined, ',')
	if session < 0 {
		return "", 0, false
	}
	pid := strings.LastIndexByte(joined[:session], ',')
	if pid < 0 {
		return "", 0, false
	}
	server, err := strconv.Atoi(joined[pid+1 : session])
	if err != nil || server <= 0 || !filepath.IsAbs(joined[:pid]) {
		return "", 0, false
	}
	return joined[:pid], server, true
}

// lookPath is the first executable regular file named name in the
// directories of path, as an absolute path, or empty. It reads the PATH this
// invocation was given rather than the process's.
func lookPath(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate
		}
	}
	return ""
}

// readOutHostWake is the human rendering of a notice typed: its row and the
// pane, as tmux names it.
func readOutHostWake(w io.Writer, answer machine.HostWake) {
	writeln(w, machine.HostWakePhrase(answer))
	writef(w, "%s %s\n", hostWakePaneLabel, printable(answer.Pane))
}

// The labels the host wake's machine text is printed after, in the
// installer's answer and in the wake's own.
const (
	hostWakePaneLabel    = "Pane:"
	hostWakeKeyLabel     = "Configuration key:"
	hostWakeCommandLabel = "Command:"
)
