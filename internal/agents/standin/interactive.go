package standin

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// The interactive mode models the Claude Code CLI's interactive interface as
// it was observed in a tmux pane, on version 2.1.301 with --ax-screen-reader,
// against a local server standing in for the model. The package
// documentation says what that observation was and what this does not model;
// everything below is one rule of it, and each is one a typist can be failed
// by.

// Exit statuses particular to the interactive mode.
const (
	// exitInteractiveArgs is a command line this mode does not read: no "--"
	// to take the first message after, or a permission mode whose footer was
	// not observed.
	exitInteractiveArgs = 10
	// exitNoTerminal is a standard input that could not be put in raw mode,
	// which is every input that is not a terminal and every platform this
	// package has no raw mode for.
	exitNoTerminal = 11
)

// Flags and screen text the interactive mode reads and draws. The footer
// strings are what the CLI drew in acceptEdits mode, idle and with a turn
// running; the first-run lines are the ones that tell its screens apart.
const (
	screenReaderFlag = "--ax-screen-reader"
	screenReaderLine = "[Screen Reader Mode: on via flag]"
	footerIdle       = "accept edits on (shift+tab to cycle)"
	footerBusy       = "accept edits on (shift+tab to cycle)  ·  esc to interrupt"
	themeScreen      = "Choose the text style that looks best with your terminal"
	trustScreen      = "Permission Required: Accessing workspace:"
	trustChoice      = "y. Yes, I trust this folder"
	keyScreen        = "Detected a custom API key in your environment"
	keyChoice        = "Do you want to use this API key?"
	inputLine        = "$"
	// approvalSuffix is how many trailing characters of an API key the CLI
	// was observed to keep as that key's approval: an approval naming them
	// skipped the key screen and one naming the whole key did not.
	approvalSuffix = 20
)

// interactiveArgs reports whether args select the interactive mode: the
// screen reader flag among the flags, and no --print. Only the words before
// "--" are flags, so a message after it that mentions either is a message.
func interactiveArgs(args []string) bool {
	flags, _, _ := splitAtDashes(args)
	return slices.Contains(flags, screenReaderFlag) && !slices.Contains(flags, printFlag)
}

// splitAtDashes is args cut at the first "--": the flags before it, the
// words after it, and whether there was one.
func splitAtDashes(args []string) ([]string, []string, bool) {
	i := slices.Index(args, "--")
	if i < 0 {
		return args, nil, false
	}
	return args[:i], args[i+1:], true
}

// flagAfter is the word following name among flags, empty when there is
// none.
func flagAfter(flags []string, name string) string {
	i := slices.Index(flags, name)
	if i < 0 || i+1 >= len(flags) {
		return ""
	}
	return flags[i+1]
}

// firstRun is the state file the CLI read its first-run answers from, as
// far as this mode reads it.
type firstRun struct {
	Onboarded bool `json:"hasCompletedOnboarding"`
	Projects  map[string]struct {
		Trusted bool `json:"hasTrustDialogAccepted"`
	} `json:"projects"`
	Keys struct {
		Approved []string `json:"approved"`
	} `json:"customApiKeyResponses"`
}

// firstRunScreen is the screen this worker opens on instead of its prompt,
// as lines, or nil when the state file answers every first-run question.
// The state file is .claude.json in CLAUDE_CONFIG_DIR, or in HOME when that
// is unset, and an unreadable one answers none of them. Trust is read for
// dir exactly as given, which is the working directory this process
// resolved, and a key is approved only by its last approvalSuffix
// characters, so each rule is as strict as what was observed and no more
// lenient.
func firstRunScreen(configDir, home, dir, key string) []string {
	base := configDir
	if base == "" {
		base = home
	}
	var state firstRun
	if raw, err := os.ReadFile(filepath.Join(base, ".claude.json")); err == nil {
		_ = json.Unmarshal(raw, &state)
	}
	switch {
	case !state.Onboarded:
		return []string{"Let's get started.", themeScreen}
	case !state.Projects[dir].Trusted:
		return []string{trustScreen, dir, trustChoice, "n. No, exit", "Enter y/n:"}
	case key != "" && !slices.Contains(state.Keys.Approved, suffix(key, approvalSuffix)):
		return []string{keyScreen, keyChoice, "y. Yes", "n. No (recommended)", "Enter y/n:"}
	}
	return nil
}

