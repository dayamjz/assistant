package agents

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StartInTerminal starts Claude Code as a terminal worker on t: the CLI's
// interactive interface, hosted on the terms TerminalWorkerRunner's mechanism
// states, reached without a person answering anything. Having this method
// is what makes the Claude Code adapter a TerminalWorkerRunner, and
// Capabilities declares the same fact.
//
// Everything below was observed of Claude Code 2.1.301 in a tmux pane,
// against a local server standing in for the model (#254), and nothing here
// re-checks it at run time except the one wait the screen reader states.
// Where a later CLI draws or reads differently, the start or a send is
// refused rather than typed blind, which is as far as that wait reaches.
//
// # Configuration directory
//
// A fresh directory is made for this worker alone, owner-only, beside its
// terminal's socket (claudeConfigFor), and given to the CLI as
// CLAUDE_CONFIG_DIR,
// which was observed to move the CLI's state file, .claude.json, there. The
// state file is written before the CLI starts, holding three answers the CLI
// would otherwise ask a person for on screens before its prompt: onboarding
// completed, the workspace trusted for spec.Dir exactly and, where it
// differs, for the path that resolves to, and, for a worker on the API key,
// the key approved. Nothing
// here reads the operator's own configuration or copies anything out of it,
// and the directory is removed once the worker has ended. Given that
// variable, the CLI was observed to write no .claude.json under HOME; it
// still wrote its caches there, and what else it reads from HOME is not
// established here.
//
// The approval is the last 20 characters of the key, which is what the CLI
// was observed to keep for one: an approval holding them skipped the key
// screen, and one holding the whole key did not. So those characters are on
// disk, in an owner-only file, for as long as the worker runs; the whole key
// is never written. A worker on the OAuth token approves no key and writes
// no part of the token: given the token alone, Claude Code 2.1.292 was
// observed to draw no screen about it.
//
// # Credential
//
// A fresh directory carries none of the operator's login, so the worker
// authenticates from its own environment, the runner's base environment
// with WorkerSpec.Env over it, with one of two credentials:
// ClaudeKeyVariable, ANTHROPIC_API_KEY, which the CLI's own help names for
// an API key, or ClaudeTokenVariable, CLAUDE_CODE_OAUTH_TOKEN, the
// long-lived token a subscription login issues, which is how a subscription
// reaches a worker that has no login of its own. The key wins: where it is
// set and not empty it is the credential and the token is removed from the
// worker's environment, and only otherwise is the token the credential,
// with the key removed, so the CLI is never handed both and which one it
// would prefer is never asked. The installed-CLI probe observed the CLI
// present the key as x-api-key and the token as a bearer Authorization,
// each alone. Where neither is set and not empty, the start is refused with
// an error wrapping ErrNoCredential that names both, before anything is
// made or started: a CLI without a credential was observed (#254) to reach
// its prompt and then fail every message, which is a worker that looks
// started and does nothing. No other form of credential is read, and
// whether a token the CLI accepts at start is still accepted later, once it
// expires or is revoked, is the account's and is not checked here.
//
// Neither value is recorded: a worker has no Record, the refusal names the
// variables and never a value, the command line carries neither, and the
// tmux server and the CLI receive the chosen one through their environment
// only.
//
// # Command line
//
// The configured entry's own flags, then the spec's grant's flags with
// spec.Route exactly as Start writes them for a pipe worker, a grant
// claudeTerminalGrantRefusal admits being GrantWorker's alone, then
// --ax-screen-reader, which draws the interface as flat text, and
// --session-id with an identifier drawn here, so the conversation is a new
// one under a name this package chose and never one resumed. The
// instructions come last, after "--", so instructions beginning with a dash
// are a message rather than a flag, and so no flag before them takes them as
// its value: --allowedTools takes every word after it up to the next flag
// and was observed to take a prompt placed right after it as a tool name.
// They are the first message as given, with the leading space the framing
// below adds where they begin with "!" or "/".
// The command that starts the terminal is bounded by TerminalCommandLimit,
// so instructions too long for it are refused before anything starts.
//
// # Typing
//
// A send is typed as one bracketed paste followed by Enter as a key of its
// own, only once the screen reads ready, which is no turn running and
// nothing in the input; claudeTerminalMessage is the framing and
// claudeScreen the reading. A send therefore waits for the turn running when
// it is made to end, and for an input a person left text in to be emptied,
// under its own ctx, and is refused with ErrTerminalNotReady, nothing typed,
// if that ctx ends first.
func (r *claudeRunner) StartInTerminal(ctx context.Context, t Terminal, spec WorkerSpec) (TerminalWorker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if err := claudeTerminalGrantRefusal(spec); err != nil {
		return nil, err
	}
	env, key, err := claudeTerminalCredential(WorkerEnvironment(r.settings.base, spec.Env), r.name)
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(spec.Instructions) {
		return nil, &invocationFieldError{field: "Instructions", reason: "is not valid UTF-8"}
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	session, err := sessionIdentifier()
	if err != nil {
		return nil, fmt.Errorf("drawing the terminal worker's session identifier: %w", err)
	}
	configDir := claudeConfigFor(t)
	err = seedClaudeConfig(spec.Dir, key, configDir)
	if err != nil {
		return nil, err
	}
	remove := func() { _ = os.RemoveAll(configDir) }
	env = append(slices.DeleteFunc(env, func(kv string) bool {
		return strings.HasPrefix(kv, claudeConfigVariable+"=")
	}), claudeConfigVariable+"="+configDir)

	args := r.claudeTerminalWorkerArgs(spec, session)
	worker, err := startTerminalWorker(ctx, t, procSpecTerminal{
		bin:     r.resolved,
		args:    args,
		dir:     spec.Dir,
		env:     env,
		grace:   r.settings.grace,
		maxOut:  r.settings.maxOut,
		cleanup: remove,
	}, "", r.terminalTyping())
	if err != nil {
		remove()
		return nil, err
	}
	return worker, nil
}

// AdoptInTerminal holds again a Claude Code terminal worker on
// AdoptTerminalWorker's terms, typed into as StartInTerminal's is. The
// configuration directory it removes at the worker's end is the one its
// start made beside the terminal's socket (claudeConfigFor), derived from
// the socket the adoption verifies and never from anything the worker or
// its server states, so nothing the worker can change names what is
// removed.
func (r *claudeRunner) AdoptInTerminal(ctx context.Context, t Terminal, pane Identity) (TerminalWorker, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	configDir := claudeConfigFor(t)
	worker, err := adoptTerminalWorker(ctx, t, pane, procSpecTerminal{
		grace:   r.settings.grace,
		maxOut:  r.settings.maxOut,
		cleanup: func() { _ = os.RemoveAll(configDir) },
	}, r.terminalTyping())
	if err != nil {
		return nil, err
	}
	return worker, nil
}

// claudeConfigFor is the configuration directory of the Claude Code worker
// on t: the socket's path with a suffix, so it is a function of the
// terminal alone, a start and an adoption of the same terminal name the
// same directory, and it sits in the socket's owner-only directory.
func claudeConfigFor(t Terminal) string { return t.Socket + ".claude" }

// terminalLaunchSessionMeasure is the session identifier
// TerminalLaunchCommandSize uses: the same length and shape sessionIdentifier
// draws, so the measured command size matches what a start would carry.
const terminalLaunchSessionMeasure = "00000000-0000-4000-8000-000000000000"

// TerminalLaunchCommandSize reports the tmux start command size when this
// adapter places spec.Instructions on that command.
func (r *claudeRunner) TerminalLaunchCommandSize(t Terminal, spec WorkerSpec) (int, bool, error) {
	if err := spec.Validate(); err != nil {
		return 0, true, err
	}
	args := r.claudeTerminalWorkerArgs(spec, terminalLaunchSessionMeasure)
	size, err := TmuxStartCommandSize(t, spec.Dir, r.resolved, args)
	return size, true, err
}

// WorkerLaunchRefusal reports whether spec cannot be started as this adapter
// would, including the tmux start command bound when t names a terminal.
func (r *claudeRunner) WorkerLaunchRefusal(t Terminal, spec WorkerSpec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	if t.Tmux == "" {
		return nil
	}
	if err := claudeTerminalGrantRefusal(spec); err != nil {
		return err
	}
	size, measures, err := r.TerminalLaunchCommandSize(t, spec)
	if err != nil {
		return err
	}
	if measures && size > TerminalCommandLimit {
		return &invocationFieldError{
			field: "command",
			reason: fmt.Sprintf("the command that starts the terminal is %d bytes, over the %d tmux is given, so the instructions it carries are too long for a terminal worker",
				size, TerminalCommandLimit),
		}
	}
	return nil
}

// claudeTerminalGrantRefusal refuses, with ErrTerminalGrantUnobserved, a
// terminal spec whose grant runs the interface outside acceptEdits: the
// ready screen claudeScreen reads, claudeFooterMark among it, was observed
// in acceptEdits mode only, and GrantSubordinate runs in the default mode
// so that it edits nothing. Widening the subordinate's grant to the
// observed mode is not this package's decision, so the terminal-hosted
// subordinate is refused and the pipe-hosted one, which reads no screen, is
// the one this adapter starts.
func claudeTerminalGrantRefusal(spec WorkerSpec) error {
	if spec.Grant() == GrantWorker {
		return nil
	}
	return &invocationFieldError{
		field: "grant",
		reason: fmt.Sprintf("a terminal worker under the %s grant runs the interface outside acceptEdits, where its ready screen was never observed",
			spec.Grant()),
		also: ErrTerminalGrantUnobserved,
	}
}

func (r *claudeRunner) claudeTerminalWorkerArgs(spec WorkerSpec, session string) []string {
	args := append(slices.Clone(r.extra), claudeGrantFlags(spec.Grant(), spec.Route)...)
	if spec.Model != "" {
		args = append(args, "--model", spec.Model)
	}
	if spec.Effort != "" {
		args = append(args, "--effort", spec.Effort)
	}
	return append(args, "--ax-screen-reader", "--session-id", session, "--", defused(spec.Instructions))
}

// ClaudeKeyVariable is the environment variable a Claude Code terminal
// worker reads an API key from, the first of the two credentials
// StartInTerminal accepts.
const ClaudeKeyVariable = "ANTHROPIC_API_KEY"

// ClaudeTokenVariable is the environment variable a Claude Code terminal
// worker reads a subscription's OAuth token from, the second of the two
// credentials StartInTerminal accepts and the one it uses only when
// ClaudeKeyVariable is unset or empty.
const ClaudeTokenVariable = "CLAUDE_CODE_OAUTH_TOKEN"

// claudeConfigVariable is the variable that points the CLI at a
// configuration directory.
const claudeConfigVariable = "CLAUDE_CONFIG_DIR"

// claudeApprovalSuffix is how many trailing characters of a key the CLI was
// observed to keep as its approval.
const claudeApprovalSuffix = 20

// claudeObservedVersion is the Claude Code version every observation this
// file and claudeScreen rest on was made on.
const claudeObservedVersion = "2.1.301"

// ErrNoCredential is the refusal to start an agent whose credential is not
// in its environment. The error wrapping it names every variable that
// would have been accepted.
var ErrNoCredential = errors.New("agents: the agent's credential is not in its environment")

// terminalTyping is the Claude Code adapter's terminal typing, on the terms
// StartInTerminal states, with Escape as its interrupt key on the terms
// CapabilityTerminalInterrupt states.
func (r *claudeRunner) terminalTyping() terminalTyping {
	return terminalTyping{frame: claudeTerminalMessage, bracketed: true, read: claudeScreen,
		interrupt: claudeInterruptKey, clear: claudeClearKey}
}

// claudeInterruptKey is the tmux name of the key that ends a running turn,
// and claudeClearKey of the one that empties the input, observed of Claude
// Code 2.1.308 on the terms CapabilityTerminalInterrupt states. Each press
// of the clear key took the input's last line out, its text and then its
// line break, so a message the interrupt had put back there was gone after
// one press per line, and a press on an empty input changed nothing.
const (
	claudeInterruptKey = "Escape"
	claudeClearKey     = "C-u"
)

// claudeTerminalCredential is env holding exactly one of the two credentials
// StartInTerminal accepts, with the API key it holds, empty when the
// credential is the OAuth token. The key is chosen when it is set and not
// empty, and the token otherwise; the variable not chosen is removed from
// env whatever it held, so the CLI is handed one credential and never has
// to choose between two. Neither set and not empty is an error wrapping
// ErrNoCredential that names both variables, and no value of either.
func claudeTerminalCredential(env []string, name string) ([]string, string, error) {
	if key := lastValue(env, ClaudeKeyVariable); key != "" {
		return withoutEnvNames(env, ClaudeTokenVariable), key, nil
	}
	if lastValue(env, ClaudeTokenVariable) != "" {
		return withoutEnvNames(env, ClaudeKeyVariable), "", nil
	}
	return nil, "", fmt.Errorf("%w: %s starts a terminal worker with a configuration directory of its own, which carries no login, so it needs %s or %s set in its environment and neither is",
		ErrNoCredential, name, ClaudeKeyVariable, ClaudeTokenVariable)
}

// lastValue is the value env gives name, the last assignment winning as it
// does for a process started with it, or empty.
func lastValue(env []string, name string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			value = v
		}
	}
	return value
}

