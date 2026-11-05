package agents

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// A pane this package did not start: the coordinator's host in a tmux pane
// of a person's own server, which PRD section 8's host wake types a notice
// into. It is the terminal worker's typing without the worker: one reading
// of who holds the pane, and one paste followed by Enter, each one tmux
// client run through runTmux under its kind's bound (tmuxReadBound,
// tmuxTypeBound), so a wedged server holds neither past it.
//
// Nothing here reads the screen. The terminal worker types only once its
// adapter reads the screen as ready; a person's host is drawn by a CLI and
// in a mode nothing here observed, so the paste is typed whatever the pane
// shows. That is the gap: text a person left in the input is submitted with
// the notice, and what a host does with input typed while it is busy is the
// host's. What is checked instead is who holds the pane, by ReadPane, which
// a caller compares with what it recorded before it types; the reading and
// the paste are two clients, so a pane that changes hands between them is
// typed into all the same.

// Pane names one pane of a tmux server this package did not start: the
// tmux binary as an absolute path, the server's socket as an absolute path,
// and the pane's identifier as tmux names it, a percent sign and a number.
type Pane struct {
	Tmux   string
	Socket string
	ID     string
}

// Validate refuses a pane named by anything but an absolute tmux binary, an
// absolute socket path within the local socket limit, and an identifier of
// tmux's pane shape, so no client is started for a name tmux would read as
// something else, a session or a window among them.
func (p Pane) Validate() error {
	if err := (Terminal{Tmux: p.Tmux, Socket: p.Socket}).Validate(); err != nil {
		return err
	}
	digits := strings.TrimPrefix(p.ID, "%")
	if digits == p.ID || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return &invocationFieldError{field: "ID", reason: fmt.Sprintf("%q is not a tmux pane identifier", p.ID)}
	}
	return nil
}

// PaneFacts is what one reading of a pane says about who holds it: the
// process identifier of the server that answered for the socket, and the
// name of the program in the pane's foreground, as tmux reports each.
type PaneFacts struct {
	ServerPID int
	Program   string
}

// paneFormat is the reading ReadPane asks tmux for, tab-separated.
const paneFormat = "#{pid}\t#{pane_id}\t#{pane_current_command}"

// ReadPane reads who holds p: the server answering on its socket and the
// program in its foreground. A server that is not there, a pane it does not
// have, and an answer that does not parse as the reading asked for are
// errors, so a caller comparing facts never compares an empty reading.
func ReadPane(ctx context.Context, p Pane) (PaneFacts, error) {
	if err := p.Validate(); err != nil {
		return PaneFacts{}, err
	}
	cmd := procgroup.Command(p.Tmux, "-S", p.Socket, "display-message", "-p", "-t", p.ID, paneFormat)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := tmuxOutput(ctx, tmuxReadBound, "the client reading the pane", cmd)
	if err != nil {
		return PaneFacts{}, fmt.Errorf("reading pane %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.Split(strings.TrimRight(string(out), "\n"), "\t")
	if len(fields) != 3 || fields[1] != p.ID || fields[2] == "" {
		return PaneFacts{}, fmt.Errorf("reading pane %s: tmux answered %q, which is not that pane's reading", p.ID, out)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return PaneFacts{}, fmt.Errorf("reading pane %s: tmux answered %q for its server's process", p.ID, fields[0])
	}
	return PaneFacts{ServerPID: pid, Program: fields[2]}, nil
}

// paneBuffer is the tmux buffer TypeIntoPane loads its text into, deleted
// by the paste.
const paneBuffer = "assistant-wake"

// TypeIntoPane types text into p as one paste, bracketed when the pane's
// program asked tmux for bracketed pastes, followed by Enter as a key of
// its own. text has to be one line of printable UTF-8 that does not begin
// with a slash or an exclamation mark, which some hosts read as a command of
// their own; anything else is refused before a client starts. A client that
// fails or runs past tmuxTypeBound may leave part of the text typed, and
// nothing here takes it back.
func TypeIntoPane(ctx context.Context, p Pane, text string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	printable := func(r rune) bool { return !unicode.IsPrint(r) && r != ' ' }
	if text == "" || !utf8.ValidString(text) || strings.IndexFunc(text, printable) >= 0 || strings.ContainsAny(text[:1], "/!") {
		return &invocationFieldError{field: "text", reason: "is not one line of printable text a host reads as a message"}
	}
	cmd := procgroup.Command(p.Tmux, "-S", p.Socket,
		"load-buffer", "-b", paneBuffer, "-", ";",
		"paste-buffer", "-p", "-d", "-b", paneBuffer, "-t", p.ID, ";",
		"send-keys", "-t", p.ID, "Enter")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := runTmux(ctx, tmuxTypeBound, "the client typing into the pane", cmd); err != nil {
		return fmt.Errorf("typing into pane %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