// suffix is the last n bytes of s, or s when it is shorter.
func suffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// submission is one input the interface took on an Enter.
type submission struct {
	kind   submissionKind
	text   string
	queued bool
}

type submissionKind int

const (
	submitMessage submissionKind = iota
	submitShell
	submitSlash
)

// input is the interactive mode's reading of its terminal: bytes in raw
// mode, with a bracketed paste kept whole and an Enter submitting what was
// typed. Its rules, each an observation of the CLI:
//
//   - Text between the bracketed paste markers is one input, with every
//     line break in it kept as a line break, so a pasted message with
//     several lines is submitted as one.
//   - Outside a paste, a carriage return is Enter: it submits the input,
//     unless the interface has not been drawn yet, in which case it is
//     dropped and the text stays unsubmitted. That is what a pane typed at
//     before the CLI drew its interface showed.
//   - A "!" or a "/" typed or pasted into an empty input makes the input a
//     shell command or a slash command rather than a message. Anything
//     before it, a space among them, leaves the input a message.
//   - An input submitted while a turn is running is queued and answered
//     after it.
//
// It is read by one goroutine, so the fields are its own except ready and
// busy, which the drawing side sets.
type input struct {
	mu    sync.Mutex
	ready bool
	busy  bool
}

func (in *input) setReady() {
	in.mu.Lock()
	in.ready = true
	in.mu.Unlock()
}

func (in *input) setBusy(busy bool) {
	in.mu.Lock()
	in.busy = busy
	in.mu.Unlock()
}

func (in *input) state() (ready, busy bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.ready, in.busy
}

// read parses r until it ends, sending every submission on out and showing
// what is typed on shown, and closes out when it does.
func (in *input) read(r io.Reader, out chan<- submission, shown *screen) {
	defer close(out)
	br := bufio.NewReader(r)
	var buf strings.Builder
	kind := submitMessage
	pasting := false
	drawn := ""
	add := func(b byte) {
		if buf.Len() == 0 && kind == submitMessage {
			switch b {
			case '!':
				kind = submitShell
				return
			case '/':
				kind = submitSlash
			}
		}
		buf.WriteByte(b)
	}
	for {
		b, err := br.ReadByte()
		if err != nil {
			return
		}
		switch {
		case b == 0x1b:
			seq := escape(br)
			switch seq {
			case "[200~":
				pasting = true
			case "[201~":
				pasting = false
			}
		case pasting && (b == '\r' || b == '\n'):
			add('\n')
		case pasting:
			add(b)
		case b == '\r':
			ready, busy := in.state()
			if !ready || (buf.Len() == 0 && kind == submitMessage) {
				continue
			}
			out <- submission{kind: kind, text: buf.String(), queued: busy}
			buf.Reset()
			kind = submitMessage
		case b == 0x7f:
			if s := buf.String(); s != "" {
				buf.Reset()
				buf.WriteString(s[:len(s)-1])
			}
		case b < 0x20:
			// A control key outside a paste is a key the CLI acts on,
			// none of which this mode models; it is not text.
		default:
			add(b)
		}
		// What is typed is drawn with the "!" a shell command took, so
		// an input in that mode never reads as empty.
		display := buf.String()
		if kind == submitShell {
			display = "!" + display
		}
		if display != drawn {
			drawn = display
			_ = shown.input(display)
		}
	}
}

