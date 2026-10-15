package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/machine"
)

// Proposing commands: what assistant init offers for the keys the test, lint
// and document stages run, and how it writes one.
//
// PRD section 5 has those stages run configured commands and hold rather
// than report a pass they did not establish, and that stays: nothing here
// makes a stage pass. What this shortens is the setup that stops the holds.
// It reads the checkout's own files for the commands the repository already
// declares, offers them, and writes them only with consent (P1): an answer
// of y at a terminal, one command at a time, or --accept-commands for all of
// them. Without either, nothing is written. A structured answer (--json) is
// never prompted for, because a caller reading one document per answer is
// not a person at a keyboard, so its consent is the flag alone.
//
// # Where the commands are written, and what that does not do
//
// They are written into internal/config's RepositoryDocument at the root of
// the checkout init ran in, merged into what the file already holds. That is
// the file P7 reads commands from, but P7 reads it as the default branch on
// origin holds it at a fresh fetch, never as a checkout holds it, so a
// written command reaches no run until the file is committed and lands on
// that branch. Every answer that wrote says so and names the branch. The
// operator's own configuration file in the home was the other place a
// command could go, and it is not used: it applies to every repository on
// the machine, and a command read out of one repository's build files is a
// fact about that repository.
//
// A key the file already names is never offered, so init does not replace
// what a person wrote; the merge adds members and changes none. The merged
// document is parsed by internal/config before it is written, so init cannot
// write a file a run would refuse. The write is anchored to the bytes the
// merge read, on writeAnchored's terms, so a file that changed in between is
// refused rather than overwritten.
//
// # What is detected, and how
//
// Detection reads files and runs nothing: no build tool, no package manager,
// no shell. It reads at most detectReadLimit bytes of each file and treats a
// larger one as declaring nothing. The table is detectors, and for each key
// the first detector that finds something wins, so a Makefile's own target,
// which is the repository's chosen entry point, is preferred over a
// language's convention:
//
//   - a Makefile target named test, or else check, and one named lint;
//   - go.mod: go test ./... and go vet ./...;
//   - package.json scripts named test and lint, run through the package
//     manager its lock file names, and npm's placeholder test script skipped;
//   - Cargo.toml: cargo test and cargo clippy with warnings denied;
//   - tox.ini: tox; pyproject.toml: pytest when it configures pytest, and
//     ruff when it configures ruff.
//
// Nothing is offered for commands.document. That key runs a command that
// updates documentation the change made stale, and no file a repository
// commonly carries declares one: a docs target builds or serves what is
// written rather than updating it, and offering one would let the document
// stage pass on something it did not establish. NotProposed says so for the
// key, and PhraseNoProseCommand says so to a person.
//
// # Residual gaps
//
// The configuration file compared against is the checkout's copy, not the
// default branch's. A checkout behind that branch, or on a branch whose copy
// differs, is offered a key the trusted copy already names, and writing it
// would make the two copies differ; the answer names the branch so a person
// committing the file sees which copy they are changing. The operator's own
// configuration file is not consulted either, so a key it sets is still
// offered for the repository, where it would take precedence. Detection is
// textual: a Makefile target defined through an include or a variable is not
// seen, and one guarded by a conditional is seen whether or not the
// condition holds. And a detected command is the repository's declaration,
// not a check that it passes or is targeted at a change: init proposes it
// and the person decides.

// acceptCommandsFlag is the consent flag for the non-interactive path.
const acceptCommandsFlag = "accept-commands"

// detectReadLimit bounds how much of one file detection reads.
const detectReadLimit = 1 << 20

// proposedKeys are the keys init considers, in the order it offers them.
var proposedKeys = []config.Key{config.KeyCommandsTest, config.KeyCommandsLint, config.KeyCommandsDocument}

// noProseCommand is NotProposed's reason for commands.document.
const noProseCommand = "init offers no command for updating documentation: no file a repository commonly carries declares one, " +
	"and a command that builds or serves documentation would let the stage pass on something it did not establish"

// nothingDetected is NotProposed's reason for a key no detector found a
// command for.
const nothingDetected = "no file in this checkout declares a command for it"

// detected is one command a detector found.
type detected struct {
	command string
	source  string
}

// detector reads the checkout and returns what it found for each key it
// knows about. It reads files only.
type detector func(fsys fs.FS) map[config.Key]detected

// detectors is the table, in order of preference.
var detectors = []detector{detectMakefile, detectGo, detectNode, detectCargo, detectPython}