// claudeSeed is the state file a terminal worker's configuration directory
// is given, on the terms StartInTerminal states.
type claudeSeed struct {
	Onboarded bool                         `json:"hasCompletedOnboarding"`
	Projects  map[string]claudeSeedProject `json:"projects"`
	Keys      claudeSeedKeys               `json:"customApiKeyResponses"`
}

type claudeSeedProject struct {
	Trusted bool `json:"hasTrustDialogAccepted"`
}

type claudeSeedKeys struct {
	Approved []string `json:"approved"`
	Rejected []string `json:"rejected"`
}

// seedClaudeConfig makes a terminal worker's configuration directory at
// configDir, which must not exist, and writes its state file. An empty key,
// which is a worker on the OAuth token, approves no key. A directory it made
// and could not finish is removed before it reports why; one standing at
// configDir already is refused and left as it is.
func seedClaudeConfig(dir, key, configDir string) error {
	projects := map[string]claudeSeedProject{dir: {Trusted: true}}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil && resolved != dir {
		projects[resolved] = claudeSeedProject{Trusted: true}
	}
	approved := []string{}
	if key != "" {
		approval := key
		if len(approval) > claudeApprovalSuffix {
			approval = approval[len(approval)-claudeApprovalSuffix:]
		}
		approved = append(approved, approval)
	}
	encoded, err := json.Marshal(claudeSeed{
		Onboarded: true,
		Projects:  projects,
		Keys:      claudeSeedKeys{Approved: approved, Rejected: []string{}},
	})
	if err != nil {
		return fmt.Errorf("encoding the terminal worker's configuration: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configDir), 0o700); err != nil {
		return fmt.Errorf("making the terminal worker's configuration directory's parent: %w", err)
	}
	if err := os.Mkdir(configDir, 0o700); err != nil {
		return fmt.Errorf("making the terminal worker's configuration directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".claude.json"), encoded, 0o600); err != nil {
		_ = os.RemoveAll(configDir)
		return fmt.Errorf("writing the terminal worker's configuration: %w", err)
	}
	return nil
}

// sessionIdentifier draws a random version 4 UUID, the form --session-id
// takes.
func sessionIdentifier() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// defused is text with a space before it when it begins with "!" or "/".
// Typed into an empty input, the CLI was observed to take a "!" as a shell
// command, run outside the worker's grant, and a "/" as a slash command
// rather than a message; a leading space was observed to leave either a
// message, which reached the model with the space.
func defused(text string) string {
	if strings.HasPrefix(text, "!") || strings.HasPrefix(text, "/") {
		return " " + text
	}
	return text
}

// claudeTerminalMessage is the Claude Code adapter's terminal framing: text
// as it is to be pasted, refused with an error wrapping ErrInvalidInvocation
// before anything is typed when it is not valid UTF-8, when it holds nothing
// but white space, or when it carries a control character other than a
// line break, which a terminal would deliver as a key rather than as text;
// the paste's own end marker begins with one. Text beginning with "!" or
// "/" is defused. It adds no newline: StartInTerminal's Enter is what
// submits it.
func claudeTerminalMessage(text string) ([]byte, error) {
	if !utf8.ValidString(text) {
		return nil, &invocationFieldError{field: "text", reason: "is not valid UTF-8"}
	}
	if strings.TrimSpace(text) == "" {
		return nil, &invocationFieldError{field: "text", reason: "holds nothing but white space, which submits nothing"}
	}
	for i, r := range text {
		if r != '\n' && unicode.IsControl(r) {
			return nil, &invocationFieldError{
				field:  "text",
				reason: fmt.Sprintf("carries control character %U at byte %d, which a terminal would deliver as a key", r, i),
			}
		}
	}
	return []byte(defused(text)), nil
}

// The pieces of the screen claudeScreen reads, as Claude Code 2.1.301 drew
// them with --ax-screen-reader. footerMark was on the footer whenever the
// interface was drawn, idle or running a turn, and on no first-run screen;
// busyMark was on it while a turn ran with nothing in the input, and 2.1.308
// was seen to drop it from a running turn's footer once text was typed into
// the input; and the input began at the first line below the footer that
// began with the prompt, the prompt alone when it was empty and the prompt,
// a no-break space and the text when it was not. With the footer idle, a
// message an interrupt put back in the input was drawn the same way, every
// line of it below the prompt's, or a placeholder naming a larger paste.
const (
	claudeFooterMark = "(shift+tab to cycle)"
	claudeBusyMark   = "esc to interrupt"
	claudeInputMark  = "$"
)

// claudeScreen reads a Claude Code terminal worker's visible screen. With no
// line carrying claudeFooterMark it is screenNotDrawn. Otherwise the input
// is read from the first line below the last such line that is the prompt
// alone or the prompt and a no-break space. When it is the prompt alone and
// no line below it is anything but blank, the input is empty, and the
// screen is screenBusy when that footer carries claudeBusyMark and
// screenReady when it does not. When the input holds text, on the prompt's
// line or below it, and the footer does not carry the mark, it is
// screenTyped. It is screenDrawn in every other case, a screen with no
// prompt below the footer among them. The last footer is read because the
// CLI redraws its footer below the conversation, so a line of the
// conversation that quotes it is above it.
//
// The input is read because the footer alone was seen to read idle while
// the input still showed the message the worker had just been sent, and in
// two runs of the installed-CLI probe that typed on the footer alone, a
// send defused with a leading space lost the space and ran as a slash
// command. That the two were the same window is the likeliest reading and
// is not established; with the input read as well, that probe has not
// failed that way since. An input holding text a person typed and did not
// send reads the same way, so a send never types into it.
func claudeScreen(screen string) screenState {
	lines := strings.Split(screen, "\n")
	footer := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], claudeFooterMark) {
			footer = i
			break
		}
	}
	if footer < 0 {
		return screenNotDrawn
	}
	prompt := -1
	for i := footer + 1; i < len(lines); i++ {
		if line := strings.TrimRight(lines[i], " "); line == claudeInputMark || strings.HasPrefix(line, claudeInputMark+"\u00a0") {
			prompt = i
			break
		}
	}
	if prompt < 0 {
		return screenDrawn
	}
	typed := strings.TrimRight(lines[prompt], " \u00a0") != claudeInputMark
	for _, line := range lines[prompt+1:] {
		typed = typed || strings.TrimSpace(line) != ""
	}
	busy := strings.Contains(lines[footer], claudeBusyMark)
	switch {
	case typed && !busy:
		return screenTyped
	case typed:
		return screenDrawn
	case busy:
		return screenBusy
	}
	return screenReady
}