// escape reads the rest of an escape sequence after its ESC: a control
// sequence up to its final byte, or the one byte of any other.
func escape(br *bufio.Reader) string {
	first, err := br.ReadByte()
	if err != nil {
		return ""
	}
	if first != '[' {
		return string(first)
	}
	seq := []byte{first}
	for len(seq) < 32 {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		seq = append(seq, b)
		if b >= 0x40 && b <= 0x7e {
			break
		}
	}
	return string(seq)
}

// screen draws the interactive interface: lines that scroll, and below them
// a footer and an input line that are redrawn in place, as the CLI redrew
// its own, so a screen read at any moment carries exactly one footer. The
// input line is the prompt alone when nothing is typed, and the prompt, a
// no-break space and the first line of what is typed otherwise. The input
// is redrawn by the goroutine reading it and the rest by the one answering,
// so every draw holds the mutex.
type screen struct {
	mu     sync.Mutex
	w      io.Writer
	footer string
	typed  string
	// shown is whether a footer is on the screen now, which is what the
	// next draw has to clear first.
	shown bool
}

// prompt is the input line for what is typed.
func (s *screen) prompt() string {
	if s.typed == "" {
		return inputLine
	}
	first, _, _ := strings.Cut(s.typed, "\n")
	return inputLine + "\u00a0" + first
}

// input redraws the input line with text as what is typed.
func (s *screen) input(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typed = text
	if !s.shown {
		return nil
	}
	_, err := io.WriteString(s.w, "\r\x1b[K"+s.prompt())
	return err
}

// lines draws text above the footer, each line of each argument on a line
// of its own, and redraws the footer below it when one is set.
func (s *screen) lines(texts ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.linesLocked(texts...)
}

func (s *screen) linesLocked(texts ...string) error {
	var b strings.Builder
	if s.shown {
		// The cursor is on the input line: back to its start, up to the
		// footer, and clear both before drawing over them.
		b.WriteString("\r\x1b[1A\x1b[J")
	}
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			b.WriteString(line + "\r\n")
		}
	}
	s.shown = s.footer != ""
	if s.shown {
		b.WriteString(s.footer + "\r\n" + s.prompt())
	}
	_, err := io.WriteString(s.w, b.String())
	return err
}

// draw sets the footer and redraws it.
func (s *screen) draw(footer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.footer = footer
	return s.linesLocked()
}

// setFooter sets the footer the next draw draws.
func (s *screen) setFooter(footer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.footer = footer
}