// detectCommands runs every detector over fsys and returns, for each key in
// proposedKeys, the first command found.
func detectCommands(fsys fs.FS) map[config.Key]detected {
	found := map[config.Key]detected{}
	for _, d := range detectors {
		for key, command := range d(fsys) {
			if _, taken := found[key]; !taken {
				found[key] = command
			}
		}
	}
	delete(found, config.KeyCommandsDocument)
	return found
}

// readBounded reads name from fsys, and reports false for a file that is not
// there, cannot be read, is not a regular file, or is over detectReadLimit.
func readBounded(fsys fs.FS, name string) ([]byte, bool) {
	file, err := fsys.Open(name)
	if err != nil {
		return nil, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, detectReadLimit+1))
	if err != nil || len(data) > detectReadLimit {
		return nil, false
	}
	return data, true
}

// exists reports whether name is a regular file in fsys, without reading it.
func exists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && info.Mode().IsRegular()
}

// makefileNames are the names GNU make looks for, in the order it looks.
var makefileNames = []string{"GNUmakefile", "makefile", "Makefile"}

// detectMakefile offers make's own targets: test, or else check, and lint.
func detectMakefile(fsys fs.FS) map[config.Key]detected {
	for _, name := range makefileNames {
		data, ok := readBounded(fsys, name)
		if !ok {
			continue
		}
		targets := makeTargets(data)
		found := map[config.Key]detected{}
		for _, target := range []string{"test", "check"} {
			if targets[target] {
				found[config.KeyCommandsTest] = detected{command: "make " + target, source: name + " target " + target}
				break
			}
		}
		if targets["lint"] {
			found[config.KeyCommandsLint] = detected{command: "make lint", source: name + " target lint"}
		}
		return found
	}
	return nil
}

// makeTargets reads the explicit rule targets a makefile declares at the
// start of a line: the words before a colon that is not part of an
// assignment. A recipe line, a comment, a variable assignment and a special
// target are not targets.
func makeTargets(data []byte) map[string]bool {
	targets := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' {
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 || (colon+1 < len(line) && line[colon+1] == '=') {
			continue
		}
		head := line[:colon]
		if strings.ContainsAny(head, "=$%?+!") {
			continue
		}
		for _, word := range strings.Fields(head) {
			if !strings.HasPrefix(word, ".") {
				targets[word] = true
			}
		}
	}
	return targets
}

// detectGo offers the go tool's own test and vet over every package.
func detectGo(fsys fs.FS) map[config.Key]detected {
	if !exists(fsys, "go.mod") {
		return nil
	}
	return map[config.Key]detected{
		config.KeyCommandsTest: {command: "go test ./...", source: "go.mod"},
		config.KeyCommandsLint: {command: "go vet ./...", source: "go.mod"},
	}
}

// npmPlaceholder is the test script npm init writes, which fails by design
// and checks nothing.
const npmPlaceholder = "no test specified"

