package service

import (
	"fmt"

	"github.com/dayamjz/assistant/internal/ipc"
)

// The foreground relation: whether a peer is in the foreground of its
// controlling terminal, decided from the kernel's credentials and the
// lineage it keeps for the peer, and nothing a caller states. It is the
// third relation over one input beside containment (contain.go) and the
// worker group set (workers.go), and it answers for two requests:
// task.respond on the local landing's approval hold, which PRD section 6
// gives the person ("You approve") and which local.go raises, and
// keptwork.answer given a discard's confirmation, the other answer section
// 6 gives the person alone (keptdiscard.go, which refuses a confirmation
// this relation admits as well while its removal is deferred).
//
// # Why this fact
//
// A coordinator agent and a person reach this service through the same
// binary, the same socket and the same user, and the service started
// neither, so no set it holds names the coordinator's process; coordinator.go
// says a caller outside every worker group is taken as the coordinator. What
// the kernel keeps about the two and neither can state is the terminal: a
// shell gives the job it runs in the foreground the foreground of the
// terminal, so a person's command has a controlling terminal and is in that
// terminal's foreground process group, while a host agent's tool runs its
// command with no controlling terminal and in a process group of its own.
// The second half was observed of the Claude Code CLI on macOS, the shape
// #225 saw of the same tool's process group with the terminal half added,
// and the first is job control as every interactive shell does it.
// agents.Lineage carries both facts out of one read of the peer's entry, and
// agents.Lineage.Foreground is the predicate.
//
// # Which way it errs
//
// Toward refusing. A peer with no controlling terminal, one in a group the
// terminal was not handed to, and one whose lineage cannot be read are all
// refused: the first two with ipc.ErrNotAtTerminal, the last with
// ipc.ErrUnavailable, so a fact that could not be established never serves
// the request. A person's command run in the background, from a script with
// no terminal, or over a connection with no pseudo-terminal is refused the
// same way, and the refusal says to run it at a terminal in the foreground.
//
// # What it does not establish
//
// A terminal's foreground is not a person. A program that allocates a
// pseudo-terminal of its own, as a terminal multiplexer does for a command
// it starts, puts that command in the foreground of a terminal, and this
// relation serves it; the plain path, a host's tool command, is what it
// refuses. So the hold it guards is answerable only from a terminal's
// foreground and not only by a person, the resolver recorded stays what
// ipc.Request.HoldResolver derives, and store.ResolvedByPerson keeps no
// producer. The lineage is read when the request is decided and not when
// the connection was accepted, so a peer whose identifier was reused between
// the two reads as the new process, the window placement.go names for the
// peer itself. On a platform with no lineage read, internal/ipc identifies
// no peer and every restricted method is refused upstream of this one.

// refuseOffTerminal refuses a request whose peer is not in the foreground of
// its controlling terminal, on the terms above: ipc.ErrNotAtTerminal naming
// why, ipc.ErrUnavailable wrapped when the peer's lineage could not be read,
// and ipc.ErrUnidentifiedPeer when the kernel named no peer. A nil answer
// means the peer was read in a terminal's foreground.
func (s *Service) refuseOffTerminal(req ipc.Request) error {
	creds, err := req.Peer.Credentials()
	if err != nil {
		return err
	}
	lineage, err := readLineage(creds.PID)
	if err != nil {
		return fmt.Errorf("%w: whether %s is in the foreground of a terminal could not be determined: %w",
			ipc.ErrUnavailable, creds, err)
	}
	switch {
	case lineage.TerminalGroup == 0:
		return fmt.Errorf("%w: %s has no controlling terminal, and %s on this hold is answered only from the foreground "+
			"of one; run it at a terminal, in the foreground, and not from a script, a background job or an agent's tool",
			ipc.ErrNotAtTerminal, peerOf(req), req.Method)
	case !lineage.Foreground():
		return fmt.Errorf("%w: %s is in process group %d and its terminal's foreground group is %d, and %s on this "+
			"hold is answered only from the foreground; run it at a terminal, in the foreground, and not as a "+
			"background job or an agent's tool",
			ipc.ErrNotAtTerminal, peerOf(req), lineage.Group, lineage.TerminalGroup, req.Method)
	}
	return nil
}
