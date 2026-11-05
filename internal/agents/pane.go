package agents

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
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
//
// tmux's named buffers belong to the server, not to a client, so every
// TypeIntoPane loads its text into a buffer named for that call alone and
// the paste deletes it; two calls on one server, two homes whose
// coordinators share it among them, never paste each other's text.

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
// process identifier of the server that answered for the socket, the time
// that server started in whole seconds since the Unix epoch, and the name of
// the program in the pane's foreground, as tmux reports each. The process
// and the start time together name one server start: a later server on the
// same socket differs in its start time even where it was given the same
// process identifier, unless both happened within the same second.
type PaneFacts struct {
	ServerPID   int
	ServerStart int64
	Program     string
}

// paneFormat is the reading ReadPane asks tmux for, tab-separated, the
// program last so a tab in its name stays part of it.
const paneFormat = "#{pid}\t#{start_time}\t#{pane_id}\t#{pane_current_command}"

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
	fields := strings.SplitN(strings.TrimRight(string(out), "\n"), "\t", 4)
	if len(fields) != 4 || fields[2] != p.ID || fields[3] == "" {
		return PaneFacts{}, fmt.Errorf("reading pane %s: tmux answered %q, which is not that pane's reading", p.ID, out)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return PaneFacts{}, fmt.Errorf("reading pane %s: tmux answered %q for its server's process", p.ID, fields[0])
	}
	start, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || start <= 0 {
		return PaneFacts{}, fmt.Errorf("reading pane %s: tmux answered %q for its server's start time", p.ID, fields[1])
	}
	return PaneFacts{ServerPID: pid, ServerStart: start, Program: fields[3]}, nil
}

// paneBufferPrefix begins the name of the tmux buffer one TypeIntoPane
// loads its text into: this process's identifier and random bytes follow,
// so no two calls on one server share a buffer.
const paneBufferPrefix = "assistant-wake-"

// paneBuffer is a buffer name for one call.
func paneBuffer() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("drawing a buffer name: %w", err)
	}
	return paneBufferPrefix + strconv.Itoa(os.Getpid()) + "-" + hex.EncodeToString(b[:]), nil
}

// TypeIntoPane types text into p as one paste, bracketed when the pane's
// program asked tmux for bracketed pastes, followed by Enter as a key of
// its own. text has to be one line of printable UTF-8 that does not begin
// with a slash or an exclamation mark, which some hosts read as a command of
// their own; anything else is refused before a client starts. A client that
// fails or runs past tmuxTypeBound may leave part of the text typed, and
// nothing here takes it back; it is followed by one client deleting the
// call's buffer, under tmuxEndBound and not under ctx, so a paste that never
// ran leaves no buffer behind unless that client fails too.
func TypeIntoPane(ctx context.Context, p Pane, text string) error {
	if err := p.Validate(); err != nil {
		return err
	}
	printable := func(r rune) bool { return !unicode.IsPrint(r) && r != ' ' }
	if text == "" || !utf8.ValidString(text) || strings.IndexFunc(text, printable) >= 0 || strings.ContainsAny(text[:1], "/!") {
		return &invocationFieldError{field: "text", reason: "is not one line of printable text a host reads as a message"}
	}
	buffer, err := paneBuffer()
	if err != nil {
		return err
	}
	cmd := procgroup.Command(p.Tmux, "-S", p.Socket,
		"load-buffer", "-b", buffer, "-", ";",
		"paste-buffer", "-p", "-d", "-b", buffer, "-t", p.ID, ";",
		"send-keys", "-t", p.ID, "Enter")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := runTmux(ctx, tmuxTypeBound, "the client typing into the pane", cmd); err != nil {
		// The buffer may be loaded and never pasted. A paste that ran
		// already deleted it, and then this client's failure says nothing.
		_ = runTmux(context.WithoutCancel(ctx), tmuxEndBound, "the client deleting the notice's buffer",
			procgroup.Command(p.Tmux, "-S", p.Socket, "delete-buffer", "-b", buffer))
		return fmt.Errorf("typing into pane %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