// detectNode offers package.json's own test and lint scripts, through the
// package manager whose lock file the checkout carries.
func detectNode(fsys fs.FS) map[config.Key]detected {
	data, ok := readBounded(fsys, "package.json")
	if !ok {
		return nil
	}
	var manifest struct {
		Scripts map[string]any `json:"scripts"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return nil
	}
	manager := "npm"
	switch {
	case exists(fsys, "pnpm-lock.yaml"):
		manager = "pnpm"
	case exists(fsys, "yarn.lock"):
		manager = "yarn"
	}
	script := func(name string) bool {
		s, ok := manifest.Scripts[name].(string)
		return ok && strings.TrimSpace(s) != "" && !strings.Contains(s, npmPlaceholder)
	}
	found := map[config.Key]detected{}
	if script("test") {
		found[config.KeyCommandsTest] = detected{command: manager + " test", source: "package.json script test"}
	}
	if script("lint") {
		found[config.KeyCommandsLint] = detected{command: manager + " run lint", source: "package.json script lint"}
	}
	return found
}

// detectCargo offers cargo's own test and clippy, with clippy's warnings
// denied so a warning is not an exit of zero.
func detectCargo(fsys fs.FS) map[config.Key]detected {
	if !exists(fsys, "Cargo.toml") {
		return nil
	}
	return map[config.Key]detected{
		config.KeyCommandsTest: {command: "cargo test", source: "Cargo.toml"},
		config.KeyCommandsLint: {command: "cargo clippy -- -D warnings", source: "Cargo.toml"},
	}
}

// detectPython offers tox where tox.ini exists, else pytest where
// pyproject.toml configures it, and ruff where pyproject.toml configures it.
// It reads table headers and nothing else of the TOML.
func detectPython(fsys fs.FS) map[config.Key]detected {
	found := map[config.Key]detected{}
	if exists(fsys, "tox.ini") {
		found[config.KeyCommandsTest] = detected{command: "tox", source: "tox.ini"}
	}
	data, ok := readBounded(fsys, "pyproject.toml")
	if !ok {
		return found
	}
	for _, line := range strings.Split(string(data), "\n") {
		header := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(header, "[tool.pytest"):
			if _, taken := found[config.KeyCommandsTest]; !taken {
				found[config.KeyCommandsTest] = detected{command: "python -m pytest", source: "pyproject.toml [tool.pytest]"}
			}
		case strings.HasPrefix(header, "[tool.ruff"):
			found[config.KeyCommandsLint] = detected{command: "ruff check .", source: "pyproject.toml [tool.ruff]"}
		}
	}
	return found
}

// proposeCommands is init's offer: it detects commands in the checkout at
// working, compares them with what the checkout's configuration file already
// names, asks for consent where it may, and writes what was consented to.
// accept is --accept-commands; ask is whether a person at a terminal may be
// asked, which the caller decides. It never returns an error: every way the
// offer can end is a result the answer carries.
func proposeCommands(working string, accept, ask bool, prompt io.Writer, answers io.Reader) machine.CommandProposals {
	path := filepath.Join(working, config.RepositoryDocument)
	proposals := machine.CommandProposals{File: config.RepositoryDocument, Proposed: []machine.ProposedCommand{}, NotProposed: []machine.NotProposed{}}

	observed, readErr := os.ReadFile(path)
	existed := readErr == nil
	var layer config.Layer
	switch {
	case errors.Is(readErr, fs.ErrNotExist):
		layer, readErr = config.Absent(config.OriginTrusted), nil
	case readErr == nil:
		layer, readErr = config.Parse(config.OriginTrusted, observed)
	}
	readable := readErr == nil

	found := detectCommands(os.DirFS(working))
	for _, key := range proposedKeys {
		if readable && layer.IsSet(key) {
			proposals.Configured = append(proposals.Configured, string(key))
			continue
		}
		command, ok := found[key]
		switch {
		case key == config.KeyCommandsDocument:
			proposals.NotProposed = append(proposals.NotProposed, machine.NotProposed{Key: string(key), Reason: noProseCommand})
		case !ok:
			proposals.NotProposed = append(proposals.NotProposed, machine.NotProposed{Key: string(key), Reason: nothingDetected})
		default:
			proposals.Proposed = append(proposals.Proposed, machine.ProposedCommand{
				Key: string(key), Command: command.command, Source: command.source,
			})
		}
	}

	if len(proposals.Proposed) == 0 {
		proposals.Result = machine.CommandsNone
		return proposals
	}
	if !readable && !accept {
		proposals.Result, proposals.Detail = machine.CommandsUnreadable, readErr.Error()
		return proposals
	}
	consented := make([]bool, len(proposals.Proposed))
	switch {
	case accept:
		for i := range consented {
			consented[i] = true
		}
	case ask:
		if !askConsent(proposals.Proposed, consented, prompt, answers) {
			proposals.Result = machine.CommandsDeclined
			return proposals
		}
	default:
		proposals.Result = machine.CommandsPrinted
		return proposals
	}
	refuse := func(refusal machine.CommandsRefusal, detail string) machine.CommandProposals {
		proposals.Result, proposals.Refusal, proposals.Detail = machine.CommandsRefused, refusal, detail
		return proposals
	}
	if !readable {
		return refuse(machine.CommandsFileUnreadable, readErr.Error())
	}
	merged, err := mergeCommands(observed, existed, proposals.Proposed, consented)
	if err != nil {
		return refuse(machine.CommandsFileUnreadable, err.Error())
	}
	mode := fs.FileMode(0o644)
	if existed {
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	}
	if err := writeAnchored(path, merged, mode, observed, existed); err != nil {
		var changed changedError
		if errors.As(err, &changed) {
			return refuse(machine.CommandsFileChanged, err.Error())
		}
		return refuse(machine.CommandsFileUnreadable, err.Error())
	}
	for i := range proposals.Proposed {
		proposals.Proposed[i].Written = consented[i]
	}
	proposals.Result = machine.CommandsWritten
	return proposals
}

// askConsent asks about each proposal in turn on prompt, reading one line of
// answers per question, and records a y or yes as consent. Anything else is
// no, and so is the end of answers, after which nothing more is asked. It
// reports whether anything was consented to.
func askConsent(proposed []machine.ProposedCommand, consented []bool, prompt io.Writer, answers io.Reader) bool {
	writeln(prompt, machine.PhraseCommandsConsent)
	if answers == nil {
		return false
	}
	reader := bufio.NewReader(answers)
	accepted := false
	for i, p := range proposed {
		writef(prompt, "%s %s = %s  [%s] [y/N] ", commandsProposedLabel, printable(p.Key), printable(p.Command), printable(p.Source))
		line, err := reader.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			consented[i], accepted = true, true
		}
		if err != nil {
			writeln(prompt)
			break
		}
	}
	return accepted
}

// mergeCommands adds the consented commands to the configuration document
// observed, under its commands object, keeping every member it already holds
// in its order, and returns the document laid out with two-space indentation.
// The result is parsed by internal/config before it is returned, so a merge
// that produced a document a run would refuse is an error rather than a
// write.
func mergeCommands(observed []byte, existed bool, proposed []machine.ProposedCommand, consented []bool) ([]byte, error) {
	document := json.RawMessage("{}")
	if existed && len(bytes.TrimSpace(observed)) > 0 {
		document = observed
	}
	top, err := objectMembers(document)
	if err != nil {
		return nil, fmt.Errorf("the configuration file is not one JSON object: %w", err)
	}
	section := map[string][]member{}
	sectionAt := map[string]int{}
	var order []string
	for i, p := range proposed {
		if !consented[i] {
			continue
		}
		parent, leaf, ok := strings.Cut(p.Key, ".")
		if !ok {
			return nil, fmt.Errorf("the key %q is not nested", p.Key)
		}
		if _, seen := section[parent]; !seen {
			at := find(top, parent)
			var members []member
			if at >= 0 {
				if members, err = objectMembers(top[at].value); err != nil {
					return nil, fmt.Errorf("%q is not an object: %w", parent, err)
				}
			}
			section[parent], sectionAt[parent] = members, at
			order = append(order, parent)
		}
		value, err := compactJSON(p.Command)
		if err != nil {
			return nil, err
		}
		section[parent] = append(section[parent], member{key: leaf, value: json.RawMessage(value)})
	}
	for _, parent := range order {
		encoded, err := encodeObject(section[parent])
		if err != nil {
			return nil, err
		}
		if at := sectionAt[parent]; at >= 0 {
			top[at].value = encoded
		} else {
			top = append(top, member{key: parent, value: encoded})
		}
	}
	merged, err := encodeObject(top)
	if err != nil {
		return nil, err
	}
	var laid bytes.Buffer
	if err := json.Indent(&laid, merged, "", "  "); err != nil {
		return nil, err
	}
	laid.WriteByte('\n')
	if _, err := config.Parse(config.OriginTrusted, laid.Bytes()); err != nil {
		return nil, fmt.Errorf("the merged configuration file would be refused: %w", err)
	}
	return laid.Bytes(), nil
}

// The labels the command offer prints its machine text after, named so a
// test can tell the labelled lines from the phrase.
const (
	commandsFileLabel       = "Configuration file:"
	commandsProposedLabel   = "Offered:"
	commandsWrittenLabel    = "Written:"
	commandsConfiguredLabel = "Already configured:"
)

// readOutCommands prints the row for what became of the offer, and beside it
// the file and each command as machine text, then the row saying nothing is
// offered for prose documentation.
func readOutCommands(w io.Writer, p machine.CommandProposals) {
	writeln(w, machine.CommandsPhrase(p))
	if p.Result != machine.CommandsNone {
		writef(w, "%s %s\n", commandsFileLabel, printable(p.File))
	}
	for _, key := range p.Configured {
		writef(w, "%s %s\n", commandsConfiguredLabel, printable(key))
	}
	for _, c := range p.Proposed {
		label := commandsProposedLabel
		if c.Written {
			label = commandsWrittenLabel
		}
		writef(w, "%s\n", commandLine(label, c))
	}
	writeln(w, machine.PhraseNoProseCommand)
}

// commandLine is one offered command as readOutCommands prints it.
func commandLine(label string, c machine.ProposedCommand) string {
	return fmt.Sprintf("%s %s = %s  [%s]", label, printable(c.Key), printable(c.Command), printable(c.Source))
}
