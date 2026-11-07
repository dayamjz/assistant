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
// against a local server standing in for the model (#254), except the
// section on the operator's home, which names the version and the date its
// own measurement was made on. Nothing here re-checks any of it at run
// time except the one wait the screen reader states.
// Where a later CLI draws or reads differently, the start or a send is
// refused rather than typed blind, which is as far as that wait reaches.
//
// # Directories of its own
//
// Two fresh directories are made for this worker alone, owner-only, beside
// its terminal's socket: a configuration directory (claudeConfigFor) given
// to the CLI as CLAUDE_CONFIG_DIR, which was observed to move the CLI's
// state file, .claude.json, there, and a cache directory (claudeCacheFor)
// given as XDG_CACHE_HOME, which is not the CLI's own variable but the one
// a program following the XDG convention reads, so what the worker runs
// caches per worker rather than in the operator's. A program that caches
// there therefore starts cold for every worker and loses it at the end,
// which is what the isolation costs. The state file is written before the
// CLI starts, holding three answers the CLI would otherwise ask a person
// for on screens before its prompt: onboarding completed, the workspace
// trusted for spec.Dir exactly and, where it differs, for the path that
// resolves to, and, for a worker on the API key, the key approved. Nothing
// here reads the operator's own configuration or copies anything out of
// it, and both directories are removed once the worker has ended; a start
// that cannot make the cache directory removes the configuration directory
// and is refused before any process exists.
//
// The approval is the last 20 characters of the key, which is what the CLI
// was observed to keep for one: an approval holding them skipped the key
// screen, and one holding the whole key did not. So those characters are on
// disk, in an owner-only file, for as long as the worker runs; the whole key
// is never written. A worker on the OAuth token approves no key and writes
// no part of the token: given the token alone, Claude Code 2.1.292 was
// observed to draw no screen about it.
//
// # What a run leaves in, and takes from, the operator's home
//
// Measured on 2026-11-06 against Claude Code 2.1.308 on macOS 27.2, by
// giving a worker a HOME nothing else wrote to and reading that HOME after
// the run: fs_usage, which would have shown every open, needs a privilege
// the measuring account did not have, so this is what a worker left and
// not the whole of what it touched. The measurement was made by hand and
// nothing re-checks the whole of it; claudecli_test.go's probe re-runs the
// part a checked-in test can, where a claude is on PATH: it gives a worker
// a HOME of its own, fails on an entry left in that HOME's .cache or
// .claude, and logs the rest of what the run left there and in the cache
// directory below, so a later CLI's new write is reported rather than
// assumed away. The marker read below is the part it does not re-run.
//
// With the two variables above set, the run left HOME's .cache and .claude
// exactly as empty as it found them, and wrote its state file, its
// history, its session and a cache of its own under CLAUDE_CONFIG_DIR. It
// wrote two things elsewhere under HOME, neither of them the CLI's
// configuration: an application bundle, Applications/Claude Code URL
// Handler.app, and, where the worker's PATH reached a gh, the device
// identifier gh writes for itself under .local/state/gh. What invoked gh
// was not established. The cache directory this adapter makes was empty
// after such a run, so it bounds what the worker runs rather than the CLI;
// that is the directory the probe logs while the worker is still up,
// since the worker's end removes it.
//
// A HOME holding a .claude/CLAUDE.md, a .claude/settings.json and a
// .claude.json, each carrying a marker, produced no model request carrying
// one, which is the only read this could observe; what else the CLI reads
// from HOME is not established here. The worker's HOME is the service's
// own, so everything else the worker runs reads the operator's home as
// before, a git identity for the commits a worker makes among them.
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
	configDir, cacheDir := claudeConfigFor(t), claudeCacheFor(t)
	err = seedClaudeConfig(spec.Dir, key, configDir)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(cacheDir, 0o700); err != nil {
		// What this call made is removed and a directory standing there
		// already is left as it is, which is the stance seedClaudeConfig
		// takes for its own.
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("making the terminal worker's cache directory: %w", err)
	}
	remove := func() { _ = os.RemoveAll(configDir); _ = os.RemoveAll(cacheDir) }
	env = withEnvValue(withEnvValue(env, claudeConfigVariable, configDir), cacheHomeVariable, cacheDir)

	args := r.claudeTerminalWorkerArgs(spec, session)
	worker, err := startTerminalWorker(ctx, t, procSpecTerminal{
		bin:     r.resolved,
		args:    args,
		dir:     spec.Dir,
		env:     env,
		grace:   r.settings.grace,
		maxOut:  r.settings.maxOut,
		cleanup: remove,
	}, "", claudeTyping)
	if err != nil {
		remove()
		return nil, err
	}
	return worker, nil
}

