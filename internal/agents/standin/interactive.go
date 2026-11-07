package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The interactive mode models the Claude Code CLI's interactive interface as
// it was observed in a tmux pane, on version 2.1.301 with --ax-screen-reader,
// and for the interrupt keys on version 2.1.308,
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
	// interruptedLine is what the CLI drew where a turn's reply would have
	// gone once the Escape key ended it, observed on version 2.1.308.
	interruptedLine = "Interrupted  · What should Claude do instead?"
	// approvalSuffix is how many trailing characters of an API key the CLI
	// was observed to keep as that key's approval: an approval naming them
	// skipped the key screen and one naming the whole key did not.
	approvalSuffix = 20
)

// escapeWait is how long an ESC waits for the byte after it before it is
// read as the Escape key alone. A terminal delivers the bytes of one
// sequence together, a paste's markers among them, so a wait this short
// only parts a key typed on its own.
const escapeWait = 50 * time.Millisecond

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

// credentialVariables are the variables the CLI was observed to
// authenticate from with no login in its configuration directory: the API
// key, presented as x-api-key, and a subscription's OAuth token, presented
// as a bearer Authorization (#320).
var credentialVariables = []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN"}

// credentialsSet is the name of every credentialVariables entry set and not
// empty in this process's environment, in that order. It reads each value
// only to tell empty from not, and returns none of them.
func credentialsSet() []string {
	var set []string
	for _, name := range credentialVariables {
		if os.Getenv(name) != "" {
			set = append(set, name)
		}
	}
	return set
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
//   - The Escape key, an ESC no other byte follows within escapeWait, is
//     handed to the answering side, which alone knows whether a turn is
//     running: one is ended by it, and with none it is recorded and does
//     nothing else. A turn it ends before drawing any of its reply has its
//     message put back in the input, through restores.
//   - Control-U outside a paste takes the last line out of the input: the
//     text after its last line break, or that line break when nothing
//     follows it, which is how the CLI took a message an interrupt had put
//     back in the input out of it, one press per line.
//
// It is read by one goroutine, so the fields are its own except ready and
// busy, which the drawing side sets.
type input struct {
	mu    sync.Mutex
	ready bool
	busy  bool
	// restores carries a message an interrupted turn puts back in the
	// input, which the reading goroutine takes, so what it holds and what
	// is drawn change together. It is buffered, so a send never waits on a
	// reader whose input has ended.
	restores chan string
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

// read parses r until it ends, sending every submission on out, every
// Escape key on escapes, and showing what is typed on shown, and closes out
// when it does.
func (in *input) read(r io.Reader, out chan<- submission, escapes chan<- struct{}, shown *screen) {
	defer close(out)
	src := bytesOf(r)
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
		var b byte
		select {
		case text := <-in.restores:
			buf.Reset()
			buf.WriteString(text)
			kind, drawn = submitMessage, text
			shown.setFooter(footerIdle)
			_ = shown.input(text)
			continue
		case next, ok := <-src:
			if !ok {
				return
			}
			b = next
		}
		switch {
		case b == 0x1b:
			seq, lone := escape(src)
			// An ESC right behind an ESC leaves the first one the Escape
			// key alone and begins a sequence of its own, so keys written
			// together are each a key, as the CLI took them.
			for seq == "\x1b" {
				escapes <- struct{}{}
				seq, lone = escape(src)
			}
			if lone {
				escapes <- struct{}{}
				continue
			}
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
		case b == 0x15:
			kept := strings.TrimSuffix(buf.String(), "\n")
			if kept == buf.String() {
				kept = kept[:strings.LastIndex(kept, "\n")+1]
			}
			buf.Reset()
			buf.WriteString(kept)
			if kept == "" {
				kind = submitMessage
			}
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

// bytesOf is r's bytes, one at a time, on a channel closed once r ends,
// so a reader can wait for the next byte with a deadline.
func bytesOf(r io.Reader) <-chan byte {
	src := make(chan byte, 4096)
	go func() {
		defer close(src)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			for _, b := range buf[:n] {
				src <- b
			}
			if err != nil {
				return
			}
		}
	}()
	return src
}

// escape reads the rest of an escape sequence after its ESC: a control
// sequence up to its final byte, or the one byte of any other. An ESC no
// byte follows within escapeWait, or at the end of the input, is the
// Escape key alone, which it reports as lone.
func escape(src <-chan byte) (string, bool) {
	var first byte
	select {
	case b, ok := <-src:
		if !ok {
			return "", true
		}
		first = b
	case <-time.After(escapeWait):
		return "", true
	}
	if first != '[' {
		return string(first), false
	}
	seq := []byte{first}
	for len(seq) < 32 {
		b, ok := <-src
		if !ok {
			break
		}
		seq = append(seq, b)
		if b >= 0x40 && b <= 0x7e {
			break
		}
	}
	return string(seq), false
}

// screen draws the interactive interface: lines that scroll, and below them
// a footer and the input that are redrawn in place, as the CLI redrew its
// own, so a screen read at any moment carries exactly one footer. The input
// is the prompt alone when nothing is typed, and otherwise the prompt, a no-
// break space and what is typed, each line of it on a line of its own, as
// the CLI drew a message an interrupt put back; a paste the CLI drew as a
// placeholder naming it is drawn whole here. The input
// is redrawn by the goroutine reading it and the rest by the one answering,
// so every draw holds the mutex.
type screen struct {
	mu sync.Mutex
	w  io.Writer
	// cols is the terminal's width, which rowsOf counts a line's rows
	// against, or 0 where it could not be read.
	cols   int
	footer string
	typed  string
	// shown is whether a footer is on the screen now, which is what the
	// next draw has to clear first, and rows is how many rows the input
	// below it took. back is how many rows above the footer the next draw
	// starts at instead, which is undraw's.
	shown bool
	rows  int
	back  int
}

// rowsOf is how many rows lines take on this screen: one each, and one more
// for every time a line runs past the last column, where the terminal wraps
// it onto the next row. A line exactly as wide as the terminal takes one
// row, because the wrap waits for the character after it. Runes are counted
// as one column each, which holds for the text this mode draws; a width it
// could not read is one row per line.
func (s *screen) rowsOf(lines []string) int {
	rows := 0
	for _, line := range lines {
		cells := utf8.RuneCountInString(line)
		if s.cols <= 0 || cells <= s.cols {
			rows++
			continue
		}
		rows += (cells + s.cols - 1) / s.cols
	}
	return rows
}

// prompt is the input for what is typed, its lines apart.
func (s *screen) prompt() []string {
	if s.typed == "" {
		return []string{inputLine}
	}
	lines := strings.Split(s.typed, "\n")
	lines[0] = inputLine + "\u00a0" + lines[0]
	return lines
}

// input redraws the footer and the input with text as what is typed.
func (s *screen) input(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typed = text
	if !s.shown {
		return nil
	}
	return s.linesLocked()
}

// undraw takes the rows drawn took off the screen and draws typed in the
// input in their place, in one redraw: what the CLI did to a turn it was
// interrupted before any of its reply, where the message's own row went
// off the screen and the message came back in the input.
func (s *screen) undraw(drawn, typed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.typed = typed
	if !s.shown {
		return nil
	}
	s.back = s.rowsOf(strings.Split(drawn, "\n"))
	return s.linesLocked()
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
		// The cursor is on the input's last line: back to its start, up to
		// the footer and past whatever undraw is taking away, and clear all
		// of it before drawing over it.
		fmt.Fprintf(&b, "\r\x1b[%dA\x1b[J", s.rows+s.back)
		s.back = 0
	}
	for _, text := range texts {
		for _, line := range strings.Split(text, "\n") {
			b.WriteString(line + "\r\n")
		}
	}
	s.shown = s.footer != ""
	if s.shown {
		prompt := s.prompt()
		s.rows = s.rowsOf(prompt)
		b.WriteString(s.footer + "\r\n" + strings.Join(prompt, "\r\n"))
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
// answer, or its exchange's own Turn, and the footer says which of the two,
// idle or running, the interface is in. A message no exchange answers draws
// a note and nothing else, so silence is never a default nobody wrote. The
// Escape key read while a turn runs ends it as the CLI was observed to: the
// turn's message is recorded Interrupted and the interface is idle again,
// with the observed interrupted line drawn in place of a reply when the
// turn drew its exchange's Progress, and with the message put back in the
// input, its own row taken off the screen with it, when it drew nothing;
// one read with no turn running is counted in
// IdleEscapes and does nothing else. A shell or slash
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
		ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"), Credentials: credentialsSet()}
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
	out := &screen{w: os.Stdout, cols: columns(int(os.Stdout.Fd()))}
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

	in := &input{restores: make(chan string, 4)}
	submitted := make(chan submission, 64)
	escapes := make(chan struct{}, 64)
	go in.read(os.Stdin, submitted, escapes, out)

	startup, turn := worker.Startup, worker.Turn
	if startup <= 0 {
		startup = DefaultStartup
	}
	if turn <= 0 {
		turn = DefaultTurn
	}
	time.Sleep(startup)

	// answer runs one message as a turn: it is recorded, drawn, shown
	// running for the turn, and answered, unless the Escape key ends the
	// turn first. An Escape read before the turn began is an idle one.
	answer := func(text string) (bool, int) {
		for idle := true; idle; {
			select {
			case <-escapes:
				call.IdleEscapes++
			default:
				idle = false
			}
		}
		call.Lines = append(call.Lines, text)
		if err := record(callsPath(control), call); err != nil {
			fmt.Fprintln(os.Stderr, "standin: "+err.Error())
			return true, exitNoRecord
		}
		in.setBusy(true)
		message := "you: " + text
		if err := out.lines(message); err != nil {
			return true, exitNoOutput
		}
		if err := out.draw(footerBusy); err != nil {
			return true, exitNoOutput
		}
		running, progress := turn, ""
		if exchange, ok := interactiveExchange(worker, text); ok {
			if exchange.Turn > 0 {
				running = exchange.Turn
			}
			progress = exchange.Progress
		}
		if progress != "" {
			if err := out.lines("claude: " + progress); err != nil {
				return true, exitNoOutput
			}
		}
		select {
		case <-time.After(running):
		case <-escapes:
			call.Interrupted = append(call.Interrupted, text)
			if err := record(callsPath(control), call); err != nil {
				return true, exitNoRecord
			}
			in.setBusy(false)
			if progress == "" {
				// The CLI took the message out of the conversation, its own
				// row with it, and put it back in the input, so the row goes
				// here too: a reader of the conversation takes the last one
				// of them for the message a reply would answer.
				out.setFooter(footerIdle)
				if err := out.undraw(message, text); err != nil {
					return true, exitNoOutput
				}
				in.restores <- text
				return false, 0
			}
			out.setFooter(footerIdle)
			if err := out.lines(interruptedLine); err != nil {
				return true, exitNoOutput
			}
			return false, 0
		}
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
	for {
		var sub submission
		select {
		case <-escapes:
			call.IdleEscapes++
			if err := record(callsPath(control), call); err != nil {
				return exitNoRecord
			}
			continue
		case next, ok := <-submitted:
			if !ok {
				return worker.Exit
			}
			sub = next
		}
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
}

// interactiveExchange is the first exchange that answers a message, and
// false when none does.
func interactiveExchange(worker *WorkerScript, text string) (Exchange, bool) {
	for _, exchange := range worker.Exchanges {
		if exchange.answers(text) {
			return exchange, true
		}
	}
	return Exchange{}, false
}

// interactiveReply is what the worker draws for a message: the first
// exchange that answers it, as "claude: " and its reply, or a note when
// none does; and whether that exchange ends the worker.
func interactiveReply(worker *WorkerScript, text string) (string, bool, error) {
	exchange, ok := interactiveExchange(worker, text)
	if !ok {
		return "standin: no exchange of the script answers this message: " + shorten(text), false, nil
	}
	reply, err := exchange.answer(text)
	if err != nil {
		return "", false, err
	}
	return "claude: " + reply, exchange.Ends, nil
}
