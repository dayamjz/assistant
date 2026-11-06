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
// of who holds the pane, and one client that checks the pane, pastes the
// text and sends Enter, each under its kind's bound (tmuxReadBound,
// tmuxTypeBound), so a wedged server holds neither past it.
//
// Every client here runs through procgroup.RunInCallersGroup, in its
// caller's process group rather than one of its own. The caller is a command
// line the service's notifier runs and ends, at its bound or as the service
// stops, by sending that line's group SIGKILL, which then reaches these
// clients too; a client in a group of its own would outlive host wake with
// nobody left to end it. The bound each runs under here ends it alone, so a
// process a client started would be ended only with the caller's group.
//
// Enter is a key, sent with send-keys, because tmux encodes a key for what
// the pane's program asked of the terminal and a pasted carriage return is
// not read as one: a real Claude Code host kept a carriage return pasted
// right after the notice as part of the text, and submitted nothing. A key
// goes to the pane's mode while it is in one, copy mode among them, and to
// every pane of a window whose panes are synchronized, so the typing client
// first checks, by tmux's own if-shell on a format, that the pane is in no
// mode, is not synchronized, and takes input, and pastes and sends Enter
// only then; otherwise it pastes nothing and the call answers a
// PaneRefusal. A pane whose input is disabled is checked because a paste
// there reaches nothing while tmux answers success.
//
// Nothing here reads the screen. The terminal worker types only once its
// adapter reads the screen as ready; a person's host is drawn by a CLI and
// in a mode nothing here observed, so the paste is typed whatever the pane
// shows. That is the gap: text a person left in the input is submitted with
// the notice, and what a host does with input typed while it is busy is the
// host's. Who holds the pane is checked by ReadPane, which a caller compares
// with what it recorded before it types; that reading and the typing are two
// clients, so a pane that changes hands between them is typed into all the
// same. The typing client's own check is its first command after the text
// is loaded, and tmux may run another client's command between that check
// and the paste, which nothing here rules out.
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
// that server started in whole seconds since the Unix epoch, the process
// identifier of the pane's first process, and the name of the program in
// the pane's foreground, as tmux reports each. The process and the start
// time together name one server start: a later server on the same socket
// differs in its start time even where it was given the same process
// identifier, unless both happened within the same second. The first
// process is the one tmux started the pane with, on the pane's terminal, so
// ReadLineage of it reads that terminal's foreground process group, whose
// leader is the program tmux names.
type PaneFacts struct {
	ServerPID   int
	ServerStart int64
	PanePID     int
	Program     string
}

// paneFormat is the reading ReadPane asks tmux for, tab-separated, the
// program last so a tab in its name stays part of it.
const paneFormat = "#{pid}\t#{start_time}\t#{pane_id}\t#{pane_pid}\t#{pane_current_command}"