// AdoptInTerminal holds again a Claude Code terminal worker on
// AdoptTerminalWorker's terms, typed into as StartInTerminal's is. The two
// directories it removes at the worker's end are the ones its start made
// beside the terminal's socket, its configuration (claudeConfigFor) and
// its cache (claudeCacheFor), each derived from the socket the adoption
// verifies and never from anything the worker or its server states, so
// nothing the worker can change names what is removed.
func (r *claudeRunner) AdoptInTerminal(ctx context.Context, t Terminal, pane Identity) (TerminalWorker, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	configDir, cacheDir := claudeConfigFor(t), claudeCacheFor(t)
	worker, err := adoptTerminalWorker(ctx, t, pane, procSpecTerminal{
		grace:   r.settings.grace,
		maxOut:  r.settings.maxOut,
		cleanup: func() { _ = os.RemoveAll(configDir); _ = os.RemoveAll(cacheDir) },
	}, claudeTyping)
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

// claudeCacheFor is the cache directory of the Claude Code worker on t,
// named from the terminal exactly as claudeConfigFor names its
// configuration directory, so it too is a function of the terminal alone
// and a start and an adoption of the same terminal name the same
// directory. home.TerminalSockets, whose layout both sit in, records that
// an adapter names a worker's directories this way.
func claudeCacheFor(t Terminal) string { return t.Socket + ".cache" }

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

// cacheHomeVariable is the variable that points a program following the XDG
// base directory convention at a cache directory. It is not Claude Code's
// own: it is read by whatever the worker runs, which is why a worker is
// given one of its own rather than the operator's.
const cacheHomeVariable = "XDG_CACHE_HOME"

// withEnvValue is env with name set to value, whatever env held for it
// before, as the one assignment a started process reads.
func withEnvValue(env []string, name, value string) []string {
	return append(withoutEnvNames(env, name), name+"="+value)
}

// claudeApprovalSuffix is how many trailing characters of a key the CLI was
// observed to keep as its approval.
const claudeApprovalSuffix = 20

// claudeObservedVersion is the Claude Code version this file's and
// claudeScreen's observations of the interface were made on: the screens,
// the framing and the command line. The measurement of what a run leaves
// in the operator's home names the version it was made on itself, because
// it was made later and against a newer CLI.
const claudeObservedVersion = "2.1.301"

// ErrNoCredential is the refusal to start an agent whose credential is not
// in its environment. The error wrapping it names every variable that
// would have been accepted.
var ErrNoCredential = errors.New("agents: the agent's credential is not in its environment")

// claudeTyping is the Claude Code adapter's terminal typing, on the terms
// StartInTerminal states.
var claudeTyping = terminalTyping{frame: claudeTerminalMessage, bracketed: true, read: claudeScreen}

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
// busyMark was on it exactly while a turn ran; and the input was the last
// line that was not blank, the prompt alone when it was empty and the
// prompt, a no-break space and the text when it was not.
const (
	claudeFooterMark = "(shift+tab to cycle)"
	claudeBusyMark   = "esc to interrupt"
	claudeInputMark  = "$"
)

// claudeScreen reads a Claude Code terminal worker's visible screen. With no
// line carrying claudeFooterMark it is screenNotDrawn. Otherwise it is
// screenReady only when the last such line does not carry claudeBusyMark
// and the last line below it that is not blank is the input prompt alone,
// and screenDrawn in every other case. The last footer is read because the
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
	switch {
	case footer < 0:
		return screenNotDrawn
	case strings.Contains(lines[footer], claudeBusyMark):
		return screenDrawn
	}
	for i := len(lines) - 1; i > footer; i-- {
		line := strings.TrimRight(lines[i], " \u00a0")
		if line == "" {
			continue
		}
		if line == claudeInputMark {
			return screenReady
		}
		return screenDrawn
	}
	return screenDrawn
}