// serveInteractiveWorker runs this process as an interactive worker: it
// records itself, draws the first-run screen its state file calls for or
// its interface, answers the message after "--" and then every input it
// is given as the script's exchanges say, and rewrites its record as each
// arrives.
//
// It draws the interface Startup after it starts, a turn takes Turn to
// answer, and the footer says which of the two, idle or running, the
// interface is in. A message no exchange answers draws a note and nothing
// else, so silence is never a default nobody wrote. A shell or slash
// command is recorded and drawn, and nothing is run for it.
//
// It exits when its input ends, which is what the end of its pane does, or
// after a reply whose exchange says Ends. A first-run screen is never left:
// nothing in this mode answers one, so a worker that opens on one reads and
// discards its input until it ends.
func serveInteractiveWorker(control string, args []string) int {
	script, err := readScript(control)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoScript
	}
	worker := script.Worker
	if worker == nil {
		worker = &WorkerScript{}
	}
	flags, after, dashed := splitAtDashes(args)
	if !dashed || len(after) != 1 {
		fmt.Fprintln(os.Stderr, `standin: an interactive worker reads its first message as the one word after "--", and this command line has none`)
		return exitInteractiveArgs
	}
	if mode := flagAfter(flags, "--permission-mode"); mode != "acceptEdits" {
		fmt.Fprintf(os.Stderr, "standin: an interactive worker's footer was observed in acceptEdits mode only, and this one is in %q\n", mode)
		return exitInteractiveArgs
	}
	dir, _ := os.Getwd()
	call := Call{Args: args, Dir: dir, Step: -1, Worker: true, Interactive: true,
		ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR")}
	seq, err := claimCall(control, call)
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: "+err.Error())
		return exitNoRecord
	}
	call.Seq = seq

	restore, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: putting the terminal in raw mode: "+err.Error())
		return exitNoTerminal
	}
	defer restore()

	// The CLI asks the terminal for bracketed paste, which is what makes a
	// paste arrive between markers at all.
	out := &screen{w: os.Stdout}
	if _, err := io.WriteString(os.Stdout, "\x1b[?2004h"); err != nil {
		return exitNoOutput
	}
	if err := out.lines(screenReaderLine); err != nil {
		return exitNoOutput
	}
	if blocked := firstRunScreen(call.ConfigDir, os.Getenv("HOME"), dir, os.Getenv("ANTHROPIC_API_KEY")); blocked != nil {
		if err := out.lines(blocked...); err != nil {
			return exitNoOutput
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		return worker.Exit
	}

	in := &input{}
	submitted := make(chan submission, 64)
	go in.read(os.Stdin, submitted, out)

	startup, turn := worker.Startup, worker.Turn
	if startup <= 0 {
		startup = DefaultStartup
	}
	if turn <= 0 {
		turn = DefaultTurn
	}
	time.Sleep(startup)

	// answer runs one message as a turn: it is recorded, drawn, shown
	// running for the turn, and answered.
	answer := func(text string) (bool, int) {
		call.Lines = append(call.Lines, text)
		if err := record(callsPath(control), call); err != nil {
			fmt.Fprintln(os.Stderr, "standin: "+err.Error())
			return true, exitNoRecord
		}
		in.setBusy(true)
		if err := out.lines("you: " + text); err != nil {
			return true, exitNoOutput
		}
		if err := out.draw(footerBusy); err != nil {
			return true, exitNoOutput
		}
		time.Sleep(turn)
		reply, ends, err := interactiveReply(worker, text)
		if err != nil {
			fmt.Fprintln(os.Stderr, "standin: "+err.Error())
			return true, exitNoOutput
		}
		in.setBusy(false)
		out.setFooter(footerIdle)
		if err := out.lines(reply, "done"); err != nil {
			return true, exitNoOutput
		}
		if ends {
			return true, worker.Exit
		}
		return false, 0
	}

	// The interface is drawn with the first message's turn already
	// running, as the CLI drew it when started with one: its footer was
	// first seen busy, never idle before the first turn.
	in.setReady()
	if done, status := answer(after[0]); done {
		return status
	}
	for sub := range submitted {
		if sub.queued {
			call.Queued = append(call.Queued, sub.text)
		}
		switch sub.kind {
		case submitShell:
			call.Shell = append(call.Shell, sub.text)
			if err := record(callsPath(control), call); err != nil {
				return exitNoRecord
			}
			if err := out.lines("! "+sub.text, "standin: a shell command was recorded and not run"); err != nil {
				return exitNoOutput
			}
		case submitSlash:
			call.Slash = append(call.Slash, sub.text)
			if err := record(callsPath(control), call); err != nil {
				return exitNoRecord
			}
			if err := out.lines(sub.text, "standin: a slash command was recorded and not run"); err != nil {
				return exitNoOutput
			}
		default:
			if done, status := answer(sub.text); done {
				return status
			}
		}
	}
	return worker.Exit
}

// interactiveReply is what the worker draws for a message: the first
// exchange that answers it, as "claude: " and its reply, or a note when
// none does; and whether that exchange ends the worker.
func interactiveReply(worker *WorkerScript, text string) (string, bool, error) {
	for _, exchange := range worker.Exchanges {
		if !exchange.answers(text) {
			continue
		}
		reply, err := exchange.answer(text)
		if err != nil {
			return "", false, err
		}
		return "claude: " + reply, exchange.Ends, nil
	}
	return "standin: no exchange of the script answers this message: " + shorten(text), false, nil
}