// ReadPane reads whose p is: the server answering on its socket, the
// pane's first process and the program in its foreground. A server that is not there, a pane it does not
// have, and an answer that does not parse as the reading asked for are
// errors, so a caller comparing facts never compares an empty reading.
func ReadPane(ctx context.Context, p Pane) (PaneFacts, error) {
	if err := p.Validate(); err != nil {
		return PaneFacts{}, err
	}
	cmd := procgroup.Command(p.Tmux, "-S", p.Socket, "display-message", "-p", "-t", p.ID, paneFormat)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := tmuxOutputBy(ctx, procgroup.RunInCallersGroup, tmuxReadBound, "the client reading the pane", cmd)
	if err != nil {
		return PaneFacts{}, fmt.Errorf("reading pane %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	fields := strings.SplitN(strings.TrimRight(string(out), "\n"), "\t", 5)
	if len(fields) != 5 || fields[2] != p.ID || fields[4] == "" {
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
	panePID, err := strconv.Atoi(fields[3])
	if err != nil || panePID <= 0 {
		return PaneFacts{}, fmt.Errorf("reading pane %s: tmux answered %q for its first process", p.ID, fields[3])
	}
	return PaneFacts{ServerPID: pid, ServerStart: start, PanePID: panePID, Program: fields[4]}, nil
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

// paneState is the format of the three facts the typing client checks
// before it pastes, in this order: whether the pane is in a mode, whether
// its window synchronizes its panes, and whether its input is disabled,
// each 1 or 0. paneTypeable is the one value that lets it paste.
const (
	paneState    = "#{pane_in_mode}#{pane_synchronized}#{pane_input_off}"
	paneTypeable = "000"
)

// paneRefused begins the line the typing client prints instead of
// pasting, followed by a space and the pane's state.
const paneRefused = "refused"

// PaneRefusal is TypeIntoPane's answer for a pane its typing client found
// it could not type into as asked: one in a mode, which would take the
// Enter; one in a window that synchronizes its panes, where the Enter would
// reach every pane; or one whose input tmux has disabled, where the paste
// would reach nothing. Nothing was pasted and no key was sent.
type PaneRefusal struct {
	Pane         string
	InMode       bool
	Synchronized bool
	InputOff     bool
}

func (r *PaneRefusal) Error() string {
	var why []string
	if r.InMode {
		why = append(why, "is in a tmux mode, copy mode among them, which would take the Enter")
	}
	if r.Synchronized {
		why = append(why, "is in a window that synchronizes its panes, so the Enter would reach every pane in it")
	}
	if r.InputOff {
		why = append(why, "has its input disabled in tmux (select-pane -d), so the paste would reach nothing")
	}
	return fmt.Sprintf("pane %s %s", r.Pane, strings.Join(why, ", and "))
}

// TypeIntoPane types text into p as one paste, bracketed when the pane's
// program asked tmux for bracketed pastes, followed by Enter as a key of its
// own, both only once the same client has read the pane as in no mode, not
// synchronized, and taking input; a pane that is not is a PaneRefusal. text
// has to be one line of printable UTF-8 that does not begin with a slash or
// an exclamation mark, which some hosts read as a command of their own;
// anything else is refused before a client starts. A client that fails or
// runs past tmuxTypeBound may leave part of the text typed, and nothing here
// takes it back; it is followed by one client deleting the call's buffer,
// under tmuxEndBound and not under ctx, so a paste that never ran leaves no
// buffer behind unless that client fails too.
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
	// The buffer and the pane are a name this file drew and a validated
	// pane identifier, so neither needs quoting in tmux's command syntax.
	cmd := procgroup.Command(p.Tmux, "-S", p.Socket,
		"load-buffer", "-b", buffer, "-", ";",
		"if-shell", "-F", "-t", p.ID, "#{==:"+paneState+","+paneTypeable+"}",
		"paste-buffer -p -d -b "+buffer+" -t "+p.ID+" ; send-keys -t "+p.ID+" Enter",
		"delete-buffer -b "+buffer+" ; display-message -p -t "+p.ID+" '"+paneRefused+" "+paneState+"'")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := tmuxOutputBy(ctx, procgroup.RunInCallersGroup, tmuxTypeBound, "the client typing into the pane", cmd)
	if err != nil {
		// The buffer may be loaded and never pasted. A paste that ran
		// already deleted it, and then this client's failure says nothing.
		_ = runTmuxBy(context.WithoutCancel(ctx), procgroup.RunInCallersGroup, tmuxEndBound, "the client deleting the notice's buffer",
			procgroup.Command(p.Tmux, "-S", p.Socket, "delete-buffer", "-b", buffer))
		return fmt.Errorf("typing into pane %s: %w: %s", p.ID, err, strings.TrimSpace(stderr.String()))
	}
	answer := strings.TrimSpace(string(out))
	if answer == "" {
		return nil
	}
	state, ok := strings.CutPrefix(answer, paneRefused+" ")
	if !ok || len(state) != len(paneTypeable) || strings.Trim(state, "01") != "" || state == paneTypeable {
		return fmt.Errorf("typing into pane %s: tmux answered %q, which is neither the paste nor its refusal", p.ID, answer)
	}
	return &PaneRefusal{Pane: p.ID, InMode: state[0] == '1', Synchronized: state[1] == '1', InputOff: state[2] == '1'}
}
